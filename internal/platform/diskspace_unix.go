// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package platform

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// FreeSpace returns the bytes available to this user on the file system that holds path.
func FreeSpace(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("checking free space at %s: %w", path, err)
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil //nolint:gosec // block counts and sizes are never negative
}
