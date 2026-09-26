package listener

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"

	"tgdown/pkg/config"
	"tgdown/pkg/downloader"
	"tgdown/pkg/logbus"
	"tgdown/pkg/storage"
	"tgdown/pkg/telegram"
)

type ListenerItem struct {
	ID        string `json:"id"`
	MessageID int64  `json:"message_id"`
	ChatID    int64  `json:"chat_id"`
	ChatName  string `json:"chat_name"`
	GroupName string `json:"group_name,omitempty"`
	TopicID   int64  `json:"topic_id,omitempty"`
	TopicName string `json:"topic_name,omitempty"`
	// SubFolder es la carpeta de destino de este archivo, relativa a la carpeta
	// de descargas. Viaja con el elemento para que descargarlo desde la bandeja
	// acabe en el mismo sitio que si se hubiera bajado solo.
	SubFolder        string  `json:"sub_folder,omitempty"`
	FileName         string  `json:"file_name"`
	CaptionFileName  string  `json:"caption_file_name,omitempty"`
	OriginalFileName string  `json:"original_file_name,omitempty"`
	Kind             string  `json:"kind"`
	TotalStr         string  `json:"total_str"`
	Status           string  `json:"status"` // "available"
	UpdatedAt        float64 `json:"updated_at"`
	CreatedAt        float64 `json:"created_at"`
}

type ListenerStateListener func(item ListenerItem)

type ListenerEngine struct {
	clientMgr *telegram.ClientManager
	storage   *storage.Storage
	engine    *downloader.Engine

	mu     sync.RWMutex
	config config.Config
	items  map[string]*ListenerItem
	// chatMap agrupa por ID de chat todas sus entradas de escucha: la del grupo
	// entero y/o una por cada tema vigilado.
	chatMap        map[int64][]config.ListenerChat
	topicNameTries map[string]bool
	stateListeners []ListenerStateListener
}

func NewListenerEngine(cm *telegram.ClientManager, st *storage.Storage, eng *downloader.Engine, cfg config.Config) *ListenerEngine {
	cfg = config.NormalizeConfig(cfg)
	le := &ListenerEngine{
		clientMgr:      cm,
		storage:        st,
		engine:         eng,
		config:         cfg,
		items:          make(map[string]*ListenerItem),
		chatMap:        make(map[int64][]config.ListenerChat),
		topicNameTries: make(map[string]bool),
	}

	le.updateChatMap(cfg)

	// Cargar items de escucha existentes desde SQLite (source='listener')
	if st != nil {
		if saved, err := st.LoadDownloads(""); err == nil {
			for id, d := range saved {
				if d.Source == "listener" && (d.Status == "available" || d.Status == "cancelled") {
					// Al reiniciar no sabemos de qué tema venía cada archivo: solo
					// se puede afinar cuando el grupo tiene una única entrada
					// vigilada. Con varias, se muestra el nombre del grupo.
					chatName := strconv.FormatInt(d.ChatID, 10)
					groupName := ""
					if entries := le.chatMap[d.ChatID]; len(entries) > 0 {
						groupName = entries[0].GroupName()
						chatName = groupName
						if len(entries) == 1 {
							chatName = entries[0].DisplayName()
						}
					}
					le.items[id] = &ListenerItem{
						ID:               d.ID,
						MessageID:        d.MessageID,
						ChatID:           d.ChatID,
						ChatName:         chatName,
						GroupName:        groupName,
						SubFolder:        d.SubFolder,
						FileName:         d.FileName,
						CaptionFileName:  d.CaptionFileName,
						OriginalFileName: d.OriginalFileName,
						Kind:             d.Kind,
						TotalStr:         d.TotalStr,
						Status:           d.Status,
						UpdatedAt:        d.UpdatedAt,
						CreatedAt:        d.CreatedAt,
					}
				}
			}
		}
	}

	// Conectar callback genérico en ClientManager
	if cm != nil {
		cm.SetMessageCallback(le.HandleMessage)
	}
	if eng != nil {
		// Mantener la bandeja de multimedia sincronizada con el motor real de
		// descargas después de pulsar "Descargar".
		eng.OnStateChange(func(download storage.DownloadItem) {
			if download.Source != "listener" {
				return
			}
			le.mu.Lock()
			item, exists := le.items[download.ID]
			if exists {
				item.Status = download.Status
				item.UpdatedAt = download.UpdatedAt
				if download.FileName != "" {
					item.FileName = download.FileName
				}
				cp := *item
				le.mu.Unlock()
				le.notifyState(cp)
				return
			}
			le.mu.Unlock()
		})
	}

	return le
}

func (le *ListenerEngine) OnStateChange(listener ListenerStateListener) {
	le.mu.Lock()
	defer le.mu.Unlock()
	le.stateListeners = append(le.stateListeners, listener)
}

func (le *ListenerEngine) notifyState(item ListenerItem) {
	le.mu.RLock()
	listeners := append([]ListenerStateListener(nil), le.stateListeners...)
	le.mu.RUnlock()

	for _, l := range listeners {
		func(listener ListenerStateListener) {
			defer func() { _ = recover() }()
			listener(item)
		}(l)
	}
}

