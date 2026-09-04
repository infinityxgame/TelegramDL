package main

import (
	"context"
	"path/filepath"
	"sync"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"tgdown/pkg/config"
	"tgdown/pkg/downloader"
	"tgdown/pkg/listener"
	"tgdown/pkg/server"
	"tgdown/pkg/storage"
	"tgdown/pkg/telegram"
	"tgdown/pkg/updater"
)

type App struct {
	ctx        context.Context
	clientMgr  *telegram.ClientManager
	storage    *storage.Storage
	downloader *downloader.Engine
	listener   *listener.ListenerEngine
	updater    *updater.AppUpdater
	server     *server.Server
	config     config.Config
	mu         sync.Mutex
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	config.InitPaths()

	dbPath := filepath.Join(config.DataDir, "tgdown.db")
	st, err := storage.NewStorage(dbPath)
	if err != nil {
		wailsRuntime.LogErrorf(ctx, "Error al iniciar storage: %v", err)
		return
	}
	a.storage = st

	cfg, err := st.LoadConfig(config.DefaultConfig(), filepath.Join(config.BaseDir, "config.json"))
	if err != nil {
		cfg = config.DefaultConfig()
	}
	a.config = cfg

	cm := telegram.NewClientManager()
	a.clientMgr = cm

	// Iniciar cliente si hay credenciales en .env
	apiID, apiHash := config.LoadEnvCredentials()
	if apiID != "" && apiHash != "" {
		_ = cm.InitClient(apiID, apiHash)
	}

	eng := downloader.NewEngine(cm, st, cfg)
	a.downloader = eng

	le := listener.NewListenerEngine(cm, st, eng, cfg)
	a.listener = le

	up := updater.NewAppUpdater()
	a.updater = up

	srv := server.NewServer(cm, st, eng, le, up, cfg, func() {
		wailsRuntime.Quit(a.ctx)
	})
	a.server = srv

	_ = srv.Start(8000)
}

func (a *App) shutdown(ctx context.Context) {
	if a.server != nil {
		a.server.Stop()
	}
	if a.storage != nil {
		_ = a.storage.Close()
	}
}

// SelectDirectory abre el diálogo nativo del sistema para seleccionar carpetas
func (a *App) SelectDirectory() (string, error) {
	selected, err := wailsRuntime.OpenDirectoryDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title:            "Seleccionar carpeta de descargas",
		DefaultDirectory: a.config.DownloadFolder,
	})
	if err != nil {
		return "", err
	}
	return selected, nil
}
