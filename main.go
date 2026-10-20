package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"tgdown/pkg/cli"
	"tgdown/pkg/config"
	"tgdown/pkg/i18n"
	"tgdown/pkg/logbus"
	"tgdown/pkg/updater"
)

//go:embed all:dashboard/dist
var assets embed.FS

// startupURL guarda la URL pasada desde línea de comandos
var startupURL string

func main() {
	// Antes que nada, el registro y la consola hablan el idioma del sistema:
	// el elegido en Ajustes se cargará de la base de datos en NewApp y lo pisará.
	i18n.SetLanguage(i18n.DetectSystemLanguage())

	if len(os.Args) > 1 {
		setupConsole()
	}

	if hasArgument("--help") || hasArgument("-h") {
		printUsage()
		return
	}
	if hasArgument("--server") {
		os.Exit(runServerMode())
	}
	if hasArgument("--update") {
		os.Exit(runUpdateMode())
	}
	if hasArgument("--download") {
		url := getArgumentValue("--download")
		if url == "" {
			fmt.Println(i18n.T("cli.downloadUrlRequired"))
			printUsage()
			os.Exit(1)
		}
		// El modo CLI escribe en la misma base de datos que la aplicación de
		// escritorio: sin el candato de instancia podían coexistir dos
		// procesos sobre el mismo archivo. Se espera a que la otra instancia
		// cierre y, si no suelta el candato, se avisa en vez de descargar.
		release, ok := waitInstanceLock(8 * time.Second)
		if !ok {
			notifyAlreadyRunning()
			os.Exit(1)
		}
		code := cli.RunCLIMode(url)
		release()
		os.Exit(code)
	}

	// Detectar si el primer argumento es una URL de Telegram (sin flag)
	url := getTelegramURLArgument()
	if url != "" {
		os.Exit(handleURLArgument(url))
	}

	runDesktopMode()
}

func hasArgument(target string) bool {
	for _, arg := range os.Args[1:] {
		if strings.EqualFold(arg, target) {
			return true
		}
		if strings.HasPrefix(target, "--") {
			if strings.EqualFold(arg, target[1:]) || strings.EqualFold(arg, "/"+target[2:]) {
				return true
			}
		}
	}
	return false
}

func getArgumentValue(flag string) string {
	for i, arg := range os.Args {
		if arg == flag && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
		if strings.HasPrefix(arg, flag+"=") {
			return strings.TrimPrefix(arg, flag+"=")
		}
	}
	return ""
}

func printUsage() {
	fmt.Println("TelegramDL")
	fmt.Println(i18n.T("cli.usageHeader"))
	fmt.Println(i18n.T("cli.usageDesktop"))
	fmt.Println(i18n.T("cli.usageServer"))
	fmt.Println(i18n.T("cli.usageUpdate"))
	fmt.Println(i18n.T("cli.usageDownload"))
	fmt.Println(i18n.T("cli.usageUrlArg"))
}

// getTelegramURLArgument detecta si algún argumento es una URL de Telegram
func getTelegramURLArgument() string {
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if strings.Contains(arg, "t.me/") || strings.Contains(arg, "telegram.org/") || strings.Contains(arg, "telegram.me/") {
			return arg
		}
	}
	return ""
}

// handleURLArgument maneja una URL pasada como argumento
func handleURLArgument(url string) int {
	config.InitPaths()
	i18n.SetLanguage(i18n.DetectSystemLanguage())

	// Verificar si ya hay una instancia corriendo
	release, ok := tryAcquireInstanceLock()
	if !ok {
		// Hay una instancia activa, enviar la URL vía IPC
		if release != nil {
			release()
		}
		return sendURLToActiveInstance(url)
	}

	// No hay instancia activa, liberar el lock y abrir la app normalmente
	// runDesktopMode() volverá a adquirir el lock internamente
	release()

	// Guardar la URL para que la app la procese al iniciar
	startupURL = url

	fmt.Println(i18n.T("cli.processingURL", url))
	runDesktopMode()
	return 0
}

// sendURLToActiveInstance envía la URL a la instancia activa vía HTTP
func sendURLToActiveInstance(url string) int {
	host := config.GetServerHost()
	port := config.GetServerPort()
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}

	endpoint := fmt.Sprintf("http://%s:%d/api/ipc/download", host, port)

	payload := map[string]string{"url": url}
	jsonData, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, i18n.T("cli.ipcEncodeError"), err)
		return 1
	}

	// http.Post no admite cabeceras: se construye la petición a mano para
	// poder marcarla como propia (SEC-03/SEC-04).
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		fmt.Fprintf(os.Stderr, i18n.T("cli.ipcConnectError"), err)
		fmt.Fprint(os.Stderr, i18n.T("cli.ipcConnectHint"))
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	// Cabecera que el servidor exige para rechazar las peticiones disparadas
	// desde una página web en el navegador (CSRF): el navegador no puede
	// fijarla, así que un sitio remoto no puede encolar descargas.
	req.Header.Set("X-TGDL-Request", "1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, i18n.T("cli.ipcConnectError"), err)
		fmt.Fprint(os.Stderr, i18n.T("cli.ipcConnectHint"))
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, i18n.T("cli.ipcStatusError"), resp.StatusCode)
		return 1
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err == nil {
		if msg, ok := result["message"].(string); ok {
			fmt.Println(msg)
		}
	}

	fmt.Println(i18n.T("cli.ipcSent"))
	return 0
}

