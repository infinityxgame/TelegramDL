package telegram

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"

	"tgdown/pkg/config"
)

type UserInfo struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
	Phone     string `json:"phone"`
	ColorID   *int   `json:"color_id,omitempty"`
}

type AuthStatus struct {
	Configured     bool      `json:"configured"`
	HasCredentials bool      `json:"has_credentials"`
	Authorized     bool      `json:"authorized"`
	Authenticated  bool      `json:"authenticated"`
	State          string    `json:"state"`
	Phone          string    `json:"phone,omitempty"`
	APIID          string    `json:"api_id,omitempty"`
	APIHash        string    `json:"api_hash,omitempty"`
	User           *UserInfo `json:"user,omitempty"`
}

type ClientManager struct {
	apiID                int
	apiHash              string
	client               *telegram.Client
	rawClient            *tg.Client
	sessionPath          string
	cancelRun            context.CancelFunc
	runWg                sync.WaitGroup
	mu                   sync.RWMutex
	readyChan            chan struct{}
	phoneCodeHash        string
	pendingPhone         string
	dispatcher           tg.UpdateDispatcher
	dispatcherRegistered bool
	channelAccessHashes  map[int64]int64
	userAccessHashes     map[int64]int64
	onGenericMessage     func(ctx context.Context, entities tg.Entities, msg *tg.Message) error
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

func (cm *ClientManager) SetMessageCallback(cb func(ctx context.Context, entities tg.Entities, msg *tg.Message) error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.onGenericMessage = cb
}

func (cm *ClientManager) cacheEntities(entities tg.Entities) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for id, ch := range entities.Channels {
		cm.channelAccessHashes[id] = ch.AccessHash
	}
	for id, u := range entities.Users {
		cm.userAccessHashes[id] = u.AccessHash
	}
}

func (cm *ClientManager) SetChannelAccessHash(channelID int64, accessHash int64) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.channelAccessHashes[channelID] = accessHash
}

func (cm *ClientManager) SetUserAccessHash(userID int64, accessHash int64) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.userAccessHashes[userID] = accessHash
}