func (le *ListenerEngine) updateChatMap(cfg config.Config) {
	le.chatMap = make(map[int64][]config.ListenerChat)
	for _, c := range cfg.ListenerChats {
		le.chatMap[c.ID] = append(le.chatMap[c.ID], c)
	}
}

func (le *ListenerEngine) UpdateConfig(cfg config.Config) {
	le.mu.Lock()
	defer le.mu.Unlock()
	le.config = config.NormalizeConfig(cfg)
	le.updateChatMap(le.config)
	// El detalle de qué cambió lo registra el servidor (logListenerConfigChanges)
	// con una línea por chat; volcar aquí el mapa entero solo llenaba el registro.
	logbus.Debug(logbus.CatListener,
		fmt.Sprintf("Configuración de escucha aplicada: activa=%v, %d chats vigilados",
			le.config.ListenerEnabled, len(le.chatMap)), "")
}

// chatIDCandidates enumera las formas en que un mismo chat puede estar anotado
// en la configuración: el ID canónico y, para canales, también el ID interno
// con y sin signo.
func chatIDCandidates(peerID int64, rawChannelID int64) []int64 {
	candidates := []int64{peerID}
	if rawChannelID != 0 {
		candidates = append(candidates, rawChannelID, -rawChannelID)
		if canonical, err := strconv.ParseInt(fmt.Sprintf("-100%d", rawChannelID), 10, 64); err == nil && canonical != peerID {
			candidates = append(candidates, canonical)
		}
	}
	return candidates
}

// topicMatches compara el tema configurado con el que trae el mensaje. En un
// grupo con temas, los mensajes del tema «General» llegan sin cabecera de tema,
// así que un 0 se interpreta como ese tema (el número 1).
func topicMatches(configured, msgTopicID int64) bool {
	if msgTopicID <= 0 {
		msgTopicID = config.GeneralTopicID
	}
	return configured == msgTopicID
}

// messageTopicID extrae de un mensaje el tema del foro al que pertenece.
// Devuelve 0 cuando el mensaje no está en un tema (grupo normal o tema
// «General», que Telegram no marca).
func messageTopicID(msg *tg.Message) int64 {
	if msg == nil {
		return 0
	}
	header, ok := msg.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || header == nil {
		return 0
	}
	// Sin esta marca no estamos ante un tema, sino ante una respuesta normal:
	// tomar su reply_to_msg_id como tema haría que cualquier respuesta pareciera
	// pertenecer a un tema inexistente.
	if !header.ForumTopic {
		return 0
	}
	// Al responder dentro de un tema, reply_to_top_id apunta al tema y
	// reply_to_msg_id al mensaje concreto. Cuando se escribe directamente en el
	// tema, solo viene reply_to_msg_id y ese es el tema.
	if topID, ok := header.GetReplyToTopID(); ok && topID != 0 {
		return int64(topID)
	}
	if msgID, ok := header.GetReplyToMsgID(); ok && msgID != 0 {
		return int64(msgID)
	}
	return 0
}

// matchChat busca la entrada de escucha que corresponde a un mensaje. Gana
// siempre la entrada más específica: si el grupo tiene vigilado ese tema
// concreto se usa esa, y solo si no hay ninguna se recurre a la entrada del
// grupo entero. Un grupo vigilado únicamente por temas ignora todo lo que
// llegue por temas distintos.
func (le *ListenerEngine) matchChat(peerID, rawChannelID, msgTopicID int64) (config.ListenerChat, bool) {
	var fallback config.ListenerChat
	haveFallback := false

	for _, candidate := range chatIDCandidates(peerID, rawChannelID) {
		entries, ok := le.chatMap[candidate]
		if !ok {
			continue
		}
		for _, chat := range entries {
			if chat.HasTopic() {
				if topicMatches(chat.Topic(), msgTopicID) {
					return chat, true
				}
				continue
			}
			if !haveFallback {
				fallback = chat
				haveFallback = true
			}
		}
	}

	return fallback, haveFallback
}

func entityChatName(entities tg.Entities, peerID, rawChannelID int64) string {
	if rawChannelID != 0 {
		if ch, ok := entities.Channels[rawChannelID]; ok && strings.TrimSpace(ch.Title) != "" {
			return strings.TrimSpace(ch.Title)
		}
	}

	if chatID := -peerID; peerID < 0 && !strings.HasPrefix(strconv.FormatInt(peerID, 10), "-100") {
		if chat, ok := entities.Chats[chatID]; ok && strings.TrimSpace(chat.Title) != "" {
			return strings.TrimSpace(chat.Title)
		}
	}

	if peerID > 0 {
		if user, ok := entities.Users[peerID]; ok {
			name := strings.TrimSpace(user.FirstName + " " + user.LastName)
			if name == "" {
				name = strings.TrimSpace(user.Username)
			}
			return name
		}
	}

	return ""
}

