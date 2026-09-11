//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
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
	fmt.Println("TelegramDL ya se está ejecutando.")
}

func registerConsoleCtrlHandler(sigCh chan os.Signal) {}
