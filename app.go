package main

import (
	"context"
	"io/fs"
	"net/http"
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
	assets     fs.FS
	mu         sync.Mutex
}

func NewApp(assets fs.FS) *App {
	config.InitPaths()

	// 1. Usar base de datos SQLite original de Python (tgdown.sqlite3)
	dbPath := filepath.Join(config.DataDir, "tgdown.sqlite3")
	st, err := storage.NewStorage(dbPath)
	if err != nil {
		// Fallback si no se puede abrir tgdown.sqlite3
		dbPath = filepath.Join(config.DataDir, "tgdown.db")
		st, _ = storage.NewStorage(dbPath)
	}

	// 2. Cargar configuración previa de SQLite o migrar desde config.json legacy
	cfg, err := st.LoadConfig(config.DefaultConfig(), filepath.Join(config.BaseDir, "config.json"))
	if err != nil {
		cfg = config.DefaultConfig()
	}

	cm := telegram.NewClientManager()
	eng := downloader.NewEngine(cm, st, cfg)
	le := listener.NewListenerEngine(cm, st, eng, cfg)
	up := updater.NewAppUpdater()

	app := &App{
		clientMgr:  cm,
		storage:    st,
		downloader: eng,
		listener:   le,
		updater:    up,
		config:     cfg,
		assets:     assets,
	}

	srv := server.NewServer(cm, st, eng, le, up, cfg, assets, func() {
		if app.ctx != nil {
			wailsRuntime.Quit(app.ctx)
		}
	})
	app.server = srv

	// Iniciar servidor HTTP/WS INMEDIATAMENTE en el puerto configurado (default 8000)
	port := config.GetServerPort()
	_ = srv.Start(port)

	// Conectar cliente de Telegram de forma asíncrona para no retrasar el inicio de la app
	go func() {
		apiID, apiHash := config.LoadEnvCredentials()
		if apiID != "" && apiHash != "" {
			_ = cm.InitClient(apiID, apiHash)
		}
	}()

	return app
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

func (a *App) shutdown(ctx context.Context) {
	if a.server != nil {
		a.server.Stop()
	}
	if a.storage != nil {
		_ = a.storage.Close()
	}
}

func (a *App) Handler() http.Handler {
	if a.server != nil {
		return a.server.Handler()
	}
	return nil
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
