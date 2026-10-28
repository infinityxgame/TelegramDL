package telegram

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"

	"tgdown/pkg/config"
	"tgdown/pkg/i18n"
)

type UserInfo struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
	Phone     string `json:"phone"`
	ColorID   *int   `json:"color_id,omitempty"`
}

// AuthStatus es lo que el panel necesita saber sobre la sesión. A propósito NO
// incluye api_id ni api_hash: al panel le basta con HasCredentials para decidir
// si muestra el formulario de configuración, y devolverlos convertía el token
// de acceso en una vía para leer las credenciales de Telegram del usuario.
type AuthStatus struct {
	Configured     bool      `json:"configured"`
	HasCredentials bool      `json:"has_credentials"`
	Authorized     bool      `json:"authorized"`
	Authenticated  bool      `json:"authenticated"`
	State          string    `json:"state"`
	Phone          string    `json:"phone,omitempty"`
	User           *UserInfo `json:"user,omitempty"`
}

type ClientManager struct {
	apiID     int
	apiHash   string
	client    *telegram.Client
	rawClient *tg.Client
	// Grupo de conexiones dedicado a bajar archivos. Ver DownloadConnections.
	downloadPool         telegram.CloseInvoker
	downloadClient       *tg.Client
	downloadPoolNotice   bool
	primaryDC            int
	dcPools              map[int]telegram.CloseInvoker
	dcClients            map[int]*tg.Client
	sessionPath          string
	cancelRun            context.CancelFunc
	runWg                sync.WaitGroup
	mu                   sync.RWMutex
	// dcMu serializa únicamente la creación de pools por DC (SEC-18). La
	// llamada de red client.DC() se hace fuera de cm.mu para no bloquear
	// todo el manejador; este candado corto evita que dos goroutines abran
	// dos pools duplicados para el mismo DC mientras tanto.
	dcMu                 sync.Mutex
	readyChan            chan struct{}
	phoneCodeHash        string
	pendingPhone         string
	dispatcher           tg.UpdateDispatcher
	dispatcherRegistered bool
	channelAccessHashes  map[int64]int64
	userAccessHashes     map[int64]int64
	chatNames            map[int64]string
	chatUsernames        map[int64]string
	onGenericMessage     func(ctx context.Context, entities tg.Entities, msg *tg.Message) error
}

// maxAccessHashEntries acota el tamaño de los mapas de access hash de
// canales/usuarios. Sin límite, una sesión de escucha (listener) de larga
// duración que atraviesa muchos chats distintos podría hacerlos crecer sin
// parar. Los hashes son recuperables bajo demanda, así que al llegar al
// límite se descarta una entrada existente (orden no determinista, pero
// barato) para dejar sitio a la nueva.
const maxAccessHashEntries = 20000

// DownloadConnections es cuántas conexiones MTProto se abren, además de la
// principal, solo para pedir bloques de archivo.
//
// Es el arreglo de fondo del problema de «los workers no suman»: MTProto
// multiplexa muchas peticiones por una conexión, pero Telegram le pone un techo
// de velocidad a cada una. Con una sola conexión daba igual poner 2 workers que
// 8: el techo era el mismo y lo único que se conseguía era provocar FLOOD_WAIT.
// Los clientes oficiales abren varias conexiones por centro de datos justo por
// esto.
//
// Ocho es lo que usan los clientes oficiales y deja margen de sobra: con
// bloques de 512 KB y 100 ms de ida y vuelta el techo teórico queda muy por
// encima de cualquier conexión doméstica. Subirlo más no acelera y sí acerca el
// límite de Telegram.
const DownloadConnections = 8

// openDownloadPool levanta el grupo de conexiones de descarga. Se llama con
// el cliente ya conectado, porque el grupo se crea contra el centro de datos de
// la sesión. Si falla, no pasa nada: las descargas siguen saliendo por la
// conexión principal, como antes.
func (cm *ClientManager) openDownloadPool() {
	cm.mu.RLock()
	client := cm.client
	alreadyOpened := cm.downloadPool != nil
	cm.mu.RUnlock()

	if client == nil || alreadyOpened {
		return
	}

	pool, err := client.Pool(int64(DownloadConnections))
	if err != nil {
		// El aviso sale una sola vez: DownloadClient reintenta abrirlo en cada
		// descarga y no tiene sentido llenar el registro con lo mismo.
		cm.mu.Lock()
		shouldWarn := !cm.downloadPoolNotice
		cm.downloadPoolNotice = true
		cm.mu.Unlock()
		if shouldWarn {
			log.Printf("[TG CLIENT] %s", i18n.T("telegram.poolError", err))
		}
		return
	}

	cm.mu.Lock()
	cm.downloadPool = pool
	cm.downloadClient = tg.NewClient(pool)
	cm.downloadPoolNotice = false
	cm.mu.Unlock()

	log.Printf("[TG CLIENT] %s", i18n.T("telegram.poolOpened", DownloadConnections))
}

