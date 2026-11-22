package storage

import (
	"database/sql"
	"path/filepath"
	"testing"

	"tgdown/pkg/config"
)

func newTestStorage(t *testing.T) *Storage {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.sqlite3")
	st, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("could not create test storage: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestAPITokenRoundTrip(t *testing.T) {
	st := newTestStorage(t)

	tok, err := st.GetAPIToken()
	if err != nil {
		t.Fatalf("unexpected error reading empty token: %v", err)
	}
	if tok != "" {
		t.Fatalf("expected empty token before saving anything, got %q", tok)
	}

	if err := st.SaveAPIToken("abc123"); err != nil {
		t.Fatalf("error saving token: %v", err)
	}

	tok, err = st.GetAPIToken()
	if err != nil {
		t.Fatalf("error reading token: %v", err)
	}
	if tok != "abc123" {
		t.Fatalf("incorrect token: got %q, want %q", tok, "abc123")
	}

	// Regenerar debe sobrescribir el valor anterior, no duplicarlo.
	if err := st.SaveAPIToken("def456"); err != nil {
		t.Fatalf("error regenerating token: %v", err)
	}
	tok, err = st.GetAPIToken()
	if err != nil {
		t.Fatalf("error reading regenerated token: %v", err)
	}
	if tok != "def456" {
		t.Fatalf("incorrect regenerated token: got %q, want %q", tok, "def456")
	}
}

func TestCredentialsRoundTrip(t *testing.T) {
	st := newTestStorage(t)

	if err := st.SaveCredentials("12345", "hashvalue"); err != nil {
		t.Fatalf("error saving credentials: %v", err)
	}

	apiID, apiHash, err := st.GetCredentials()
	if err != nil {
		t.Fatalf("error reading credentials: %v", err)
	}
	if apiID != "12345" || apiHash != "hashvalue" {
		t.Fatalf("incorrect credentials: id=%q hash=%q", apiID, apiHash)
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
		t.Fatalf("error saving download: %v", err)
	}

	loaded, err := st.LoadDownloads("")
	if err != nil {
		t.Fatalf("error loading downloads: %v", err)
	}
	got, ok := loaded["item-1"]
	if !ok {
		t.Fatal("saved download does not appear upon reload")
	}
	if got.FileName != "video.mp4" || got.ChatID != -100123 || got.MessageID != 10 {
		t.Fatalf("incorrect data after reload: %+v", got)
	}

	// Actualizar (upsert) no debe crear una segunda fila.
	item.Status = "completed"
	item.Progress = 100
	if err := st.SaveDownload(item); err != nil {
		t.Fatalf("error updating download: %v", err)
	}
	loaded, err = st.LoadDownloads("")
	if err != nil {
		t.Fatalf("error reloading after update: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 record after upsert, got %d", len(loaded))
	}
	if loaded["item-1"].Status != "completed" {
		t.Fatalf("status was not updated: %+v", loaded["item-1"])
	}
}

func TestChunksLifecycle(t *testing.T) {
	st := newTestStorage(t)

	// download_chunks tiene FOREIGN KEY(download_id) REFERENCES downloads(id),
	// así que la descarga padre debe existir antes de insertar sus chunks.
	if err := st.SaveDownload(DownloadItem{
		ID:       "dl-1",
		FileName: "video.mp4",
		Status:   "downloading",
	}); err != nil {
		t.Fatalf("error creating parent download: %v", err)
	}

	if err := st.AddChunks("dl-1", []int64{0, 1, 2, 1}); err != nil {
		t.Fatalf("error adding chunks: %v", err)
	}

	chunks, err := st.Chunks("dl-1")
	if err != nil {
		t.Fatalf("error reading chunks: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("expected 3 unique chunks (0,1,2), got %d: %+v", len(chunks), chunks)
	}

	if err := st.DeleteChunks("dl-1"); err != nil {
		t.Fatalf("error deleting chunks: %v", err)
	}
	chunks, err = st.Chunks("dl-1")
	if err != nil {
		t.Fatalf("error re-reading chunks after delete: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks after delete, got %d", len(chunks))
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
			t.Fatalf("error saving %s: %v", id, err)
		}
	}

	removed, err := st.ClearFinishedDownloads()
	if err != nil {
		t.Fatalf("error clearing history: %v", err)
	}
	if removed != 3 {
		t.Fatalf("expected 3 removed records (done/failed/cancelled), got %d", removed)
	}

	remaining, err := st.LoadDownloads("")
	if err != nil {
		t.Fatalf("error reloading after clear: %v", err)
	}
	if _, ok := remaining["active-1"]; !ok {
		t.Fatal("an active download should not be deleted when clearing history")
	}
	if _, ok := remaining["queued-1"]; !ok {
		t.Fatal("a queued download should not be deleted when clearing history")
	}
	if len(remaining) != 2 {
		t.Fatalf("expected 2 remaining downloads, got %d", len(remaining))
	}
}

func TestListenerChatsRoundTripWithTopics(t *testing.T) {
	st := newTestStorage(t)

	cfg := config.DefaultConfig()
	cfg.ListenerChats = []config.ListenerChat{
		{ID: -1001, Name: "Mi Grupo", FPhotos: true, FVideos: true, FAudios: true, FDocs: true, FStickers: true},
		{ID: -1001, TopicID: config.TopicPointer(57), TopicName: "Películas", Name: "Mi Grupo", AutoDownload: true, FVideos: true},
		{ID: -1001, TopicID: config.TopicPointer(9), TopicName: "Música", Name: "Mi Grupo", FAudios: true},
	}
	cfg = config.NormalizeConfig(cfg)

	if err := st.SaveConfig(cfg); err != nil {
		t.Fatalf("error saving config: %v", err)
	}

	loaded, err := st.LoadConfig(config.DefaultConfig(), "")
	if err != nil {
		t.Fatalf("error reloading config: %v", err)
	}
	if len(loaded.ListenerChats) != 3 {
		t.Fatalf("a group should be able to have multiple topic entries, got %d: %+v",
			len(loaded.ListenerChats), loaded.ListenerChats)
	}

	byKey := make(map[string]config.ListenerChat, len(loaded.ListenerChats))
	for _, c := range loaded.ListenerChats {
		byKey[c.Key()] = c
	}

	whole, ok := byKey["-1001"]
	if !ok || whole.HasTopic() {
		t.Fatalf("missing whole group entry: %+v", loaded.ListenerChats)
	}

	pelis, ok := byKey["-1001:57"]
	if !ok {
		t.Fatalf("missing entry for topic 57: %+v", loaded.ListenerChats)
	}
	if pelis.TopicName != "Películas" || !pelis.AutoDownload {
		t.Fatalf("topic entry was not saved completely: %+v", pelis)
	}
	if got, want := pelis.DisplayName(), "Películas · Mi Grupo"; got != want {
		t.Fatalf("incorrect display name: got %q, want %q", got, want)
	}

	if _, ok := byKey["-1001:9"]; !ok {
		t.Fatalf("missing entry for topic 9: %+v", loaded.ListenerChats)
	}
}

func TestMigrationFromSchemaWithoutTopics(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.sqlite3")

	// Base de datos tal y como la dejaban las versiones anteriores: una sola
	// fila por grupo, con chat_id como clave primaria.
	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("could not create legacy database: %v", err)
	}
	_, err = legacy.Exec(`
		CREATE TABLE listener_chats (
			chat_id INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			auto_download INTEGER NOT NULL DEFAULT 0,
			f_photos INTEGER NOT NULL DEFAULT 1,
			f_videos INTEGER NOT NULL DEFAULT 1,
			f_audios INTEGER NOT NULL DEFAULT 1,
			f_docs INTEGER NOT NULL DEFAULT 1,
			f_stickers INTEGER NOT NULL DEFAULT 1
		);
		INSERT INTO listener_chats VALUES(-1001, 'Grupo Antiguo', 1, 1, 0, 1, 1, 0);
	`)
	if err != nil {
		t.Fatalf("could not prepare legacy table: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("could not close legacy database: %v", err)
	}

	st, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("migration should not prevent opening database: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	loaded, err := st.LoadConfig(config.DefaultConfig(), "")
	if err != nil {
		t.Fatalf("error loading migrated config: %v", err)
	}
	if len(loaded.ListenerChats) != 1 {
		t.Fatalf("expected to preserve existing chat: %+v", loaded.ListenerChats)
	}

	chat := loaded.ListenerChats[0]
	if chat.ID != -1001 || chat.Name != "Grupo Antiguo" {
		t.Fatalf("migrated chat lost data: %+v", chat)
	}
	if chat.HasTopic() {
		t.Fatalf("legacy chat monitors entire group, not a topic: %+v", chat)
	}
	if !chat.AutoDownload || chat.FVideos || chat.FStickers || !chat.FPhotos {
		t.Fatalf("filters did not survive migration: %+v", chat)
	}

	// Tras migrar, el mismo grupo admite entradas por tema.
	cfg := loaded
	cfg.ListenerChats = append(cfg.ListenerChats, config.ListenerChat{
		ID: -1001, TopicID: config.TopicPointer(57), TopicName: "Series", Name: "Grupo Antiguo",
	})
	if err := st.SaveConfig(config.NormalizeConfig(cfg)); err != nil {
		t.Fatalf("could not save topic entry after migration: %v", err)
	}
	again, err := st.LoadConfig(config.DefaultConfig(), "")
	if err != nil {
		t.Fatalf("error reloading: %v", err)
	}
	if len(again.ListenerChats) != 2 {
		t.Fatalf("expected 2 entries for same group: %+v", again.ListenerChats)
	}
}

func TestProxyDeduplicationAndDelete(t *testing.T) {
	st := newTestStorage(t)

	// Añadir primer proxy
	p1, err := st.AddProxy(config.SavedProxy{
		Type: "socks5",
		Host: "1.1.1.1",
		Port: 1080,
	})
	if err != nil {
		t.Fatalf("error adding proxy: %v", err)
	}

	// Añadir el mismo proxy de nuevo no debe crear un duplicado
	p2, err := st.AddProxy(config.SavedProxy{
		Type: "socks5",
		Host: "1.1.1.1",
		Port: 1080,
	})
	if err != nil {
		t.Fatalf("error adding duplicate proxy: %v", err)
	}
	if p1.ID != p2.ID {
		t.Fatalf("expected duplicate proxy to return existing ID %d, got %d", p1.ID, p2.ID)
	}

	proxies, err := st.GetProxies()
	if err != nil {
		t.Fatalf("error getting proxies: %v", err)
	}
	if len(proxies) != 1 {
		t.Fatalf("expected 1 proxy in list, got %d", len(proxies))
	}

	// Eliminar el proxy debe dejar la lista vacía
	if err := st.DeleteProxy(p1.ID); err != nil {
		t.Fatalf("error deleting proxy: %v", err)
	}

	proxiesAfter, err := st.GetProxies()
	if err != nil {
		t.Fatalf("error getting proxies after delete: %v", err)
	}
	if len(proxiesAfter) != 0 {
		t.Fatalf("expected empty proxy list, got %d", len(proxiesAfter))
	}
}
