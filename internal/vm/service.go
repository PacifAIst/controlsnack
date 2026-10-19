// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"context"
	"io"

	"github.com/PacifAIst/controlsnack/internal/config"
)

// Doctor produces the read-only virtualization report behind `controlsnack doctor` (PLAN.md section 7.10);
// DoctorService is the implementation.
type Doctor interface {
	// Run writes the report to w (JSON when asJSON) and returns an error with exit code 3 when no VM can run.
	Run(ctx context.Context, w io.Writer, cfg config.Config, asJSON bool) error
}
