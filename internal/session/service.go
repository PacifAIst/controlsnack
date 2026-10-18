// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"io"
	"os"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
	"github.com/PacifAIst/controlsnack-local/internal/vm"
)

// Runner runs one session end to end: boot the VM, serve the viewer, drive the model.
type Runner interface {
	Run(ctx context.Context, cfg config.Config) error
}

type runner struct {
	out     io.Writer
	deps    deps
	backend vm.Backend
}

// NewRunner returns the runner.
func NewRunner() Runner {
	return &runner{out: os.Stdout, deps: realDeps(), backend: vm.RealBackend()}
}

func (r *runner) Run(ctx context.Context, cfg config.Config) error {
	if cfg.Session.NoLLM && cfg.Viewer.Mode != viewer.ModeNone {
		return exitcode.Usagef("--no-llm holds the VM without a model for testing, and needs --viewer none")
	}
	return r.session(ctx, cfg)
}
