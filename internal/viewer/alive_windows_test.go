// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package viewer

import "golang.org/x/sys/windows"

// stillActive is the exit code of a process that has not ended.
const stillActive = 259

// processAlive reports whether a process with this number is running.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) //nolint:gosec // a process number
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == stillActive
}
