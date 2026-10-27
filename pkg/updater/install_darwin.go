//go:build darwin

package updater

// En macOS una aplicación no es un ejecutable suelto, sino un bundle .app: una
// carpeta con el binario, el Info.plist y los recursos, sellada por una firma
// de código que cubre todo su contenido. Reemplazar solo el binario de dentro
// —que es lo que hace selfupdate— invalida ese sello y además deja el
// Info.plist con la versión antigua, así que el sistema puede negarse a abrir
// la aplicación con un "está dañada".
//
// La forma correcta, y la que usa Sparkle por debajo, es sustituir el bundle
// entero de una pieza. Eso es lo que hace este archivo. No hace falta ninguna
// firma de Apple: funciona igual con la firma ad-hoc que genera el propio
// build, porque el bundle nuevo llega ya sellado desde la publicación y aquí
// no se toca nada de su interior.

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"tgdown/pkg/i18n"
	"tgdown/pkg/logbus"
)

// appTranslocationMarker aparece en la ruta cuando macOS ejecuta la aplicación
// desde una copia de solo lectura, cosa que hace cuando se abre directamente
// desde el .zip descargado sin haberla movido a Aplicaciones.
const appTranslocationMarker = "/AppTranslocation/"

// installPlatform sustituye el bundle .app completo.
//
// Devuelve handled=false cuando no estamos dentro de un bundle —por ejemplo el
// modo --server lanzado desde el binario suelto—: en ese caso el reemplazo
// normal de selfupdate sí es lo correcto y quien llama sigue por ahí.
//
// Cuando termina bien no regresa: relanza la aplicación y termina el proceso.
func (u *AppUpdater) installPlatform(archivePath, tempDir string) (bool, error) {
	exePath, err := os.Executable()
	if err != nil {
		return false, nil
	}
	if resolved, errLink := filepath.EvalSymlinks(exePath); errLink == nil {
		exePath = resolved
	}

	currentBundle := bundleForExecutable(exePath)
	if currentBundle == "" {
		return false, nil
	}

	if strings.Contains(currentBundle, appTranslocationMarker) {
		return true, fmt.Errorf("%s", i18n.T("updater.darwinTranslocation"))
	}

	// 1. Extraer con ditto en vez de con el unzip de Go. ditto conserva los
	//    permisos de ejecución, los enlaces simbólicos y los atributos
	//    extendidos que un bundle necesita para arrancar, y es justo la
	//    herramienta con la que se empaqueta al publicar.
	stage := filepath.Join(tempDir, "stage")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return true, fmt.Errorf(i18n.T("updater.darwinTempDirError"), err)
	}
	if err := extractForMac(archivePath, stage); err != nil {
		return true, err
	}

	newBundle := findAppBundle(stage)
	if newBundle == "" {
		return true, fmt.Errorf("%s", i18n.T("updater.darwinNoAppBundle"))
	}

	// 2. Quitar la marca de cuarentena de la copia recién bajada. Sin esto
	//    macOS trataría la aplicación como recién descargada de internet cada
	//    vez que se actualiza.
	_ = exec.Command("xattr", "-dr", "com.apple.quarantine", newBundle).Run()

	// 3. Apartar el bundle actual. Se renombra dentro de su misma carpeta a
	//    propósito: así el rename es atómico y no puede fallar por cruzar de
	//    volumen, cosa que sí pasaría moviéndolo a /tmp. Que el ejecutable en
	//    marcha viva dentro no es problema en macOS.
	backup := currentBundle + ".old"
	_ = os.RemoveAll(backup)
	if err := os.Rename(currentBundle, backup); err != nil {
		return true, fmt.Errorf(i18n.T("updater.darwinReplaceError"), currentBundle, err)
	}

	// 4. Poner el nuevo en el sitio del viejo.
	if err := runTool("ditto", newBundle, currentBundle); err != nil {
		// Deshacer: el usuario se queda como estaba, no sin aplicación.
		_ = os.RemoveAll(currentBundle)
		if rollbackErr := os.Rename(backup, currentBundle); rollbackErr != nil {
			return true, fmt.Errorf(i18n.T("updater.darwinRollbackFailed"), err, rollbackErr)
		}
		return true, fmt.Errorf(i18n.T("updater.darwinCopyError"), err)
	}

	// 5. Borrar la copia vieja. Si falla —cosa rara— no se aborta nada: la
	//    limpieza se reintenta en el siguiente arranque desde cleanPlatform.
	if err := os.RemoveAll(backup); err != nil {
		log.Printf("[UPDATER] %s", i18n.T("updater.oldFileError", backup, err))
	}

	logbus.Success(logbus.CatUpdater, i18n.T("updater.applied"), currentBundle)
	u.setProgress("finishing", 0, 0, 100)

	// 6. Relanzar. La nueva instancia se abre con un retraso corto para que
	//    este proceso alcance a salir y suelte el bloqueo de instancia única
	//    antes de que la otra lo pida.
	relaunchBundle(currentBundle)

	time.Sleep(1 * time.Second)
	_ = os.RemoveAll(tempDir)
	os.Exit(0)

	return true, nil
}

