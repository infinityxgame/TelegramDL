//go:build windows

package main

import (
	"log"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"

	"tgdown/pkg/i18n"
)

var (
	winmm          = syscall.NewLazyDLL("winmm.dll")
	procPlaySoundW = winmm.NewProc("PlaySoundW")
)

const (
	sndAsync     = 0x0001
	sndMemory    = 0x0004
	sndNoDefault = 0x0002
)

func playNotificationSound() {
	if len(notificationWav) == 0 {
		return
	}
	procPlaySoundW.Call(
		uintptr(unsafe.Pointer(&notificationWav[0])),
		0,
		uintptr(sndAsync|sndMemory|sndNoDefault),
	)
}

func registerAppUserModelID(appID, iconPath string) {
	if appID == "" || iconPath == "" {
		return
	}
	keyPath := `SOFTWARE\Classes\AppUserModelId\` + appID
	key, _, err := registry.CreateKey(registry.CURRENT_USER, keyPath, registry.SET_VALUE)
	if err == nil {
		_ = key.SetStringValue("DisplayName", appID)
		_ = key.SetStringValue("IconUri", iconPath)
		_ = key.Close()
	}
}

// registerCustomURLScheme registra el protocolo telegramdl:// en el registro de Windows
// asociando la ruta absoluta del ejecutable obtenida dinámicamente con os.Executable()
func registerCustomURLScheme() {
	exePath, err := os.Executable()
	if err != nil || exePath == "" {
		return
	}

	keyPath := `Software\Classes\telegramdl`
	k, _, err := registry.CreateKey(registry.CURRENT_USER, keyPath, registry.SET_VALUE)
	if err == nil {
		_ = k.SetStringValue("", "URL:TelegramDL Protocol")
		_ = k.SetStringValue("URL Protocol", "")
		_ = k.Close()
	}

	cmdPath := `Software\Classes\telegramdl\shell\open\command`
	cmdKey, _, err := registry.CreateKey(registry.CURRENT_USER, cmdPath, registry.SET_VALUE)
	if err == nil {
		_ = cmdKey.SetStringValue("", `"`+exePath+`" "%1"`)
		_ = cmdKey.Close()
	}
}

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
	showNotice(i18n.T("cli.alreadyRunning"))
}

// showNotice enseña un mensaje sin depender de que exista la ventana de la
// aplicación ni una consola. Hace falta para los fallos de arranque: abierta con
// doble clic, la aplicación no tiene dónde escribir.
func showNotice(msg string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	// SEC-38: UTF16PtrFromString solo falla si la cadena lleva un NUL embebido,
	// algo que ni el literal del título ni las cadenas i18n pueden contener,
	// pero el error ya no se descarta en silencio (regla 5). Si el texto
	// fallara, se registra y se aborta el aviso antes que pasar NULL a
	// MessageBoxW. La firma sigue sin devolver error a propósito: quien llama
	// está en mitad de un fallo de arranque y no tiene dónde ir. log.Printf es
	// seguro aquí: como mucho cae en stderr (posiblemente inexistente en una
	// app de doble clic) y jamás depende de la ventana que no se ha llegado a
	// abrir.
	text, err := syscall.UTF16PtrFromString(msg)
	if err != nil {
		log.Printf("[SYS] cannot show startup alert, invalid message text: %v", err)
		return
	}
	// Literal fijo sin NUL: el fallo es imposible, pero se registra igualmente
	// por la misma regla 5 en lugar de un `_ =` mudo.
	title, err := syscall.UTF16PtrFromString("TelegramDL")
	if err != nil {
		log.Printf("[SYS] cannot convert message box title: %v", err)
		return
	}
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
	// SEC-38: el retorno se comprueba en vez de descartarlo. El manejador es
	// lo que hace que Ctrl+C cierre la app ordenadamente: si el registro
	// falla, la única pista es esta línea. r1 == 0 significa fallo. El segundo
	// dword del retorno (r2) no significa nada en esta API y se ignora a
	// propósito; el tercer valor solo se muestra dentro del fallo, porque
	// syscall.Proc.Call construye el error con GetLastError y lo devuelve
	// también en éxito (como Errno(0)).
	if r1, _, callErr := setHandler.Call(cb, 1); r1 == 0 {
		log.Printf("[SYS] SetConsoleCtrlHandler failed: %v", callErr)
	}

	// Vigilar Stdin solo si de verdad hay una consola detrás.
	//
	// Antes se lanzaba siempre, y cuando el primer Read fallaba se daba por
	// hecho que la consola se había cerrado. Lanzado sin consola —un acceso
	// directo con --server, una tarea programada, el servicio arrancando al
	// inicio— ese primer Read falla en el acto, así que el servidor se apagaba
	// solo un instante después de arrancar.
	if !hasConsole() {
		return
	}

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

// hasConsole comprueba que Stdin sea una consola de verdad. GetConsoleMode solo
// funciona con un manejador de consola: con una entrada redirigida, cerrada o
// inexistente devuelve error, que es exactamente lo que hay que distinguir.
func hasConsole() bool {
	handle, err := syscall.GetStdHandle(syscall.STD_INPUT_HANDLE)
	if err != nil || handle == 0 || handle == syscall.InvalidHandle {
		return false
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	var mode uint32
	ret, _, _ := kernel32.NewProc("GetConsoleMode").Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&mode)),
	)
	return ret != 0
}
