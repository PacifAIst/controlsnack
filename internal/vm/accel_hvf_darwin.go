// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package vm

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// hvfSupported reads sysctl kern.hv_support. The entitlement of the QEMU binary is not visible here; the probe
// launch that follows a yes is what shows it.
func hvfSupported() (bool, error) {
	v, err := unix.SysctlUint32("kern.hv_support")
	if err != nil {
		return false, fmt.Errorf("reading sysctl kern.hv_support: %w", err)
	}
	return v == 1, nil
}
