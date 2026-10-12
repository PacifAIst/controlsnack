// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !unix

package qemu

import "io"

// RunReaper exists only where the reaper is needed (macOS); Windows ties QEMU to the controller with a job object.
func RunReaper(_ int, _ []string, report io.Writer, _, _ io.Writer) int {
	_, _ = io.WriteString(report, reaperMagic+" error=the reaper is not used on this operating system\n")
	return 2
}
