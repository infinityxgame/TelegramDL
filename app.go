package main

import (
	"context"
	_ "embed"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"git.sr.ht/~jackmordaunt/go-toast/v2/wintoast"
	"github.com/energye/systray"
	"github.com/gen2brain/beeep"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"tgdown/pkg/config"
	"tgdown/pkg/downloader"
	"tgdown/pkg/i18n"
	"tgdown/pkg/listener"
	"tgdown/pkg/logbus"
	"tgdown/pkg/server"
	"tgdown/pkg/storage"
	"tgdown/pkg/telegram"
	"tgdown/pkg/updater"
)

//go:embed build/appicon.png
var appIconPng []byte

//go:embed build/windows/icon.ico
var trayIcon []byte

//go:embed dashboard/public/notification.wav
var notificationWav []byte

type App struct {
	ctx        context.Context
	clientMgr  *telegram.ClientManager
	storage    *storage.Storage
	downloader *downloader.Engine
	listener   *listener.ListenerEngine
	updater    *updater.AppUpdater
	server     *server.Server
	// config es la copia que consume la bandeja del sistema (MinimizeToTray).
	// El diálogo de carpetas NO la usa: pregunta al servidor con GetConfig()
	// para no quedarse congelado en el valor de arranque (SEC-40).
	config             config.Config
	assets             fs.FS
	mu                 sync.Mutex
	startupURL         string // URL pasada desde línea de comandos al iniciar
	trayOpenItem       *systray.MenuItem
	trayExitItem       *systray.MenuItem
	trayReady          bool
	lastTrayNotifyTime time.Time
}

func NewApp(assets fs.FS) (*App, error) {
	config.InitPaths()

	// 0. Arrancar el registro antes que nada: a partir de aquí, todo lo que la
	// aplicación escriba con log.Printf acaba también en la vista de logs del
	// panel y en ~/.tgdown/logs/log_DD-MM-YY.log.
	logFileErr := logbus.Init(config.DataDir)
	logbus.Attach()
	logbus.Success(logbus.CatSystem, i18n.T("system.appStarted", config.AppVersion), "")
	if logFileErr != nil {
		logbus.Warn(logbus.CatSystem, i18n.T("system.logFileError"), logFileErr.Error())
	} else if path := logbus.FilePath(); path != "" {
		logbus.Info(logbus.CatSystem, i18n.T("system.logFileActive"), path)
	}

	// 1. Abrir la base de datos única (la de Python: tgdown.sqlite3). Si está
	// dañada, OpenResilient la aparta renombrada, crea una nueva y rescata en
	// ella todo lo legible; el fallback a un segundo archivo (tgdown.db) se
	// retiró porque repartía los datos entre dos bases según cuál abriera.
	dbPath := filepath.Join(config.DataDir, "tgdown.sqlite3")
	st, err := storage.OpenResilient(dbPath)
	if err != nil {
		// Sin base de datos no hay nada que arrancar. El error exacto sube a
		// quien llama: en el escritorio se muestra en el aviso de arranque y
		// en el modo servidor sale por la consola.
		logbus.Error(logbus.CatSystem, i18n.T("system.dbOpenFatal"), err.Error())
		return nil, err
	}

	// SEC-30: la base de datos (y sus ficheros -wal/-shm) se acaban de crear
	// aquí con los permisos por defecto del sistema; se cierran a 0600 justo
	// después de un NewStorage exitoso. dbPath ya apunta a la BD realmente
	// usada, incluido el caso de respaldo (tgdown.db).
	config.SecureDataDir(dbPath, dbPath+"-wal", dbPath+"-shm")

	// 1.b Volcar a la base de datos lo que quedara en el .env de una versión
	// anterior y fijar desde ahí la dirección de escucha. Tiene que ir antes de
	// cualquier config.GetServerPort/GetServerHost, porque es de donde esas
	// funciones sacan el valor: pkg/config no puede leer la base de datos por sí
	// mismo sin crear un ciclo de imports.
	st.MigrateLegacyEnv()
	bindHost, bindPort := st.ServerBinding()
	config.SetServerBinding(bindHost, bindPort)

	// 2. Cargar configuración previa de SQLite o migrar desde config.json legacy
	cfg, err := st.LoadConfig(config.DefaultConfig(), filepath.Join(config.BaseDir, "config.json"))
	if err != nil {
		cfg = config.DefaultConfig()
	}

	// El idioma del registro manda lo que el usuario eligió en Ajustes. Hasta
	// que se cargó esta línea, los mensajes salían en el idioma detectado del
	// sistema (fijado en main), que es lo único disponible antes de abrir la
	// base de datos.
	if cfg.Language != "" {
		i18n.SetLanguage(cfg.Language)
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
		assets:     assets,
	}

	srv := server.NewServer(cm, st, eng, le, up, cfg, assets, func() {
		if ctx := app.context(); ctx != nil {
			wailsRuntime.Quit(ctx)
		}
	})
	app.server = srv

	srv.SetLanguageChangeHandler(func() {
		app.UpdateTrayMenuTitles()
	})
	srv.SetConfigChangeHandler(func(newCfg config.Config) {
		app.SetConfig(newCfg)
	})

	// Permitir que el panel abra el diálogo nativo de carpetas aunque se esté
	// viendo en un navegador del propio equipo, y no solo dentro de la ventana
	// de la aplicación: así el selector es siempre el mismo en local. El
	// servidor solo atiende esta llamada desde el propio equipo.
	srv.SetFolderPicker(func() (string, error) {
		if app.context() == nil {
			return "", fmt.Errorf("%s", i18n.T("app.windowNotReady"))
		}
		return app.SelectDirectory()
	})

	// Iniciar el System Tray en segundo plano
	go app.initSystemTray()

	// Registrar en el log todo lo que hagan los motores de descarga y escucha.
	app.attachLogWatchers()

	// Iniciar servidor HTTP/WS INMEDIATAMENTE en el puerto configurado (default 8000)
	port := config.GetServerPort()
	if err := srv.Start(port); err != nil {
		fmt.Fprintf(os.Stderr, i18n.T("server.startError")+": %v\n", port, err)
		logbus.Error(logbus.CatServer, i18n.T("server.startError", port), err.Error())
	} else {
		logbus.Success(logbus.CatServer, i18n.T("server.listening", config.GetServerHost(), port), "")
	}

	// Cargar credenciales desde la base de datos (con fallback a .env) e inicializar cliente
	apiID, apiHash, _ := st.GetCredentials()
	if apiID != "" && apiHash != "" {
		_ = cm.InitClient(apiID, apiHash)
	} else {
		logbus.Warn(logbus.CatTelegram, i18n.T("telegram.noCredentials"), i18n.T("telegram.noCredentialsHint"))
	}

	return app, nil
}

