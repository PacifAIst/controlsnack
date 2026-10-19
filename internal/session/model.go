// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/PacifAIst/controlsnack/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack/internal/config"
	"github.com/PacifAIst/controlsnack/internal/llm/backend"
	"github.com/PacifAIst/controlsnack/internal/llm/detect"
	"github.com/PacifAIst/controlsnack/internal/llm/vision"
)

// ModelFinder is the part of *detect.Detector that a session uses to find its model.
type ModelFinder interface {
	Resolve(ctx context.Context, cfg config.Config) (detect.Target, []detect.ModelInfo, error)
	Probe(ctx context.Context, t detect.Target, force bool) (detect.Capabilities, error)
	Backend(t detect.Target) (backend.Backend, error)
}

// Model is what a session knows about its model before it starts a VM: where it is, what it can do and how the session will use
// it.
type Model struct {
	Target  detect.Target
	Caps    detect.Capabilities
	Setup   vision.Setup
	Backend backend.Backend
	// Notices are things to tell the person before the first turn: the model is not on this machine; it cannot see.
	Notices []string
}

// ResolveModel finds the model the configuration names (or the first local one that sees), finds out what it can do (or reads it
// from the cache) and decides how the session works with it. It is done before the VM is started: a minute and a half of boot is
// a long time to learn that there is no model.
func ResolveModel(ctx context.Context, f ModelFinder, cfg config.Config) (Model, error) {
	t, _, err := f.Resolve(ctx, cfg)
	if err != nil {
		return Model{}, err
	}
	caps, err := f.Probe(ctx, t, false)
	if err != nil {
		return Model{}, fmt.Errorf("finding out what %s can do: %w", textclean.Line(t.Model, 120), err)
	}
	if !caps.Usable() {
		return Model{}, fmt.Errorf("%s cannot name its actions any of the ways a session needs (tool calls, JSON held to a schema, JSON asked for in the prompt): use another model; `controlsnack llm probe` shows what it did", textclean.Line(caps.Model, 120))
	}
	setup, err := vision.Plan(caps, t.Local, cfg.LLM)
	if err != nil {
		return Model{}, err
	}
	b, err := f.Backend(t)
	if err != nil {
		return Model{}, err
	}
	if b == nil {
		return Model{}, errors.New("there is no backend for the model")
	}
	m := Model{Target: t, Caps: caps, Setup: setup, Backend: b}
	if n := t.Notice(); n != "" {
		m.Notices = append(m.Notices, n)
	}
	m.Notices = append(m.Notices, setup.Notices...)
	return m, nil
}
