// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qmp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// historySize is how many events a client remembers. A waiter that starts late still sees the recent past, which
// matters for events that fire once, such as the guest opening the agent port.
const historySize = 256

// Event is an asynchronous notification from QEMU.
type Event struct {
	Name string
	Data json.RawMessage
	Time time.Time
}

// Event names the controller cares about.
const (
	EventVserportChange = "VSERPORT_CHANGE"
	EventShutdown       = "SHUTDOWN"
	EventStop           = "STOP"
	EventResume         = "RESUME"
	EventReset          = "RESET"
	EventPowerdown      = "POWERDOWN"
)

// VserportChange is the data of a VSERPORT_CHANGE event: the guest opened or closed a virtio-serial port.
type VserportChange struct {
	ID   string `json:"id"`
	Open bool   `json:"open"`
}

// Shutdown is the data of a SHUTDOWN event.
type Shutdown struct {
	Guest  bool   `json:"guest"`
	Reason string `json:"reason"`
}

// Decode unmarshals the event's data.
func (e Event) Decode(v any) error {
	if len(e.Data) == 0 {
		return nil
	}
	return json.Unmarshal(e.Data, v)
}

// eventLog is the bounded history of events plus the broadcast that wakes waiters.
type eventLog struct {
	mu      sync.Mutex
	history []Event
	first   int // sequence number of history[0]
	ports   map[string]bool
	changed chan struct{} // closed and replaced whenever something is added or the connection ends
}

func (l *eventLog) init() { l.changed = make(chan struct{}); l.ports = map[string]bool{} }

func (l *eventLog) add(ev Event) {
	l.mu.Lock()
	l.history = append(l.history, ev)
	if len(l.history) > historySize {
		drop := len(l.history) - historySize
		l.history = append([]Event(nil), l.history[drop:]...)
		l.first += drop
	}
	if ev.Name == EventVserportChange {
		var v VserportChange
		if ev.Decode(&v) == nil && v.ID != "" {
			l.ports[v.ID] = v.Open
		}
	}
	old := l.changed
	l.changed = make(chan struct{})
	l.mu.Unlock()
	close(old)
}

// wake releases waiters without adding an event (the connection ended).
func (l *eventLog) wake() {
	l.mu.Lock()
	old := l.changed
	l.changed = make(chan struct{})
	l.mu.Unlock()
	close(old)
}

// Events returns the remembered events, oldest first.
func (c *Client) Events() []Event {
	c.events.mu.Lock()
	defer c.events.mu.Unlock()
	return append([]Event(nil), c.events.history...)
}

// WaitEvent returns the first remembered or future event for which match is true. Events that happened before the
// call are considered first, so an event cannot be lost between "connect" and "wait".
func (c *Client) WaitEvent(ctx context.Context, match func(Event) bool) (Event, error) {
	next := 0 // sequence number of the next event to look at
	for {
		c.events.mu.Lock()
		if next < c.events.first {
			next = c.events.first
		}
		for next < c.events.first+len(c.events.history) {
			ev := c.events.history[next-c.events.first]
			next++
			if match(ev) {
				c.events.mu.Unlock()
				return ev, nil
			}
		}
		changed := c.events.changed
		c.events.mu.Unlock()

		select {
		case <-changed:
		case <-ctx.Done():
			return Event{}, ctx.Err()
		case <-c.done:
			// The connection ended, but events that arrived with its last bytes are still looked at once more.
			c.events.mu.Lock()
			more := next < c.events.first+len(c.events.history)
			c.events.mu.Unlock()
			if !more {
				return Event{}, c.failure
			}
		}
	}
}

// WaitPortOpen waits until the guest has the virtio-serial port (the chardev id, e.g. "agent0") open. It returns at
// once if the latest VSERPORT_CHANGE for the port said open, and keeps waiting after a close.
func (c *Client) WaitPortOpen(ctx context.Context, id string) error {
	for {
		c.events.mu.Lock()
		open, changed := c.events.ports[id], c.events.changed
		c.events.mu.Unlock()
		if open {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return fmt.Errorf("waiting for the guest to open port %s: %w", id, ctx.Err())
		case <-c.done:
			return fmt.Errorf("waiting for the guest to open port %s: %w", id, c.failure)
		}
	}
}

// PortOpen reports the last known state of a virtio-serial port.
func (c *Client) PortOpen(id string) bool {
	c.events.mu.Lock()
	defer c.events.mu.Unlock()
	return c.events.ports[id]
}
