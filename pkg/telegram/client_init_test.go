package telegram

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInitClientRechazaAPIIDInvalidoSinDestruirEstado cubre el fix SEC-01:
// un api_id inválido debe devolver error SIN ejecutar stopRunning() y sin
// mutar el manejador, de modo que la sesión MTProto viva no muera porque un
// atacante envíe credenciales corruptas a /api/auth/credentials.
//
// Se construye a mano en vez de NewClientManager para no tocar ~/.tgdown ni
// la sesión real del usuario durante los tests.
func TestInitClientRechazaAPIIDInvalidoSinDestruirEstado(t *testing.T) {
	cm := &ClientManager{}
	cm.apiID = 42
	// Centinela: stopRunning() pondría readyChan a nil y repondría los mapas
	// de DC. Si la validación falla antes de tocar cm, nada de esto cambia.
	cm.readyChan = make(chan struct{})

	if err := cm.InitClient("basura", "abc"); err == nil {
		t.Fatal("un api_id inválido debe devolver error")
	}
	if cm.apiID != 42 {
		t.Fatalf("cm.apiID fue modificado: got %d, want 42", cm.apiID)
	}
	if cm.client != nil {
		t.Fatal("cm.client no debe destruirse cuando la validación falla")
	}
	if cm.readyChan == nil {
		t.Fatal("cm.readyChan no debe tocarse: stopRunning() no debe ejecutarse en un error de validación")
	}
	if cm.dcPools != nil || cm.dcClients != nil {
		t.Fatal("los mapas de DC no deben reponerse: stopRunning() no debe ejecutarse en un error de validación")
	}
}

// TestHaySesionGuardada cubre el guard que pkg/server usará para permitir el
// setup de /api/auth/ sin token solo mientras no exista sesión guardada
// (SECURITY_FIXPLAN.md §1.3).
func TestHaySesionGuardada(t *testing.T) {
	dir := t.TempDir()

	// (a) Fichero de sesión existente → true.
	conSesion := filepath.Join(dir, "tg_session.json")
	if err := os.WriteFile(conSesion, []byte(`{"dc_id":2}`), 0o600); err != nil {
		t.Fatalf("no se pudo crear el fichero de sesión de prueba: %v", err)
	}
	cmCon := &ClientManager{sessionPath: conSesion}
	if !cmCon.HaySesionGuardada() {
		t.Error("con el fichero de sesión presente debe devolver true")
	}

	// (b) Path inexistente → false.
	cmSin := &ClientManager{sessionPath: filepath.Join(dir, "sesion_inexistente.json")}
	if cmSin.HaySesionGuardada() {
		t.Error("sin fichero de sesión debe devolver false")
	}

	// (c) Nil-safe: un manejador nulo no tiene sesión y no debe paniquear.
	var cmNulo *ClientManager
	if cmNulo.HaySesionGuardada() {
		t.Error("un ClientManager nil debe devolver false")
	}
}

func TestShouldEnableWebSocket(t *testing.T) {
	os.Unsetenv("TGDL_USE_WS")
	os.Unsetenv("TGDL_WEBSOCKET")

	if shouldEnableWebSocket() {
		t.Error("shouldEnableWebSocket() debe ser false cuando no se pasa flag ni env var")
	}

	os.Setenv("TGDL_USE_WS", "1")
	if !shouldEnableWebSocket() {
		t.Error("shouldEnableWebSocket() debe ser true con TGDL_USE_WS=1")
	}
	os.Unsetenv("TGDL_USE_WS")

	os.Setenv("TGDL_WEBSOCKET", "true")
	if !shouldEnableWebSocket() {
		t.Error("shouldEnableWebSocket() debe ser true con TGDL_WEBSOCKET=true")
	}
	os.Unsetenv("TGDL_WEBSOCKET")
}

func TestShouldDisableIPv6(t *testing.T) {
	os.Unsetenv("TGDL_NO_IPV6")
	os.Unsetenv("TGDL_DISABLE_IPV6")

	if shouldDisableIPv6() {
		t.Error("shouldDisableIPv6() debe ser false cuando no se pasa flag ni env var")
	}

	os.Setenv("TGDL_NO_IPV6", "1")
	if !shouldDisableIPv6() {
		t.Error("shouldDisableIPv6() debe ser true con TGDL_NO_IPV6=1")
	}
	os.Unsetenv("TGDL_NO_IPV6")

	os.Setenv("TGDL_DISABLE_IPV6", "true")
	if !shouldDisableIPv6() {
		t.Error("shouldDisableIPv6() debe ser true con TGDL_DISABLE_IPV6=true")
	}
	os.Unsetenv("TGDL_DISABLE_IPV6")
}
