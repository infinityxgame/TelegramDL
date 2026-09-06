package updater

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creativeprojects/go-selfupdate"
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
	release *selfupdate.Release
}

type AppUpdater struct {
	currentVersion string
	repoURL        string
	mu             sync.RWMutex
	progress       Progress
}

func NewAppUpdater() *AppUpdater {
	config.InitPaths()
	return &AppUpdater{
		currentVersion: config.AppVersion,
		repoURL:        config.GithubRepo,
		progress: Progress{
			Status: "idle",
		},
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

func (u *AppUpdater) CheckForUpdate() (*ReleaseInfo, *ReleaseAsset, error) {
	latest, found, err := selfupdate.DetectLatest(context.Background(), selfupdate.ParseSlug(u.repoURL))
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, nil, nil
	}

	// Comparar versiones
	if latest.LessOrEqual(u.currentVersion) {
		return nil, nil, nil
	}

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
		u.setProgress("Descargando actualización...", 0, 0, 20)

		exe, err := os.Executable()
		if err != nil {
			u.setProgress("error: "+err.Error(), 0, 0, 0)
			return
		}

		// Reemplazo nativo del binario
		err = selfupdate.UpdateTo(context.Background(), rel.release.AssetURL, rel.release.AssetName, exe)
		if err != nil {
			u.setProgress("error: "+err.Error(), 0, 0, 0)
			return
		}

		u.setProgress("Actualización completada. Reiniciando...", 0, 0, 100)
		time.Sleep(1 * time.Second)

		u.restartApp()
	}()

	return nil
}

func (u *AppUpdater) restartApp() {
	self, err := os.Executable()
	if err != nil {
		os.Exit(0)
	}

	// En Windows, el binario ya fue reemplazado (el viejo se movió a .old)
	// Lanzamos la nueva instancia y cerramos la actual.
	cmd := exec.Command(self, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	_ = cmd.Start()
	os.Exit(0)
}
