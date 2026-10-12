// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package vm

import "testing"

// TestWHPXPresentOnThisMachine calls the real WHvGetCapability. Whatever this machine answers, the call must
// neither crash nor produce an inconsistent pair; the answer is logged because it decides what `doctor` says.
// Measured on the development machine (Windows 10 19045, WSL2 running, optional feature HypervisorPlatform
// reported as disabled): HypervisorPresent=true.
func TestWHPXPresentOnThisMachine(t *testing.T) {
	present, err := whpxPresent()
	t.Logf("WinHvPlatform.dll: HypervisorPresent=%v, err=%v", present, err)
	if err != nil && present {
		t.Error("an error must come with present=false")
	}
}
