//go:build !windows

package main

// trayState es una estructura vacía en sistemas sin bandeja nativa para evitar
// vincular dependencias externas o colisionar con Cocoa en macOS.
type trayState struct{}

func (a *App) isTraySupported() bool {
	return false
}

func (a *App) initSystemTray() {
	// No-op en macOS y Linux: Wails v2 gestiona el bucle de eventos principal (NSApp run)
	// y las librerías externas de tray colisionan con el delegado y causan SIGILL en macOS 12 Monterey.
}

func (a *App) quitTray() {
	// No-op
}

func (a *App) updateTrayMenuTitles() {
	// No-op
}

func (a *App) hideWindowToTray() {
	// No-op en sistemas sin bandeja del sistema
}
