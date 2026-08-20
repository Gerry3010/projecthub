// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

//go:build windows

package ptyhost

import (
	"os"
	"syscall"
)

// signalProcess has no process-group equivalent on Windows: there is no SIGHUP and
// os.Process.Signal only honours Kill. The graceful path degrades to an immediate
// stop, which is what the platform offers.
func signalProcess(p *os.Process, sig syscall.Signal) {
	if p == nil {
		return
	}
	if sig == syscall.SIGKILL {
		_ = p.Kill()
	}
}