// carpetaDeChat devuelve la subcarpeta donde van los archivos de esta entrada
// de escucha, relativa a la carpeta de descargas, y cadena vacía si el reparto
// por chat está apagado.
//
// La carpeta se calcula una sola vez y se guarda en la configuración del chat.
// A partir de ahí es fija: si el canal se renombra en Telegram, sus archivos
// siguen cayendo todos en la misma carpeta en vez de repartirse en dos.
func (le *ListenerEngine) carpetaDeChat(chatCfg config.ListenerChat) string {
	le.mu.Lock()

	if !le.config.OrganizeByChat {
		le.mu.Unlock()
		return ""
	}

	clave := chatCfg.Key()
	var entrada *config.ListenerChat
	for i := range le.config.ListenerChats {
		if le.config.ListenerChats[i].Key() == clave {
			entrada = &le.config.ListenerChats[i]
			break
		}
	}

	// El chat dejó de estar configurado entre que llegó el mensaje y esta
	// llamada: se calcula la carpeta al vuelo y no se guarda nada.
	if entrada == nil {
		le.mu.Unlock()
		return carpetaAlVuelo(chatCfg)
	}

	cambiado := false
	if strings.TrimSpace(entrada.Folder) == "" {
		entrada.Folder = le.carpetaLibreDeGrupo(chatCfg)
		cambiado = true
	}
	if entrada.HasTopic() && strings.TrimSpace(entrada.TopicFolder) == "" {
		entrada.TopicFolder = le.carpetaLibreDeTema(*entrada)
		cambiado = true
	}

	ruta := entrada.CarpetaRelativa()
	if cambiado {
		le.updateChatMap(le.config)
	}
	cfg := le.config
	le.mu.Unlock()

	if cambiado && le.storage != nil {
		go func() {
			if err := le.storage.SaveConfig(cfg); err != nil {
				log.Printf("[LISTENER] error guardando la carpeta del chat en BD: %v", err)
			}
		}()
	}

	return ruta
}

// carpetaLibreDeGrupo elige el nombre de carpeta del grupo. Se llama con le.mu
// tomado.
func (le *ListenerEngine) carpetaLibreDeGrupo(chatCfg config.ListenerChat) string {
	// El grupo entero y cada uno de sus temas son entradas distintas, pero
	// comparten la carpeta del grupo: si otra entrada del mismo chat ya tiene
	// una, se reusa.
	for _, otro := range le.config.ListenerChats {
		if otro.ID == chatCfg.ID && strings.TrimSpace(otro.Folder) != "" {
			return otro.Folder
		}
	}

	nombre := strings.TrimSpace(chatCfg.Name)
	if esSoloNumero(nombre) {
		// Todavía no sabemos el título del chat: lo que hay es su propio ID.
		return downloader.CarpetaPorID(chatCfg.ID)
	}

	carpeta := downloader.NombreCarpetaChat(nombre, chatCfg.ID)
	for _, otro := range le.config.ListenerChats {
		if otro.ID == chatCfg.ID {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(otro.Folder), carpeta) {
			// Dos chats distintos con el mismo título: al segundo se le pone su
			// ID detrás para que no compartan destino.
			return downloader.NombreCarpetaConID(carpeta, chatCfg.ID)
		}
	}
	return carpeta
}

// carpetaLibreDeTema elige el nombre de la subcarpeta del tema, que cuelga de
// la del grupo. Se llama con le.mu tomado.
func (le *ListenerEngine) carpetaLibreDeTema(entrada config.ListenerChat) string {
	carpeta := downloader.NombreCarpetaTema(entrada.TopicLabel(), entrada.Topic())
	for _, otro := range le.config.ListenerChats {
		if otro.ID != entrada.ID || otro.Topic() == entrada.Topic() {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(otro.TopicFolder), carpeta) {
			// «tema_<id>» sí es único dentro del grupo.
			return downloader.NombreCarpetaTema("", entrada.Topic())
		}
	}
	return carpeta
}

// carpetaAlVuelo calcula la carpeta sin mirar ni tocar la configuración.
func carpetaAlVuelo(chatCfg config.ListenerChat) string {
	nombre := strings.TrimSpace(chatCfg.Name)
	var carpeta string
	if esSoloNumero(nombre) {
		carpeta = downloader.CarpetaPorID(chatCfg.ID)
	} else {
		carpeta = downloader.NombreCarpetaChat(nombre, chatCfg.ID)
	}
	if chatCfg.HasTopic() {
		return filepath.Join(carpeta, downloader.NombreCarpetaTema(chatCfg.TopicLabel(), chatCfg.Topic()))
	}
	return carpeta
}

