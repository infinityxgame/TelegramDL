//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

func setupConsole() {
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
		if h, err := syscall.GetStdHandle(syscall.STD_INPUT_HANDLE); err == nil {
			os.Stdin = os.NewFile(uintptr(h), "/dev/stdin")
		}
	}
}

// tryAcquireInstanceLock usa un mutex con nombre de Windows para impedir que
// dos instancias de la app compartan la misma carpeta de descargas con mapas
// de reservas independientes: colisionarían sobre los mismos archivos
// temporales y se borrarían trabajo mutuamente.
func tryAcquireInstanceLock() (func(), bool) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	name, err := syscall.UTF16PtrFromString(`Local\TelegramDL_SingleInstance`)
	if err != nil {
		return func() {}, true
	}
	handle, _, callErr := kernel32.NewProc("CreateMutexW").Call(0, 0, uintptr(unsafe.Pointer(name)))
	const errAlreadyExists = syscall.Errno(183) // ERROR_ALREADY_EXISTS
	if handle == 0 {
		// Sin mutex no hay forma de garantizar exclusión; no bloqueamos el arranque.
		return func() {}, true
	}
	if callErr == errAlreadyExists {
		kernel32.NewProc("CloseHandle").Call(handle)
		return nil, false
	}
	return func() { kernel32.NewProc("CloseHandle").Call(handle) }, true
}

// notifyAlreadyRunning avisa al usuario cuando intenta abrir una segunda instancia.
func notifyAlreadyRunning() {
	user32 := syscall.NewLazyDLL("user32.dll")
	text, _ := syscall.UTF16PtrFromString("TelegramDL ya se está ejecutando. Revisa la ventana abierta o la bandeja del sistema.")
	title, _ := syscall.UTF16PtrFromString("TelegramDL")
	user32.NewProc("MessageBoxW").Call(0,
		uintptr(unsafe.Pointer(text)),
		uintptr(unsafe.Pointer(title)),
		0x40) // MB_ICONINFORMATION
}

func registerConsoleCtrlHandler(sigCh chan os.Signal) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	setHandler := kernel32.NewProc("SetConsoleCtrlHandler")
	cb := syscall.NewCallback(func(ctrlType uint32) uintptr {
		sigCh <- os.Interrupt
		return 1
	})
	_, _, _ = setHandler.Call(cb, 1)

	// Detectar pérdida de consola o señales de interrupción a través de Stdin
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				// Si Stdin falla, es que la consola se ha cerrado
				sigCh <- os.Interrupt
				return
			}
			// Si detectamos específicamente Ctrl+C (ASCII 3) en el buffer de entrada
			if n > 0 && buf[0] == 3 {
				sigCh <- os.Interrupt
				return
			}
		}
	}()
}
