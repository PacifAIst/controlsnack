// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"os"

	"github.com/PacifAIst/controlsnack-local/internal/buildinfo"
)

// exitNotImplemented matches the host's exit code for not-yet-built functionality.
const exitNotImplemented = 70

func main() {
	fmt.Fprintf(os.Stderr, "controlsnack-agent %s\nthe agent is not implemented yet (P3-T10)\n", buildinfo.Get())
	os.Exit(exitNotImplemented)
}
