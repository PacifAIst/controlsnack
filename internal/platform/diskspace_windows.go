// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package platform

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// FreeSpace returns the bytes available to this user on the volume that holds path.
func FreeSpace(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, fmt.Errorf("checking free space at %s: %w", path, err)
	}
	return free, nil
}
