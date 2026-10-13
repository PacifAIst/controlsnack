// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !unix && !windows

package platform

import "errors"

// FreeSpace is not available on this system.
func FreeSpace(string) (uint64, error) {
	return 0, errors.New("free space cannot be read on this operating system")
}
