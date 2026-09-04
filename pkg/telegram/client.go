package telegram

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
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
	apiID         int
	apiHash       string
	client        *telegram.Client
	rawClient     *tg.Client
	sessionPath   string
	cancelRun     context.CancelFunc
	runWg         sync.WaitGroup
	mu            sync.RWMutex
	readyChan     chan struct{}
	phoneCodeHash string
	pendingPhone  string
	dispatcher    tg.UpdateDispatcher
	onNewMessage  func(ctx context.Context, entities tg.Entities, update *tg.UpdateNewChannelMessage) error
}

func NewClientManager() *ClientManager {
	config.InitPaths()
	sessPath := filepath.Join(config.DataDir, "tg_session.json")
	importPyrogramSession(sessPath)

	cm := &ClientManager{
		sessionPath: sessPath,
		dispatcher:  tg.NewUpdateDispatcher(),
	}
	return cm
}

func importPyrogramSession(targetJSONPath string) {
	// Si ya existe la sesión de gotd, no sobreescribir
	if _, err := os.Stat(targetJSONPath); err == nil {
		return
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

	var dcID int
	var authKey []byte
	var userID int64
	row := db.QueryRow("SELECT dc_id, auth_key, user_id FROM sessions LIMIT 1")
	if err := row.Scan(&dcID, &authKey, &userID); err != nil || len(authKey) != 256 {
		return
	}

	h := sha1.Sum(authKey)
	authKeyID := h[len(h)-8:]

	dcIP := "149.154.175.50"
	switch dcID {
	case 1:
		dcIP = "149.154.175.50"
	case 2:
		dcIP = "149.154.167.50"
	case 3:
		dcIP = "149.154.175.100"
	case 4:
		dcIP = "149.154.167.91"
	case 5:
		dcIP = "91.108.56.165"
	}

	sess := map[string]any{
		"Version": 1,
		"Data": map[string]any{
			"Config": map[string]any{
				"ThisDC": dcID,
			},
			"DC":        dcID,
			"Addr":      fmt.Sprintf("%s:443", dcIP),
			"AuthKey":   authKey,
			"AuthKeyID": authKeyID,
			"Salt":      0,
		},
	}

	data, err := json.Marshal(sess)
	if err == nil {
		_ = os.WriteFile(targetJSONPath, data, 0600)
	}
}

func (cm *ClientManager) SetMessageCallback(cb func(ctx context.Context, entities tg.Entities, update *tg.UpdateNewChannelMessage) error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.onNewMessage = cb
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
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.stopRunning()

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

	// Configurar dispatcher de actualizaciones
	cm.dispatcher.OnNewChannelMessage(func(ctx context.Context, entities tg.Entities, update *tg.UpdateNewChannelMessage) error {
		cm.mu.RLock()
		cb := cm.onNewMessage
		cm.mu.RUnlock()
		if cb != nil {
			return cb(ctx, entities, update)
		}
		return nil
	})

	opts := telegram.Options{
		SessionStorage: &session.FileStorage{
			Path: cm.sessionPath,
		},
		UpdateHandler: cm.dispatcher,
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
		err := client.Run(ctx, func(runCtx context.Context) error {
			readyOnce.Do(func() {
				close(cm.readyChan)
			})
			<-runCtx.Done()
			return runCtx.Err()
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			// Intento de conexión falló
		}
	}()

	return nil
}

func (cm *ClientManager) stopRunning() {
	if cm.cancelRun != nil {
		cm.cancelRun()
		cm.cancelRun = nil
	}
	cm.runWg.Wait()
	cm.client = nil
	cm.rawClient = nil
}

func (cm *ClientManager) WaitReady(ctx context.Context) error {
	cm.mu.RLock()
	ready := cm.readyChan
	cm.mu.RUnlock()

	if ready == nil {
		return errors.New("cliente no inicializado")
	}

	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
	waitCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
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

	cm.mu.Lock()
	cm.stopRunning()
	_ = os.Remove(cm.sessionPath)
	cm.mu.Unlock()

	return nil
}
