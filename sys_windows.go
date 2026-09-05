//go:build windows

package main

import (
	"os"
	"syscall"
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
