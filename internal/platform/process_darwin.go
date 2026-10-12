// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package platform

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func processStartTime(pid int) (string, error) {
	ki, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if ki.Proc.P_pid == 0 {
		return "", ErrNoSuchProcess
	}
	st := ki.Proc.P_starttime
	return fmt.Sprintf("darwin:%d.%06d", st.Sec, st.Usec), nil
}