// esSoloNumero indica si el «título» del chat es en realidad su ID, que es lo
// que se guarda mientras Telegram no nos haya dicho cómo se llama.
func esSoloNumero(nombre string) bool {
	nombre = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(nombre), "-"))
	if nombre == "" {
		return true
	}
	for _, r := range nombre {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// prepararCarpeta crea la carpeta de destino y deja dentro, la primera vez, un
// «_nombre.txt» con el título real del chat. Es lo que permite reconocer una
// carpeta «chat_1001234…» desde el Explorador sin abrir el programa.
func (le *ListenerEngine) prepararCarpeta(sub string, chatCfg config.ListenerChat) {
	if strings.TrimSpace(sub) == "" {
		return
	}

	le.mu.RLock()
	base := le.config.DownloadFolder
	le.mu.RUnlock()
	if strings.TrimSpace(base) == "" {
		return
	}

	sub = downloader.RutaRelativaSegura(sub)
	if sub == "" {
		return
	}

	ruta := filepath.Join(base, sub)
	if err := os.MkdirAll(ruta, 0o755); err != nil {
		log.Printf("[LISTENER] no se pudo crear la carpeta %s: %v", ruta, err)
		return
	}

	aviso := filepath.Join(ruta, "_nombre.txt")
	if _, err := os.Stat(aviso); err == nil {
		return
	}
	contenido := downloader.TextoCarpetaChat(chatCfg.GroupName(), chatCfg.ID, chatCfg.TopicLabel())
	if err := os.WriteFile(aviso, []byte(contenido), 0o644); err != nil {
		log.Printf("[LISTENER] no se pudo escribir %s: %v", aviso, err)
	}
}

func (le *ListenerEngine) rememberChatName(peerID, rawChannelID int64, name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}

	le.mu.Lock()
	updated := false
	for i := range le.config.ListenerChats {
		chat := &le.config.ListenerChats[i]
		if chat.ID == peerID || (rawChannelID != 0 && chat.ID == rawChannelID) {
			if chat.Name != name {
				chat.Name = name
				updated = true
			}
		}
	}
	if updated {
		le.updateChatMap(le.config)
	}
	cfg := le.config
	le.mu.Unlock()

	// Alimentamos siempre la caché de títulos: es la que permite que el registro
	// muestre el nombre del chat en vez de su ID.
	if le.clientMgr != nil {
		le.clientMgr.RememberChatName(peerID, name)
	}

	if updated && le.storage != nil {
		go func() {
			if err := le.storage.SaveConfig(cfg); err != nil {
				log.Printf("[LISTENER] error guardando configuración en BD: %v", err)
			}
		}()
	}
}

// ensureTopicName pregunta a Telegram el título de un tema vigilado cuando la
// configuración todavía no lo tiene, y lo guarda. Se intenta una sola vez por
// tema y en segundo plano, para no retrasar el manejo del mensaje que lo
// disparó ni repetir la consulta con cada archivo que llegue.
func (le *ListenerEngine) ensureTopicName(ctx context.Context, chat config.ListenerChat) {
	if !chat.HasTopic() || le.clientMgr == nil {
		return
	}

	key := chat.Key()
	le.mu.Lock()
	if le.topicNameTries == nil {
		le.topicNameTries = make(map[string]bool)
	}
	if le.topicNameTries[key] {
		le.mu.Unlock()
		return
	}
	le.topicNameTries[key] = true
	le.mu.Unlock()

	chatID := chat.ID
	topicID := chat.Topic()
	go func() {
		// El contexto del mensaje se cancela en cuanto se atiende; para una
		// consulta que va por libre hace falta uno propio.
		reqCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()

		name, err := le.ResolveTopicName(reqCtx, chatID, topicID)
		if err != nil || strings.TrimSpace(name) == "" {
			return
		}
		le.rememberTopicName(chatID, topicID, name)
	}()
}

// rememberTopicName guarda el título de un tema en la configuración y lo
// persiste, igual que se hace con el nombre del grupo.
func (le *ListenerEngine) rememberTopicName(chatID, topicID int64, name string) {
	name = strings.TrimSpace(name)
	if name == "" || topicID <= 0 {
		return
	}

	le.mu.Lock()
	updated := false
	for i := range le.config.ListenerChats {
		chat := &le.config.ListenerChats[i]
		if chat.ID == chatID && chat.Topic() == topicID && chat.TopicName != name {
			chat.TopicName = name
			updated = true
		}
	}
	if updated {
		le.updateChatMap(le.config)
	}
	cfg := le.config
	le.mu.Unlock()

	if updated && le.storage != nil {
		if err := le.storage.SaveConfig(cfg); err != nil {
			log.Printf("[LISTENER] error guardando configuración en BD: %v", err)
		}
	}
}

// ChatName devuelve el nombre configurado de un chat vigilado, o cadena vacía
// si ese chat no está en la lista de escucha. Es el nombre del grupo, sin el
// tema: quien lo usa (el registro de actividad) solo conoce el ID del chat.
func (le *ListenerEngine) ChatName(chatID int64) string {
	le.mu.RLock()
	defer le.mu.RUnlock()
	for _, chat := range le.chatMap[chatID] {
		name := strings.TrimSpace(chat.Name)
		if name != "" && name != strconv.FormatInt(chatID, 10) {
			return name
		}
	}
	return ""
}

func (le *ListenerEngine) GetItems() []ListenerItem {
	le.mu.RLock()
	defer le.mu.RUnlock()

	res := make([]ListenerItem, 0, len(le.items))
	for _, item := range le.items {
		res = append(res, *item)
	}
	// Orden cronológico de llegada: el primero que llega se muestra primero y
	// los nuevos se van añadiendo debajo. Si dos archivos comparten marca de
	// tiempo (llegan en el mismo instante), se desempata por el ID de mensaje,
	// que Telegram asigna de forma creciente dentro de cada chat.
	sort.SliceStable(res, func(i, j int) bool {
		if res[i].CreatedAt != res[j].CreatedAt {
			return res[i].CreatedAt < res[j].CreatedAt
		}
		if res[i].ChatID == res[j].ChatID && res[i].MessageID != res[j].MessageID {
			return res[i].MessageID < res[j].MessageID
		}
		return res[i].ID < res[j].ID
	})
	return res
}

