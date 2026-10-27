//go:build windows

package main

import (
	_ "embed"

	"github.com/energye/systray"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"tgdown/pkg/i18n"
)

//go:embed build/windows/icon.ico
var trayIcon []byte

type trayState struct {
	openItem *systray.MenuItem
	exitItem *systray.MenuItem
	ready    bool
}

func (a *App) isTraySupported() bool {
	return true
}

func (a *App) initSystemTray() {
	systray.Run(a.onTrayReady, a.onTrayExit)
}

func (a *App) onTrayReady() {
	systray.SetTitle("Telegram DL")
	systray.SetTooltip("Telegram DL")
	if len(trayIcon) > 0 {
		systray.SetIcon(trayIcon)
	}

	a.tray.openItem = systray.AddMenuItem(i18n.T("tray.open"), "")
	a.tray.openItem.Click(func() {
		a.RestoreWindow()
	})

	a.tray.exitItem = systray.AddMenuItem(i18n.T("tray.exit"), "")
	a.tray.exitItem.Click(func() {
		a.QuitApp()
	})

	systray.SetOnClick(func(_ systray.IMenu) {
		a.RestoreWindow()
	})
	systray.SetOnDClick(func(_ systray.IMenu) {
		a.RestoreWindow()
	})

	a.tray.ready = true
}

func (a *App) onTrayExit() {
	a.tray.ready = false
}

func (a *App) quitTray() {
	if a.tray.ready {
		systray.Quit()
	}
}

func (a *App) updateTrayMenuTitles() {
	if !a.tray.ready {
		return
	}
	if a.tray.openItem != nil {
		a.tray.openItem.SetTitle(i18n.T("tray.open"))
	}
	if a.tray.exitItem != nil {
		a.tray.exitItem.SetTitle(i18n.T("tray.exit"))
	}
}

func (a *App) hideWindowToTray() {
	if ctx := a.context(); ctx != nil {
		if len(trayIcon) > 0 {
			systray.SetIcon(trayIcon)
		}
		wailsRuntime.WindowHide(ctx)
		a.ShowTrayNotification()
	}
}
