package main

import (
	"context"
	"embed"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"tgdown/pkg/config"
	"tgdown/pkg/updater"
)

//go:embed all:dashboard/dist
var assets embed.FS

func main() {
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

func printUsage() {
	fmt.Println("TelegramDL")
	fmt.Println("Uso:")
	fmt.Println("  TGDown.exe              Abrir la interfaz de escritorio")
	fmt.Println("  TGDown.exe --server     Ejecutar el servidor sin abrir ventana")
	fmt.Println("  TGDown.exe --update     Buscar e instalar la última actualización")
}

func runDesktopMode() {
	app := NewApp(assets)

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
	})

	if err != nil {
		println("Error:", err.Error())
	}
}

func runServerMode() int {
	app := NewApp(assets)
	if app.server == nil {
		fmt.Fprintln(os.Stderr, "Error: no se pudo inicializar el servidor")
		return 1
	}

	fmt.Printf("Servidor iniciado en http://%s:%d\n", config.GetServerHost(), config.GetServerPort())
	fmt.Println("Modo servidor activo. Presiona Ctrl+C para detenerlo.")

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt)

	// Manejo de consola multiplataforma
	registerConsoleCtrlHandler(sigCh)

	// Esperar la señal
	sig := <-sigCh
	fmt.Printf("\n[SISTEMA] Cerrando TelegramDL (Señal: %v)...\n", sig)

	// Watchdog de seguridad: no más de 5 segundos para cerrar
	go func() {
		time.Sleep(5 * time.Second)
		fmt.Println("[ALERTA] Forzando cierre inmediato.")
		os.Exit(1)
	}()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	app.shutdown(shutdownCtx)

	fmt.Println("[SISTEMA] TelegramDL detenido. Ya puedes cerrar la consola o seguir usándola.")
	_ = os.Stdout.Sync()
	os.Exit(0)
	return 0
}

func runUpdateMode() int {
	appUpdater := updater.NewAppUpdater()
	release, asset, err := appUpdater.CheckForUpdate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error al comprobar actualizaciones: %v\n", err)
		return 1
	}
	if release == nil || asset == nil {
		fmt.Printf("La aplicación ya está actualizada (v%s).\n", config.AppVersion)
		return 0
	}

	fmt.Printf("Actualizando de v%s a %s con %s...\n", config.AppVersion, release.TagName, asset.Name)
	if err := appUpdater.InstallUpdate(release); err != nil {
		fmt.Fprintf(os.Stderr, "Error al iniciar la actualización: %v\n", err)
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
				fmt.Printf("Estado: %s (%d%%)\n", progress.Status, progress.Percentage)
			}
			lastStatus = progress.Status
		}
		time.Sleep(500 * time.Millisecond)
	}
}
