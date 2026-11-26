package downloader

// Pruebas de la importación de descargas (SEV-01): el JSON de importación es
// entrada no confiable y no puede fijar una ruta fuera de la carpeta de
// descargas. También cubren la defensa en profundidad de los sumideros, que
// dejan intacto el disco cuando la ruta guardada no es de fiar.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tgdown/pkg/config"
	"tgdown/pkg/storage"
)

// newImportTestEngine monta un motor con SQLite real, que es lo que exige
// ImportDownloads para persistir las entradas. Se construye con NewEngine para
// no duplicar el estado interno y se apaga al terminar: sus bucles de
// persistencia escribirían en la base ya cerrada.
func newImportTestEngine(t *testing.T) (*Engine, *storage.Storage) {
	t.Helper()

	st, err := storage.NewStorage(filepath.Join(t.TempDir(), "import.db"))
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := st.Close(); closeErr != nil {
			t.Errorf("storage Close: %v", closeErr)
		}
	})

	cfg := config.DefaultConfig()
	cfg.DownloadFolder = t.TempDir()
	eng := NewEngine(nil, st, cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := eng.Shutdown(ctx); shutdownErr != nil {
			t.Errorf("Shutdown: %v", shutdownErr)
		}
	})
	return eng, st
}

// TestImportDownloadsRejectsEscapingSubFolder comprueba que una subcarpeta con
// «..», ruta absoluta o unidad de Windows descarta la entrada entera.
func TestImportDownloadsRejectsEscapingSubFolder(t *testing.T) {
	eng, st := newImportTestEngine(t)

	cases := []string{
		"../../outside",
		`..\..\outside`,
		"/etc",
		`C:\Windows`,
	}
	for _, sub := range cases {
		t.Run(sub, func(t *testing.T) {
			payload := DownloadsExportPayload{Downloads: []ExportedDownload{{
				DownloadItem: storage.DownloadItem{
					ID:        "evil-" + sub,
					FileName:  "pwned.txt",
					SubFolder: sub,
				},
			}}}

			imported, _, err := eng.ImportDownloads(payload)
			if err != nil {
				t.Fatalf("ImportDownloads: %v", err)
			}
			if imported != 0 {
				t.Fatalf("se importaron %d entradas con SubFolder %q", imported, sub)
			}

			saved, err := st.LoadDownloads("")
			if err != nil {
				t.Fatalf("LoadDownloads: %v", err)
			}
			if _, ok := saved["evil-"+sub]; ok {
				t.Fatalf("la entrada con SubFolder %q se persistió", sub)
			}
		})
	}
}

// TestImportDownloadsSanitizesFileNameAndStaysInside comprueba que un nombre
// con separadores no escapa: se sanea y la ruta final queda dentro.
func TestImportDownloadsSanitizesFileNameAndStaysInside(t *testing.T) {
	eng, st := newImportTestEngine(t)

	payload := DownloadsExportPayload{Downloads: []ExportedDownload{{
		DownloadItem: storage.DownloadItem{
			ID:        "evil-name",
			FileName:  "../../outside.txt",
			SubFolder: "",
		},
	}}}

	imported, _, err := eng.ImportDownloads(payload)
	if err != nil {
		t.Fatalf("ImportDownloads: %v", err)
	}
	if imported != 1 {
		t.Fatalf("se esperaba 1 entrada importada, se obtuvieron %d", imported)
	}

	saved, err := st.LoadDownloads("")
	if err != nil {
		t.Fatalf("LoadDownloads: %v", err)
	}
	item, ok := saved["evil-name"]
	if !ok {
		t.Fatal("la entrada saneada no se guardó")
	}
	if !PathWithin(eng.config.DownloadFolder, item.FilePath) {
		t.Fatalf("FilePath %q quedó fuera de %q", item.FilePath, eng.config.DownloadFolder)
	}
	if item.FileName == "../../outside.txt" {
		t.Fatal("el nombre peligroso se guardó sin sanear")
	}
}

