// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux && !windows && !darwin

package vm

import (
	"context"

	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
)

// collectFacts knows nothing about this operating system beyond its name: there is no hardware accelerator to
// diagnose, so software emulation is the only answer.
func collectFacts(_ context.Context, h qemu.Host, _ *qemu.Install) Facts {
	return Facts{OS: h.GOOS(), Arch: h.GOARCH(), OSVersion: h.GOOS()}
}