// cleanPlatform retira un bundle .old que se haya quedado atrás porque no se
// pudo borrar mientras la aplicación corría desde dentro de él.
func (u *AppUpdater) cleanPlatform() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, errLink := filepath.EvalSymlinks(exePath); errLink == nil {
		exePath = resolved
	}

	bundle := bundleForExecutable(exePath)
	if bundle == "" {
		return
	}

	backup := bundle + ".old"
	if _, err := os.Stat(backup); err != nil {
		return
	}
	log.Printf("[UPDATER] %s", i18n.T("updater.oldBundleFound", backup))
	if err := os.RemoveAll(backup); err != nil {
		log.Printf("[UPDATER] %s", i18n.T("updater.oldBundleError", backup, err))
	}
}

// bundleForExecutable devuelve la ruta del .app que contiene al ejecutable, o
// cadena vacía si el binario no vive dentro de un bundle. La estructura que se
// espera es Algo.app/Contents/MacOS/binario.
func bundleForExecutable(exePath string) string {
	macOSDir := filepath.Dir(exePath)
	contentsDir := filepath.Dir(macOSDir)
	bundle := filepath.Dir(contentsDir)

	if filepath.Base(macOSDir) != "MacOS" || filepath.Base(contentsDir) != "Contents" {
		return ""
	}
	if !strings.HasSuffix(bundle, ".app") {
		return ""
	}
	return bundle
}

// findAppBundle localiza el primer .app válido dentro de lo descomprimido.
func findAppBundle(root string) string {
	var found string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		// __MACOSX es la carpeta de metadatos que crea ditto al comprimir; no
		// contiene ninguna aplicación de verdad.
		if d.Name() == "__MACOSX" {
			return filepath.SkipDir
		}
		if !strings.HasSuffix(d.Name(), ".app") {
			return nil
		}
		if _, statErr := os.Stat(filepath.Join(path, "Contents", "MacOS")); statErr != nil {
			return nil
		}
		found = path
		return filepath.SkipAll
	})
	return found
}

func extractForMac(archivePath, dest string) error {
	lower := strings.ToLower(archivePath)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		if err := runTool("ditto", "-x", "-k", archivePath, dest); err != nil {
			return fmt.Errorf(i18n.T("updater.darwinExtractError"), err)
		}
		return nil
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return untarGzip(archivePath, dest)
	default:
		return fmt.Errorf(i18n.T("updater.darwinUnsupportedFormat"), filepath.Base(archivePath))
	}
}

// runTool ejecuta una herramienta del sistema y devuelve su salida de error
// dentro del error de Go, que si no se pierde y el fallo queda sin explicación.
func runTool(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			return err
		}
		return fmt.Errorf("%s: %s", err, detail)
	}
	return nil
}

// relaunchBundle abre la aplicación recién instalada en una sesión propia, para
// que siga viva cuando este proceso termine.
func relaunchBundle(bundle string) {
	script := fmt.Sprintf("sleep 2; open -n %s", shellQuote(bundle))
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	log.Printf("[UPDATER] %s", i18n.T("updater.relaunching", bundle))
	if err := cmd.Start(); err != nil {
		log.Printf("[UPDATER] %s", i18n.T("updater.relaunchError", err))
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