// closeDownloadPool cierra el grupo al caerse la conexión. Es idempotente.
func (cm *ClientManager) closeDownloadPool() {
	cm.mu.Lock()
	pool := cm.downloadPool
	cm.downloadPool = nil
	cm.downloadClient = nil
	dcPools := cm.dcPools
	cm.dcPools = make(map[int]telegram.CloseInvoker)
	cm.dcClients = make(map[int]*tg.Client)
	cm.mu.Unlock()

	if pool != nil {
		if err := pool.Close(); err != nil {
			log.Printf("[TG CLIENT] %s", i18n.T("telegram.poolCloseError", err))
		}
	}
	for dcID, p := range dcPools {
		if p != nil {
			if err := p.Close(); err != nil {
				log.Printf("[TG CLIENT] %s", i18n.T("telegram.poolCloseDCErr", dcID, err))
			}
		}
	}
}

// DownloadClient es el cliente por el que salen las peticiones de bloques de
// archivo. Devuelve el grupo de conexiones cuando está disponible y la conexión
// principal en caso contrario, así que quien lo llame no tiene que comprobar
// nada.
//
// Separarlo de RawClient tiene un segundo efecto que importa: las consultas de
// metadatos (nombre y tamaño del archivo) siguen yendo por la conexión
// principal y dejan de competir con los bloques.
func (cm *ClientManager) DownloadClient() *tg.Client {
	cm.mu.RLock()
	dl := cm.downloadClient
	cm.mu.RUnlock()
	if dl != nil {
		return dl
	}

	// El grupo se abre al conectar, pero si en ese momento todavía no había
	// sesión iniciada pudo fallar. Aquí se reintenta, así que la primera
	// descarga después de iniciar sesión ya lo levanta.
	cm.openDownloadPool()

	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.downloadClient != nil {
		return cm.downloadClient
	}
	return cm.rawClient
}

// PrimaryDC devuelve el ID del centro de datos principal de la sesión actual.
func (cm *ClientManager) PrimaryDC() int {
	cm.mu.RLock()
	if cm.primaryDC > 0 {
		dc := cm.primaryDC
		cm.mu.RUnlock()
		return dc
	}
	cm.mu.RUnlock()

	if data, err := os.ReadFile(cm.sessionPath); err == nil {
		var sess struct {
			Data struct {
				DC int `json:"DC"`
			} `json:"Data"`
		}
		if err := json.Unmarshal(data, &sess); err == nil && sess.Data.DC > 0 {
			cm.mu.Lock()
			cm.primaryDC = sess.Data.DC
			cm.mu.Unlock()
			return sess.Data.DC
		}
	}
	return 0
}

// DownloadClientForDC devuelve un cliente configurado para descargar archivos
// alojados en un centro de datos (DC) específico. Si dc <= 0 o coincide con el
// DC principal de la sesión, devuelve DownloadClient() normal.
func (cm *ClientManager) DownloadClientForDC(ctx context.Context, dc int) (*tg.Client, error) {
	primaryDC := cm.PrimaryDC()
	if dc <= 0 || (primaryDC > 0 && dc == primaryDC) {
		return cm.DownloadClient(), nil
	}

	cm.mu.RLock()
	dlClient, ok := cm.dcClients[dc]
	client := cm.client
	cm.mu.RUnlock()

	if ok && dlClient != nil {
		return dlClient, nil
	}

	if client == nil {
		return nil, errors.New(i18n.T("telegram.notConnected"))
	}

	// SEC-18: la apertura del pool es I/O de red, así que NO se hace con
	// cm.mu tomado (antes bloqueaba DownloadClient, GetChatName, RawClient,
	// WaitReady, FetchDialogs, Stop y company durante toda la llamada).
	// dcMu serializa solo la creación para que dos descargas simultáneas no
	// abran pools duplicados para el mismo DC.
	cm.dcMu.Lock()
	defer cm.dcMu.Unlock()

	// Doble comprobación: otra goroutine pudo publicar el pool mientras
	// esperábamos a dcMu. Si ya está publicado, se devuelve sin tocar la red.
	cm.mu.RLock()
	dlClient, ok = cm.dcClients[dc]
	cm.mu.RUnlock()
	if ok && dlClient != nil {
		return dlClient, nil
	}

	pool, err := client.DC(ctx, dc, int64(DownloadConnections))
	if err != nil {
		log.Printf("[TG CLIENT] %s", i18n.T("telegram.poolOpenDCErr", dc, err))
		return nil, fmt.Errorf("error al conectar a DC %d: %w", dc, err)
	}

	dlClient = tg.NewClient(pool)

	// Publicación con cm.mu tomado solo el tiempo de la asignación.
	cm.mu.Lock()
	// Carrera residual: la sesión pudo reiniciarse (stopRunning/InitClient)
	// mientras DC conectaba. Ese pool pertenece al cliente viejo y quedaría
	// publicado en un manejador que ya no lo usa, así que se descarta con su
	// API de cierre en lugar de dejarlo abandonado.
	if cm.client != client {
		cm.mu.Unlock()
		if cerrErr := pool.Close(); cerrErr != nil {
			log.Printf("[TG CLIENT] Error cerrando pool DC %d descartado: %v", dc, cerrErr)
		}
		return nil, errors.New("cliente de Telegram no conectado")
	}
	if cm.dcPools == nil {
		cm.dcPools = make(map[int]telegram.CloseInvoker)
		cm.dcClients = make(map[int]*tg.Client)
	}
	cm.dcPools[dc] = pool
	cm.dcClients[dc] = dlClient
	// El candado se suelta antes de loguear: es precisamente el motivo del
	// reordenamiento (SEC-18), para no retener cm.mu durante una llamada de
	// red o de log.
	cm.mu.Unlock()

	log.Printf("[TG CLIENT] %s", i18n.T("telegram.poolDCOpened", dc, DownloadConnections))
	return dlClient, nil
}

