// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package killswitch

import (
	"context"

	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
)

// Client is what `controlsnack kill` uses to stop a session running in another process.
type Client interface {
	// Kill asks the active session to die and reports once QEMU is gone.
	Kill(ctx context.Context) error
}

// NewClient returns the placeholder client until P2-T08 implements the lock file and the kill call.
func NewClient() Client { return pendingClient{} }

type pendingClient struct{}

func (pendingClient) Kill(context.Context) error { return exitcode.Pending("P2-T08") }