// TestImportDownloadsCreatesSafeNestedFolder comprueba que una subcarpeta
// legítima se conserva y se crea en disco.
func TestImportDownloadsCreatesSafeNestedFolder(t *testing.T) {
	eng, st := newImportTestEngine(t)

	payload := DownloadsExportPayload{Downloads: []ExportedDownload{{
		DownloadItem: storage.DownloadItem{
			ID:        "good",
			FileName:  "video.mp4",
			SubFolder: "chat_1/tema_2",
		},
	}}}

	imported, _, err := eng.ImportDownloads(payload)
	if err != nil {
		t.Fatalf("ImportDownloads: %v", err)
	}
	if imported != 1 {
		t.Fatalf("se esperaba 1 entrada importada, se obtuvieron %d", imported)
	}

	saved, err := st.LoadDownloads("")
	if err != nil {
		t.Fatalf("LoadDownloads: %v", err)
	}
	item := saved["good"]
	want := filepath.Join(eng.config.DownloadFolder, "chat_1", "tema_2", "video.mp4")
	if item.FilePath != want {
		t.Fatalf("FilePath = %q, se esperaba %q", item.FilePath, want)
	}
	if _, statErr := os.Stat(filepath.Dir(item.FilePath)); statErr != nil {
		t.Fatalf("no se creó la carpeta de destino: %v", statErr)
	}
}

// TestImportDownloadsTreatsEncodedTraversalAsLiteralName comprueba que un
// «..%2f» no se interpreta como separador: el JSON no URL-decodifica, así que
// queda como parte literal del nombre y no puede escapar de la carpeta.
func TestImportDownloadsTreatsEncodedTraversalAsLiteralName(t *testing.T) {
	eng, st := newImportTestEngine(t)

	payload := DownloadsExportPayload{Downloads: []ExportedDownload{{
		DownloadItem: storage.DownloadItem{
			ID:        "encoded",
			FileName:  "..%2f..%2fevil.txt",
			SubFolder: "..%2f..%2f",
		},
	}}}

	imported, _, err := eng.ImportDownloads(payload)
	if err != nil {
		t.Fatalf("ImportDownloads: %v", err)
	}
	if imported != 1 {
		t.Fatalf("se esperaba 1 entrada importada, se obtuvieron %d", imported)
	}

	saved, err := st.LoadDownloads("")
	if err != nil {
		t.Fatalf("LoadDownloads: %v", err)
	}
	item, ok := saved["encoded"]
	if !ok {
		t.Fatal("la entrada no se guardó")
	}
	if !PathWithin(eng.config.DownloadFolder, item.FilePath) {
		t.Fatalf("FilePath %q quedó fuera de %q", item.FilePath, eng.config.DownloadFolder)
	}
}

// TestImportDownloadsRejectsEmptyFileName comprueba que una entrada sin nombre
// no se guarda como si apuntara a un directorio.
func TestImportDownloadsRejectsEmptyFileName(t *testing.T) {
	eng, _ := newImportTestEngine(t)

	payload := DownloadsExportPayload{Downloads: []ExportedDownload{{
		DownloadItem: storage.DownloadItem{ID: "no-name"},
	}}}

	imported, _, err := eng.ImportDownloads(payload)
	if err != nil {
		t.Fatalf("ImportDownloads: %v", err)
	}
	if imported != 0 {
		t.Fatalf("se importó una entrada sin nombre: %d", imported)
	}
}

