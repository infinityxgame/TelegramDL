package main

import (
	"context"
	"embed"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
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

func setupConsole() {
	if runtime.GOOS == "windows" {
		kernel32 := syscall.NewLazyDLL("kernel32.dll")
		attachConsole := kernel32.NewProc("AttachConsole")
		const ATTACH_PARENT_PROCESS = ^uint32(0)
		r, _, _ := attachConsole.Call(uintptr(ATTACH_PARENT_PROCESS))
		if r != 0 {
			if h, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE); err == nil {
				os.Stdout = os.NewFile(uintptr(h), "/dev/stdout")
			}
			if h, err := syscall.GetStdHandle(syscall.STD_ERROR_HANDLE); err == nil {
				os.Stderr = os.NewFile(uintptr(h), "/dev/stderr")
			}
		}
	}
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
	app := NewApp(nil)
	if app.server == nil {
		fmt.Fprintln(os.Stderr, "Error: no se pudo inicializar el servidor")
		return 1
	}

	fmt.Printf("Servidor iniciado en http://%s:%d\n", config.GetServerHost(), config.GetServerPort())
	fmt.Println("Modo servidor activo. Presiona Ctrl+C para detenerlo.")

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals
	signal.Stop(signals)
	app.shutdown(context.Background())
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
