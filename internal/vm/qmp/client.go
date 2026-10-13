// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qmp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// maxMessage bounds one QMP message. Replies are small; a line this long means something other than QEMU is
// talking, and the connection is dropped.
const maxMessage = 4 << 20

// defaultTimeout is how long a write to QEMU, or the greeting, may take when the caller's context sets no deadline.
const defaultTimeout = 10 * time.Second

var (
	// ErrClosed is returned for use of a client that was closed.
	ErrClosed = errors.New("QMP connection closed")
	// ErrCommandNotAllowed is returned for a command outside the allowlist.
	ErrCommandNotAllowed = errors.New("QMP command not allowed")
)

// allowedCommands is the closed set of QMP commands this program sends. QMP can do far more than the controller
// needs, among it running arbitrary monitor commands, adding backends and devices, and starting migrations; none of
// that is reachable through this client, even by mistake.
var allowedCommands = map[string]bool{
	"qmp_capabilities": true,
	"query-status":     true,
	"query-version":    true,
	"query-chardev":    true,
	"quit":             true,
	"system_powerdown": true,
	"stop":             true,
	"cont":             true,
	"screendump":       true,
	"input-send-event": true,
}

// Error is an error reply from QEMU.
type Error struct {
	Class string `json:"class"`
	Desc  string `json:"desc"`
}

func (e *Error) Error() string { return "QMP " + e.Class + ": " + e.Desc }

// Greeting is what QEMU announces on connect.
type Greeting struct {
	QMP struct {
		Version struct {
			QEMU struct {
				Major int `json:"major"`
				Minor int `json:"minor"`
				Micro int `json:"micro"`
			} `json:"qemu"`
			Package string `json:"package"`
		} `json:"version"`
		Capabilities []string `json:"capabilities"`
	} `json:"QMP"`
}

type message struct {
	QMP       json.RawMessage `json:"QMP"`
	Return    json.RawMessage `json:"return"`
	Error     *Error          `json:"error"`
	Event     string          `json:"event"`
	Data      json.RawMessage `json:"data"`
	Timestamp *struct {
		Seconds      int64 `json:"seconds"`
		Microseconds int64 `json:"microseconds"`
	} `json:"timestamp"`
	ID json.RawMessage `json:"id"`
}

type response struct {
	ret json.RawMessage
	err *Error
}

// Client is a connection to QEMU's QMP control channel. It is safe for concurrent use.
type Client struct {
	conn     net.Conn
	r        *bufio.Reader
	greeting Greeting

	wmu    sync.Mutex
	nextID atomic.Uint64

	mu      sync.Mutex
	pending map[uint64]chan response
	events  eventLog

	done    chan struct{}
	failure error
	once    sync.Once
}

// NewClient takes over an established connection: it reads the greeting, negotiates capabilities and starts
// reading. ctx bounds the negotiation only. On error the connection is closed.
func NewClient(ctx context.Context, conn net.Conn) (*Client, error) {
	c := &Client{conn: conn, r: bufio.NewReaderSize(conn, 64<<10), pending: map[uint64]chan response{}, done: make(chan struct{})}
	c.events.init()

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultTimeout)
	}
	_ = conn.SetReadDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Unix(1, 0)) })
	line, err := readMessage(c.r)
	stop()
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("reading the QMP greeting: %w", err)
	}
	if err := json.Unmarshal(line, &c.greeting); err != nil || !bytes.Contains(line, []byte(`"QMP"`)) {
		_ = conn.Close()
		return nil, fmt.Errorf("the peer did not greet like QEMU: %.80q", line)
	}
	_ = conn.SetReadDeadline(time.Time{})

	go c.readLoop()
	if err := c.Execute(ctx, "qmp_capabilities", nil, nil); err != nil {
		c.fail(err)
		return nil, fmt.Errorf("negotiating QMP capabilities: %w", err)
	}
	return c, nil
}

// Greeting returns what QEMU announced, including its version.
func (c *Client) Greeting() Greeting { return c.greeting }

// Done is closed when the connection has ended, for whatever reason.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err returns why the connection ended; nil while it is up.
func (c *Client) Err() error {
	select {
	case <-c.done:
		return c.failure
	default:
		return nil
	}
}

// Close ends the connection. Calls in flight return ErrClosed.
func (c *Client) Close() error {
	c.fail(ErrClosed)
	return nil
}

// fail records why the connection ended (the first reason wins), closes it and wakes everyone waiting.
func (c *Client) fail(err error) {
	c.once.Do(func() {
		c.failure = err
		close(c.done)
		_ = c.conn.Close()
		c.events.wake()
	})
}

// Execute sends a command and decodes the reply into out (when not nil). A QMP error reply is an *Error. Only
// commands on the allowlist are sent.
func (c *Client) Execute(ctx context.Context, cmd string, args, out any) error {
	if !allowedCommands[cmd] {
		return fmt.Errorf("%w: %q", ErrCommandNotAllowed, cmd)
	}
	id := c.nextID.Add(1)
	req := map[string]any{"execute": cmd, "id": id}
	if args != nil {
		req["arguments"] = args
	}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", cmd, err)
	}

	ch := make(chan response, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return c.failure
	default:
	}
	c.pending[id] = ch
	c.mu.Unlock()
	forget := func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }

	if err := c.write(ctx, append(data, '\n')); err != nil {
		forget()
		return err
	}
	finish := func(r response) error {
		if r.err != nil {
			return r.err
		}
		if out != nil && len(r.ret) > 0 {
			if err := json.Unmarshal(r.ret, out); err != nil {
				return fmt.Errorf("decoding the reply to %s: %w", cmd, err)
			}
		}
		return nil
	}
	select {
	case r := <-ch:
		return finish(r)
	case <-ctx.Done():
		forget()
		return ctx.Err()
	case <-c.done:
		// QEMU often answers and exits at once (quit, a failed capabilities negotiation): when both the reply and the
		// loss of the connection are ready, select picks at random, and a reply that arrived must win.
		select {
		case r := <-ch:
			return finish(r)
		default:
		}
		forget()
		return c.failure
	}
}

func (c *Client) write(ctx context.Context, b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultTimeout)
	}
	_ = c.conn.SetWriteDeadline(deadline)
	if _, err := c.conn.Write(b); err != nil {
		c.fail(fmt.Errorf("writing to QEMU: %w", err))
		return c.failure
	}
	return nil
}

func (c *Client) readLoop() {
	for {
		line, err := readMessage(c.r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.EOF // QEMU closed the channel: it is exiting
			}
			c.fail(fmt.Errorf("QMP connection lost: %w", err))
			return
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil {
			c.fail(fmt.Errorf("QMP sent something that is not JSON: %.80q", line))
			return
		}
		switch {
		case m.Event != "":
			ev := Event{Name: m.Event, Data: m.Data}
			if m.Timestamp != nil {
				ev.Time = time.Unix(m.Timestamp.Seconds, m.Timestamp.Microseconds*1000)
			}
			c.events.add(ev)
		case m.ID != nil:
			var id uint64
			if json.Unmarshal(m.ID, &id) != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- response{ret: m.Return, err: m.Error}
			}
		}
	}
}

// readMessage reads one newline-terminated message of at most maxMessage bytes.
func readMessage(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxMessage {
			return nil, fmt.Errorf("a QMP message longer than %d bytes", maxMessage)
		}
		switch {
		case err == nil:
			return bytes.TrimSpace(line), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) > 0:
			return nil, io.ErrUnexpectedEOF
		default:
			return nil, err
		}
	}
}
