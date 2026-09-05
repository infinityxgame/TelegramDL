//go:build !windows

package main

import "os"

func setupConsole() {}

func registerConsoleCtrlHandler(sigCh chan os.Signal) {}
