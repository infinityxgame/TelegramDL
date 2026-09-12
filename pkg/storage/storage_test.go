package storage

import (
	"path/filepath"
	"testing"
)

func newTestStorage(t *testing.T) *Storage {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.sqlite3")
	st, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("no se pudo crear el storage de prueba: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestAPITokenRoundTrip(t *testing.T) {
	st := newTestStorage(t)

	tok, err := st.GetAPIToken()
	if err != nil {
		t.Fatalf("error inesperado leyendo token vacío: %v", err)
	}
	if tok != "" {
		t.Fatalf("se esperaba token vacío antes de guardar nada, got %q", tok)
	}

	if err := st.SaveAPIToken("abc123"); err != nil {
		t.Fatalf("error guardando token: %v", err)
	}

	tok, err = st.GetAPIToken()
	if err != nil {
		t.Fatalf("error leyendo token: %v", err)
	}
	if tok != "abc123" {
		t.Fatalf("token incorrecto: got %q, want %q", tok, "abc123")
	}

	// Regenerar debe sobrescribir el valor anterior, no duplicarlo.
	if err := st.SaveAPIToken("def456"); err != nil {
		t.Fatalf("error regenerando token: %v", err)
	}
	tok, err = st.GetAPIToken()
	if err != nil {
		t.Fatalf("error leyendo token regenerado: %v", err)
	}
	if tok != "def456" {
		t.Fatalf("token regenerado incorrecto: got %q, want %q", tok, "def456")
	}
}

func TestCredentialsRoundTrip(t *testing.T) {
	st := newTestStorage(t)

	if err := st.SaveCredentials("12345", "hashvalue"); err != nil {
		t.Fatalf("error guardando credenciales: %v", err)
	}

	apiID, apiHash, err := st.GetCredentials()
	if err != nil {
		t.Fatalf("error leyendo credenciales: %v", err)
	}
	if apiID != "12345" || apiHash != "hashvalue" {
		t.Fatalf("credenciales incorrectas: id=%q hash=%q", apiID, apiHash)
	}
}

func TestSaveAndLoadDownloadRoundTrip(t *testing.T) {
	st := newTestStorage(t)

	item := DownloadItem{
		ID:         "item-1",
		JobID:      "job-1",
		MessageID:  10,
		ChatID:     -100123,
		FileName:   "video.mp4",
		Status:     "queued",
		Progress:   0,
		Kind:       "video",
		Source:     "manual",
		TotalBytes: 1000,
	}
	if err := st.SaveDownload(item); err != nil {
		t.Fatalf("error guardando descarga: %v", err)
	}

	loaded, err := st.LoadDownloads("")
	if err != nil {
		t.Fatalf("error cargando descargas: %v", err)
	}
	got, ok := loaded["item-1"]
	if !ok {
		t.Fatal("la descarga guardada no aparece al recargar")
	}
	if got.FileName != "video.mp4" || got.ChatID != -100123 || got.MessageID != 10 {
		t.Fatalf("datos incorrectos tras recargar: %+v", got)
	}

	// Actualizar (upsert) no debe crear una segunda fila.
	item.Status = "completed"
	item.Progress = 100
	if err := st.SaveDownload(item); err != nil {
		t.Fatalf("error actualizando descarga: %v", err)
	}
	loaded, err = st.LoadDownloads("")
	if err != nil {
		t.Fatalf("error recargando tras update: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("se esperaba 1 registro tras el upsert, hay %d", len(loaded))
	}
	if loaded["item-1"].Status != "completed" {
		t.Fatalf("el estado no se actualizó: %+v", loaded["item-1"])
	}
}

func TestChunksLifecycle(t *testing.T) {
	st := newTestStorage(t)

	if err := st.AddChunks("dl-1", []int64{0, 1, 2, 1}); err != nil {
		t.Fatalf("error agregando chunks: %v", err)
	}

	chunks, err := st.Chunks("dl-1")
	if err != nil {
		t.Fatalf("error leyendo chunks: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("se esperaban 3 chunks únicos (0,1,2), hay %d: %+v", len(chunks), chunks)
	}

	if err := st.DeleteChunks("dl-1"); err != nil {
		t.Fatalf("error borrando chunks: %v", err)
	}
	chunks, err = st.Chunks("dl-1")
	if err != nil {
		t.Fatalf("error releyendo chunks tras borrar: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("se esperaban 0 chunks tras borrar, hay %d", len(chunks))
	}
}

func TestClearFinishedDownloadsKeepsActiveOnes(t *testing.T) {
	st := newTestStorage(t)

	statuses := map[string]string{
		"active-1":    "downloading",
		"queued-1":    "queued",
		"done-1":      "completed",
		"failed-1":    "failed",
		"cancelled-1": "cancelled",
	}
	for id, status := range statuses {
		if err := st.SaveDownload(DownloadItem{ID: id, Status: status, FileName: id}); err != nil {
			t.Fatalf("error guardando %s: %v", id, err)
		}
	}

	removed, err := st.ClearFinishedDownloads()
	if err != nil {
		t.Fatalf("error limpiando historial: %v", err)
	}
	if removed != 3 {
		t.Fatalf("se esperaban 3 registros eliminados (done/failed/cancelled), se eliminaron %d", removed)
	}

	remaining, err := st.LoadDownloads("")
	if err != nil {
		t.Fatalf("error recargando tras limpiar: %v", err)
	}
	if _, ok := remaining["active-1"]; !ok {
		t.Fatal("una descarga activa no debería borrarse al limpiar el historial")
	}
	if _, ok := remaining["queued-1"]; !ok {
		t.Fatal("una descarga en cola no debería borrarse al limpiar el historial")
	}
	if len(remaining) != 2 {
		t.Fatalf("se esperaban 2 descargas restantes, hay %d", len(remaining))
	}
}
