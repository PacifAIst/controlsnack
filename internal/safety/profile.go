// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package safety

import (
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/config"
)

// Profile is the resolved safety posture of one session: the budgets in force and how rule classes are treated.
// It is built once from the validated configuration and then only read.
//
// The only difference between the safe and the unsafe profile is the list in PLAN.md section 6.2. The invariants
// of section 6.1 are not part of a profile; they hold regardless.
type Profile struct {
	Unsafe bool
	// Step makes every action need confirmation (--step).
	Step bool

	ActionsPerSec      float64
	ActionsPerResponse int
	ActionsPerTask     int
	MaxTurns           int
	SessionTimeout     time.Duration
	IdleTimeout        time.Duration
	ConfirmTimeout     time.Duration

	// AllowHeadless permits model runs with no viewer attached (--viewer none).
	AllowHeadless bool
	// AllowPlainHTTPRemoteLLM permits an http:// model endpoint on a non-local host.
	AllowPlainHTTPRemoteLLM bool
}

// ProfileFor resolves the profile for a configuration. Budgets are clamped to the bounds of the active mode as
// a second line of defense: configuration validation already rejects out-of-range values, but a Config built in
// code (a test, an embedder) must not be able to exceed them either.
func ProfileFor(c config.Config) Profile {
	b := config.BoundsFor(c.Safety.Unsafe)
	def := config.Defaults().Safety
	return Profile{
		Unsafe: c.Safety.Unsafe,
		Step:   c.Safety.Step,

		ActionsPerSec:      clampFloat(c.Safety.ActionsPerSec, def.ActionsPerSec, b.ActionsPerSec),
		ActionsPerResponse: clampInt(c.Safety.ActionsPerResponse, def.ActionsPerResponse, b.ActionsPerResponse),
		ActionsPerTask:     clampInt(c.Safety.ActionsPerTask, def.ActionsPerTask, b.ActionsPerTask),
		MaxTurns:           clampInt(c.Safety.MaxTurns, def.MaxTurns, b.MaxTurns),
		SessionTimeout:     clampDuration(c.Safety.Timeout.D(), def.Timeout.D(), b.SessionTimeout),
		IdleTimeout:        clampDuration(c.Safety.IdleTimeout.D(), def.IdleTimeout.D(), b.SessionTimeout),
		ConfirmTimeout:     clampDuration(c.Safety.ConfirmTimeout.D(), def.ConfirmTimeout.D(), 10*time.Minute),

		AllowHeadless:           c.Safety.Unsafe,
		AllowPlainHTTPRemoteLLM: c.Safety.Unsafe,
	}
}

// Apply turns the verdict the rules asked for into the verdict that is enforced.
//
// Under --unsafe, confirm-class matches become allow (they are still audited) and deny-class matches become
// confirm. --step then makes anything that would run need a confirmation, even under --unsafe: asking for
// step-by-step control always wins.
func (p Profile) Apply(v Verdict) Verdict {
	if p.Unsafe {
		switch v {
		case Confirm:
			v = Allow
		case Deny:
			v = Confirm
		}
	}
	if p.Step && v == Allow {
		v = Confirm
	}
	return v
}

// The clamp helpers pull a value into (0, hi]. A value that is not positive is nonsense (a zero rate would stall
// the session, a zero timeout would end it instantly), so it falls back to the safe default def, never to the
// loosest setting.
func clampInt(v, def, hi int) int {
	if v < 1 {
		v = def
	}
	return min(v, hi)
}

func clampFloat(v, def, hi float64) float64 {
	if v <= 0 {
		v = def
	}
	return min(v, hi)
}

func clampDuration(v, def, hi time.Duration) time.Duration {
	if v <= 0 {
		v = def
	}
	return min(v, hi)
}
