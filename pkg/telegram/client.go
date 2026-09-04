package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

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
	Configured bool      `json:"configured"`
	Authorized bool      `json:"authorized"`
	Phone      string    `json:"phone,omitempty"`
	User       *UserInfo `json:"user,omitempty"`
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
	cm := &ClientManager{
		sessionPath: filepath.Join(config.DataDir, "tg_session.json"),
		dispatcher:  tg.NewUpdateDispatcher(),
	}
	return cm
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

	if apiID == 0 || apiHash == "" || client == nil {
		return AuthStatus{Configured: false, Authorized: false}
	}

	// Esperar brevemente a que el cliente MTProto esté conectado
	waitCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if err := cm.WaitReady(waitCtx); err != nil {
		return AuthStatus{Configured: true, Authorized: false}
	}

	authClient := client.Auth()
	status, err := authClient.Status(ctx)
	if err != nil || !status.Authorized {
		return AuthStatus{Configured: true, Authorized: false}
	}

	user, err := client.Self(ctx)
	if err != nil {
		return AuthStatus{Configured: true, Authorized: true}
	}

	var colorID *int
	if pc, ok := user.Color.(*tg.PeerColor); ok {
		if c, ok := pc.GetColor(); ok {
			colorID = &c
		}
	}

	return AuthStatus{
		Configured: true,
		Authorized: true,
		Phone:      user.Phone,
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
