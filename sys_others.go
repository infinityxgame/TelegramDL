//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"

	"tgdown/pkg/config"
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
	const msg = "TelegramDL ya se está ejecutando. Revisa la ventana abierta o la bandeja del sistema."
	// En Windows el aviso usa un MessageBox nativo (ver sys_windows.go), visible
	// aunque el usuario haya abierto la app haciendo doble clic sin consola. Aquí
	// intentamos un aviso equivalente con las herramientas de notificación/dialogo
	// habituales de cada escritorio; si ninguna está disponible, caemos al mensaje
	// por consola (útil al menos cuando se lanza desde una terminal).
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