func (cm *ClientManager) FetchDialogs(ctx context.Context) error {
	cm.mu.RLock()
	raw := cm.rawClient
	cm.mu.RUnlock()
	if raw == nil {
		return errors.New("cliente no conectado")
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
			if ch, ok := c.(*tg.Channel); ok {
				cm.channelAccessHashes[ch.ID] = ch.AccessHash
			}
		}
		for _, u := range users {
			if usr, ok := u.(*tg.User); ok {
				cm.userAccessHashes[usr.ID] = usr.AccessHash
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
		return 0, errors.New("cliente no conectado")
	}

	username = strings.TrimPrefix(username, "@")
	res, err := raw.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{
		Username: username,
	})
	if err != nil {
		return 0, fmt.Errorf("no se pudo resolver @%s: %w", username, err)
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	for _, c := range res.Chats {
		if ch, ok := c.(*tg.Channel); ok {
			cm.channelAccessHashes[ch.ID] = ch.AccessHash
		}
	}
	for _, u := range res.Users {
		if usr, ok := u.(*tg.User); ok {
			cm.userAccessHashes[usr.ID] = usr.AccessHash
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

	return 0, errors.New("tipo de chat desconocido")
}

func (cm *ClientManager) WaitReady(ctx context.Context) error {
	cm.mu.RLock()
	ready := cm.readyChan
	client := cm.client
	cm.mu.RUnlock()

	if client == nil {
		return errors.New("cliente no configurado")
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
		return errors.New("tiempo de espera agotado conectando a Telegram")
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
	// Detener la sesión anterior antes de tomar el mutex. La goroutine de
	// Telegram puede necesitarlo mientras termina.
	cm.stopRunning()

	cm.mu.Lock()
	defer cm.mu.Unlock()

	apiIDStr = strings.TrimSpace(apiIDStr)
	apiHash = strings.TrimSpace(apiHash)
	if apiIDStr == "" || apiHash == "" {
		cm.apiID = 0
		cm.apiHash = ""
		cm.client = nil
		cm.rawClient = nil
		return nil
	}

	id, err := strconv.Atoi(apiIDStr)
	if err != nil {
		return fmt.Errorf("API_ID inválido: %w", err)
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
			SystemVersion: "Windows 11",
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
		log.Printf("[TG CLIENT] Iniciando conexión con Telegram MTProto (API_ID: %d)...", id)
		err := client.Run(ctx, func(runCtx context.Context) error {
			readyOnce.Do(func() {
				close(readyChan)
				go func() {
					time.Sleep(300 * time.Millisecond)
					_ = cm.FetchDialogs(context.Background())
				}()
			})

			log.Printf("[TG CLIENT] Conexión MTProto establecida exitosamente.")
			self, err := client.Self(runCtx)
			if err == nil && self != nil {
				log.Printf("[TG CLIENT] Sesión activa como: %s %s (@%s, ID: %d). Iniciando gaps manager...", self.FirstName, self.LastName, self.Username, self.ID)
				return gaps.Run(runCtx, client.API(), self.ID, updates.AuthOptions{
					IsBot: false,
				})
			}

			log.Printf("[TG CLIENT] Cliente conectado sin sesión autenticada.")
			<-runCtx.Done()
			return runCtx.Err()
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("[TG CLIENT ERROR] Falló la ejecución del cliente: %v", err)
		}
	}()

	return nil
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
	cm.readyChan = nil
	cm.mu.Unlock()
}

func (cm *ClientManager) GetAuthStatus(ctx context.Context) AuthStatus {
	cm.mu.RLock()
	client := cm.client
	apiID := cm.apiID
	apiHash := cm.apiHash
	cm.mu.RUnlock()

	var apiIDStr string
	if apiID != 0 {
		apiIDStr = strconv.Itoa(apiID)
	}

	if apiID == 0 || apiHash == "" || client == nil {
		return AuthStatus{
			Configured:     false,
			HasCredentials: false,
			Authorized:     false,
			Authenticated:  false,
			State:          "UNCONFIGURED",
			APIID:          apiIDStr,
			APIHash:        apiHash,
		}
	}

	// Esperar brevemente a que el cliente MTProto esté conectado
	waitCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if err := cm.WaitReady(waitCtx); err != nil {
		return AuthStatus{
			Configured:     true,
			HasCredentials: true,
			Authorized:     false,
			Authenticated:  false,
			State:          "NEED_PHONE",
			APIID:          apiIDStr,
			APIHash:        apiHash,
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
			APIID:          apiIDStr,
			APIHash:        apiHash,
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
			APIID:          apiIDStr,
			APIHash:        apiHash,
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
		APIID:          apiIDStr,
		APIHash:        apiHash,
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
		return "", errors.New("cliente no configurado")
	}

	if err := cm.WaitReady(ctx); err != nil {
		return "", fmt.Errorf("error al conectar con Telegram: %w", err)
	}

	phone = strings.TrimSpace(phone)
	sentCode, err := client.Auth().SendCode(ctx, phone, auth.SendCodeOptions{})
	if err != nil {
		return "", fmt.Errorf("error al enviar código: %w", err)
	}

	sc, ok := sentCode.(*tg.AuthSentCode)
	if !ok {
		return "", errors.New("respuesta inesperada de Telegram")
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
		return "", errors.New("cliente no configurado")
	}

	if err := cm.WaitReady(ctx); err != nil {
		return "", fmt.Errorf("error al conectar con Telegram: %w", err)
	}

	_, err := client.Auth().SignIn(ctx, phone, code, phoneCodeHash)
	if err != nil {
		if errors.Is(err, auth.ErrPasswordAuthNeeded) || strings.Contains(strings.ToUpper(err.Error()), "SESSION_PASSWORD_NEEDED") {
			return "2fa_required", nil
		}
		return "", fmt.Errorf("código inválido o expirado: %w", err)
	}

	return "ok", nil
}

func (cm *ClientManager) Verify2FA(ctx context.Context, password string) error {
	cm.mu.Lock()
	client := cm.client
	cm.mu.Unlock()

	if client == nil {
		return errors.New("cliente no configurado")
	}

	if err := cm.WaitReady(ctx); err != nil {
		return fmt.Errorf("error al conectar con Telegram: %w", err)
	}

	_, err := client.Auth().Password(ctx, password)
	if err != nil {
		return fmt.Errorf("contraseña 2FA incorrecta: %w", err)
	}

	return nil
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
