// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Command controlsnack boots a throwaway local Linux desktop VM with QEMU and lets an LLM operate it
// through a validated tool API, with every safety control on by default.
//
// The cobra command tree arrives in Phase 1 (P1-T01); until then this entry point only identifies the build.
package main

import (
	"fmt"
	"os"

	"github.com/PacifAIst/controlsnack-local/internal/buildinfo"
)

// exitNotImplemented is the exit code for commands that exist but are not built yet (PLAN.md P1-T05).
const exitNotImplemented = 70

func main() {
	fmt.Fprintf(os.Stderr, "controlsnack %s\nthe command tree is not implemented yet\n", buildinfo.Get())
	os.Exit(exitNotImplemented)
}