func boundedHashSet(m map[int64]int64, id int64, hash int64) {
	if _, exists := m[id]; !exists && len(m) >= maxAccessHashEntries {
		for k := range m {
			delete(m, k)
			break
		}
	}
	m[id] = hash
}

// boundedNameSet guarda el título de un chat con el mismo criterio de tamaño
// acotado que los access hash. Se usa para poder mostrar nombres legibles en el
// registro de actividad en lugar de IDs numéricos.
func boundedNameSet(m map[int64]string, id int64, name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	if _, exists := m[id]; !exists && len(m) >= maxAccessHashEntries {
		for k := range m {
			delete(m, k)
			break
		}
	}
	m[id] = name
}

// channelPeerID convierte el ID interno de un canal en el ID canónico con
// prefijo -100 que usa el resto de la aplicación.
func channelPeerID(channelID int64) int64 {
	id, _ := strconv.ParseInt(fmt.Sprintf("-100%d", channelID), 10, 64)
	return id
}

// userDisplayName arma el nombre visible de un usuario a partir de sus campos.
func userDisplayName(u *tg.User) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name = strings.TrimSpace(u.Username)
	}
	return name
}

// GetChatName devuelve el título cacheado de un chat a partir de su ID
// canónico (-100... para canales, -... para grupos básicos, positivo para
// usuarios). Devuelve cadena vacía si todavía no se conoce.
func (cm *ClientManager) GetChatName(peerID int64) string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.chatNames[peerID]
}

// GetChatUsername devuelve el nombre de usuario (@username) cacheado de un chat a partir de su ID
func (cm *ClientManager) GetChatUsername(peerID int64) string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.chatUsernames[peerID]
}

// RememberChatName permite a otros componentes alimentar la caché de nombres
// (por ejemplo, la escucha cuando resuelve un chat vigilado).
func (cm *ClientManager) RememberChatName(peerID int64, name string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	boundedNameSet(cm.chatNames, peerID, name)
}

// RememberChatUsername permite a otros componentes alimentar la caché de usernames
func (cm *ClientManager) RememberChatUsername(peerID int64, username string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if username != "" {
		boundedNameSet(cm.chatUsernames, peerID, strings.TrimPrefix(username, "@"))
	}
}

func NewClientManager() *ClientManager {
	config.InitPaths()
	sessPath := filepath.Join(config.DataDir, "tg_session.json")
	importPyrogramSession(sessPath)

	cm := &ClientManager{
		sessionPath:         sessPath,
		dispatcher:          tg.NewUpdateDispatcher(),
		channelAccessHashes: make(map[int64]int64),
		userAccessHashes:    make(map[int64]int64),
		chatNames:           make(map[int64]string),
		chatUsernames:       make(map[int64]string),
		dcPools:             make(map[int]telegram.CloseInvoker),
		dcClients:           make(map[int]*tg.Client),
	}
	return cm
}

func GetPyrogramCredentials() (string, error) {
	sessionFiles := []string{
		filepath.Join(config.DataDir, "downloader_session.session"),
		filepath.Join(config.BaseDir, "downloader_session.session"),
	}
	for _, sf := range sessionFiles {
		if _, err := os.Stat(sf); err == nil {
			db, err := sql.Open("sqlite", sf)
			if err == nil {
				defer db.Close()
				var apiID int
				if err := db.QueryRow("SELECT api_id FROM sessions LIMIT 1").Scan(&apiID); err == nil && apiID != 0 {
					return strconv.Itoa(apiID), nil
				}
			}
		}
	}
	return "", errors.New("not found")
}

