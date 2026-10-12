// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
)

// Runner runs one session end to end: boot the VM, serve the viewer, drive the model.
type Runner interface {
	Run(ctx context.Context, cfg config.Config) error
}

// NewRunner returns the placeholder runner until P7-T06 wires the real one.
func NewRunner() Runner { return pendingRunner{} }

type pendingRunner struct{}

func (pendingRunner) Run(context.Context, config.Config) error { return exitcode.Pending("P7-T06") }
