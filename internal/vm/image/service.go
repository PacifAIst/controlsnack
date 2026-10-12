// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"context"
	"io"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
)

// BuildOptions are the flags of `controlsnack image build`.
type BuildOptions struct {
	Yes   bool // do not ask before downloading
	Force bool // rebuild even if an image with the same recipe exists
}

// Manager is what the `controlsnack image` commands use; all output goes to w.
type Manager interface {
	Build(ctx context.Context, w io.Writer, cfg config.Config, opts BuildOptions) error
	List(ctx context.Context, w io.Writer) error
	Remove(ctx context.Context, w io.Writer, id string) error
	Verify(ctx context.Context, w io.Writer, id string) error
	Import(ctx context.Context, w io.Writer, qcow2, manifest string) error
	// Path prints the path of an image; an empty id means the newest one.
	Path(ctx context.Context, w io.Writer, id string) error
}

// NewManager returns the placeholder until P3-T06 and P3-T08 implement the image pipeline.
func NewManager() Manager { return pendingManager{} }

type pendingManager struct{}

func (pendingManager) Build(context.Context, io.Writer, config.Config, BuildOptions) error {
	return exitcode.Pending("P3-T08")
}
func (pendingManager) List(context.Context, io.Writer) error { return exitcode.Pending("P3-T06") }
func (pendingManager) Remove(context.Context, io.Writer, string) error {
	return exitcode.Pending("P3-T06")
}
func (pendingManager) Verify(context.Context, io.Writer, string) error {
	return exitcode.Pending("P3-T06")
}
func (pendingManager) Import(context.Context, io.Writer, string, string) error {
	return exitcode.Pending("P3-T06")
}
func (pendingManager) Path(context.Context, io.Writer, string) error {
	return exitcode.Pending("P3-T06")
}
