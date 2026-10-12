// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package vm

import (
	"os"
	"strings"
	"testing"
)

// TestOpenKVMOnThisMachine checks that the error for each usual cause names the cause.
func TestOpenKVMOnThisMachine(t *testing.T) {
	err := openKVM()
	_, statErr := os.Stat(kvmDevice)
	t.Logf("%s: %v", kvmDevice, err)
	switch {
	case err == nil:
	case os.IsNotExist(statErr):
		if !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("%v", err)
		}
	case os.IsPermission(unwrapOpen(err)) || strings.Contains(err.Error(), "kvm group"):
		if !strings.Contains(err.Error(), "kvm group") {
			t.Errorf("the permission error says how to fix it: %v", err)
		}
	default:
		t.Logf("another failure: %v", err)
	}
}

func unwrapOpen(err error) error {
	if pe, ok := err.(*os.PathError); ok { //nolint:errorlint // a test helper over an error this package builds itself
		return pe
	}
	return err
}
