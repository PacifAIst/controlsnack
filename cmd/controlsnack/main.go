// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Command controlsnack boots a throwaway local Linux desktop VM with QEMU and lets an LLM operate it
// through a validated tool API, with every safety control on by default.
package main

import (
	"os"

	"github.com/PacifAIst/controlsnack-local/internal/cli"
)

func main() {
	os.Exit(cli.Main())
}