func (le *ListenerEngine) HandleMessage(ctx context.Context, entities tg.Entities, msg *tg.Message) error {
	le.mu.RLock()
	enabled := le.config.ListenerEnabled
	le.mu.RUnlock()

	if msg == nil {
		return nil
	}

	// Obtener únicamente el ID del chat que contiene el mensaje.
	// El remitente no debe usarse para decidir si el chat está vigilado:
	// un usuario puede publicar en grupos o canales no configurados.
	var peerID int64
	var rawChannelID int64
	switch p := msg.PeerID.(type) {
	case *tg.PeerChannel:
		peerID, _ = strconv.ParseInt(fmt.Sprintf("-100%d", p.ChannelID), 10, 64)
		rawChannelID = p.ChannelID
	case *tg.PeerChat:
		peerID = -p.ChatID
	case *tg.PeerUser:
		peerID = p.UserID
	}

	if !enabled {
		return nil
	}
	if msg.Out {
		return nil
	}

	// El tema al que pertenece el mensaje decide si nos interesa: un grupo
	// vigilado por temas solo acepta los suyos.
	msgTopicID := messageTopicID(msg)

	le.mu.RLock()
	chatCfg, watched := le.matchChat(peerID, rawChannelID, msgTopicID)
	le.mu.RUnlock()

	if !watched {
		return nil
	}

	if actualName := entityChatName(entities, peerID, rawChannelID); actualName != "" {
		chatCfg.Name = actualName
		le.rememberChatName(peerID, rawChannelID, actualName)
	}
	if chatCfg.Name == "" {
		chatCfg.Name = strconv.FormatInt(peerID, 10)
	}

	// Si el tema se configuró sin nombre (por ejemplo pegando un enlace), se
	// pregunta a Telegram una sola vez y se guarda para los siguientes archivos.
	if chatCfg.HasTopic() && strings.TrimSpace(chatCfg.TopicName) == "" {
		le.ensureTopicName(ctx, chatCfg)
	}

	// Nombre del tema seguido del nombre del grupo, para saber de dónde viene.
	chatName := chatCfg.DisplayName()

	// Un mensaje sin multimedia (texto, encuesta, servicio...) simplemente no
	// interesa a la escucha: no se registra nada para no llenar el log.
	mediaInfo := downloader.ExtractMediaInfo(msg)
	if mediaInfo == nil {
		return nil
	}

	// Filtros por tipo de medio. Cuando un archivo se descarta por filtro sí se
	// deja constancia (en nivel detalle), porque explica por qué no apareció.
	allowed := true
	switch mediaInfo.Kind {
	case downloader.KindPhoto:
		allowed = chatCfg.FPhotos
	case downloader.KindVideo:
		allowed = chatCfg.FVideos
	case downloader.KindSong:
		allowed = chatCfg.FAudios
	case downloader.KindSticker:
		allowed = chatCfg.FStickers
	case downloader.KindFile:
		allowed = chatCfg.FDocs
	}
	if !allowed {
		logbus.Debug(logbus.CatListener,
			fmt.Sprintf("Archivo omitido por filtros: %s", mediaInfo.FileName),
			fmt.Sprintf("Chat: %s · Mensaje: %d · Tipo: %s desactivado", chatName, msg.ID, mediaInfo.Kind))
		return nil
	}

	// Subcarpeta de destino. Vacía cuando el reparto por chat está apagado, y
	// entonces el archivo cae en la carpeta de descargas de siempre.
	subCarpeta := le.carpetaDeChat(chatCfg)

	itemID := fmt.Sprintf("listener:%d:%d", peerID, msg.ID)
	// Mayor precisión para evitar colisiones cuando llegan varios archivos en
	// el mismo segundo: así la bandeja conserva el orden real de llegada.
	now := float64(time.Now().UnixNano()) / 1e9

	dlItem := storage.DownloadItem{
		ID:         itemID,
		JobID:      fmt.Sprintf("listener:%d", peerID),
		MessageID:  int64(msg.ID),
		ChatID:     peerID,
		FileName:   mediaInfo.FileName,
		Status:     "available",
		Kind:       string(mediaInfo.Kind),
		TotalStr:   config.FormatBytes(float64(mediaInfo.FileSize)),
		TotalBytes: mediaInfo.FileSize,
		SubFolder:  subCarpeta,
		Source:     "listener",
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if chatCfg.AutoDownload {
		// La carpeta se crea solo cuando el archivo va a bajarse de verdad: un
		// mensaje que se queda en la bandeja no deja carpetas vacías por ahí.
		go le.prepararCarpeta(subCarpeta, chatCfg)

		dlItem.Status = "queued"
		if le.storage != nil {
			if err := le.storage.SaveDownload(dlItem); err != nil {
				log.Printf("[LISTENER] error guardando estado de descarga en BD: %v", err)
			}
		}
		if le.engine != nil {
			le.engine.QueueItem(dlItem)
		}
	} else {
		if le.storage != nil {
			if err := le.storage.SaveDownload(dlItem); err != nil {
				log.Printf("[LISTENER] error guardando estado de descarga en BD: %v", err)
			}
		}
		item := &ListenerItem{
			ID:               itemID,
			MessageID:        int64(msg.ID),
			ChatID:           peerID,
			ChatName:         chatName,
			GroupName:        chatCfg.GroupName(),
			TopicID:          chatCfg.Topic(),
			TopicName:        chatCfg.TopicLabel(),
			SubFolder:        subCarpeta,
			FileName:         mediaInfo.FileName,
			CaptionFileName:  mediaInfo.CaptionFileName,
			OriginalFileName: mediaInfo.OriginalFileName,
			Kind:             string(mediaInfo.Kind),
			TotalStr:         config.FormatBytes(float64(mediaInfo.FileSize)),
			Status:           "available",
			UpdatedAt:        now,
			CreatedAt:        now,
		}

		le.mu.Lock()
		le.items[itemID] = item
		le.mu.Unlock()
		le.notifyState(*item)
	}

	return nil
}

func (le *ListenerEngine) DownloadItem(itemID string) error {
	le.mu.Lock()
	item, ok := le.items[itemID]
	if ok {
		// Al añadirlo a descargas, deja de ser un elemento pendiente de la
		// bandeja. El progreso continúa visible en el monitor de descargas.
		delete(le.items, itemID)

		le.mu.Unlock()

		go le.prepararCarpeta(item.SubFolder, config.ListenerChat{
			ID:        item.ChatID,
			Name:      item.GroupName,
			TopicID:   config.TopicPointer(item.TopicID),
			TopicName: item.TopicName,
		})

		dlItem := storage.DownloadItem{
			ID:               item.ID,
			JobID:            fmt.Sprintf("listener:%d", item.ChatID),
			MessageID:        item.MessageID,
			ChatID:           item.ChatID,
			FileName:         item.FileName,
			CaptionFileName:  item.CaptionFileName,
			OriginalFileName: item.OriginalFileName,
			Status:           "queued",
			Kind:             item.Kind,
			TotalStr:         item.TotalStr,
			SubFolder:        item.SubFolder,
			Source:           "listener",
			CreatedAt:        float64(time.Now().Unix()),
			UpdatedAt:        float64(time.Now().Unix()),
		}

		if le.storage != nil {
			if err := le.storage.SaveDownload(dlItem); err != nil {
				log.Printf("[LISTENER] error guardando estado de descarga en BD: %v", err)
			}
		}
		if le.engine != nil {
			le.engine.QueueItem(dlItem)
		}
		return nil
	}
	le.mu.Unlock()

	// Si no está en items pero está en SQLite
	if le.storage != nil {
		if saved, err := le.storage.LoadDownloads(""); err == nil {
			if dl, exists := saved[itemID]; exists {
				dl.Status = "queued"
				if err := le.storage.SaveDownload(dl); err != nil {
					log.Printf("[LISTENER] error guardando estado de descarga en BD: %v", err)
				}
				if le.engine != nil {
					le.engine.QueueItem(dl)
				}
				return nil
			}
		}
	}

	return errors.New("multimedia no encontrado en escucha")
}

func (le *ListenerEngine) RemoveItem(itemID string) {
	le.mu.Lock()
	item, ok := le.items[itemID]
	if ok {
		delete(le.items, itemID)
	}
	le.mu.Unlock()

	if le.storage != nil {
		if err := le.storage.DeleteDownload(itemID); err != nil {
			log.Printf("[LISTENER] error eliminando descarga de BD: %v", err)
		}
		if err := le.storage.DeleteChunks(itemID); err != nil {
			log.Printf("[LISTENER] error eliminando chunks de BD: %v", err)
		}
	}
	if le.engine != nil {
		_ = le.engine.DeleteDownload(itemID, false)
	}

	if ok && item != nil {
		le.notifyState(*item)
	}
}

func (le *ListenerEngine) UpdateItemFileName(itemID string, newFileName string) error {
	le.mu.Lock()
	item, ok := le.items[itemID]
	if !ok {
		le.mu.Unlock()
		return fmt.Errorf("item no encontrado: %s", itemID)
	}
	item.FileName = newFileName
	cp := *item
	le.mu.Unlock()

	// Actualizar en storage
	if le.storage != nil {
		if err := le.storage.UpdateDownloadFileName(itemID, newFileName); err != nil {
			return fmt.Errorf("error actualizando nombre en BD: %w", err)
		}
	}

	le.notifyState(cp)
	return nil
}

func (le *ListenerEngine) ClearItems() {
	le.mu.Lock()
	ids := make([]string, 0, len(le.items))
	for id := range le.items {
		ids = append(ids, id)
	}
	le.items = make(map[string]*ListenerItem)
	le.mu.Unlock()

	for _, id := range ids {
		if le.storage != nil {
			if err := le.storage.DeleteDownload(id); err != nil {
				log.Printf("[LISTENER] error eliminando descarga de BD: %v", err)
			}
			if err := le.storage.DeleteChunks(id); err != nil {
				log.Printf("[LISTENER] error eliminando chunks de BD: %v", err)
			}
		}
		if le.engine != nil {
			_ = le.engine.DeleteDownload(id, false)
		}
	}
	le.notifyState(ListenerItem{})
}

type ResolvedChatInfo struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Username string `json:"username,omitempty"`
	// IsForum indica que el supergrupo tiene temas, así que el panel puede
	// ofrecer la lista de temas en lugar de vigilar el grupo entero.
	IsForum bool `json:"is_forum"`
}

