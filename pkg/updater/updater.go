package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	goupdate "github.com/creativeprojects/go-selfupdate"
	"github.com/minio/selfupdate"
	"tgdown/pkg/config"
)

type Progress struct {
	Status     string `json:"status"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
	Percentage int    `json:"percentage"`
}

type ReleaseAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
}

type ReleaseInfo struct {
	TagName string         `json:"tag_name"`
	Body    string         `json:"body"`
	Assets  []ReleaseAsset `json:"assets"`
	release *goupdate.Release
}

type AppUpdater struct {
	currentVersion string
	repoURL        string
	mu             sync.RWMutex
	progress       Progress
	postponedUpdate string
}

func NewAppUpdater() *AppUpdater {
	config.InitPaths()
	u := &AppUpdater{
		currentVersion: config.AppVersion,
		repoURL:        config.GithubRepo,
		progress: Progress{
			Status: "idle",
		},
	}
	u.CleanOldVersion()
	return u
}

// CleanOldVersion busca y elimina archivos temporales de actualizaciones previas (.old)
func (u *AppUpdater) CleanOldVersion() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}

	dir := filepath.Dir(exePath)
	base := filepath.Base(exePath)

	// Intentar detectar archivos .old (estándar de selfupdate y variantes con punto inicial)
	targets := []string{
		exePath + ".old",
		filepath.Join(dir, "."+base+".old"),
	}

	for _, target := range targets {
		if _, err := os.Stat(target); err == nil {
			log.Printf("[UPDATER] Detectado archivo de versión antigua: %s. Eliminando...", target)
			// En Windows, intentamos eliminarlo. os.Remove funciona incluso con atributo oculto.
			if err := os.Remove(target); err != nil {
				log.Printf("[UPDATER] No se pudo eliminar la versión antigua %s: %v", target, err)
			} else {
				log.Printf("[UPDATER] Versión antigua eliminada: %s", target)
			}
		}
	}
}

func (u *AppUpdater) GetProgress() Progress {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.progress
}

func (u *AppUpdater) setProgress(status string, downloaded, total int64, pct int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.progress = Progress{
		Status:     status,
		Downloaded: downloaded,
		Total:      total,
		Percentage: pct,
	}
}

// Postpone marca una versión específica para no volver a notificar en esta sesión
func (u *AppUpdater) Postpone(version string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.postponedUpdate = version
}

// IsPostponed verifica si una versión ya fue pospuesta
func (u *AppUpdater) IsPostponed(version string) bool {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.postponedUpdate == version
}

func (u *AppUpdater) CheckForUpdate() (*ReleaseInfo, *ReleaseAsset, error) {
	log.Printf("[UPDATER] Comprobando actualizaciones para %s (Versión actual: %s)", u.repoURL, u.currentVersion)

	// 1. Intentar detección automática primero
	updater, err := goupdate.NewUpdater(goupdate.Config{})
	if err != nil {
		log.Printf("[UPDATER] Error al crear updater: %v", err)
		return nil, nil, err
	}

	latest, found, err := updater.DetectLatest(context.Background(), goupdate.ParseSlug(u.repoURL))
	if err != nil {
		log.Printf("[UPDATER] Error al detectar última versión: %v", err)
		return nil, nil, err
	}

	// 2. Validar si el asset detectado es para nuestra plataforma
	if found {
		assetName := strings.ToLower(latest.AssetName)
		isWin := strings.Contains(assetName, "win") || strings.Contains(assetName, "exe")
		isLin := strings.Contains(assetName, "linux")
		isMac := strings.Contains(assetName, "darwin") || strings.Contains(assetName, "mac")

		currentOS := runtime.GOOS
		if (currentOS == "windows" && !isWin) || (currentOS == "linux" && !isLin) || (currentOS == "darwin" && !isMac) {
			log.Printf("[UPDATER] El asset detectado automáticamente (%s) no parece correcto para %s. Reintentando con filtros...", latest.AssetName, currentOS)
			found = false
		}
	}

	// 3. Si no se encontró o no era válido, forzar con filtros específicos
	if !found {
		var filters []string
		switch runtime.GOOS {
		case "windows":
			filters = []string{"win"}
		case "darwin":
			filters = []string{"mac"}
		case "linux":
			filters = []string{"linux"}
		}

		updater, _ = goupdate.NewUpdater(goupdate.Config{
			Filters: filters,
		})
		latest, found, err = updater.DetectLatest(context.Background(), goupdate.ParseSlug(u.repoURL))
	}

	if !found || latest == nil {
		log.Printf("[UPDATER] No se encontró ninguna actualización compatible.")
		return nil, nil, nil
	}

	log.Printf("[UPDATER] Última versión encontrada: %s (Asset: %s)", latest.Version(), latest.AssetName)

	// Comparar versiones (normalizando)
	currV := strings.TrimPrefix(u.currentVersion, "v")
	if latest.LessOrEqual(currV) {
		log.Printf("[UPDATER] La versión actual (%s) ya está al día respecto a %s", u.currentVersion, latest.Version())
		return nil, nil, nil
	}

	log.Printf("[UPDATER] ¡Nueva actualización disponible! %s -> %s", u.currentVersion, latest.Version())

	rel := &ReleaseInfo{
		TagName: latest.Version(),
		Body:    latest.ReleaseNotes,
		release: latest,
	}
	asset := &ReleaseAsset{
		Name:        latest.AssetName,
		DownloadURL: latest.AssetURL,
		Size:        int64(latest.AssetByteSize),
	}
	rel.Assets = []ReleaseAsset{*asset}

	return rel, asset, nil
}

func (u *AppUpdater) InstallUpdate(rel *ReleaseInfo) error {
	if rel.release == nil {
		return fmt.Errorf("información de actualización no válida")
	}

	go func() {
		u.setProgress("Iniciando...", 0, 0, 0)

		tempDir, err := os.MkdirTemp("", "tgdown_update")
		if err != nil {
			u.setProgress("error: no se pudo crear directorio temporal", 0, 0, 0)
			return
		}
		defer os.RemoveAll(tempDir)

		// 1. Descarga con progreso manual
		archivePath := filepath.Join(tempDir, rel.release.AssetName)
		u.setProgress("Descargando actualización...", 0, int64(rel.release.AssetByteSize), 5)

		if err := u.downloadWithProgress(rel.release.AssetURL, archivePath); err != nil {
			u.setProgress("error: descarga fallida: "+err.Error(), 0, 0, 0)
			return
		}

		// 2. Extracción
		u.setProgress("Extrayendo archivos...", 0, 0, 85)
		extractPath := filepath.Join(tempDir, "extracted")
		_ = os.MkdirAll(extractPath, 0755)

		lowerArch := strings.ToLower(archivePath)
		if strings.HasSuffix(lowerArch, ".zip") {
			if err := unzip(archivePath, extractPath); err != nil {
				u.setProgress("error: fallo al descomprimir zip: "+err.Error(), 0, 0, 0)
				return
			}
		} else if strings.HasSuffix(lowerArch, ".tar.gz") || strings.HasSuffix(lowerArch, ".tgz") {
			if err := untarGz(archivePath, extractPath); err != nil {
				u.setProgress("error: fallo al descomprimir tar.gz: "+err.Error(), 0, 0, 0)
				return
			}
		} else {
			// Si no es un archivo comprimido, lo tratamos como el binario directo
			extractPath = archivePath
		}

		// 3. Buscar el binario ejecutable real dentro de lo extraído
		newBinaryPath := u.findExecutable(extractPath)
		if newBinaryPath == "" {
			u.setProgress("error: no se encontró el ejecutable en la descarga", 0, 0, 0)
			return
		}

		// 4. Aplicar actualización atómica (reemplazo seguro)
		u.setProgress("Instalando...", 0, 0, 95)
		exePath, err := os.Executable()
		if err != nil {
			u.setProgress("error: no se pudo obtener ruta del ejecutable", 0, 0, 0)
			return
		}

		newBinaryFile, err := os.Open(newBinaryPath)
		if err != nil {
			u.setProgress("error: no se pudo abrir nuevo binario", 0, 0, 0)
			return
		}
		defer newBinaryFile.Close()

		log.Printf("[UPDATER] Aplicando actualización sobre: %s", exePath)
		err = selfupdate.Apply(newBinaryFile, selfupdate.Options{
			TargetPath: exePath,
		})
		if err != nil {
			u.setProgress("error: fallo al aplicar actualización: "+err.Error(), 0, 0, 0)
			return
		}

		u.setProgress("¡Actualizado! Reiniciando...", 0, 0, 100)
		time.Sleep(2 * time.Second)

		u.restartApp(exePath)
	}()

	return nil
}

func (u *AppUpdater) downloadWithProgress(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub devolvió código %d", resp.StatusCode)
	}

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	total := resp.ContentLength
	var downloaded int64
	buffer := make([]byte, 64*1024)

	for {
		n, err := resp.Body.Read(buffer)
		if n > 0 {
			if _, werr := out.Write(buffer[:n]); werr != nil {
				return werr
			}
			downloaded += int64(n)
			pct := 0
			if total > 0 {
				pct = 5 + int(float64(downloaded)/float64(total)*80) // 5% a 85%
			}
			u.setProgress("Descargando...", downloaded, total, pct)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (u *AppUpdater) findExecutable(path string) string {
	fi, err := os.Stat(path)
	if err == nil && !fi.IsDir() {
		return path
	}

	var found string
	var priority int = -1

	_ = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		name := strings.ToLower(info.Name())
		ext := strings.ToLower(filepath.Ext(name))

		// En Windows solo aceptamos .exe
		// En otros sistemas aceptamos sin extensión o cualquier cosa que parezca el binario
		isWindows := runtime.GOOS == "windows"
		if isWindows && ext != ".exe" {
			return nil
		}

		currPriority := -1
		// Nombres exactos tienen prioridad máxima
		if name == "telegramdl.exe" || name == "tgdown.exe" || name == "telegramdl" || name == "tgdown" {
			currPriority = 100
		} else if strings.Contains(name, "telegramdl") || strings.Contains(name, "tgdown") {
			currPriority = 50
		} else if !isWindows && ext == "" {
			// En Linux/Mac, un archivo sin extensión podría ser el binario
			currPriority = 10
		}

		if currPriority > priority {
			priority = currPriority
			found = p
		}

		if priority == 100 {
			return io.EOF // Encontrado el mejor match posible
		}
		return nil
	})
	return found
}

func (u *AppUpdater) restartApp(exePath string) {
	// Usamos la ruta original donde instalamos el nuevo binario
	// En lugar de os.Executable() que podría devolver la ruta del archivo .old en Windows

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// En Windows, ejecutar el binario directamente suele funcionar si no heredamos nada
		cmd = exec.Command(exePath, os.Args[1:]...)
	} else {
		cmd = exec.Command(exePath, os.Args[1:]...)
	}

	log.Printf("[UPDATER] Lanzando nueva versión: %s", exePath)
	err := cmd.Start()
	if err != nil {
		log.Printf("[UPDATER] Error crítico al reiniciar aplicación: %v", err)
	}

	os.Exit(0)
}

// Funciones auxiliares de extracción
func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)
		if !strings.HasPrefix(fpath, filepath.Clean(dest)+string(os.PathSeparator)) {
			continue
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(fpath, os.ModePerm)
			continue
		}
		if err = os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return err
		}
		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}
		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func untarGz(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dest, header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(target, 0755)
		case tar.TypeReg:
			_ = os.MkdirAll(filepath.Dir(target), 0755)
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			_, err = io.Copy(outFile, tr)
			outFile.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}
