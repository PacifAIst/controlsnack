// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package a11y

import (
	"context"
	"errors"
)

// Errors a Bus reports.
var (
	// ErrUnsupported means the element does not implement what was asked (it has no Component, no Action...). It is
	// an ordinary answer, not a failure: most elements lack most interfaces.
	ErrUnsupported = errors.New("the element does not support this")
	// ErrGone means the element no longer exists: its application closed it, or it was never there.
	ErrGone = errors.New("the element no longer exists")
	// ErrNoBus means there is no accessibility bus to talk to.
	ErrNoBus = errors.New("no accessibility bus")
)

// Ref names an element on the accessibility bus: the application's connection and the object path (AT-SPI's own
// reference, "(so)").
type Ref struct {
	Bus  string
	Path string
}

func (r Ref) String() string { return r.Bus + r.Path }

// App is an application registered with the accessibility registry.
type App struct {
	Ref  // its root object
	Name string
	PID  int
}

// Info is what an element says about itself.
type Info struct {
	Role        uint32
	State       StateSet
	Name        string
	Description string
	ChildCount  int
}

// Bus is the accessibility bus as the walker and the actions use it. The real one speaks D-Bus to AT-SPI; tests
// provide a tree in memory.
type Bus interface {
	// Apps lists the registered applications.
	Apps(ctx context.Context) ([]App, error)
	// Info reads an element's role, states, name and description.
	Info(ctx context.Context, r Ref) (Info, error)
	// Children lists an element's children in order.
	Children(ctx context.Context, r Ref) ([]Ref, error)
	// Extents is the element's rectangle in screen pixels (ErrUnsupported without a Component).
	Extents(ctx context.Context, r Ref) (x, y, w, h int, err error)
	// Actions names the actions the element offers, in AT-SPI's order; empty when it has none.
	Actions(ctx context.Context, r Ref) ([]string, error)
	// Text reads up to max characters of the element's text (ErrUnsupported without a Text interface).
	Text(ctx context.Context, r Ref, max int) (string, error)
	// Number reads the element's numeric value (ErrUnsupported without a Value interface).
	Number(ctx context.Context, r Ref) (float64, error)
	// DoAction performs action number index and reports whether the element did it.
	DoAction(ctx context.Context, r Ref, index int) (bool, error)
	// GrabFocus gives the element the keyboard focus and reports whether it took it.
	GrabFocus(ctx context.Context, r Ref) (bool, error)
	// SetText replaces the text of an editable element and reports whether it did (ErrUnsupported without
	// EditableText).
	SetText(ctx context.Context, r Ref, text string) (bool, error)
	// Close ends the connection.
	Close() error
}