// ForumTopicInfo es un tema de un grupo, tal y como lo necesita el panel para
// dejar elegir cuál vigilar.
type ForumTopicInfo struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Closed bool   `json:"closed,omitempty"`
}

// ResolveChat consulta a Telegram los datos de un chat y, de paso, guarda su
// título en la caché del cliente para que el registro de actividad pueda
// mostrar nombres en lugar de IDs.
func (le *ListenerEngine) ResolveChat(ctx context.Context, chatID int64) (ResolvedChatInfo, error) {
	info, err := le.resolveChat(ctx, chatID)
	if le.clientMgr != nil && info.Name != "" && info.Name != strconv.FormatInt(chatID, 10) {
		le.clientMgr.RememberChatName(chatID, info.Name)
	}
	return info, err
}

func (le *ListenerEngine) resolveChat(ctx context.Context, chatID int64) (ResolvedChatInfo, error) {
	raw := le.clientMgr.RawClient()
	if raw == nil {
		return ResolvedChatInfo{ID: chatID, Name: strconv.FormatInt(chatID, 10), Type: "chat"}, errors.New("cliente no conectado")
	}

	info := ResolvedChatInfo{
		ID:   chatID,
		Name: strconv.FormatInt(chatID, 10),
		Type: "chat",
	}

	// 1. Si es canal o supergrupo (-100...) o grupo básico
	if chatID < 0 {
		channelID := -chatID
		s := fmt.Sprintf("%d", chatID)
		isMegagroupOrChannel := strings.HasPrefix(s, "-100") && len(s) > 4
		if isMegagroupOrChannel {
			if parsed, err := strconv.ParseInt(s[4:], 10, 64); err == nil {
				channelID = parsed
			}
		}

		if isMegagroupOrChannel {
			accessHash, found := le.clientMgr.GetChannelAccessHash(channelID)
			if !found {
				_ = le.clientMgr.FetchDialogs(ctx)
				accessHash, _ = le.clientMgr.GetChannelAccessHash(channelID)
			}

			res, err := raw.ChannelsGetChannels(ctx, []tg.InputChannelClass{
				&tg.InputChannel{ChannelID: channelID, AccessHash: accessHash},
			})
			if err == nil {
				chats := res.GetChats()
				if len(chats) > 0 {
					if ch, ok := chats[0].(*tg.Channel); ok {
						info.Name = ch.Title
						info.Username = ch.Username
						info.IsForum = ch.Forum
						if ch.Megagroup {
							info.Type = "supergroup"
						} else {
							info.Type = "channel"
						}
						return info, nil
					}
				}
			}

			// Si Telegram aún no entregó el access hash del canal, la consulta
			// directa puede fallar aunque el canal sí esté en los diálogos del
			// usuario. Los diálogos incluyen el título y permiten resolverlo de
			// inmediato al añadirlo a la lista.
			if resDialogs, errDialogs := raw.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
				OffsetPeer: &tg.InputPeerEmpty{},
				Limit:      100,
			}); errDialogs == nil {
				var chats []tg.ChatClass
				switch dialogs := resDialogs.(type) {
				case *tg.MessagesDialogs:
					chats = dialogs.Chats
				case *tg.MessagesDialogsSlice:
					chats = dialogs.Chats
				}
				for _, chat := range chats {
					if ch, ok := chat.(*tg.Channel); ok && ch.ID == channelID {
						info.Name = ch.Title
						info.Username = ch.Username
						info.IsForum = ch.Forum
						if ch.Megagroup {
							info.Type = "supergroup"
						} else {
							info.Type = "channel"
						}
						return info, nil
					}
				}
			}
		} else {
			// Grupo básico
			res, err := raw.MessagesGetChats(ctx, []int64{-chatID})
			if err == nil {
				chats := res.GetChats()
				if len(chats) > 0 {
					if ch, ok := chats[0].(*tg.Chat); ok {
						info.Name = ch.Title
						info.Type = "group"
						return info, nil
					}
				}
			}
		}
	} else {
		// 2. ID positivo: Usuario o Bot (o ID de canal sin -100)
		accessHash, found := le.clientMgr.GetUserAccessHash(chatID)
		if !found {
			_ = le.clientMgr.FetchDialogs(ctx)
			accessHash, _ = le.clientMgr.GetUserAccessHash(chatID)
		}

		res, err := raw.UsersGetUsers(ctx, []tg.InputUserClass{
			&tg.InputUser{UserID: chatID, AccessHash: accessHash},
		})
		if err == nil && len(res) > 0 {
			if u, ok := res[0].(*tg.User); ok {
				name := strings.TrimSpace(u.FirstName + " " + u.LastName)
				if name == "" {
					name = u.Username
				}
				if name != "" {
					info.Name = name
				}
				info.Username = u.Username
				if u.Bot {
					info.Type = "bot"
				} else {
					info.Type = "user"
				}
				return info, nil
			}
		}

		// Probar si era un canal pasado sin -100
		accessHash, found = le.clientMgr.GetChannelAccessHash(chatID)
		if found {
			resCh, errCh := raw.ChannelsGetChannels(ctx, []tg.InputChannelClass{
				&tg.InputChannel{ChannelID: chatID, AccessHash: accessHash},
			})
			if errCh == nil && len(resCh.GetChats()) > 0 {
				if ch, ok := resCh.GetChats()[0].(*tg.Channel); ok {
					info.Name = ch.Title
					info.Username = ch.Username
					info.IsForum = ch.Forum
					if ch.Megagroup {
						info.Type = "supergroup"
					} else {
						info.Type = "channel"
					}
					return info, nil
				}
			}
		}
	}

	return info, nil
}