func importPyrogramSession(targetJSONPath string) {
	// Si ya tenemos una sesión válida con clave de 256 bytes, no sobreescribir
	if fi, err := os.Stat(targetJSONPath); err == nil && fi.Size() > 100 {
		fileStorage := &session.FileStorage{Path: targetJSONPath}
		loader := &session.Loader{Storage: fileStorage}
		if data, err := loader.Load(context.Background()); err == nil && data != nil && len(data.AuthKey) == 256 {
			return
		}
	}

	sessionFiles := []string{
		filepath.Join(config.DataDir, "downloader_session.session"),
		filepath.Join(config.BaseDir, "downloader_session.session"),
	}

	var foundPath string
	for _, sf := range sessionFiles {
		if _, err := os.Stat(sf); err == nil {
			foundPath = sf
			break
		}
	}
	if foundPath == "" {
		return
	}

	db, err := sql.Open("sqlite", foundPath)
	if err != nil {
		return
	}
	defer db.Close()

	var dcID, apiID, port int
	var authKey []byte
	var userID int64
	var serverAddr string
	row := db.QueryRow("SELECT dc_id, api_id, auth_key, user_id, server_address, port FROM sessions LIMIT 1")
	if err := row.Scan(&dcID, &apiID, &authKey, &userID, &serverAddr, &port); err != nil || len(authKey) != 256 {
		return
	}

	if serverAddr == "" {
		switch dcID {
		case 1:
			serverAddr = "149.154.175.53"
		case 2:
			serverAddr = "149.154.167.50"
		case 3:
			serverAddr = "149.154.175.100"
		case 4:
			serverAddr = "149.154.167.91"
		case 5:
			serverAddr = "91.108.56.165"
		default:
			serverAddr = "149.154.175.53"
		}
	}
	if port == 0 {
		port = 443
	}

	h := sha1.Sum(authKey)
	keyID := h[len(h)-8:]

	data := session.Data{
		DC:        dcID,
		Addr:      fmt.Sprintf("%s:%d", serverAddr, port),
		AuthKey:   authKey,
		AuthKeyID: keyID,
	}

	fileStorage := &session.FileStorage{Path: targetJSONPath}
	loader := &session.Loader{Storage: fileStorage}
	_ = loader.Save(context.Background(), &data)
}

// systemVersionLabel reporta el sistema operativo real en vez de un valor
// fijo, para que la sesión activa que ve el usuario en Telegram (Ajustes >
// Dispositivos) refleje la plataforma en la que realmente corre TGDown.
func systemVersionLabel() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows"
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	default:
		return runtime.GOOS
	}
}

func (cm *ClientManager) SetMessageCallback(cb func(ctx context.Context, entities tg.Entities, msg *tg.Message) error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.onGenericMessage = cb
}

func (cm *ClientManager) cacheEntities(entities tg.Entities) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for id, ch := range entities.Channels {
		boundedHashSet(cm.channelAccessHashes, id, ch.AccessHash)
		boundedNameSet(cm.chatNames, channelPeerID(id), ch.Title)
		if ch.Username != "" {
			boundedNameSet(cm.chatUsernames, channelPeerID(id), ch.Username)
		}
	}
	for id, u := range entities.Users {
		boundedHashSet(cm.userAccessHashes, id, u.AccessHash)
		boundedNameSet(cm.chatNames, id, userDisplayName(u))
		if u.Username != "" {
			boundedNameSet(cm.chatUsernames, id, u.Username)
		}
	}
	for id, c := range entities.Chats {
		boundedNameSet(cm.chatNames, -id, c.Title)
	}
}

func (cm *ClientManager) SetChannelAccessHash(channelID int64, accessHash int64) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	boundedHashSet(cm.channelAccessHashes, channelID, accessHash)
}

func (cm *ClientManager) SetUserAccessHash(userID int64, accessHash int64) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	boundedHashSet(cm.userAccessHashes, userID, accessHash)
}

func (cm *ClientManager) FetchDialogs(ctx context.Context) error {
	cm.mu.RLock()
	raw := cm.rawClient
	cm.mu.RUnlock()
	if raw == nil {
		return errors.New(i18n.T("telegram.notConnectedShort"))
	}

	var offsetPeer tg.InputPeerClass = &tg.InputPeerEmpty{}
	var offsetID int = 0
	var offsetDate int = 0

	for page := 0; page < 5; page++ {
		res, err := raw.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
			OffsetPeer: offsetPeer,
			OffsetID:   offsetID,
			OffsetDate: offsetDate,
			Limit:      100,
		})
		if err != nil {
			return err
		}

		var chats []tg.ChatClass
		var users []tg.UserClass
		var messages []tg.MessageClass

		switch d := res.(type) {
		case *tg.MessagesDialogs:
			chats = d.Chats
			users = d.Users
			messages = d.Messages
		case *tg.MessagesDialogsSlice:
			chats = d.Chats
			users = d.Users
			messages = d.Messages
		}

		cm.mu.Lock()
		for _, c := range chats {
			switch ch := c.(type) {
			case *tg.Channel:
				boundedHashSet(cm.channelAccessHashes, ch.ID, ch.AccessHash)
				boundedNameSet(cm.chatNames, channelPeerID(ch.ID), ch.Title)
				if ch.Username != "" {
					boundedNameSet(cm.chatUsernames, channelPeerID(ch.ID), ch.Username)
				}
			case *tg.Chat:
				boundedNameSet(cm.chatNames, -ch.ID, ch.Title)
			}
		}
		for _, u := range users {
			if usr, ok := u.(*tg.User); ok {
				boundedHashSet(cm.userAccessHashes, usr.ID, usr.AccessHash)
				boundedNameSet(cm.chatNames, usr.ID, userDisplayName(usr))
				if usr.Username != "" {
					boundedNameSet(cm.chatUsernames, usr.ID, usr.Username)
				}
			}
		}
		cm.mu.Unlock()

		if len(messages) == 0 || len(chats) == 0 {
			break
		}

		lastMsg, ok := messages[len(messages)-1].(*tg.Message)
		if !ok {
			break
		}
		offsetID = lastMsg.ID
		offsetDate = lastMsg.Date

		switch p := lastMsg.PeerID.(type) {
		case *tg.PeerChannel:
			cm.mu.RLock()
			hash := cm.channelAccessHashes[p.ChannelID]
			cm.mu.RUnlock()
			offsetPeer = &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: hash}
		case *tg.PeerUser:
			cm.mu.RLock()
			hash := cm.userAccessHashes[p.UserID]
			cm.mu.RUnlock()
			offsetPeer = &tg.InputPeerUser{UserID: p.UserID, AccessHash: hash}
		case *tg.PeerChat:
			offsetPeer = &tg.InputPeerChat{ChatID: p.ChatID}
		default:
			offsetPeer = &tg.InputPeerEmpty{}
		}
	}

	return nil
}

