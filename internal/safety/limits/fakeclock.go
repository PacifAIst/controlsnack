// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package limits

import (
	"context"
	"sync"
	"time"
)

// FakeClock is a manually advanced Clock for tests, here and in the packages that drive Limits. Sleep does not
// wait: it moves the clock forward by the requested duration, which is what a real wait would have done.
type FakeClock struct {
	mu    sync.Mutex
	now   time.Time
	slept time.Duration
}

// NewFakeClock starts at a fixed, arbitrary time.
func NewFakeClock() *FakeClock {
	return &FakeClock{now: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
}

// Now returns the fake time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the fake time forward.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Slept returns the total time spent in Sleep.
func (c *FakeClock) Slept() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.slept
}

// Sleep advances the clock by d unless ctx is already done.
func (c *FakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d > 0 {
		c.mu.Lock()
		c.now = c.now.Add(d)
		c.slept += d
		c.mu.Unlock()
	}
	return nil
}