// inputPeer construye el peer de MTProto para un ID de chat canónico,
// recuperando el access hash de la caché del cliente (y refrescando los
// diálogos si todavía no lo tenemos).
func (le *ListenerEngine) inputPeer(ctx context.Context, chatID int64) (tg.InputPeerClass, error) {
	if le.clientMgr == nil || le.clientMgr.RawClient() == nil {
		return nil, errors.New("cliente no conectado")
	}

	if chatID > 0 {
		accessHash, found := le.clientMgr.GetUserAccessHash(chatID)
		if !found {
			_ = le.clientMgr.FetchDialogs(ctx)
			accessHash, _ = le.clientMgr.GetUserAccessHash(chatID)
		}
		return &tg.InputPeerUser{UserID: chatID, AccessHash: accessHash}, nil
	}

	s := strconv.FormatInt(chatID, 10)
	if !strings.HasPrefix(s, "-100") || len(s) <= 4 {
		// Grupo básico: no admite temas, pero devolvemos un peer válido para que
		// quien llame decida qué hacer.
		return &tg.InputPeerChat{ChatID: -chatID}, nil
	}

	channelID, err := strconv.ParseInt(s[4:], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("ID de canal inválido: %s", s)
	}
	accessHash, found := le.clientMgr.GetChannelAccessHash(channelID)
	if !found {
		_ = le.clientMgr.FetchDialogs(ctx)
		accessHash, _ = le.clientMgr.GetChannelAccessHash(channelID)
	}
	return &tg.InputPeerChannel{ChannelID: channelID, AccessHash: accessHash}, nil
}