// setContext y context guardan el contexto de Wails detrás del mutex. Lo
// escribe startup (el hilo de la interfaz) y lo leen los observadores de estado
// de los motores de descarga y escucha, que corren en otras goroutines: sin el
// candado eso es una carrera de datos, y el campo mu existía sin usarse.
func (a *App) setContext(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ctx = ctx
}

func (a *App) context() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ctx
}

func (a *App) startup(ctx context.Context) {
	a.setContext(ctx)

	// Monitorear en segundo plano la minimización de la ventana nativa
	go a.monitorMinimize()

	// Emitir cambios de estado directamente a la interfaz nativa de Wails
	a.downloader.OnStateChange(func(item storage.DownloadItem) {
		if c := a.context(); c != nil && a.server != nil {
			wailsRuntime.EventsEmit(c, "tgdl:state", a.server.BuildStateSnapshot())
		}
	})
	a.listener.OnStateChange(func(item listener.ListenerItem) {
		if c := a.context(); c != nil && a.server != nil {
			wailsRuntime.EventsEmit(c, "tgdl:state", a.server.BuildStateSnapshot())
		}
	})
}

// shutdown detiene la aplicación en el orden que evita perder estado (SEC-36):
// primero el motor de descargas, que todavía necesita el cliente de Telegram
// vivo para persistir su situación y devolver los bloques en vuelo; después el
// cliente de Telegram; después el servidor HTTP, para que ninguna petición
// entrante siga tocando la base de datos cuando se cierre; y por último SQLite.
// Cerrar antes el cliente o la base de datos mientras hay descargas vivas era
// lo que dejaba estados sin guardar y volcados contra una BD ya cerrada.
func (a *App) shutdown(ctx context.Context) {
	logbus.Info(logbus.CatSystem, i18n.T("system.closing"), "")
	if a.trayReady {
		systray.Quit()
	}
	if a.downloader != nil {
		// Un error aquí (p. ej. context.DeadlineExceeded) significa que
		// quedaron trabajos o escrituras sin terminar: se registra para que el
		// cierre incompleto se vea en los registros, nunca en silencio.
		if err := a.downloader.Shutdown(ctx); err != nil {
			logbus.Warn(logbus.CatSystem, "engine shutdown incomplete", err.Error())
		}
	}
	if a.clientMgr != nil {
		a.clientMgr.Stop()
	}
	if a.server != nil {
		a.server.Stop()
	}
	if a.storage != nil {
		if err := a.storage.Close(); err != nil {
			logbus.Error(logbus.CatSystem, "database close failed", err.Error())
		}
	}
	logbus.CloseFile()
}

