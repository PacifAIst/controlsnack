// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agentclient

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentbin"
)

// Errors the caller can test for with errors.Is.
var (
	// ErrAgentDown: there is no live connection to the agent (never connected, heartbeats missed, stream broken).
	ErrAgentDown = errors.New("the guest agent is not reachable")
	// ErrBadResponse: the agent answered in a way the protocol does not allow. The connection is dropped.
	ErrBadResponse = errors.New("the guest agent sent an invalid response")
	// ErrClosed: the client was closed.
	ErrClosed = errors.New("the agent client is closed")
)

// RemoteError is an error the agent reported for a request.
type RemoteError struct {
	Code    string // one of protocol.Code*
	Message string
}

func (e *RemoteError) Error() string { return e.Code + ": " + e.Message }

// ErrorCode is the protocol error code; callers that must not import this package ask for it with an interface.
func (e *RemoteError) ErrorCode() string { return e.Code }

// State is where the client is in the life of its connection.
type State int

// The states OnState reports.
const (
	StateDown       State = iota // no connection
	StateConnecting              // dialing, pushing the agent, shaking hands
	StateUp                      // the agent is answering
)

func (s State) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateUp:
		return "up"
	}
	return "down"
}

// Dialer opens a fresh connection to the agent's chardev. The client calls it again after every loss of the agent:
// closing the old connection is what tells a hung agent in the guest that the host is gone, and a new one starts
// from a clean stream.
type Dialer func(ctx context.Context) (net.Conn, error)

// Options configure a Client. Zero values take the defaults noted.
type Options struct {
	Dial        Dialer
	Agent       agentbin.Agent
	HostVersion string
	SessionID   string
	Features    protocol.Features
	// User is the account the agent must run as; Required are the capabilities it must announce.
	User     string
	Required []string

	Interval       time.Duration // heartbeat period and the time allowed for each answer (2 s)
	Misses         int           // unanswered heartbeats in a row before the agent counts as down (3)
	InstallTimeout time.Duration // pushing the agent and getting the stub's verdict (60 s)
	HelloTimeout   time.Duration // the handshake (10 s)
	WriteTimeout   time.Duration // a single write to the port (20 s); a stuck write drops the connection
	RetryMin       time.Duration // first wait between reconnection attempts (500 ms)
	RetryMax       time.Duration // longest wait (5 s)

	// OnEvent receives the agent's own events (id 0). OnState hears about every change of State; err is why the
	// client went down. Both run on the client's goroutines and must not block.
	OnEvent func(protocol.EventBody)
	OnState func(s State, err error)
	Log     *slog.Logger
	Rand    io.Reader // source of nonces (crypto/rand)
}

func (o *Options) fill() error {
	switch {
	case o.Dial == nil:
		return errors.New("agentclient: Options.Dial is required")
	case len(o.Agent.Binary) == 0 || o.Agent.SHA256 == "" || o.Agent.Arch == "":
		return errors.New("agentclient: Options.Agent is empty")
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&o.Interval, 2*time.Second)
	def(&o.InstallTimeout, 60*time.Second)
	def(&o.HelloTimeout, 10*time.Second)
	def(&o.WriteTimeout, 20*time.Second)
	def(&o.RetryMin, 500*time.Millisecond)
	def(&o.RetryMax, 5*time.Second)
	if o.Misses <= 0 {
		o.Misses = 3
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return nil
}

// Client is the host's end of the agent protocol. Methods are safe for concurrent use.
type Client struct {
	opt Options

	connMu sync.Mutex // one connection attempt at a time
	cur    atomic.Pointer[link]
	closed atomic.Bool
}

// New checks the options and returns a Client that is not yet connected.
func New(opt Options) (*Client, error) {
	if err := opt.fill(); err != nil {
		return nil, err
	}
	return &Client{opt: opt}, nil
}

func (c *Client) state(s State, err error) {
	if c.opt.OnState != nil {
		c.opt.OnState(s, err)
	}
}

// Connect makes one attempt: dial, push the agent, handshake. It does not retry; the caller decides how long to
// wait for a slow guest by choosing the context. Call it once the guest has opened the port (the QMP event
// VSERPORT_CHANGE), then run Supervise to keep the connection alive.
func (c *Client) Connect(ctx context.Context) error {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.closed.Load() {
		return ErrClosed
	}
	c.state(StateConnecting, nil)
	l, err := c.establish(ctx)
	if err != nil {
		c.state(StateDown, err)
		return err
	}
	if c.closed.Load() { // closed while we were shaking hands
		l.fail(ErrClosed)
		return ErrClosed
	}
	c.cur.Store(l)
	go l.readLoop(c.opt.OnEvent, c.opt.Log)
	go l.heartbeat(c.opt.Interval, c.opt.Misses)
	c.state(StateUp, nil)
	return nil
}

func (c *Client) establish(ctx context.Context) (*link, error) {
	nonce, err := newNonce(c.opt.Rand)
	if err != nil {
		return nil, err
	}
	conn, err := c.opt.Dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("connecting to the agent port: %w", err)
	}
	br := bufio.NewReaderSize(conn, 64<<10)
	if err := install(ctx, conn, br, c.opt.Agent, nonce, c.opt.InstallTimeout); err != nil {
		_ = conn.Close()
		return nil, err
	}
	pc := protocol.NewConn(br, timedWriter{conn, c.opt.WriteTimeout}, conn)
	hello := protocol.Hello{Proto: protocol.ProtoVersion, HostVersion: c.opt.HostVersion, SessionID: c.opt.SessionID, Nonce: nonce, Features: c.opt.Features}
	expect := protocol.Expect{Nonce: nonce, AgentSHA256: c.opt.Agent.SHA256, Arch: c.opt.Agent.Arch, User: c.opt.User, Required: c.opt.Required}
	ack, err := handshake(ctx, conn, pc, hello, expect, c.opt.HelloTimeout)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	// The connection outlives the context of the Connect call that made it; it ends with fail.
	lctx, lcancel := context.WithCancel(context.WithoutCancel(ctx))
	return &link{conn: conn, pc: pc, ack: ack, pending: map[uint64]chan protocol.Frame{}, done: make(chan struct{}), ctx: lctx, cancel: lcancel}, nil
}

