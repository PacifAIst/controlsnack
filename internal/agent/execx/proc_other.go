// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !unix

package execx

import (
	"os"
	"os/exec"
	"syscall"
)

// configure refuses: process groups are what makes a timeout reach the whole command, and only the Linux guest runs
// commands.
func configure(*exec.Cmd) error { return ErrUnsupported }

func signalGroup(*exec.Cmd, syscall.Signal) {}

func exitCode(ps *os.ProcessState, _ error) int {
	if ps == nil {
		return -1
	}
	return ps.ExitCode()
}
