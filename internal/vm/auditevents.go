// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"github.com/PacifAIst/controlsnack-local/internal/safety/audit"
)

// Appender is the part of the audit log the launcher writes to; *audit.Log satisfies it.
type Appender interface {
	Append(r audit.Record) (audit.Record, error)
}

// AuditEvents is the Events implementation that writes the accelerator decisions to the audit log as lifecycle
// records (PLAN.md section 7.9, steps 1 and 5): accel_selected {requested, chosen, tried[]} and accel_fallback
// {requested, from, used, reason, stderr}. Other listeners (the terminal warning, the viewer notice) wrap it.
//
// A failed write is remembered and returned by Err: the audit log's own sticky error already stops the session
// (invariant 8), this only lets the caller see why.
type AuditEvents struct {
	Log     Appender
	Forward Events // optional: called after the record is written
	err     error
}

// AccelSelected records the choice of accelerator.
func (a *AuditEvents) AccelSelected(r AccelResult) {
	a.append(audit.Record{Kind: audit.KindLifecycle, Event: audit.EventAccelSelected, Actor: "system", Data: audit.ToRaw(r)})
	if a.Forward != nil {
		a.Forward.AccelSelected(r)
	}
}

// AccelFallback records that tcg was used because the hardware accelerator failed at launch.
func (a *AuditEvents) AccelFallback(f Fallback) {
	a.append(audit.Record{Kind: audit.KindLifecycle, Event: audit.EventAccelFallback, Actor: "system",
		Reason: f.Reason, Data: audit.ToRaw(f)})
	if a.Forward != nil {
		a.Forward.AccelFallback(f)
	}
}

// Err returns the first error from writing a record.
func (a *AuditEvents) Err() error { return a.err }

func (a *AuditEvents) append(r audit.Record) {
	if _, err := a.Log.Append(r); err != nil && a.err == nil {
		a.err = err
	}
}