// TestDeleteDownloadSkipsUntrustedLegacyPath reproduce una fila antigua creada
// antes de la corrección: aunque la entrada se borre del historial, el archivo
// de fuera de la carpeta de descargas no se toca.
func TestDeleteDownloadSkipsUntrustedLegacyPath(t *testing.T) {
	eng, _ := newImportTestEngine(t)

	outsideDir := t.TempDir()
	victim := filepath.Join(outsideDir, "victim.txt")
	if err := os.WriteFile(victim, []byte("NO BORRAR"), 0600); err != nil {
		t.Fatal(err)
	}

	eng.downloads["legacy"] = &storage.DownloadItem{
		ID:       "legacy",
		FileName: "../victim.txt",
		FilePath: victim,
		Status:   "completed",
	}

	if err := eng.DeleteDownload("legacy", true); err != nil {
		t.Fatalf("DeleteDownload: %v", err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("se borró un archivo fuera de la carpeta de descargas: %v", err)
	}
	if _, ok := eng.downloads["legacy"]; ok {
		t.Fatal("la entrada no se quitó del historial")
	}
}

// TestPathWithin cubre la comprobación de contención con rutas del sistema.
func TestPathWithin(t *testing.T) {
	base := t.TempDir()

	if !PathWithin(base, filepath.Join(base, "sub", "file.bin")) {
		t.Fatal("una ruta dentro de base se rechazó")
	}
	if !PathWithin(base, base) {
		t.Fatal("la propia base debe considerarse dentro")
	}
	if PathWithin(base, filepath.Join(base, "..", "outside.bin")) {
		t.Fatal("una ruta con «..» que sale de base se aceptó")
	}
	if PathWithin(base, t.TempDir()) {
		t.Fatal("un directorio temporal ajeno se aceptó como dentro")
	}
}

// TestItemFilePathTrusted cubre la validación de los sumideros: se acepta la
// ruta dentro de la carpeta actual y la de una carpeta antigua cuyos
// componentes coinciden, y se rechaza cualquier componente con traversal.
func TestItemFilePathTrusted(t *testing.T) {
	current := t.TempDir()
	oldBase := t.TempDir()

	cases := []struct {
		name     string
		item     storage.DownloadItem
		expected bool
	}{
		{
			name:     "sin ruta",
			item:     storage.DownloadItem{FileName: "video.mp4"},
			expected: false,
		},
		{
			name: "dentro de la carpeta actual",
			item: storage.DownloadItem{
				FileName: "video.mp4",
				FilePath: filepath.Join(current, "chat", "video.mp4"),
			},
			expected: true,
		},
		{
			name: "carpeta antigua con los mismos componentes",
			item: storage.DownloadItem{
				FileName:  "video.mp4",
				SubFolder: "chat",
				FilePath:  filepath.Join(oldBase, "chat", "video.mp4"),
			},
			expected: true,
		},
		{
			name: "carpeta antigua con otro nombre",
			item: storage.DownloadItem{
				FileName: "video.mp4",
				FilePath: filepath.Join(oldBase, "secret.txt"),
			},
			expected: false,
		},
		{
			name: "subcarpeta con traversal",
			item: storage.DownloadItem{
				FileName:  "video.mp4",
				SubFolder: "../../etc",
				FilePath:  filepath.Join(oldBase, "video.mp4"),
			},
			expected: false,
		},
		{
			name: "nombre con separadores",
			item: storage.DownloadItem{
				FileName: "../victim.txt",
				FilePath: filepath.Join(oldBase, "victim.txt"),
			},
			expected: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ItemFilePathTrusted(tc.item, current); got != tc.expected {
				t.Fatalf("ItemFilePathTrusted = %v, se esperaba %v", got, tc.expected)
			}
		})
	}
}

// TestDeleteDownloadKeepsWorkingAfterFolderChange evita la regresión contraria
// a la del test anterior: una entrada guardada bajo una carpeta de descargas
// anterior sigue pudiendo borrar su archivo.
func TestDeleteDownloadKeepsWorkingAfterFolderChange(t *testing.T) {
	eng, _ := newImportTestEngine(t)

	oldBase := t.TempDir()
	filePath := filepath.Join(oldBase, "chat", "video.mp4")
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("datos"), 0600); err != nil {
		t.Fatal(err)
	}

	eng.downloads["old-folder"] = &storage.DownloadItem{
		ID:        "old-folder",
		FileName:  "video.mp4",
		SubFolder: "chat",
		FilePath:  filePath,
		Status:    "completed",
	}

	if err := eng.DeleteDownload("old-folder", true); err != nil {
		t.Fatalf("DeleteDownload: %v", err)
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("el archivo de la carpeta antigua no se borró: %v", err)
	}
}
