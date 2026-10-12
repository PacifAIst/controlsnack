// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix && !linux

package qemu

import (
	"os/exec"
	"syscall"
)

// prepareCmd puts the child in its own process group. These systems have no way to end a child with its parent;
// the reaper wrapper (reaper.go) does that, so the reaper itself must be able to clean up and is not special here.
func prepareCmd(cmd *exec.Cmd, _ bool) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
