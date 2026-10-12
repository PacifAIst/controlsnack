// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package qemu

import (
	"os/exec"
	"syscall"
)

// prepareCmd puts the child in its own process group, so a Ctrl+C aimed at the controller does not reach it, and
// has the kernel kill it when the controller's thread that started it goes away (Start pins that thread). The
// reaper wrapper does its own watching and must not be killed without a chance to clean up.
func prepareCmd(cmd *exec.Cmd, reaper bool) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if !reaper {
		cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	}
}
