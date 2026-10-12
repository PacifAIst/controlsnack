// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux && !darwin && !windows

package platform

import (
	"errors"
	"syscall"
)

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// processStartTime is unknown on this platform; SameProcess therefore never matches and nothing is killed on a
// PID alone.
func processStartTime(int) (string, error) { return "", nil }
