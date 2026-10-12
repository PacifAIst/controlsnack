// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package qemu

import (
	"errors"
	"os"
	"sync/atomic"
	"syscall"
)

// adopt has nothing to do on Unix: the child is tied to the controller by Pdeathsig (Linux) or the reaper (macOS).
func adopt(int) (release func(), err error) { return func() {}, nil }

// killTree ends the process group of pid, which is QEMU's own (it was started with Setpgid), so anything QEMU
// started goes with it. If the group cannot be signaled, the process is killed directly.
func killTree(pid int, proc *os.Process, reaped *atomic.Bool) error {
	if reaped.Load() {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-pid, syscall.SIGKILL)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.ESRCH):
		return os.ErrProcessDone
	}
	return proc.Kill()
}
