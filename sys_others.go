//go:build !windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"tgdown/pkg/config"
	"tgdown/pkg/i18n"
)

func setupConsole() {}

// tryAcquireInstanceLock bloquea un archivo en el directorio de datos para
// impedir que dos instancias compartan la misma carpeta de descargas con
// mapas de reservas independientes (se pisarían los archivos temporales).
func tryAcquireInstanceLock() (func(), bool) {
	config.InitPaths()
	lockPath := filepath.Join(config.DataDir, "instance.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		// Sin archivo de bloqueo no hay garantía de exclusión; no bloqueamos el arranque.
		return func() {}, true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, false
	}
	return func() { _ = f.Close() }, true
}

func notifyAlreadyRunning() {
	showNotice(i18n.T("cli.alreadyRunning"))
}

// showNotice enseña un mensaje sin depender de que exista la ventana de la
// aplicación. En Windows es un MessageBox nativo (ver sys_windows.go), visible
// aunque el usuario haya abierto la app haciendo doble clic sin consola. Aquí
// intentamos el aviso equivalente con las herramientas de notificación o diálogo
// habituales de cada escritorio; si ninguna está disponible, caemos al mensaje
// por consola (útil al menos cuando se lanza desde una terminal).
func showNotice(msg string) {
	if trySystemNotification(msg) {
		return
	}
	fmt.Println(msg)
}

// trySystemNotification intenta mostrar un aviso nativo sin depender de que la
// ventana de la app ya exista. Devuelve false si no encontró ninguna
// herramienta utilizable, para que el llamador use el aviso de consola.
func trySystemNotification(msg string) bool {
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf(`display dialog %q with title "TelegramDL" buttons {"OK"} default button "OK" with icon note`, msg)
		return exec.Command("osascript", "-e", script).Run() == nil
	case "linux":
		if _, err := exec.LookPath("notify-send"); err == nil {
			return exec.Command("notify-send", "TelegramDL", msg).Run() == nil
		}
		if _, err := exec.LookPath("zenity"); err == nil {
			return exec.Command("zenity", "--info", "--title=TelegramDL", "--text="+msg).Run() == nil
		}
		return false
	default:
		return false
	}
}

func registerConsoleCtrlHandler(sigCh chan os.Signal) {}

// registerCustomURLScheme registra el protocolo telegramdl:// en sistemas no-Windows
// (Linux mediante XDG Mime Handler y macOS mediante LaunchServices).
func registerCustomURLScheme() {
	switch runtime.GOOS {
	case "linux":
		registerLinuxCustomURLScheme()
	case "darwin":
		registerMacCustomURLScheme()
	}
}

// registerLinuxCustomURLScheme crea o actualiza el archivo .desktop en el directorio XDG del usuario
// y registra telegramdl.desktop como el manejador predeterminado para el protocolo telegramdl://
func registerLinuxCustomURLScheme() {
	exePath, err := os.Executable()
	if err != nil || exePath == "" {
		return
	}
	if evalPath, err := filepath.EvalSymlinks(exePath); err == nil && evalPath != "" {
		exePath = evalPath
	}

	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		return
	}

	appsDir := filepath.Join(homeDir, ".local", "share", "applications")
	if err := os.MkdirAll(appsDir, 0755); err != nil {
		log.Printf("[SYS] %s", i18n.T("system.xdgMkdirError", err))
		return
	}

	desktopFile := filepath.Join(appsDir, "telegramdl.desktop")
	content := fmt.Sprintf(`[Desktop Entry]
Name=TelegramDL
Comment=Telegram Downloader Desktop
Exec="%s" %%u
Icon=telegramdl
Type=Application
Terminal=false
MimeType=x-scheme-handler/telegramdl;
Categories=Network;FileTransfer;
`, exePath)

	existing, err := os.ReadFile(desktopFile)
	if err != nil || string(existing) != content {
		if err := os.WriteFile(desktopFile, []byte(content), 0644); err != nil {
			log.Printf("[SYS] %s", i18n.T("system.xdgDesktopError", err))
			return
		}
	}

	if xdgMime, err := exec.LookPath("xdg-mime"); err == nil {
		cmd := exec.Command(xdgMime, "default", "telegramdl.desktop", "x-scheme-handler/telegramdl")
		if err := cmd.Run(); err != nil {
			log.Printf("[SYS] %s", i18n.T("system.xdgMimeError", err))
		}
	}
}

// registerMacCustomURLScheme registra el paquete .app en LaunchServices de macOS
// cuando la aplicación se ejecuta desde un bundle .app.
func registerMacCustomURLScheme() {
	exePath, err := os.Executable()
	if err != nil || exePath == "" {
		return
	}
	if idx := strings.Index(exePath, ".app/Contents/MacOS/"); idx != -1 {
		appPath := exePath[:idx+4]
		lsregister := "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
		if _, err := os.Stat(lsregister); err == nil {
			cmd := exec.Command(lsregister, "-f", appPath)
			if err := cmd.Run(); err != nil {
				log.Printf("[SYS] %s", i18n.T("system.macLaunchServicesError", err))
			}
		}
	}
}

func registerAppUserModelID(appID, iconPath string) {}

func playNotificationSound() {
	if len(notificationWav) == 0 {
		return
	}
	tempPath := filepath.Join(os.TempDir(), "tgdown_notification.wav")
	if _, err := os.Stat(tempPath); os.IsNotExist(err) {
		_ = os.WriteFile(tempPath, notificationWav, 0644)
	}
	if _, err := exec.LookPath("afplay"); err == nil {
		_ = exec.Command("afplay", tempPath).Start()
	} else if _, err := exec.LookPath("paplay"); err == nil {
		_ = exec.Command("paplay", tempPath).Start()
	} else if _, err := exec.LookPath("aplay"); err == nil {
		_ = exec.Command("aplay", tempPath).Start()
	}
}