// Supervise keeps the agent connected: whenever the connection ends it dials again, pushes the agent again and
// repeats the handshake, with a growing pause between failed attempts. It returns when ctx ends or the client is
// closed. Call Connect first.
func (c *Client) Supervise(ctx context.Context) {
	wait := c.opt.RetryMin
	for ctx.Err() == nil && !c.closed.Load() {
		if l := c.cur.Load(); l != nil {
			select {
			case <-ctx.Done():
				return
			case <-l.done:
			}
			if c.closed.Load() {
				return
			}
			c.opt.Log.Warn("lost the guest agent", "error", l.cause())
			c.state(StateDown, l.cause())
			c.cur.CompareAndSwap(l, nil)
		}
		for ctx.Err() == nil && !c.closed.Load() {
			err := c.Connect(ctx)
			if err == nil {
				wait = c.opt.RetryMin
				break
			}
			c.opt.Log.Warn("cannot reach the guest agent", "error", err, "retry_in", wait.String())
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = min(wait*2, c.opt.RetryMax)
		}
	}
}

// Ack returns what the current agent said about itself, and false when there is none.
func (c *Client) Ack() (protocol.HelloAck, bool) {
	l := c.cur.Load()
	if l == nil || l.isDone() {
		return protocol.HelloAck{}, false
	}
	return l.ack, true
}

// Up reports whether an agent is connected and answering.
func (c *Client) Up() bool {
	l := c.cur.Load()
	return l != nil && !l.isDone()
}

// Down returns a channel that is closed when the current connection ends. Before the first Connect it is already
// closed.
func (c *Client) Down() <-chan struct{} {
	if l := c.cur.Load(); l != nil {
		return l.done
	}
	ch := make(chan struct{})
	close(ch)
	return ch
}

// Close ends the connection for good.
func (c *Client) Close() error {
	c.closed.Store(true)
	if l := c.cur.Load(); l != nil {
		l.fail(ErrClosed)
	}
	return nil
}

// Call sends one request and returns the response's attachment. The response body is decoded strictly into res
// (nil to ignore it). A response of type error becomes a *RemoteError. When ctx ends first, the agent is told to
// cancel the request and ctx's error is returned.
func (c *Client) Call(ctx context.Context, typ string, req any, blob []byte, res any) ([]byte, error) {
	l := c.cur.Load()
	if l == nil {
		return nil, ErrAgentDown
	}
	return l.call(ctx, typ, req, blob, res, true)
}

// --- one connection ------------------------------------------------------------------------------------------------

type link struct {
	conn net.Conn
	pc   *protocol.Conn
	ack  protocol.HelloAck

	next atomic.Uint64 // request 1 was the hello

	mu      sync.Mutex
	pending map[uint64]chan protocol.Frame
	err     error
	done    chan struct{}
	ctx     context.Context // ends with the connection
	cancel  context.CancelFunc
	once    sync.Once
}

// timedWriter limits each write to the port, so an agent that stopped reading cannot block the host forever.
type timedWriter struct {
	c       net.Conn
	timeout time.Duration
}

