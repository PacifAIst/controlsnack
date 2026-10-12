// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package overlay

import (
	"context"
	"io"

	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
)

// Store is what the `controlsnack vm` commands use to manage kept VMs (D-26); all output goes to w.
type Store interface {
	List(ctx context.Context, w io.Writer) error
	Remove(ctx context.Context, w io.Writer, name string) error
}

// NewStore returns the placeholder until P3-T09 implements kept VMs.
func NewStore() Store { return pendingStore{} }

type pendingStore struct{}

func (pendingStore) List(context.Context, io.Writer) error { return exitcode.Pending("P3-T09") }
func (pendingStore) Remove(context.Context, io.Writer, string) error {
	return exitcode.Pending("P3-T09")
}