func (a *App) Handler() http.Handler {
	if a.server != nil {
		return a.server.Handler()
	}
	return nil
}

// SelectDirectory abre el diálogo nativo del sistema para seleccionar carpetas
func (a *App) SelectDirectory() (string, error) {
	// El caso «ventana no lista» ya queda cubierto aquí: sin contexto de Wails
	// no hay forma de abrir el diálogo nativo (el folderPicker del servidor
	// comprueba lo mismo antes de llamarnos).
	ctx := a.context()
	if ctx == nil {
		return "", fmt.Errorf("%s", i18n.T("app.windowNotReady"))
	}
	// La fuente de verdad de la configuración es Server.config, que los
	// handlers de guardado actualizan al vuelo: leerla aquí hace que el
	// diálogo arranque en la carpeta que el usuario tenga AHORA, no en la de
	// al abrir la sesión (SEC-40). Sin servidor (o si vino vacía) se cae a la
	// carpeta por defecto del sistema.
	defaultDir := config.GetDefaultDownloadFolder()
	if a.server != nil {
		if folder := a.server.GetConfig().DownloadFolder; folder != "" {
			defaultDir = folder
		}
	}
	selected, err := wailsRuntime.OpenDirectoryDialog(ctx, wailsRuntime.OpenDialogOptions{
		Title:            i18n.T("settings.selectFolderTitle"),
		DefaultDirectory: defaultDir,
	})
	if err != nil {
		return "", err
	}
	return selected, nil
}

type ServerInfo struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

func (a *App) GetServerInfo() ServerInfo {
	return ServerInfo{
		Host: config.GetServerHost(),
		Port: config.GetServerPort(),
	}
}

// PostponeUpdate marca la actualización para no mostrarse más en esta sesión
func (a *App) PostponeUpdate(version string) {
	if a.updater != nil {
		a.updater.Postpone(version)
	}
}

// GetLocalToken devuelve el token de acceso a la API HTTP local a través del
// binding nativo de Wails (IPC interno, nunca por red). Es el mecanismo que
// usa la propia app de escritorio para autenticar sus llamadas a /api/* sin
// que el usuario tenga que escribir nada: solo un cliente remoto (navegador
// en otro dispositivo) necesita introducirlo manualmente una vez.
func (a *App) GetLocalToken() string {
	if a.server == nil {
		return ""
	}
	return a.server.APIToken()
}

// RegenerateLocalToken rota el token de acceso a la API. Cualquier
// dispositivo remoto que use el token anterior deberá reconfigurarse con el
// nuevo valor devuelto aquí.
func (a *App) RegenerateLocalToken() string {
	if a.server == nil {
		return ""
	}
	return a.server.RegenerateToken()
}

// GetStartupURL devuelve la URL pasada desde línea de comandos al iniciar y la limpia.
func (a *App) GetStartupURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	url := a.startupURL
	a.startupURL = ""
	return url
}

// SetStartupURL establece la URL de inicio.
func (a *App) SetStartupURL(url string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.startupURL = url
}

func (a *App) initSystemTray() {
	systray.Run(a.onTrayReady, a.onTrayExit)
}

func (a *App) onTrayReady() {
	systray.SetTitle("Telegram DL")
	systray.SetTooltip("Telegram DL")
	if len(trayIcon) > 0 {
		systray.SetIcon(trayIcon)
	}

	a.trayOpenItem = systray.AddMenuItem(i18n.T("tray.open"), "")
	a.trayOpenItem.Click(func() {
		a.RestoreWindow()
	})

	a.trayExitItem = systray.AddMenuItem(i18n.T("tray.exit"), "")
	a.trayExitItem.Click(func() {
		a.QuitApp()
	})

	systray.SetOnClick(func(_ systray.IMenu) {
		a.RestoreWindow()
	})
	systray.SetOnDClick(func(_ systray.IMenu) {
		a.RestoreWindow()
	})

	a.trayReady = true
}

func (a *App) onTrayExit() {
	a.trayReady = false
}

func (a *App) RestoreWindow() {
	if ctx := a.context(); ctx != nil {
		wailsRuntime.WindowShow(ctx)
		wailsRuntime.WindowUnminimise(ctx)
	}
}

func (a *App) QuitApp() {
	if a.trayReady {
		systray.Quit()
	}
	if ctx := a.context(); ctx != nil {
		wailsRuntime.Quit(ctx)
	}
}

