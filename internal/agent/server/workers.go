// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"sync"
	"time"

	"github.com/PacifAIst/controlsnack/internal/protocol"
)

// The worker pools of PLAN.md section 8. A request waits in the queue of its pool and is served by one of the pool's
// workers, in the order the requests arrived; `ping` and `cancel` never go through a pool.
const (
	PoolCapture = "capture" // screenshots, screen and window information
	PoolInput   = "input"   // keyboard and mouse: one at a time, in order
	PoolA11y    = "a11y"
	PoolOCR     = "ocr"
	PoolExec    = "exec"
	PoolBrowser = "browser"
	PoolWaitFor = "wait_for"
	PoolFile    = "file" // file_peek
	poolOther   = "other"
)

// DefaultWorkers is how many requests each pool serves at once.
var DefaultWorkers = map[string]int{
	PoolCapture: 1, PoolInput: 1, PoolA11y: 1, PoolOCR: 1, PoolExec: 2, PoolBrowser: 1, PoolWaitFor: 1, PoolFile: 2, poolOther: 1,
}

// PoolOf names the pool that serves a request type.
func PoolOf(requestType string) string {
	switch requestType {
	case protocol.TypeScreenshot, protocol.TypeScreenInfo, protocol.TypeWindows:
		return PoolCapture
	case protocol.TypeInput:
		return PoolInput
	case protocol.TypeA11yTree, protocol.TypeA11yAction:
		return PoolA11y
	case protocol.TypeOCR:
		return PoolOCR
	case protocol.TypeExec:
		return PoolExec
	case protocol.TypeBrowser:
		return PoolBrowser
	case protocol.TypeWaitFor:
		return PoolWaitFor
	case protocol.TypeFilePeek:
		return PoolFile
	}
	return poolOther
}

// CapabilityOf names the capability a request type belongs to in the handshake; "" for types that have none.
func CapabilityOf(requestType string) string {
	switch requestType {
	case protocol.TypePing:
		return protocol.CapPing
	case protocol.TypeScreenshot, protocol.TypeScreenInfo:
		return protocol.CapScreenshot
	case protocol.TypeWindows:
		return protocol.CapWindows
	case protocol.TypeInput:
		return protocol.CapInput
	case protocol.TypeA11yTree, protocol.TypeA11yAction:
		return protocol.CapA11y
	case protocol.TypeOCR:
		return protocol.CapOCR
	case protocol.TypeExec:
		return protocol.CapExec
	case protocol.TypeBrowser:
		return protocol.CapBrowser
	case protocol.TypeWaitFor:
		return protocol.CapWaitFor
	case protocol.TypeFilePeek:
		return protocol.CapFilePeek
	}
	return ""
}

// job is one request on its way through a pool.
type job struct {
	id      uint64
	queued  time.Time
	frame   protocol.Frame
	handler Handler
	pool    *pool
	ctx     context.Context
	cancel  context.CancelFunc
}

// pool is a first-in-first-out queue with a fixed number of workers. Workers are started when there is work and end
// when the queue is empty, so an idle agent holds no goroutines.
type pool struct {
	name  string
	limit int

	mu      sync.Mutex
	queue   []*job
	running int // workers alive
}

// push queues the job and reports whether a new worker must be started for it.
func (p *pool) push(j *job) (startWorker bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queue = append(p.queue, j)
	if p.running < p.limit {
		p.running++
		return true
	}
	return false
}

// next hands a worker the oldest waiting job, or tells it to stop. The worker count drops under the same lock that
// found the queue empty, so a job pushed a moment later starts a new worker instead of waiting for one that left.
func (p *pool) next() *job {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		p.running--
		return nil
	}
	j := p.queue[0]
	p.queue[0] = nil
	p.queue = p.queue[1:]
	return j
}

// remove takes a job out of the queue if it is still waiting there (it will then never run) and reports whether it
// did.
func (p *pool) remove(j *job) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, q := range p.queue {
		if q == j {
			p.queue = append(p.queue[:i], p.queue[i+1:]...)
			return true
		}
	}
	return false
}
