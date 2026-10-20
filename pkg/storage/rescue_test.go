package storage

// Pruebas de la recuperación ante bases de datos dañadas (rescue.go). Se
// corrompen archivos reales a propósito para reproducir lo que se vio en
// producción: una BD que no abre no puede tumbar el arranque, y todo lo
// legible debe acabar en la base nueva.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// destroyHeader sobrescribe el archivo con basura: la cabecera deja de ser
// una cabecera y la base es ilegible por completo. Es la corrupción total,
// la que en producción obligaba a borrar la base a mano.
func destroyHeader(t *testing.T, path string) {
	t.Helper()
	garbage := bytes.Repeat([]byte{0x58}, 512)
	if err := os.WriteFile(path, garbage, 0o600); err != nil {
		t.Fatalf("could not destroy database header: %v", err)
	}
}

// corruptMiddle pisa bytes en mitad del archivo sin tocar la cabecera: el
// daño alcanza solo a las páginas que caigan ahí, como un sector defectuoso.
func corruptMiddle(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("could not open database to corrupt it: %v", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("could not stat database: %v", err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{0xFF}, 256), fi.Size()/2); err != nil {
		t.Fatalf("could not corrupt database middle: %v", err)
	}
}

// countMatches cuenta los archivos que cumplen un patrón de Glob.
func countMatches(t *testing.T, pattern string) int {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("could not list %s: %v", pattern, err)
	}
	return len(matches)
}