func (a *App) UpdateTrayMenuTitles() {
	if !a.trayReady {
		return
	}
	if a.trayOpenItem != nil {
		a.trayOpenItem.SetTitle(i18n.T("tray.open"))
	}
	if a.trayExitItem != nil {
		a.trayExitItem.SetTitle(i18n.T("tray.exit"))
	}
}

// HideToTray oculta la ventana al system tray, muestra el ícono en la bandeja y envía la notificación nativa
func (a *App) HideToTray() {
	if ctx := a.context(); ctx != nil {
		if len(trayIcon) > 0 {
			systray.SetIcon(trayIcon)
		}
		wailsRuntime.WindowHide(ctx)
		a.ShowTrayNotification()
	}
}

// ShowTrayNotification muestra una notificación nativa del sistema informando que la app sigue en segundo plano
func (a *App) ShowTrayNotification() {
	a.mu.Lock()
	if time.Since(a.lastTrayNotifyTime) < 3*time.Second {
		a.mu.Unlock()
		return
	}
	a.lastTrayNotifyTime = time.Now()
	a.mu.Unlock()

	go func() {
		appName := i18n.T("tray.notificationTitle")
		body := i18n.T("tray.notificationBody")
		iconPath := a.getAppIconPngPath()

		beeep.AppName = appName

		// Registrar AppUserModelId para que Windows ponga el ícono PNG en la cabecera (al lado de appName)
		registerAppUserModelID(appName, iconPath)

		// Construir XML Toast explícito con hint-style="body" para asegurar texto normal sin negritas
		xml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<toast duration="short">
    <visual>
        <binding template="ToastGeneric">
            <text hint-style="body" hint-wrap="true">%s</text>
        </binding>
    </visual>
    <audio src="ms-winsoundevent:Notification.Default" />
</toast>`, body)

		if err := wintoast.Push(appName, xml, wintoast.PowershellFallback); err != nil {
			if errBeeep := beeep.Notify(" ", body, ""); errBeeep != nil {
				logbus.Warn(logbus.CatSystem, i18n.T("system.trayNotifyError", errBeeep), errBeeep.Error())
			}
		}
	}()
}

// ShowQueueFinishedNotification muestra una notificación nativa del sistema e infla el sonido de la web cuando finaliza la cola
func (a *App) ShowQueueFinishedNotification() {
	go func() {
		// Reproducir el sonido personalizado notification.wav en segundo plano
		playNotificationSound()

		appName := i18n.T("tray.notificationTitle")
		body := i18n.T("tray.completedBody")
		iconPath := a.getAppIconPngPath()

		beeep.AppName = appName

		// Registrar AppUserModelId para que Windows ponga el ícono PNG en la cabecera (al lado de appName)
		registerAppUserModelID(appName, iconPath)

		// Construir XML Toast explícito con audio silencioso para que suene únicamente notification.wav
		xml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<toast duration="short">
    <visual>
        <binding template="ToastGeneric">
            <text hint-style="body" hint-wrap="true">%s</text>
        </binding>
    </visual>
    <audio silent="true" />
</toast>`, body)

		if err := wintoast.Push(appName, xml, wintoast.PowershellFallback); err != nil {
			if errBeeep := beeep.Notify(appName, body, ""); errBeeep != nil {
				logbus.Warn(logbus.CatSystem, i18n.T("system.trayNotifyError", errBeeep), errBeeep.Error())
			}
		}
	}()
}

func (a *App) getAppIconPngPath() string {
	if len(appIconPng) == 0 {
		return ""
	}
	iconPath := filepath.Join(os.TempDir(), "tgdown_appicon.png")
	if _, err := os.Stat(iconPath); os.IsNotExist(err) {
		if err := os.WriteFile(iconPath, appIconPng, 0644); err != nil {
			return ""
		}
	}
	return iconPath
}

func (a *App) SetConfig(cfg config.Config) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.config = cfg
}

func (a *App) monitorMinimize() {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	wasMinimised := false

	for {
		select {
		case <-ticker.C:
			ctx := a.context()
			if ctx == nil {
				continue
			}

			a.mu.Lock()
			enabled := a.config.MinimizeToTray
			a.mu.Unlock()

			if !enabled {
				wasMinimised = false
				continue
			}

			isMinimised := wailsRuntime.WindowIsMinimised(ctx)
			if isMinimised && !wasMinimised {
				wasMinimised = true
				a.HideToTray()
			} else if !isMinimised {
				wasMinimised = false
			}
		}
	}
}
