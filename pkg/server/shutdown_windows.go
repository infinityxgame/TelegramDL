//go:build windows

package server

// Apagar el equipo desde la aplicación, en Windows.
//
// shutdown.exe es la vía oficial y no pasa por ningún intérprete de comandos:
// los argumentos son fijos, así que no hay nada que inyectar. No se usa /f a
// propósito: si otra aplicación tiene trabajo sin guardar, merece poder frenar
// el apagado en lugar de perderlo. La espera de cortesía ya ocurrió antes de
// llegar aquí (ver shutdownDelay), así que el temporizador es 0.

import "os/exec"

func apagarEquipo() error {
	return exec.Command("shutdown", "/s", "/t", "0").Start()
}
