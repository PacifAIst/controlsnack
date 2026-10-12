// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package vm

import (
	"errors"
	"fmt"
	"os"
)

const kvmDevice = "/dev/kvm"

// openKVM opens /dev/kvm for reading and writing, the way QEMU will. The error says what to do about the usual
// causes.
func openKVM() error {
	f, err := os.OpenFile(kvmDevice, os.O_RDWR, 0)
	switch {
	case err == nil:
		return f.Close()
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%s does not exist: the CPU lacks virtualization, it is turned off in the firmware, or the kvm module is not loaded", kvmDevice)
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("no permission to open %s: this user is not in the kvm group", kvmDevice)
	}
	return fmt.Errorf("opening %s: %w", kvmDevice, err)
}
