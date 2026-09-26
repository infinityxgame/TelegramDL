//go:build !windows

package server

// Apagar el equipo desde la aplicación, fuera de Windows.
//
// En Linux la vía que no pide contraseña es systemctl, que pasa por polkit y
// deja que el usuario de escritorio apague sin ser root; shutdown -h queda como
// plan B para sistemas sin systemd. En macOS el apagado sin privilegios de
// administrador se pide con AppleScript sobre System Events.

import (
	"fmt"
	"os/exec"
	"runtime"
)

func shutdownSystem() error {
	if runtime.GOOS == "darwin" {
		return exec.Command("osascript", "-e", `tell application "System Events" to shut down`).Start()
	}

	if _, err := exec.LookPath("systemctl"); err == nil {
		if err := exec.Command("systemctl", "poweroff").Start(); err == nil {
			return nil
		}
	}
	if _, err := exec.LookPath("shutdown"); err == nil {
		return exec.Command("shutdown", "-h", "now").Start()
	}
	return fmt.Errorf("no se encontró ninguna herramienta de apagado (systemctl/shutdown)")
}
