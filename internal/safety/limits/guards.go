// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package limits

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"sync/atomic"
)

// DefaultBreakerThreshold is the number of consecutive invalid or failed actions that pauses the session
// (PLAN.md section 6.6).
const DefaultBreakerThreshold = 5

// DefaultLoopThreshold is how many identical actions in a row, with the screen not changing, count as a loop.
const DefaultLoopThreshold = 3

// Breaker pauses the session after too many consecutive failures: a model that keeps producing invalid or
// failing actions is not making progress and should not keep burning the user's budget.
type Breaker struct {
	mu          sync.Mutex
	threshold   int
	consecutive int
	tripped     bool
}

// NewBreaker creates a breaker; a threshold below 1 selects the default of 5.
func NewBreaker(threshold int) *Breaker {
	if threshold < 1 {
		threshold = DefaultBreakerThreshold
	}
	return &Breaker{threshold: threshold}
}

// Record notes the outcome of an action and reports whether the breaker has tripped. Success clears the streak.
// Once tripped it stays tripped until Reset.
func (b *Breaker) Record(ok bool) (tripped bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ok {
		b.consecutive = 0
		return b.tripped
	}
	b.consecutive++
	if b.consecutive >= b.threshold {
		b.tripped = true
	}
	return b.tripped
}

// Tripped reports whether the breaker has tripped.
func (b *Breaker) Tripped() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tripped
}

// Reset clears the streak and the tripped state; the user does this by resuming.
func (b *Breaker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consecutive, b.tripped = 0, false
}

// LoopDetector notices a model repeating itself: the same tool with the same arguments several times in a row
// while the screen stays as it was.
type LoopDetector struct {
	mu        sync.Mutex
	threshold int
	lastKey   string
	lastHash  string
	count     int
}

// NewLoopDetector creates a detector; a threshold below 2 selects the default of 3.
func NewLoopDetector(threshold int) *LoopDetector {
	if threshold < 2 {
		threshold = DefaultLoopThreshold
	}
	return &LoopDetector{threshold: threshold}
}

// Observe records an executed action (key from ActionKey) and the hash of the screen after it, and reports
// whether the model is now looping. A different action, or a changed screen, starts the count over.
func (d *LoopDetector) Observe(key, screenHash string) (looping bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.count > 0 && key == d.lastKey && screenHash == d.lastHash {
		d.count++
	} else {
		d.lastKey, d.lastHash, d.count = key, screenHash, 1
	}
	return d.count >= d.threshold
}

// Reset forgets the history; the user does this by resuming.
func (d *LoopDetector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastKey, d.lastHash, d.count = "", "", 0
}

// ActionKey identifies an action by its tool and arguments for the loop detector. Arguments are hashed through
// their JSON form, in which map keys are sorted, so the order the model wrote them in does not matter.
func ActionKey(tool string, args any) string {
	data, err := json.Marshal(args)
	if err != nil {
		data = []byte("unmarshalable")
	}
	sum := sha256.Sum256(append([]byte(tool+"\x00"), data...))
	return hex.EncodeToString(sum[:])
}

// Gate allows one LLM request in flight at a time (PLAN.md section 6.6).
type Gate struct{ busy atomic.Bool }

// TryAcquire takes the gate if it is free. The returned function releases it and may be called more than once.
func (g *Gate) TryAcquire() (release func(), ok bool) {
	if !g.busy.CompareAndSwap(false, true) {
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(func() { g.busy.Store(false) }) }, true
}
