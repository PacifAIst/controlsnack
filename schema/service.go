// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package schema

import "io"

// Formats are the renderings `controlsnack schema --format` accepts.
var Formats = []string{"jsonmode", "openai", "anthropic", "ollama"}

// Printer renders the tool schema for one backend format and one tool profile (full or compact).
// internal/llm/actions provides it.
type Printer interface {
	Print(w io.Writer, format, profile string) error
}
