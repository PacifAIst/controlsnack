// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qemu

import (
	"strings"
	"sync"
)

// DefaultRingSize is how much of QEMU's output is kept for error messages (PLAN.md section 7.4).
const DefaultRingSize = 64 << 10

// Ring keeps the last Size bytes written to it. Writes never block and never fail, so a talkative child cannot
// stall on a full pipe, and memory use is fixed.
type Ring struct {
	mu    sync.Mutex
	buf   []byte
	start int // index of the oldest byte
	n     int // bytes held
}

// NewRing creates a ring that keeps size bytes.
func NewRing(size int) *Ring {
	if size <= 0 {
		size = DefaultRingSize
	}
	return &Ring{buf: make([]byte, size)}
}

// Write appends p, dropping the oldest bytes when the ring is full.
func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := len(p)
	if len(p) >= len(r.buf) { // only the last Size bytes can survive
		p = p[len(p)-len(r.buf):]
		r.start, r.n = 0, len(p)
		copy(r.buf, p)
		return total, nil
	}
	end := (r.start + r.n) % len(r.buf)
	first := copy(r.buf[end:], p)
	copy(r.buf, p[first:])
	r.n += len(p)
	if r.n > len(r.buf) {
		r.start = (r.start + r.n - len(r.buf)) % len(r.buf)
		r.n = len(r.buf)
	}
	return total, nil
}

// Bytes returns what the ring holds, oldest first.
func (r *Ring) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]byte, r.n)
	first := copy(out, r.buf[r.start:min(r.start+r.n, len(r.buf))])
	copy(out[first:], r.buf[:r.n-first])
	return out
}

// String returns the contents as text. Bytes cut off in the middle of a character at the start are dropped.
func (r *Ring) String() string {
	return strings.ToValidUTF8(string(r.Bytes()), "")
}

// Tail returns the last n bytes of the contents (all of it when shorter), starting on a line boundary when it can.
func (r *Ring) Tail(n int) string {
	s := r.String()
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 && i+1 < len(s) {
		s = s[i+1:]
	}
	return strings.ToValidUTF8(s, "")
}
