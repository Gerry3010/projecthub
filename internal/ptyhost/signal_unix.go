// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

//go:build !windows

package ptyhost

import (
	"os"
	"syscall"
)

// signalProcess sends sig to the process GROUP led by p (a PTY child is a session
// leader, so its pid is its group id), falling back to the single process when the
// group send fails — e.g. because the child already reaped its own children.
func signalProcess(p *os.Process, sig syscall.Signal) {
	if p == nil {
		return
	}
	if err := syscall.Kill(-p.Pid, sig); err == nil {
		return
	}
	_ = p.Signal(sig)
}