func (w timedWriter) Write(p []byte) (int, error) {
	_ = w.c.SetWriteDeadline(time.Now().Add(w.timeout))
	return w.c.Write(p)
}

func (l *link) isDone() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

// fail ends the connection, once; the first reason wins.
func (l *link) fail(err error) {
	l.once.Do(func() {
		l.mu.Lock()
		l.err = err
		l.mu.Unlock()
		close(l.done)
		l.cancel()
		_ = l.conn.Close()
	})
}

func (l *link) cause() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// downError is what callers get once the connection is gone.
func (l *link) downError() error {
	if cause := l.cause(); errors.Is(cause, ErrClosed) {
		return cause
	} else if cause != nil {
		return fmt.Errorf("%w: %w", ErrAgentDown, cause)
	}
	return ErrAgentDown
}

func (l *link) readLoop(onEvent func(protocol.EventBody), log *slog.Logger) {
	for {
		f, err := l.pc.Recv()
		if err != nil {
			l.fail(fmt.Errorf("reading from the agent: %w", err))
			return
		}
		if f.Header.ID == 0 {
			if f.Header.Type == protocol.TypeEvent && onEvent != nil {
				var ev protocol.EventBody
				if err := protocol.DecodeBody(f, &ev); err != nil {
					log.Warn("ignoring a malformed agent event", "error", err)
					continue
				}
				onEvent(ev)
			}
			continue
		}
		l.mu.Lock()
		ch := l.pending[f.Header.ID]
		delete(l.pending, f.Header.ID)
		l.mu.Unlock()
		if ch != nil {
			ch <- f // buffered: never blocks
		} // else a late answer to a request that was canceled or timed out
	}
}

func (l *link) forget(id uint64) {
	l.mu.Lock()
	delete(l.pending, id)
	l.mu.Unlock()
}

func (l *link) call(ctx context.Context, typ string, req any, blob []byte, res any, cancelRemote bool) ([]byte, error) {
	if l.isDone() {
		return nil, l.downError()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := l.next.Add(1)
	fr, err := protocol.NewFrame(id, typ, req, blob)
	if err != nil {
		return nil, err
	}
	ch := make(chan protocol.Frame, 1)
	l.mu.Lock()
	l.pending[id] = ch
	l.mu.Unlock()
	if err := l.pc.Send(fr); err != nil {
		l.forget(id)
		if protocol.IsViolation(err) { // we refused to build it; the connection is fine
			return nil, err
		}
		l.fail(fmt.Errorf("writing to the agent: %w", err))
		return nil, l.downError()
	}
	select {
	case f := <-ch:
		return l.finish(typ, f, res)
	case <-ctx.Done():
		l.forget(id)
		if cancelRemote {
			if cf, err := protocol.NewFrame(l.next.Add(1), protocol.TypeCancel, protocol.CancelBody{ID: id}, nil); err == nil {
				_ = l.pc.Send(cf) // best effort: the answer, if it comes, is dropped
			}
		}
		return nil, ctx.Err()
	case <-l.done:
		return nil, l.downError()
	}
}

// finish checks a response against the request it answers.
func (l *link) finish(typ string, f protocol.Frame, res any) ([]byte, error) {
	switch f.Header.Type {
	case protocol.TypeError:
		var e protocol.ErrorBody
		if err := protocol.DecodeBody(f, &e); err != nil {
			return nil, l.violation(err)
		}
		return nil, &RemoteError{Code: clip(e.Code), Message: clip(e.Message)}
	case protocol.ResponseOf(typ):
		if res != nil {
			if err := protocol.DecodeBody(f, res); err != nil {
				return nil, l.violation(err)
			}
		}
		return f.Blob, nil
	}
	return nil, l.violation(fmt.Errorf("expected %q, got %q", protocol.ResponseOf(typ), f.Header.Type))
}

// violation drops the connection: an agent that answers outside the protocol cannot be trusted with the next
// request either.
func (l *link) violation(err error) error {
	err = fmt.Errorf("%w: %w", ErrBadResponse, err)
	l.fail(err)
	return err
}

// heartbeat pings every interval; limit unanswered pings in a row end the connection.
func (l *link) heartbeat(interval time.Duration, limit int) {
	t := time.NewTicker(interval)
	defer t.Stop()
	misses := 0
	for {
		select {
		case <-l.done:
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(l.ctx, interval)
		_, err := l.call(ctx, protocol.TypePing, nil, nil, nil, false)
		cancel()
		switch {
		case err == nil:
			misses = 0
		case errors.Is(err, context.DeadlineExceeded):
			misses++
			if misses >= limit {
				l.fail(fmt.Errorf("%d heartbeats in a row went unanswered", misses))
				return
			}
		default:
			return // the connection is already gone
		}
	}
}