func (cm *ClientManager) GetChannelAccessHash(channelID int64) (int64, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	h, ok := cm.channelAccessHashes[channelID]
	return h, ok
}

func (cm *ClientManager) GetUserAccessHash(userID int64) (int64, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	h, ok := cm.userAccessHashes[userID]
	return h, ok
}

func (cm *ClientManager) ResolveUsername(ctx context.Context, username string) (int64, error) {
	cm.mu.RLock()
	raw := cm.rawClient
	cm.mu.RUnlock()
	if raw == nil {
		return 0, errors.New(i18n.T("telegram.notConnectedShort"))
	}

	username = strings.TrimPrefix(username, "@")
	res, err := raw.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{
		Username: username,
	})
	if err != nil {
		return 0, fmt.Errorf(i18n.T("telegram.resolveError"), username, err)
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	for _, c := range res.Chats {
		switch ch := c.(type) {
		case *tg.Channel:
			boundedHashSet(cm.channelAccessHashes, ch.ID, ch.AccessHash)
			boundedNameSet(cm.chatNames, channelPeerID(ch.ID), ch.Title)
			if ch.Username != "" {
				boundedNameSet(cm.chatUsernames, channelPeerID(ch.ID), ch.Username)
			}
		case *tg.Chat:
			boundedNameSet(cm.chatNames, -ch.ID, ch.Title)
		}
	}
	for _, u := range res.Users {
		if usr, ok := u.(*tg.User); ok {
			boundedHashSet(cm.userAccessHashes, usr.ID, usr.AccessHash)
			boundedNameSet(cm.chatNames, usr.ID, userDisplayName(usr))
			if usr.Username != "" {
				boundedNameSet(cm.chatUsernames, usr.ID, usr.Username)
			}
		}
	}

	switch p := res.Peer.(type) {
	case *tg.PeerChannel:
		tgID, _ := strconv.ParseInt(fmt.Sprintf("-100%d", p.ChannelID), 10, 64)
		return tgID, nil
	case *tg.PeerChat:
		return -p.ChatID, nil
	case *tg.PeerUser:
		return p.UserID, nil
	}

	return 0, errors.New(i18n.T("telegram.unknownChatType"))
}

func (cm *ClientManager) WaitReady(ctx context.Context) error {
	cm.mu.RLock()
	ready := cm.readyChan
	client := cm.client
	cm.mu.RUnlock()

	if client == nil {
		return errors.New(i18n.T("telegram.notConfigured"))
	}
	if ready == nil {
		return nil
	}

	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(15 * time.Second):
		return errors.New(i18n.T("telegram.timeoutConnecting"))
	}
}

func (cm *ClientManager) RawClient() *tg.Client {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.rawClient
}

func (cm *ClientManager) Client() *telegram.Client {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.client
}