// maxForumTopics acota cuántos temas se traen para el desplegable del panel.
// Un grupo con más temas que esto es rarísimo y paginar aquí solo complicaría
// la vista sin aportar nada.
const maxForumTopics = 200

// ResolveTopics devuelve los temas de un grupo con temas, para que el panel
// deje elegir cuál vigilar. Si el grupo no tiene temas, la lista viene vacía.
func (le *ListenerEngine) ResolveTopics(ctx context.Context, chatID int64) ([]ForumTopicInfo, error) {
	if le.clientMgr == nil {
		return nil, errors.New("cliente no conectado")
	}
	raw := le.clientMgr.RawClient()
	if raw == nil {
		return nil, errors.New("cliente no conectado")
	}

	peer, err := le.inputPeer(ctx, chatID)
	if err != nil {
		return nil, err
	}
	if _, isChannel := peer.(*tg.InputPeerChannel); !isChannel {
		return []ForumTopicInfo{}, nil
	}

	res, err := raw.MessagesGetForumTopics(ctx, &tg.MessagesGetForumTopicsRequest{
		Peer:  peer,
		Limit: maxForumTopics,
	})
	if err != nil {
		return nil, fmt.Errorf("no se pudieron obtener los temas: %w", err)
	}

	topics := make([]ForumTopicInfo, 0, len(res.Topics))
	for _, t := range res.Topics {
		topic, ok := t.(*tg.ForumTopic)
		if !ok {
			continue
		}
		topics = append(topics, ForumTopicInfo{
			ID:     int64(topic.ID),
			Name:   strings.TrimSpace(topic.Title),
			Closed: topic.Closed,
		})
	}
	return topics, nil
}

// ResolveTopicName devuelve el título de un tema concreto.
func (le *ListenerEngine) ResolveTopicName(ctx context.Context, chatID, topicID int64) (string, error) {
	if le.clientMgr == nil {
		return "", errors.New("cliente no conectado")
	}
	raw := le.clientMgr.RawClient()
	if raw == nil {
		return "", errors.New("cliente no conectado")
	}
	if topicID <= 0 {
		return "", nil
	}

	peer, err := le.inputPeer(ctx, chatID)
	if err != nil {
		return "", err
	}
	if _, isChannel := peer.(*tg.InputPeerChannel); !isChannel {
		return "", nil
	}

	res, err := raw.MessagesGetForumTopicsByID(ctx, &tg.MessagesGetForumTopicsByIDRequest{
		Peer:   peer,
		Topics: []int{int(topicID)},
	})
	if err != nil {
		return "", fmt.Errorf("no se pudo obtener el tema %d: %w", topicID, err)
	}

	for _, t := range res.Topics {
		if topic, ok := t.(*tg.ForumTopic); ok && int64(topic.ID) == topicID {
			return strings.TrimSpace(topic.Title), nil
		}
	}
	return "", fmt.Errorf("el tema %d no existe en este grupo", topicID)
}
