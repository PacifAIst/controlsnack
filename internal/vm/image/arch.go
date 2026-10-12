// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import "runtime"

// hostArch is the guest architecture of this machine: guests are never emulated across architectures (D-01).
func hostArch() string { return ArchFor(runtime.GOARCH) }

// ArchFor maps a Go architecture name to the guest architecture; unknown ones map to themselves.
func ArchFor(goarch string) string {
	switch goarch {
	case "amd64":
		return ArchX86_64
	case "arm64":
		return ArchAarch64
	}
	return goarch
}
