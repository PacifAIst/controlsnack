// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package a11y

import (
	"context"
	"sync"
)

// LazyBus connects to the accessibility bus when it is first needed, and again after the connection has broken. The
// agent starts with the desktop session, and the accessibility bus may come up a moment later (or be restarted with the
// session); the agent must neither fail to start nor keep a dead connection.
type LazyBus struct {
	dial func(context.Context) (Bus, error)

	mu  sync.Mutex
	bus Bus
}

// NewLazyBus returns a bus that connects with dial on first use.
func NewLazyBus(dial func(context.Context) (Bus, error)) *LazyBus { return &LazyBus{dial: dial} }

// current returns the connection, making it if there is none.
func (l *LazyBus) current(ctx context.Context) (Bus, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.bus != nil {
		return l.bus, nil
	}
	b, err := l.dial(ctx)
	if err != nil {
		return nil, err
	}
	l.bus = b
	return b, nil
}

// reset drops a connection that failed, if it is still the current one.
func (l *LazyBus) reset(bad Bus) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.bus == bad && bad != nil {
		_ = bad.Close()
		l.bus = nil
	}
}

// Apps lists the applications. It is the first question of every tree request, so it is where a broken connection
// shows: the connection is dropped and made again once.
func (l *LazyBus) Apps(ctx context.Context) ([]App, error) {
	b, err := l.current(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := b.Apps(ctx)
	if err == nil || ctx.Err() != nil {
		return apps, err
	}
	l.reset(b)
	if b, err = l.current(ctx); err != nil {
		return nil, err
	}
	return b.Apps(ctx)
}

// with runs f on the current connection.
func with[T any](ctx context.Context, l *LazyBus, f func(Bus) (T, error)) (T, error) {
	b, err := l.current(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	return f(b)
}

// Info implements Bus.
func (l *LazyBus) Info(ctx context.Context, r Ref) (Info, error) {
	return with(ctx, l, func(b Bus) (Info, error) { return b.Info(ctx, r) })
}

// Children implements Bus.
func (l *LazyBus) Children(ctx context.Context, r Ref) ([]Ref, error) {
	return with(ctx, l, func(b Bus) ([]Ref, error) { return b.Children(ctx, r) })
}

// Extents implements Bus.
func (l *LazyBus) Extents(ctx context.Context, r Ref) (x, y, w, h int, err error) {
	b, err := l.current(ctx)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return b.Extents(ctx, r)
}

// Actions implements Bus.
func (l *LazyBus) Actions(ctx context.Context, r Ref) ([]string, error) {
	return with(ctx, l, func(b Bus) ([]string, error) { return b.Actions(ctx, r) })
}

// Text implements Bus.
func (l *LazyBus) Text(ctx context.Context, r Ref, max int) (string, error) {
	return with(ctx, l, func(b Bus) (string, error) { return b.Text(ctx, r, max) })
}

// Number implements Bus.
func (l *LazyBus) Number(ctx context.Context, r Ref) (float64, error) {
	return with(ctx, l, func(b Bus) (float64, error) { return b.Number(ctx, r) })
}

// DoAction implements Bus.
func (l *LazyBus) DoAction(ctx context.Context, r Ref, index int) (bool, error) {
	return with(ctx, l, func(b Bus) (bool, error) { return b.DoAction(ctx, r, index) })
}

// GrabFocus implements Bus.
func (l *LazyBus) GrabFocus(ctx context.Context, r Ref) (bool, error) {
	return with(ctx, l, func(b Bus) (bool, error) { return b.GrabFocus(ctx, r) })
}

// SetText implements Bus.
func (l *LazyBus) SetText(ctx context.Context, r Ref, text string) (bool, error) {
	return with(ctx, l, func(b Bus) (bool, error) { return b.SetText(ctx, r, text) })
}

// Close closes the connection, if there is one.
func (l *LazyBus) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.bus == nil {
		return nil
	}
	err := l.bus.Close()
	l.bus = nil
	return err
}
