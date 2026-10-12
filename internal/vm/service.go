// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"context"
	"io"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
)

// Doctor produces the read-only virtualization report behind `controlsnack doctor` (PLAN.md section 7.10).
type Doctor interface {
	// Run writes the report to w (JSON when asJSON) and returns an error with exit code 3 when no VM can run.
	Run(ctx context.Context, w io.Writer, cfg config.Config, asJSON bool) error
}

// NewDoctor returns the placeholder until P3-T14 implements the report.
func NewDoctor() Doctor { return pendingDoctor{} }

type pendingDoctor struct{}

func (pendingDoctor) Run(context.Context, io.Writer, config.Config, bool) error {
	return exitcode.Pending("P3-T14")
}
