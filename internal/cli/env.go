// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"io"
	"os"
	"runtime"

	"github.com/PacifAIst/controlsnack/internal/llm/actions"
	"github.com/PacifAIst/controlsnack/internal/llm/detect"
	"github.com/PacifAIst/controlsnack/internal/platform"
	"github.com/PacifAIst/controlsnack/internal/safety/audit"
	"github.com/PacifAIst/controlsnack/internal/safety/killswitch"
	"github.com/PacifAIst/controlsnack/internal/session"
	"github.com/PacifAIst/controlsnack/internal/vm"
	"github.com/PacifAIst/controlsnack/internal/vm/image"
	"github.com/PacifAIst/controlsnack/internal/vm/overlay"
	"github.com/PacifAIst/controlsnack/schema"
)

// Services are the implementations the commands call. Each interface lives in the package that owns the
// behavior (PLAN.md P1-T03); tests replace them with fakes.
type Services struct {
	Runner session.Runner
	Killer killswitch.Client
	Images image.Manager
	VMs    overlay.Store
	Audit  audit.Admin
	Doctor vm.Doctor
	LLM    detect.Service
	Schema schema.Printer
}

// DefaultServices wires the real implementations (placeholders that exit 70 until their task lands).
func DefaultServices() Services {
	return Services{
		Runner: session.NewRunner(),
		Killer: killswitch.NewClient(sessionsDir),
		Images: vm.NewImageManager(),
		VMs:    overlay.NewStore(),
		Audit:  audit.NewAdmin(auditDir),
		Doctor: vm.NewDoctor(),
		LLM:    detect.NewService(),
		Schema: actions.NewPrinter(),
	}
}

// auditDir locates the audit directory when an audit command runs.
func auditDir() (string, error) {
	d, err := platform.DefaultDirs()
	if err != nil {
		return "", err
	}
	return d.AuditDir(), nil
}

// sessionsDir locates the directory of the active-session lock when `kill` runs.
func sessionsDir() (string, error) {
	d, err := platform.DefaultDirs()
	if err != nil {
		return "", err
	}
	return d.SessionsDir(), nil
}

// Env is everything the command tree takes from the outside world, so tests can run it hermetically.
type Env struct {
	Stdout, Stderr io.Writer
	Services       Services
	// Getenv, UserConfigDir and HostOS default to the os and runtime values.
	Getenv        func(string) string
	UserConfigDir func() (string, error)
	HostOS        string
}

func (e *Env) fillDefaults() {
	if e.Stdout == nil {
		e.Stdout = os.Stdout
	}
	if e.Stderr == nil {
		e.Stderr = os.Stderr
	}
	if e.Getenv == nil {
		e.Getenv = os.Getenv
	}
	if e.UserConfigDir == nil {
		e.UserConfigDir = os.UserConfigDir
	}
	if e.HostOS == "" {
		e.HostOS = runtime.GOOS
	}
	// Fill only what the caller left out, so tests can fake one service and keep the placeholders for the rest.
	d := DefaultServices()
	if e.Services.Runner == nil {
		e.Services.Runner = d.Runner
	}
	if e.Services.Killer == nil {
		e.Services.Killer = d.Killer
	}
	if e.Services.Images == nil {
		e.Services.Images = d.Images
	}
	if e.Services.VMs == nil {
		e.Services.VMs = d.VMs
	}
	if e.Services.Audit == nil {
		e.Services.Audit = d.Audit
	}
	if e.Services.Doctor == nil {
		e.Services.Doctor = d.Doctor
	}
	if e.Services.LLM == nil {
		e.Services.LLM = d.LLM
	}
	if e.Services.Schema == nil {
		e.Services.Schema = d.Schema
	}
}
