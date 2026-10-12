// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package detect

import (
	"context"
	"io"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
)

// Service is what `controlsnack llm` uses; all output goes to w.
type Service interface {
	// Probe tests the configured (or auto-detected) backend and model for vision, WebP and tool calling.
	Probe(ctx context.Context, w io.Writer, cfg config.Config) error
	// Models lists the models the backend offers.
	Models(ctx context.Context, w io.Writer, cfg config.Config) error
}

// NewService returns the placeholder until P5-T07 implements detection and the probe.
func NewService() Service { return pendingService{} }

type pendingService struct{}

func (pendingService) Probe(context.Context, io.Writer, config.Config) error {
	return exitcode.Pending("P5-T07")
}
func (pendingService) Models(context.Context, io.Writer, config.Config) error {
	return exitcode.Pending("P5-T07")
}
