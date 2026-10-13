// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package execx

import (
	"os"
	"os/exec"
	"syscall"
)

// configure puts the command in a process group of its own, so that signals can reach everything it starts.
func configure(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

// signalGroup sends a signal to the command's whole process group.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	// A negative pid addresses the group; the shell is its leader, so the group id is the shell's pid.
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}

// exitCode is the shell convention: the exit status, or 128 plus the signal that killed the process.
func exitCode(ps *os.ProcessState, waitErr error) int {
	if ps == nil {
		return -1
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}
