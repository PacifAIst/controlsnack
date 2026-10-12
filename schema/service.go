// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package schema

import (
	"io"

	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
)

// Formats are the renderings `controlsnack schema --format` accepts.
var Formats = []string{"jsonmode", "openai", "anthropic", "ollama"}

// Printer renders the tool schema for one backend format.
type Printer interface {
	Print(w io.Writer, format string) error
}

// NewPrinter returns the placeholder until P5-T01 and P5-T02 implement the tool schemas.
func NewPrinter() Printer { return pendingPrinter{} }

type pendingPrinter struct{}

func (pendingPrinter) Print(io.Writer, string) error { return exitcode.Pending("P5-T01") }
