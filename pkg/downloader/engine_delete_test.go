package downloader

import (
	"os"
	"path/filepath"
	"testing"

	"tgdown/pkg/config"
	"tgdown/pkg/storage"
)

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.DownloadFolder = t.TempDir()
	return NewEngine(nil, nil, cfg)
}

// Un duplicado comparte file_path con el original: eliminar la entrada
// duplicada nunca debe borrar el archivo del disco.
func TestDeleteDownloadDuplicateKeepsSharedFile(t *testing.T) {
	eng := newTestEngine(t)
	original := filepath.Join(eng.config.DownloadFolder, "video.mp4")
	if err := os.WriteFile(original, []byte("VIDEO"), 0644); err != nil {
		t.Fatal(err)
	}

	eng.downloads["orig"] = &storage.DownloadItem{ID: "orig", ChatID: 1, MessageID: 1, FilePath: original, Status: "completed"}
	eng.downloads["dup"] = &storage.DownloadItem{ID: "dup", ChatID: 1, MessageID: 2, FilePath: original, Status: "duplicate"}
	eng.chatMsgMap["1:1"] = "orig"
	eng.chatMsgMap["1:2"] = "dup"

	if err := eng.DeleteDownload("dup", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("el archivo original fue borrado al eliminar el duplicado: %v", err)
	}
	if _, ok := eng.chatMsgMap["1:2"]; ok {
		t.Fatal("chatMsgMap no se limpió al borrar el duplicado")
	}

	// Cuando ya no queda ninguna otra referencia, el último propietario sí
	// puede borrar el archivo.
	if err := eng.DeleteDownload("orig", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(original); !os.IsNotExist(err) {
		t.Fatal("el archivo debería borrarse al eliminar al último propietario")
	}
	if _, ok := eng.chatMsgMap["1:1"]; ok {
		t.Fatal("chatMsgMap no se limpió al borrar el original")
	}
}

// Si se borra primero la entrada original mientras el duplicado sigue
// referenciando la ruta, el archivo tampoco se toca.
func TestDeleteDownloadOriginalKeptWhileDuplicateReferences(t *testing.T) {
	eng := newTestEngine(t)
	original := filepath.Join(eng.config.DownloadFolder, "video.mp4")
	if err := os.WriteFile(original, []byte("VIDEO"), 0644); err != nil {
		t.Fatal(err)
	}

	eng.downloads["orig"] = &storage.DownloadItem{ID: "orig", ChatID: 1, MessageID: 1, FilePath: original, Status: "completed"}
	eng.downloads["dup"] = &storage.DownloadItem{ID: "dup", ChatID: 1, MessageID: 2, FilePath: original, Status: "duplicate"}

	if err := eng.DeleteDownload("orig", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("el archivo fue borrado aunque el duplicado aún lo referencia: %v", err)
	}
}

// Una entrada normal sin compartir ruta borra su archivo como siempre.
func TestDeleteDownloadOwnedFileIsRemoved(t *testing.T) {
	eng := newTestEngine(t)
	file := filepath.Join(eng.config.DownloadFolder, "otro.mp4")
	if err := os.WriteFile(file, []byte("OTRO"), 0644); err != nil {
		t.Fatal(err)
	}
	temp := file + ".temp"
	if err := os.WriteFile(temp, []byte("TMP"), 0644); err != nil {
		t.Fatal(err)
	}

	eng.downloads["solo"] = &storage.DownloadItem{ID: "solo", ChatID: 1, MessageID: 9, FilePath: file, Status: "failed"}

	if err := eng.DeleteDownload("solo", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("el archivo propio debería haberse borrado")
	}
	if _, err := os.Stat(temp); !os.IsNotExist(err) {
		t.Fatal("el temporal propio debería haberse borrado")
	}
}
