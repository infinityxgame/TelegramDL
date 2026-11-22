package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"tgdown/pkg/config"
	"tgdown/pkg/downloader"
	"tgdown/pkg/listener"
	"tgdown/pkg/notifier"
	"tgdown/pkg/storage"
)

// rawTestToken es un token sintético con el formato id:hash de los bots de
// Telegram: nunca es real y solo existe para que MaskToken tenga un caso
// representativo (SEC-25).
const rawTestToken = "1234567890:AAExampleTokenSecretForTests0000"

// newMaskingTestServer monta un servidor mínimo pero completo: storage real
// en un fichero temporal (handleSettings guarda en SQLite), motor de descargas
// y escuchador sin sesión de Telegram ni red. Sin esos tres, los handlers
// reventarían al tocar s.storage / s.downloader / s.listener nulos.
func newMaskingTestServer(t *testing.T) *Server {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.DownloadFolder = t.TempDir()
	cfg.NotificationBotToken = rawTestToken

	st, err := storage.NewStorage(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatalf("no se pudo crear el storage de prueba: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("no se pudo cerrar el storage de prueba: %v", err)
		}
	})

	engine := downloader.NewEngine(nil, nil, cfg)
	le := listener.NewListenerEngine(nil, nil, engine, cfg)

	return &Server{
		config:     cfg,
		storage:    st,
		downloader: engine,
		listener:   le,
	}
}

// TestBuildStateSnapshotMasksNotificationBotToken cubre SEC-25 sobre el
// snapshot que viaja por WebSocket y Wails a todos los clientes conectados:
// el token del bot no puede aparecer en claro en ninguno de sus campos.
func TestBuildStateSnapshotMasksNotificationBotToken(t *testing.T) {
	s := newMaskingTestServer(t)

	snap := s.buildStateSnapshot()

	settings, ok := snap["settings"].(config.Config)
	if !ok {
		t.Fatalf("tipo inesperado en snap[\"settings\"]: %T", snap["settings"])
	}
	if settings.NotificationBotToken == rawTestToken {
		t.Error("el snapshot expone notification_bot_token en claro")
	}
	if want := notifier.MaskToken(rawTestToken); settings.NotificationBotToken != want {
		t.Errorf("notification_bot_token = %q, se esperaba la máscara %q",
			settings.NotificationBotToken, want)
	}

	// Vista completa serializada: ni el token ni otro secreto deben asomar en
	// ningún campo del snapshot (descargas, escucha, disco, logs...).
	encoded, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("no se pudo serializar el snapshot: %v", err)
	}
	if strings.Contains(string(encoded), rawTestToken) {
		t.Fatalf("el token en claro aparece en el snapshot serializado: %s", encoded)
	}

	// Enmascarar el snapshot no debe tocar la fuente: el notificador sigue
	// necesitando el token real para enviar.
	if got := s.GetConfig().NotificationBotToken; got != rawTestToken {
		t.Errorf("s.config.NotificationBotToken = %q, se esperaba el token real", got)
	}
}

// TestHandleSettingsMasksNotificationBotToken cubre SEC-25 en la respuesta del
// guardado de ajustes: el panel hace Object.assign con ella, así que si trae
// el token en claro pisa la máscara que devolvió el GET. Comprueba también que
// ni la config viva ni SQLite se quedan con la máscara.
func TestHandleSettingsMasksNotificationBotToken(t *testing.T) {
	s := newMaskingTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/settings",
		strings.NewReader(`{"parallel_chunks": false}`))
	w := httptest.NewRecorder()
	s.handleSettings(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("se esperaba 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, rawTestToken) {
		t.Fatalf("la respuesta trae el token en claro: %s", body)
	}
	masked := notifier.MaskToken(rawTestToken)
	if !strings.Contains(body, masked) {
		t.Fatalf("la respuesta no trae el token enmascarado (%q): %s", masked, body)
	}

	// La máscara solo puede salir en la copia serializada: ni la config viva
	// ni la fila de SQLite deben quedársela.
	if got := s.GetConfig().NotificationBotToken; got != rawTestToken {
		t.Errorf("s.config.NotificationBotToken = %q, se esperaba el token real", got)
	}
	saved, err := s.storage.LoadConfig(config.DefaultConfig(), "")
	if err != nil {
		t.Fatalf("no se pudo releer la configuración guardada: %v", err)
	}
	if saved.NotificationBotToken != rawTestToken {
		t.Errorf("SQLite guardó %q, se esperaba el token real", saved.NotificationBotToken)
	}

	// Fallback de guardado: si el panel manda la máscara de vuelta al volver
	// a guardar, el token real se conserva en lugar de fijar la máscara.
	reqMasked := httptest.NewRequest(http.MethodPost, "/api/settings",
		strings.NewReader(`{"notification_bot_token": "`+masked+`"}`))
	wMasked := httptest.NewRecorder()
	s.handleSettings(wMasked, reqMasked)
	if wMasked.Code != http.StatusOK {
		t.Fatalf("se esperaba 200 al reenviar la máscara, got %d: %s",
			wMasked.Code, wMasked.Body.String())
	}
	if got := s.GetConfig().NotificationBotToken; got != rawTestToken {
		t.Errorf("el fallback de guardado pisó el token: s.config.NotificationBotToken = %q", got)
	}
}

// TestHandleSettingsPreservesProxyState comprueba que al modificar ajustes como
// el color del tema (color_id) con el proxy activo, el proxy se mantiene activo.
func TestHandleSettingsPreservesProxyState(t *testing.T) {
	s := newMaskingTestServer(t)
	s.config.ProxyEnabled = true
	s.config.ProxyHost = "127.0.0.1"
	s.config.ProxyPort = 1080

	req := httptest.NewRequest(http.MethodPost, "/api/settings",
		strings.NewReader(`{"color_id": 3, "proxy_enabled": true}`))
	w := httptest.NewRecorder()
	s.handleSettings(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("se esperaba 200, got %d: %s", w.Code, w.Body.String())
	}
	if !s.GetConfig().ProxyEnabled {
		t.Errorf("el proxy fue desactivado al cambiar el tema (color_id)")
	}
}