func (cm *ClientManager) InitClient(apiIDStr, apiHash string) error {
	// SEC-01: validar las credenciales ANTES de tocar la sesión. Antes,
	// stopRunning() se ejecutaba primero y un api_id inválido mataba el
	// cliente MTProto vivo solo para devolver después un error. La
	// validación no lee ni escribe cm, así que un error de formato sale sin
	// llamar a stopRunning() y sin modificar ningún campo del manejador.
	apiIDStr = strings.TrimSpace(apiIDStr)
	apiHash = strings.TrimSpace(apiHash)

	// Borrado intencionado de credenciales: solo en ese caso se decide parar
	// la sesión actual, porque el usuario pide vaciar la configuración.
	borrarSesion := apiIDStr == "" || apiHash == ""
	id := 0
	if !borrarSesion {
		var err error
		id, err = strconv.Atoi(apiIDStr)
		if err != nil {
			// Sin stopRunning() ni mutación de cm: el estado previo queda intacto.
			return fmt.Errorf(i18n.T("telegram.invalidAPIID"), err)
		}
	}

	// Detener la sesión anterior antes de tomar el mutex. La goroutine de
	// Telegram puede necesitarlo mientras termina.
	cm.stopRunning()

	cm.mu.Lock()
	defer cm.mu.Unlock()

	if borrarSesion {
		cm.apiID = 0
		cm.apiHash = ""
		cm.client = nil
		cm.rawClient = nil
		return nil
	}

	cm.apiID = id
	cm.apiHash = apiHash
	cm.readyChan = make(chan struct{})
	readyChan := cm.readyChan

	// Registrar los handlers una sola vez. InitClient puede ejecutarse varias
	// veces durante la autenticación o al cambiar credenciales.
	if !cm.dispatcherRegistered {
		cm.dispatcher.OnNewMessage(func(ctx context.Context, entities tg.Entities, update *tg.UpdateNewMessage) error {
			cm.cacheEntities(entities)
			msg, ok := update.Message.(*tg.Message)
			if !ok || msg.Out {
				return nil
			}
			cm.mu.RLock()
			cb := cm.onGenericMessage
			cm.mu.RUnlock()
			if cb != nil {
				return cb(ctx, entities, msg)
			}
			return nil
		})

		cm.dispatcher.OnNewChannelMessage(func(ctx context.Context, entities tg.Entities, update *tg.UpdateNewChannelMessage) error {
			cm.cacheEntities(entities)
			msg, ok := update.Message.(*tg.Message)
			if !ok || msg.Out {
				return nil
			}
			cm.mu.RLock()
			cb := cm.onGenericMessage
			cm.mu.RUnlock()
			if cb != nil {
				return cb(ctx, entities, msg)
			}
			return nil
		})

		cm.dispatcher.OnEditMessage(func(ctx context.Context, entities tg.Entities, update *tg.UpdateEditMessage) error {
			cm.cacheEntities(entities)
			msg, ok := update.Message.(*tg.Message)
			if !ok || msg.Out {
				return nil
			}
			cm.mu.RLock()
			cb := cm.onGenericMessage
			cm.mu.RUnlock()
			if cb != nil {
				return cb(ctx, entities, msg)
			}
			return nil
		})

		cm.dispatcher.OnEditChannelMessage(func(ctx context.Context, entities tg.Entities, update *tg.UpdateEditChannelMessage) error {
			cm.cacheEntities(entities)
			msg, ok := update.Message.(*tg.Message)
			if !ok || msg.Out {
				return nil
			}
			cm.mu.RLock()
			cb := cm.onGenericMessage
			cm.mu.RUnlock()
			if cb != nil {
				return cb(ctx, entities, msg)
			}
			return nil
		})
		cm.dispatcherRegistered = true
	}

	gaps := updates.New(updates.Config{
		Handler: cm.dispatcher,
	})

	opts := telegram.Options{
		SessionStorage: &session.FileStorage{
			Path: cm.sessionPath,
		},
		UpdateHandler: gaps,
		Device: telegram.DeviceConfig{
			DeviceModel:   "TGDown Desktop",
			SystemVersion: systemVersionLabel(),
			AppVersion:    config.AppVersion,
			LangCode:      "es",
		},
	}

	client := telegram.NewClient(cm.apiID, cm.apiHash, opts)
	cm.client = client
	cm.rawClient = client.API()

	ctx, cancel := context.WithCancel(context.Background())
	cm.cancelRun = cancel

	readyOnce := sync.Once{}
	cm.runWg.Add(1)
	go func() {
		defer cm.runWg.Done()
		// Sin el API_ID: el registro se comparte en capturas y el identificador
		// de la aplicación no aporta nada para diagnosticar.
		log.Printf("[TG CLIENT] %s", i18n.T("telegram.connecting"))
		err := client.Run(ctx, func(runCtx context.Context) error {
			readyOnce.Do(func() {
				close(readyChan)
				go func() {
					time.Sleep(300 * time.Millisecond)
					_ = cm.FetchDialogs(context.Background())
				}()
			})

			log.Printf("[TG CLIENT] %s", i18n.T("telegram.connected"))

			// Los bloques de archivo salen por su propio grupo de conexiones.
			// Se abre aquí, con la sesión ya conectada, y se cierra al salir de
			// Run, que es cuando se cae la conexión o se cierra el programa.
			cm.openDownloadPool()
			defer cm.closeDownloadPool()

			// Bucle de espera de autenticación para iniciar el manejador de actualizaciones (gaps)
			for {
				self, err := client.Self(runCtx)
				if err == nil && self != nil {
					log.Printf("[TG CLIENT] %s", i18n.T("telegram.sessionActive", self.FirstName, self.LastName, self.Username, self.ID))
					return gaps.Run(runCtx, client.API(), self.ID, updates.AuthOptions{
						IsBot: false,
					})
				}

				log.Printf("[TG CLIENT] %s", i18n.T("telegram.waitingAuth"))
				select {
				case <-runCtx.Done():
					return runCtx.Err()
				case <-time.After(5 * time.Second):
					// Reintentar check de Self()
				}
			}
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("[TG CLIENT ERROR] %s", i18n.T("telegram.runError", err))
		}
	}()

	return nil
}

func (cm *ClientManager) Stop() {
	cm.stopRunning()
}

func (cm *ClientManager) stopRunning() {
	cm.mu.Lock()
	cancel := cm.cancelRun
	cm.cancelRun = nil
	cm.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	cm.runWg.Wait()

	cm.mu.Lock()
	cm.client = nil
	cm.rawClient = nil
	cm.downloadPool = nil
	cm.downloadClient = nil
	cm.downloadPoolNotice = false
	cm.readyChan = nil
	cm.dcPools = make(map[int]telegram.CloseInvoker)
	cm.dcClients = make(map[int]*tg.Client)
	cm.primaryDC = 0
	cm.mu.Unlock()
}

// HaySesionGuardada informa si existe el fichero de sesión de Telegram en
// disco. El panel (pkg/server) usa este método para decidir si los endpoints
// de configuración de /api/auth/ pueden responder sin token SOLO mientras no
// haya sesión guardada: es el único momento en que configurar la cuenta no
// expone ni destruye nada ajeno (ver SECURITY_FIXPLAN.md §1.3, cierre de
// SEC-01). Nil-safe: un manejador nulo no tiene sesión.
func (cm *ClientManager) HaySesionGuardada() bool {
	if cm == nil {
		return false
	}
	// Copiar el path y soltar el candado ANTES de os.Stat: no se hace I/O con
	// el candado tomado, aunque la llamada sea barata (estilo SEC-18).
	cm.mu.RLock()
	sessionPath := cm.sessionPath
	cm.mu.RUnlock()

	// Cualquier error de stat (incluido «no existe») cuenta como «sin
	// sesión»: es la respuesta conservadora, porque hace exigir token.
	_, err := os.Stat(sessionPath)
	return err == nil
}

func (cm *ClientManager) GetAuthStatus(ctx context.Context) AuthStatus {
	cm.mu.RLock()
	client := cm.client
	apiID := cm.apiID
	apiHash := cm.apiHash
	cm.mu.RUnlock()

	if apiID == 0 || apiHash == "" || client == nil {
		return AuthStatus{
			Configured:     false,
			HasCredentials: false,
			Authorized:     false,
			Authenticated:  false,
			State:          "UNCONFIGURED",
		}
	}

	// Esperar brevemente a que el cliente MTProto esté conectado.
	// Si hay una sesión guardada, somos más pacientes (5s) porque es probable que conecte.
	// Si no, damos tiempo suficiente (3s) para la primera conexión.
	timeout := 3 * time.Second
	if _, err := os.Stat(cm.sessionPath); err == nil {
		timeout = 5 * time.Second
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := cm.WaitReady(waitCtx); err != nil {
		return AuthStatus{
			Configured:     true,
			HasCredentials: true,
			Authorized:     false,
			Authenticated:  false,
			State:          "NEED_PHONE",
		}
	}

	authClient := client.Auth()
	status, err := authClient.Status(ctx)
	if err != nil || !status.Authorized {
		return AuthStatus{
			Configured:     true,
			HasCredentials: true,
			Authorized:     false,
			Authenticated:  false,
			State:          "NEED_PHONE",
		}
	}

	user, err := client.Self(ctx)
	if err != nil {
		return AuthStatus{
			Configured:     true,
			HasCredentials: true,
			Authorized:     true,
			Authenticated:  true,
			State:          "LOGGED_IN",
		}
	}

	var colorID *int
	if pc, ok := user.Color.(*tg.PeerColor); ok {
		if c, ok := pc.GetColor(); ok {
			colorID = &c
		}
	}

	return AuthStatus{
		Configured:     true,
		HasCredentials: true,
		Authorized:     true,
		Authenticated:  true,
		State:          "LOGGED_IN",
		Phone:          user.Phone,
		User: &UserInfo{
			ID:        user.ID,
			FirstName: user.FirstName,
			LastName:  user.LastName,
			Username:  user.Username,
			Phone:     user.Phone,
			ColorID:   colorID,
		},
	}
}

func (cm *ClientManager) SendCode(ctx context.Context, phone string) (string, error) {
	cm.mu.Lock()
	client := cm.client
	cm.mu.Unlock()

	if client == nil {
		return "", errors.New(i18n.T("telegram.notConfigured"))
	}

	if err := cm.WaitReady(ctx); err != nil {
		return "", fmt.Errorf(i18n.T("telegram.connectError"), err)
	}

	phone = strings.TrimSpace(phone)
	sentCode, err := client.Auth().SendCode(ctx, phone, auth.SendCodeOptions{})
	if err != nil {
		return "", fmt.Errorf(i18n.T("telegram.sendCodeError"), err)
	}

	sc, ok := sentCode.(*tg.AuthSentCode)
	if !ok {
		return "", errors.New(i18n.T("telegram.unexpectedResponse"))
	}

	cm.mu.Lock()
	cm.phoneCodeHash = sc.PhoneCodeHash
	cm.pendingPhone = phone
	cm.mu.Unlock()

	return sc.PhoneCodeHash, nil
}

func (cm *ClientManager) VerifyCode(ctx context.Context, phone, code, phoneCodeHash string) (string, error) {
	cm.mu.Lock()
	client := cm.client
	if phoneCodeHash == "" {
		phoneCodeHash = cm.phoneCodeHash
	}
	if phone == "" {
		phone = cm.pendingPhone
	}
	cm.mu.Unlock()

	if client == nil {
		return "", errors.New(i18n.T("telegram.notConfigured"))
	}

	if err := cm.WaitReady(ctx); err != nil {
		return "", fmt.Errorf(i18n.T("telegram.connectError"), err)
	}

	_, err := client.Auth().SignIn(ctx, phone, code, phoneCodeHash)
	if err != nil {
		if errors.Is(err, auth.ErrPasswordAuthNeeded) || strings.Contains(strings.ToUpper(err.Error()), "SESSION_PASSWORD_NEEDED") {
			return "2fa_required", nil
		}
		return "", fmt.Errorf(i18n.T("telegram.invalidCode"), err)
	}

	return "ok", nil
}

func (cm *ClientManager) Verify2FA(ctx context.Context, password string) error {
	cm.mu.Lock()
	client := cm.client
	cm.mu.Unlock()

	if client == nil {
		return errors.New(i18n.T("telegram.notConfigured"))
	}

	if err := cm.WaitReady(ctx); err != nil {
		return fmt.Errorf(i18n.T("telegram.connectError"), err)
	}

	_, err := client.Auth().Password(ctx, password)
	if err != nil {
		return fmt.Errorf(i18n.T("telegram.invalid2FA"), err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Mensajes guardados
//
// El chat que Telegram llama "Mensajes guardados" es el chat del usuario
// consigo mismo (InputPeerSelf). Es el sitio natural para dejarse una nota que
// se quiere leer desde otro dispositivo: nadie más puede verla.
// ---------------------------------------------------------------------------

// messageRandomID genera el identificador que Telegram usa para descartar
// mensajes duplicados si una petición se reintenta.
func messageRandomID() (int64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, fmt.Errorf(i18n.T("telegram.generateIDError"), err)
	}
	return int64(binary.LittleEndian.Uint64(buf[:])), nil
}

// codeEntities marca fragmentos del mensaje para que Telegram los pinte como
// código: al tocarlos, la aplicación los copia al portapapeles de una vez, que
// es justo lo que hace falta para un token largo.
//
// Los desplazamientos de Telegram se cuentan en unidades UTF-16, no en bytes ni
// en caracteres, así que hay que convertir antes de medir: con un emoji delante
// (que ocupa dos unidades) cualquier otra cuenta desplazaría el resaltado.
func codeEntities(text string, fragments []string) []tg.MessageEntityClass {
	entities := make([]tg.MessageEntityClass, 0, len(fragments))
	for _, fragment := range fragments {
		if fragment == "" {
			continue
		}
		idx := strings.Index(text, fragment)
		if idx < 0 {
			continue
		}
		entities = append(entities, &tg.MessageEntityCode{
			Offset: len(utf16.Encode([]rune(text[:idx]))),
			Length: len(utf16.Encode([]rune(fragment))),
		})
	}
	return entities
}

// SendToSavedMessages publica un mensaje en los Mensajes guardados del usuario.
// Los fragmentos que se pasen en codeFragments se envían con formato de código
// para poder copiarlos con un toque desde el móvil.
func (cm *ClientManager) SendToSavedMessages(ctx context.Context, text string, codeFragments ...string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New(i18n.T("telegram.nothingToSend"))
	}

	cm.mu.RLock()
	client := cm.client
	cm.mu.RUnlock()

	if client == nil {
		return errors.New(i18n.T("telegram.notConfigured"))
	}
	if err := cm.WaitReady(ctx); err != nil {
		return fmt.Errorf(i18n.T("telegram.connectError"), err)
	}

	// Sin sesión iniciada la llamada fallaría con un error críptico de MTProto;
	// es mejor decir exactamente qué falta.
	if status, err := client.Auth().Status(ctx); err != nil || status == nil || !status.Authorized {
		return errors.New(i18n.T("telegram.noSessionStarted"))
	}

	cm.mu.RLock()
	raw := cm.rawClient
	cm.mu.RUnlock()
	if raw == nil {
		return errors.New("Telegram no está conectado")
	}

	randomID, err := messageRandomID()
	if err != nil {
		return err
	}

	req := &tg.MessagesSendMessageRequest{
		Peer:      &tg.InputPeerSelf{},
		Message:   text,
		RandomID:  randomID,
		NoWebpage: true,
	}
	if entities := codeEntities(text, codeFragments); len(entities) > 0 {
		req.SetEntities(entities)
	}

	if _, err := raw.MessagesSendMessage(ctx, req); err != nil {
		return fmt.Errorf(i18n.T("telegram.sendMessageError"), err)
	}
	return nil
}

// GetSelfInfo devuelve la ID del usuario de Telegram y su nombre de pantalla si la sesión está activa.
func (cm *ClientManager) GetSelfInfo(ctx context.Context) (int64, string, error) {
	cm.mu.RLock()
	client := cm.client
	cm.mu.RUnlock()

	if client == nil {
		return 0, "", errors.New("client not connected")
	}

	user, err := client.Self(ctx)
	if err != nil || user == nil {
		return 0, "", fmt.Errorf("could not get self user: %w", err)
	}

	name := user.FirstName
	if user.LastName != "" {
		name += " " + user.LastName
	}
	if user.Username != "" {
		name += " (@" + user.Username + ")"
	}

	return user.ID, name, nil
}

func (cm *ClientManager) Logout(ctx context.Context) error {
	cm.mu.Lock()
	client := cm.client
	rawClient := cm.rawClient
	cm.mu.Unlock()

	if rawClient != nil {
		_, _ = rawClient.AuthLogOut(ctx)
	} else if client != nil {
		_, _ = client.API().AuthLogOut(ctx)
	}

	cm.stopRunning()
	_ = os.Remove(cm.sessionPath)

	return nil
}
