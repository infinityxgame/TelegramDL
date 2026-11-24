package telegram

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

type mockManagedClient struct {
	runErr   error
	runFunc  func(ctx context.Context) error
	selfUser *tg.User
	selfErr  error
}

func (m *mockManagedClient) Run(ctx context.Context, f func(context.Context) error) error {
	if m.runFunc != nil {
		return m.runFunc(ctx)
	}
	if f != nil {
		_ = f(ctx)
	}
	return m.runErr
}

func (m *mockManagedClient) API() *tg.Client {
	return nil
}

func (m *mockManagedClient) Auth() *auth.Client {
	return nil
}

func (m *mockManagedClient) Self(ctx context.Context) (*tg.User, error) {
	return m.selfUser, m.selfErr
}

func (m *mockManagedClient) telegramClient() *telegram.Client {
	return nil
}

type mockAuthClient struct {
	status    *auth.Status
	statusErr error
	sentCode  tg.AuthSentCodeClass
	sendErr   error
	signErr   error
	passErr   error
}

func (m *mockAuthClient) Status(ctx context.Context) (*auth.Status, error) {
	return m.status, m.statusErr
}

func (m *mockAuthClient) SendCode(ctx context.Context, phone string, opts auth.SendCodeOptions) (tg.AuthSentCodeClass, error) {
	return m.sentCode, m.sendErr
}

func (m *mockAuthClient) SignIn(ctx context.Context, phone, code, phoneCodeHash string) (tg.AuthAuthorizationClass, error) {
	return nil, m.signErr
}

func (m *mockAuthClient) Password(ctx context.Context, password string) (tg.AuthAuthorizationClass, error) {
	return nil, m.passErr
}

type mockEvidenceStore struct {
	mu        sync.Mutex
	pending   bool
	sessionID string
	user      *UserInfo
}

func (m *mockEvidenceStore) MarkTelegramAuthPending() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending = true
	m.sessionID = ""
	m.user = nil
	return nil
}

func (m *mockEvidenceStore) MarkTelegramAuthorized(sessionID string, user UserInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending = false
	m.sessionID = sessionID
	m.user = &user
	return nil
}

func (m *mockEvidenceStore) GetTelegramAuthEvidence() (string, *UserInfo, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessionID, m.user, m.pending, nil
}

func (m *mockEvidenceStore) ClearTelegramAuthEvidence() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending = false
	m.sessionID = ""
	m.user = nil
	return nil
}

func TestNormalizePhone(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: " +34 600 11 22 33 ", want: "+34600112233"},
		{input: "34600112233", want: "+34600112233"},
		{input: "+1 (555) 123-4567", want: "+15551234567"},
		{input: "123", want: ""}, // Demasiado corto
		{input: "", want: ""},
	}

	for _, tt := range tests {
		got := normalizePhone(tt.input)
		if got != tt.want {
			t.Errorf("normalizePhone(%q) = %q; want %q", tt.input, got, tt.want)
		}
	}
}

func TestSuperviseAndPublishClientMock(t *testing.T) {
	cm := &ClientManager{
		apiID:     12345,
		apiHash:   "0123456789abcdef0123456789abcdef",
		connected: false,
	}

	mockClient := &mockManagedClient{
		runFunc: func(ctx context.Context) error {
			return nil
		},
	}

	cm.newClient = func(opts telegram.Options) managedClient {
		return mockClient
	}

	gen := cm.generation
	ok := cm.publishClient(gen, transportDirect, mockClient)
	if !ok {
		t.Fatalf("publishClient debe retornar true para la generación actual")
	}

	if cm.mClient != mockClient {
		t.Fatalf("mClient no fue guardado en ClientManager")
	}
}

func TestInitClientExplicitDoesNotClearEvidenceOnRoutineStart(t *testing.T) {
	store := &mockEvidenceStore{
		sessionID: "existing_session_fp",
		user: &UserInfo{
			ID:    100,
			Phone: "+34600112233",
		},
	}

	cm := &ClientManager{
		sessionStore: store,
	}

	// Reinicio rutinario (isNewSetup = false)
	err := cm.InitClientExplicit("12345", "0123456789abcdef0123456789abcdef", false)
	if err != nil {
		t.Fatalf("InitClientExplicit falló: %v", err)
	}

	sessID, user, pending, _ := store.GetTelegramAuthEvidence()
	if pending {
		t.Errorf("isNewSetup = false no debe marcar la autenticación como pendiente")
	}
	if sessID != "existing_session_fp" || user == nil {
		t.Errorf("isNewSetup = false debe conservar la evidencia previa de la sesión")
	}

	// Configuración explícita nueva (isNewSetup = true)
	err = cm.InitClientExplicit("12345", "0123456789abcdef0123456789abcdef", true)
	if err != nil {
		t.Fatalf("InitClientExplicit falló en nuevo setup: %v", err)
	}

	sessIDNew, userNew, pendingNew, _ := store.GetTelegramAuthEvidence()
	if !pendingNew {
		t.Errorf("isNewSetup = true debe marcar la autenticación como pendiente")
	}
	if sessIDNew != "" || userNew != nil {
		t.Errorf("isNewSetup = true debe limpiar la evidencia previa")
	}
}

func TestAuthStatusConnectingWithSession(t *testing.T) {
	dir := t.TempDir()
	sessFile := filepath.Join(dir, "tg_session.json")
	_ = os.WriteFile(sessFile, []byte("fake_session_data"), 0o600)

	fp := sessionFingerprint(sessFile)
	if fp == "" {
		t.Fatalf("sessionFingerprint debió calcular la huella del fichero de prueba")
	}

	store := &mockEvidenceStore{
		sessionID: fp,
		user: &UserInfo{
			ID:        123,
			FirstName: "Test",
			Phone:     "+34600112233",
		},
	}

	cm := &ClientManager{
		apiID:        12345,
		apiHash:      "0123456789abcdef0123456789abcdef",
		sessionPath:  sessFile,
		sessionStore: store,
		client:       &telegram.Client{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	status := cm.GetAuthStatus(ctx)
	if status.State != "CONNECTING" {
		t.Fatalf("AuthStatus estado esperado 'CONNECTING', obtenido '%s'", status.State)
	}
	if !status.SessionKnown {
		t.Errorf("SessionKnown debe ser true cuando la huella de la sesión coincide")
	}
	if status.Phone != "+34600112233" {
		t.Errorf("Phone esperado '+34600112233', obtenido '%s'", status.Phone)
	}
}

func TestFormatAuthError(t *testing.T) {
	errTimeout := formatAuthError("send_code", context.DeadlineExceeded)
	if errTimeout == nil || errTimeout.Error() != "connection error: context deadline exceeded" {
		t.Errorf("error de timeout esperado 'connection error...', obtenido %v", errTimeout)
	}

	errInvalidCode := formatAuthError("verify_code", errors.New("PHONE_CODE_INVALID"))
	if errInvalidCode == nil || errInvalidCode.Error() != "invalid verification code" {
		t.Errorf("error de código inválido esperado 'invalid verification code', obtenido %v", errInvalidCode)
	}
}