func TestOpenResilientLeavesHealthyDatabaseAlone(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tgdown.sqlite3")

	st, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("could not create database: %v", err)
	}
	if err := st.SaveCredentials("12345", "hashvalue"); err != nil {
		t.Fatalf("could not save credentials: %v", err)
	}
	if err := st.SaveAPIToken("token-1"); err != nil {
		t.Fatalf("could not save token: %v", err)
	}
	if err := st.SaveDownload(DownloadItem{ID: "dl-1", FileName: "video.mp4", Status: "completed", Progress: 100}); err != nil {
		t.Fatalf("could not save download: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("could not close database: %v", err)
	}

	st2, err := OpenResilient(dbPath)
	if err != nil {
		t.Fatalf("healthy database should open without rescue: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })

	// La base sana no se toca: los datos siguen y no se genera ningún
	// archivo de rescate.
	apiID, apiHash, err := st2.GetCredentials()
	if err != nil {
		t.Fatalf("could not read credentials: %v", err)
	}
	if apiID != "12345" || apiHash != "hashvalue" {
		t.Fatalf("credentials lost after reopen: %q/%q", apiID, apiHash)
	}
	loaded, err := st2.LoadDownloads("")
	if err != nil {
		t.Fatalf("could not load downloads: %v", err)
	}
	if _, ok := loaded["dl-1"]; !ok {
		t.Fatal("download lost after reopen")
	}
	if g := countMatches(t, filepath.Join(dir, "*corrupt*")); g != 0 {
		t.Fatalf("healthy database should not be set aside, found %d rescue files", g)
	}
	if g := countMatches(t, filepath.Join(dir, "*legacy*")); g != 0 {
		t.Fatalf("no legacy files expected, found %d", g)
	}
}

func TestSalvageRecoversReadableRows(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.sqlite3")

	old, err := NewStorage(oldPath)
	if err != nil {
		t.Fatalf("could not create source database: %v", err)
	}
	if err := old.SaveCredentials("999", "oldhash"); err != nil {
		t.Fatalf("could not save credentials: %v", err)
	}
	if err := old.SaveDownload(DownloadItem{
		ID: "dl-9", FileName: "foto.jpg", Status: "completed",
		ChatID: -100, MessageID: 5, TotalBytes: 42, Progress: 100,
	}); err != nil {
		t.Fatalf("could not save download: %v", err)
	}
	if err := old.AddChunks("dl-9", []int64{0, 1}); err != nil {
		t.Fatalf("could not save chunks: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("could not close source database: %v", err)
	}

	st, err := NewStorage(filepath.Join(dir, "new.sqlite3"))
	if err != nil {
		t.Fatalf("could not create target database: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	st.salvageFrom(oldPath)

	apiID, apiHash, err := st.GetCredentials()
	if err != nil {
		t.Fatalf("could not read salvaged credentials: %v", err)
	}
	if apiID != "999" || apiHash != "oldhash" {
		t.Fatalf("credentials were not salvaged: %q/%q", apiID, apiHash)
	}
	loaded, err := st.LoadDownloads("")
	if err != nil {
		t.Fatalf("could not load salvaged downloads: %v", err)
	}
	item, ok := loaded["dl-9"]
	if !ok {
		t.Fatal("download was not salvaged")
	}
	if item.FileName != "foto.jpg" || item.ChatID != -100 || item.MessageID != 5 || item.TotalBytes != 42 {
		t.Fatalf("salvaged download lost data: %+v", item)
	}
	chunks, err := st.Chunks("dl-9")
	if err != nil {
		t.Fatalf("could not read salvaged chunks: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 salvaged chunks, got %d", len(chunks))
	}
}

func TestOpenResilientRebuildsFromDestroyedHeader(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tgdown.sqlite3")

	st, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("could not create database: %v", err)
	}
	if err := st.SaveAPIToken("will-be-lost"); err != nil {
		t.Fatalf("could not save token: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("could not close database: %v", err)
	}

	destroyHeader(t, dbPath)

	st2, err := OpenResilient(dbPath)
	if err != nil {
		t.Fatalf("a destroyed header must not prevent startup: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })

	if g := countMatches(t, filepath.Join(dir, "*.corrupt-*")); g != 1 {
		t.Fatalf("expected the damaged file set aside once, got %d", g)
	}
	if err := st2.QuickCheck(); err != nil {
		t.Fatalf("rebuilt database should be healthy: %v", err)
	}

	// Nada era legible en la base destruida, así que lo que importa es que
	// la nueva funcione de pe a pa: escribir y leer.
	if err := st2.SaveAPIToken("fresh-token"); err != nil {
		t.Fatalf("could not write to rebuilt database: %v", err)
	}
	tok, err := st2.GetAPIToken()
	if err != nil {
		t.Fatalf("could not read from rebuilt database: %v", err)
	}
	if tok != "fresh-token" {
		t.Fatalf("rebuilt database did not persist: %q", tok)
	}
}

func TestOpenResilientAbsorbsLegacyFallback(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tgdown.sqlite3")
	legacyPath := filepath.Join(dir, "tgdown.db")

	// El respaldo tgdown.db de las versiones anteriores, con datos reales
	// que no hay que perder.
	legacy, err := NewStorage(legacyPath)
	if err != nil {
		t.Fatalf("could not create legacy database: %v", err)
	}
	if err := legacy.SaveCredentials("22222", "legacyhash"); err != nil {
		t.Fatalf("could not save legacy credentials: %v", err)
	}
	if err := legacy.SaveDownload(DownloadItem{ID: "dl-legacy", FileName: "doc.pdf", Status: "completed", Progress: 100}); err != nil {
		t.Fatalf("could not save legacy download: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("could not close legacy database: %v", err)
	}

	// La base principal, inservible.
	st, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("could not create main database: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("could not close main database: %v", err)
	}
	destroyHeader(t, dbPath)

	st2, err := OpenResilient(dbPath)
	if err != nil {
		t.Fatalf("rescue with legacy fallback must succeed: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })

	if _, err := os.Stat(legacyPath); err == nil {
		t.Fatal("tgdown.db should have been renamed aside, not left in place")
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error checking tgdown.db: %v", err)
	}
	if g := countMatches(t, filepath.Join(dir, "tgdown.db*legacy*")); g < 1 {
		t.Fatal("legacy database should have been renamed with a .legacy- suffix")
	}

	apiID, apiHash, err := st2.GetCredentials()
	if err != nil {
		t.Fatalf("could not read absorbed credentials: %v", err)
	}
	if apiID != "22222" || apiHash != "legacyhash" {
		t.Fatalf("legacy credentials were not absorbed: %q/%q", apiID, apiHash)
	}
	loaded, err := st2.LoadDownloads("")
	if err != nil {
		t.Fatalf("could not load absorbed downloads: %v", err)
	}
	if _, ok := loaded["dl-legacy"]; !ok {
		t.Fatal("legacy download was not absorbed")
	}
}

func TestOpenResilientSurvivesMidFileCorruption(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tgdown.sqlite3")

	st, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("could not create database: %v", err)
	}
	for i := 0; i < 20; i++ {
		item := DownloadItem{
			ID:       fmt.Sprintf("dl-%d", i),
			FileName: fmt.Sprintf("f%d.bin", i),
			Status:   "completed",
			Progress: 100,
		}
		if err := st.SaveDownload(item); err != nil {
			t.Fatalf("could not save download %d: %v", i, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("could not close database: %v", err)
	}

	corruptMiddle(t, dbPath)

	st2, err := OpenResilient(dbPath)
	if err != nil {
		t.Fatalf("mid-file corruption must not prevent startup: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })

	if err := st2.QuickCheck(); err != nil {
		t.Fatalf("database after recovery should be healthy: %v", err)
	}
	if err := st2.SaveDownload(DownloadItem{ID: "after", FileName: "x.bin", Status: "completed", Progress: 100}); err != nil {
		t.Fatalf("recovered database should accept writes: %v", err)
	}
}
