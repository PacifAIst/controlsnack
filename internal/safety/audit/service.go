// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"context"
	"io"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
)

// Admin is what the `controlsnack audit` commands use; all output goes to w.
type Admin interface {
	// Verify checks the hash chain of one log file.
	Verify(ctx context.Context, w io.Writer, file string) error
	// List prints the known audit logs.
	List(ctx context.Context, w io.Writer) error
	// Show prints one log; ref is a file path or a session id.
	Show(ctx context.Context, w io.Writer, ref string) error
	// Prune deletes logs older than the given age.
	Prune(ctx context.Context, w io.Writer, olderThan time.Duration) error
}

// NewAdmin returns the placeholder until P2-T07 implements the log.
func NewAdmin() Admin { return pendingAdmin{} }

type pendingAdmin struct{}

func (pendingAdmin) Verify(context.Context, io.Writer, string) error {
	return exitcode.Pending("P2-T07")
}
func (pendingAdmin) List(context.Context, io.Writer) error         { return exitcode.Pending("P2-T07") }
func (pendingAdmin) Show(context.Context, io.Writer, string) error { return exitcode.Pending("P2-T07") }
func (pendingAdmin) Prune(context.Context, io.Writer, time.Duration) error {
	return exitcode.Pending("P2-T07")
}
