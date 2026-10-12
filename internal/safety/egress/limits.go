// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package egress

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// The bridge limits of PLAN.md section 6.11.
const (
	DefaultMaxStreams  = 32
	DefaultIdleTimeout = 5 * time.Minute
	DefaultByteBudget  = int64(2) << 30 // 2 GiB per session
)

// ErrTooManyStreams and ErrByteBudget are returned when a limit is reached.
var (
	ErrTooManyStreams = errors.New("too many concurrent connections through the network bridge")
	ErrByteBudget     = errors.New("the session's network byte budget is used up")
)

// Limits bounds what the bridge may carry in one session.
type Limits struct {
	MaxStreams  int
	IdleTimeout time.Duration
	ByteBudget  int64
}

// DefaultLimits returns the limits of the plan.
func DefaultLimits() Limits {
	return Limits{MaxStreams: DefaultMaxStreams, IdleTimeout: DefaultIdleTimeout, ByteBudget: DefaultByteBudget}
}

// Tracker enforces Limits. The bridge opens a stream with Open, reports traffic with AddBytes and closes the
// stream with the release function. It is safe for concurrent use.
type Tracker struct {
	limits Limits
	mu     sync.Mutex
	active int
	bytes  atomic.Int64
}

// NewTracker creates a tracker; zero fields in l select the defaults.
func NewTracker(l Limits) *Tracker {
	d := DefaultLimits()
	if l.MaxStreams <= 0 {
		l.MaxStreams = d.MaxStreams
	}
	if l.IdleTimeout <= 0 {
		l.IdleTimeout = d.IdleTimeout
	}
	if l.ByteBudget <= 0 {
		l.ByteBudget = d.ByteBudget
	}
	return &Tracker{limits: l}
}

// IdleTimeout is how long a stream may carry no data before the bridge closes it.
func (t *Tracker) IdleTimeout() time.Duration { return t.limits.IdleTimeout }

// Open reserves a stream slot. The release function frees it and may be called more than once.
func (t *Tracker) Open() (release func(), err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.bytes.Load() >= t.limits.ByteBudget {
		return nil, ErrByteBudget
	}
	if t.active >= t.limits.MaxStreams {
		return nil, fmt.Errorf("%w (limit %d)", ErrTooManyStreams, t.limits.MaxStreams)
	}
	t.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			t.active--
			t.mu.Unlock()
		})
	}, nil
}

// AddBytes charges n bytes of traffic (both directions) to the session. It returns ErrByteBudget once the budget
// is exceeded, and the bridge then closes its streams.
func (t *Tracker) AddBytes(n int64) error {
	if t.bytes.Add(n) > t.limits.ByteBudget {
		return ErrByteBudget
	}
	return nil
}

// Stats returns the open streams and the bytes carried so far.
func (t *Tracker) Stats() (streams int, bytes int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active, t.bytes.Load()
}