// waitInstanceLock reintenta unos segundos antes de rendirse: tras una
// actualización, el proceso anterior lanza el nuevo ejecutable y tarda un
// instante en liberar el bloqueo al salir.
func waitInstanceLock(timeout time.Duration) (func(), bool) {
	deadline := time.Now().Add(timeout)
	for {
		release, ok := tryAcquireInstanceLock()
		if ok || time.Now().After(deadline) {
			return release, ok
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func runDesktopMode() {
	release, ok := waitInstanceLock(8 * time.Second)
	if !ok {
		notifyAlreadyRunning()
		return
	}
	defer release()

	app, appErr := NewApp(assets)
	if app == nil {
		// NewApp solo falla cuando no consigue abrir ni recuperar la base de
		// datos. Abierta con doble clic no hay consola donde leer el motivo,
		// así que el aviso tiene que ser una ventana del sistema, y llevar el
		// error exacto: es la única pista para distinguir un disco lleno de
		// un permiso o un archivo bloqueado.
		showNotice(i18n.T("cli.dbOpenAviso", config.DataDir, appErr))
		return
	}

	// Si hay una URL de inicio, procesarla directamente en el backend
	if startupURL != "" {
		app.SetStartupURL(startupURL)

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			time.Sleep(500 * time.Millisecond)
			if err := app.server.EnqueueURL(ctx, startupURL); err != nil {
				logbus.Warn(logbus.CatSystem, i18n.T("system.startupURLError"), err.Error())
			}
		}()
	}

	err := wails.Run(&options.App{
		Title:     "Telegram DL",
		Width:     1280,
		Height:    720,
		MinWidth:  950,
		MinHeight: 680,
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: app.Handler(),
		},
		BackgroundColour: &options.RGBA{R: 7, G: 17, B: 31, A: 255},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []any{
			app,
		},
		Debug: options.Debug{
			OpenInspectorOnStartup: false,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			BackdropType:         windows.None,
		},
		Mac: &mac.Options{
			TitleBar: &mac.TitleBar{
				TitlebarAppearsTransparent: true,
				HideTitle:                  false,
				HideTitleBar:               false,
				FullSizeContent:            false,
				UseToolbar:                 false,
				HideToolbarSeparator:       true,
			},
			Appearance:           mac.NSAppearanceNameDarkAqua,
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}

func runServerMode() int {
	release, ok := waitInstanceLock(8 * time.Second)
	if !ok {
		fmt.Fprintln(os.Stderr, i18n.T("cli.alreadyRunning"))
		return 1
	}
	defer release()

	app, appErr := NewApp(assets)
	if app == nil {
		fmt.Fprintln(os.Stderr, i18n.T("cli.dbOpenError", config.DataDir, appErr))
		return 1
	}
	if app.server == nil {
		fmt.Fprintln(os.Stderr, i18n.T("cli.serverInitError"))
		return 1
	}

	fmt.Printf(i18n.T("cli.serverStarted"), config.GetServerHost(), config.GetServerPort())
	fmt.Println(i18n.T("cli.serverModeNote"))

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt)

	// Manejo de consola multiplataforma
	registerConsoleCtrlHandler(sigCh)

	// Esperar la señal
	sig := <-sigCh
	fmt.Printf("\n%s\n", i18n.T("cli.closing", sig))

	// Watchdog de seguridad: no más de 5 segundos para cerrar
	go func() {
		time.Sleep(5 * time.Second)
		fmt.Println(i18n.T("cli.forceClose"))
		os.Exit(1)
	}()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	app.shutdown(shutdownCtx)

	fmt.Println(i18n.T("cli.stopped"))
	_ = os.Stdout.Sync()
	os.Exit(0)
	return 0
}

func runUpdateMode() int {
	appUpdater := updater.NewAppUpdater()
	release, asset, err := appUpdater.CheckForUpdate()
	if err != nil {
		fmt.Fprintf(os.Stderr, i18n.T("cli.checkError"), err)
		return 1
	}
	if release == nil || asset == nil {
		fmt.Printf(i18n.T("cli.alreadyUpdated"), config.AppVersion)
		return 0
	}

	fmt.Printf(i18n.T("cli.updating"), config.AppVersion, release.TagName, asset.Name)
	if err := appUpdater.InstallUpdate(release); err != nil {
		fmt.Fprintf(os.Stderr, i18n.T("cli.installError"), err)
		return 1
	}

	lastStatus := ""
	for {
		progress := appUpdater.GetProgress()
		if progress.Status != lastStatus {
			if strings.HasPrefix(progress.Status, "error:") {
				fmt.Fprintln(os.Stderr, progress.Status)
				return 1
			}
			if progress.Status != "" {
				fmt.Printf(i18n.T("cli.progressState"), progress.Status, progress.Percentage)
			}
			lastStatus = progress.Status
		}
		time.Sleep(500 * time.Millisecond)
	}
}
