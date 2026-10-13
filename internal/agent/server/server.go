// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

// Handler serves one request type. It returns the response body (nil for none) and an attachment (nil for none).
// Returning a *HandlerError sends that code to the host; any other error is reported as an internal error. The
// context ends when the host cancels the request or the connection ends.
type Handler func(ctx context.Context, req protocol.Frame) (body any, blob []byte, err error)

// HandlerError is an error with a protocol error code.
type HandlerError struct {
	Code    string
	Message string
}

func (e *HandlerError) Error() string { return e.Code + ": " + e.Message }

// Fail builds a HandlerError.
func Fail(code, format string, args ...any) *HandlerError {
	return &HandlerError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Config is what the dispatcher needs to answer the handshake and to route requests.
type Config struct {
	Version    string // agent version
	SelfSHA256 string // SHA-256 of the running agent binary: the host compares it with what it pushed
	Arch       string // x86_64 or aarch64
	User       string // the account the agent runs as
	// Nonce is the one passed on the command line (and in the push line); the host must repeat it in its hello.
	Nonce   string
	Display protocol.Display
	// Handlers serve the request types beyond ping, which the dispatcher answers itself. The capabilities announced
	// in the handshake are ping, the capability of every registered handler (CapabilityOf) and Capabilities, which
	// names the ones that are not request types (netproxy).
	Handlers     map[string]Handler
	Capabilities []string
	// Workers overrides DefaultWorkers per pool (PLAN.md section 8): how many requests of that pool are served at
	// once. Requests wait in their pool's queue, oldest first, while pings and cancels are still read and served; at
	// most 64 requests may be running or waiting in all, after which the answer is "busy".
	Workers map[string]int
	Log     *slog.Logger
}

// ErrHandshake is returned when the host's first frame is not an acceptable hello.
var ErrHandshake = errors.New("handshake refused")

// maxErrorMessage bounds the text of an error frame: handler errors can carry arbitrary command output.
const maxErrorMessage = 1000

// maxQueued bounds the requests running or waiting for a slot; past it the agent answers "busy".
const maxQueued = 64

// Serve runs the protocol on the port until the host disconnects (nil), the context ends, or the host breaks the
// protocol (an error: the stub then waits for a fresh push). rw is the port; Serve closes closer, if not nil, when
// it ends, which is how a blocked read is released.
func Serve(ctx context.Context, rw io.ReadWriter, closer io.Closer, cfg Config) error {
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn := protocol.NewConn(bufio.NewReaderSize(rw, 64<<10), rw, closer)
	defer func() {
		if closer != nil {
			_ = closer.Close()
		}
	}()

	s := &session{cfg: cfg, conn: conn, log: log, ctx: ctx, inflight: map[uint64]*job{}, pools: newPools(cfg.Workers)}
	if err := s.handshake(); err != nil {
		return err
	}
	log.Info("host connected", "arch", cfg.Arch, "user", cfg.User)

	// A blocked read ends when the context does.
	stop := context.AfterFunc(ctx, func() {
		if closer != nil {
			_ = closer.Close()
		}
	})
	defer stop()

	defer s.wg.Wait()
	defer cancel() // before the wait: handlers see the end of the session
	for {
		f, err := conn.Recv()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
				return nil
			}
			return fmt.Errorf("reading from the host: %w", err)
		}
		s.dispatch(f)
	}
}

type session struct {
	cfg  Config
	conn *protocol.Conn
	log  *slog.Logger
	ctx  context.Context // the session's: it ends with the connection
	wg   sync.WaitGroup  // the workers

	mu       sync.Mutex
	inflight map[uint64]*job
	pools    map[string]*pool
}

// newPools makes one pool per name, with the default number of workers unless overridden (at least one).
func newPools(override map[string]int) map[string]*pool {
	pools := make(map[string]*pool, len(DefaultWorkers))
	for name, n := range DefaultWorkers {
		if o, ok := override[name]; ok {
			n = o
		}
		pools[name] = &pool{name: name, limit: max(n, 1)}
	}
	return pools
}

func (s *session) handshake() error {
	f, err := s.conn.Recv()
	if err != nil {
		return fmt.Errorf("%w: reading the hello: %w", ErrHandshake, err)
	}
	refuse := func(format string, args ...any) error {
		msg := fmt.Sprintf(format, args...)
		s.sendError(f.Header.ID, protocol.CodeInvalidArgs, msg)
		return fmt.Errorf("%w: %s", ErrHandshake, msg)
	}
	if f.Header.Type != protocol.TypeHello || f.Header.ID == 0 {
		return refuse("the first frame must be a hello request, got %q (id %d)", f.Header.Type, f.Header.ID)
	}
	var hello protocol.Hello
	if err := protocol.DecodeBody(f, &hello); err != nil {
		return refuse("%v", err)
	}
	major, err := protocol.ProtoMajor(hello.Proto)
	want, _ := protocol.ProtoMajor(protocol.ProtoVersion)
	if err != nil || major != want {
		return refuse("the host speaks protocol %q, this agent speaks %s", hello.Proto, protocol.ProtoVersion)
	}
	if s.cfg.Nonce != "" && hello.Nonce != s.cfg.Nonce {
		return refuse("the hello does not carry the nonce this agent was started with")
	}
	caps := s.capabilities()
	ack := protocol.HelloAck{Proto: protocol.ProtoVersion, AgentVersion: s.cfg.Version, AgentSHA256: s.cfg.SelfSHA256,
		Nonce: hello.Nonce, Arch: s.cfg.Arch, User: s.cfg.User, Display: s.cfg.Display, Capabilities: caps}
	resp, err := protocol.NewFrame(f.Header.ID, protocol.TypeHelloAck, ack, nil)
	if err != nil {
		return err
	}
	return s.conn.Send(resp)
}

func (s *session) dispatch(f protocol.Frame) {
	h := f.Header
	switch {
	case h.Type == protocol.TypeCancel:
		s.cancelRequest(f)
		return
	case h.ID == 0:
		s.log.Warn("a request with id 0 is ignored: that id is for events", "type", h.Type)
		return
	case h.Type == protocol.TypeHello:
		s.sendError(h.ID, protocol.CodeInvalidArgs, "already greeted")
		return
	case h.Type == protocol.TypePing:
		s.reply(h, nil, nil, nil) // inline: the heartbeat never waits behind other work
		return
	}
	handler, ok := s.cfg.Handlers[h.Type]
	if !ok {
		s.sendError(h.ID, protocol.CodeUnknownType, fmt.Sprintf("unknown message type %q", truncate(h.Type, 64)))
		return
	}
	p := s.pools[PoolOf(h.Type)]
	rctx, cancel := context.WithCancel(s.ctx)
	j := &job{id: h.ID, frame: f, handler: handler, pool: p, ctx: rctx, cancel: cancel, queued: time.Now()}

	s.mu.Lock()
	switch {
	case s.inflight[h.ID] != nil:
		s.mu.Unlock()
		cancel()
		s.sendError(h.ID, protocol.CodeBusy, "a request with this id is already running")
		return
	case len(s.inflight) >= maxQueued:
		s.mu.Unlock()
		cancel()
		s.sendError(h.ID, protocol.CodeBusy, "too many requests are waiting")
		return
	}
	s.inflight[h.ID] = j
	s.mu.Unlock()

	// The job waits in its pool's queue, not in the reader: pings and cancels must always get through.
	if p.push(j) {
		s.wg.Add(1)
		go s.worker(p)
	}
}

// cancelRequest aborts a request by id. One that is still waiting for a worker is answered at once and never runs; one
// that is running is told to stop through its context and answers itself.
func (s *session) cancelRequest(f protocol.Frame) {
	var c protocol.CancelBody
	if err := protocol.DecodeBody(f, &c); err != nil {
		s.log.Warn("bad cancel", "error", err)
		return
	}
	s.mu.Lock()
	j := s.inflight[c.ID]
	s.mu.Unlock()
	if j == nil {
		return
	}
	j.cancel()
	if j.pool.remove(j) {
		s.finish(j)
		s.sendError(j.id, protocol.CodeCanceled, "canceled")
		s.logRequest(j, 0, protocol.CodeCanceled)
	}
}

// finish forgets a request that is over.
func (s *session) finish(j *job) {
	s.mu.Lock()
	delete(s.inflight, j.id)
	s.mu.Unlock()
	j.cancel()
}

// worker serves the pool's queue, oldest request first, until it is empty.
func (s *session) worker(p *pool) {
	defer s.wg.Done()
	for {
		j := p.next()
		if j == nil {
			return
		}
		s.run(j)
	}
}

// run calls the handler and sends what it produced.
func (s *session) run(j *job) {
	defer s.finish(j)
	if s.ctx.Err() != nil {
		return // the connection is gone: nobody to answer
	}
	if j.ctx.Err() != nil { // canceled while it waited, in the instant between the queue and here
		s.sendError(j.id, protocol.CodeCanceled, "canceled")
		return
	}
	began := time.Now()
	outcome := "ok"
	defer func() {
		if r := recover(); r != nil { // a handler bug must not take the agent down
			s.log.Error("handler panicked", "type", j.frame.Header.Type, "panic", fmt.Sprint(r))
			s.sendError(j.id, protocol.CodeInternal, "internal error")
			outcome = protocol.CodeInternal
		}
		s.logRequest(j, time.Since(began), outcome)
	}()
	body, blob, err := j.handler(j.ctx, j.frame)
	switch {
	case j.ctx.Err() != nil && err == nil && s.canceled(j.ctx):
		s.sendError(j.id, protocol.CodeCanceled, "canceled")
		outcome = protocol.CodeCanceled
	case err != nil:
		var he *HandlerError
		if errors.As(err, &he) {
			s.sendError(j.id, he.Code, he.Message)
			outcome = he.Code
		} else {
			s.log.Error("request failed", "type", j.frame.Header.Type, "id", j.id, "error", err)
			s.sendError(j.id, protocol.CodeInternal, err.Error())
			outcome = protocol.CodeInternal
		}
	default:
		s.reply(j.frame.Header, body, blob, nil)
	}
}

// logRequest records one request. Only the type, the pool, the timings and the outcome are logged, never the body
// or the result: they hold what the model typed and what the screen showed.
func (s *session) logRequest(j *job, took time.Duration, outcome string) {
	level := slog.LevelDebug
	if outcome != "ok" {
		level = slog.LevelWarn
	}
	s.log.Log(context.Background(), level, "request", "type", j.frame.Header.Type, "id", j.id, "pool", j.pool.name,
		"queued_ms", time.Since(j.queued).Milliseconds()-took.Milliseconds(), "took_ms", took.Milliseconds(), "outcome", outcome)
}

// canceled reports whether the request's context was canceled by the host (as opposed to the session ending).
func (s *session) canceled(ctx context.Context) bool { return errors.Is(ctx.Err(), context.Canceled) }

func (s *session) reply(req protocol.Header, body any, blob []byte, _ error) {
	f, err := protocol.NewFrame(req.ID, protocol.ResponseOf(req.Type), body, blob)
	if err != nil {
		s.sendError(req.ID, protocol.CodeInternal, "encoding the response: "+err.Error())
		return
	}
	if err := s.conn.Send(f); err != nil {
		s.log.Warn("cannot send a response", "type", req.Type, "error", err)
	}
}

func (s *session) sendError(id uint64, code, msg string) {
	f, err := protocol.NewFrame(id, protocol.TypeError, protocol.ErrorBody{Code: code, Message: truncate(msg, maxErrorMessage)}, nil)
	if err != nil {
		return
	}
	if err := s.conn.Send(f); err != nil {
		s.log.Warn("cannot send an error", "code", code, "error", err)
	}
}

func truncate(s string, n int) string {
	s = strings.ToValidUTF8(s, "?")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// capabilities is what the handshake announces: ping, the capability of every registered handler, and the extra ones
// from the configuration, sorted and without duplicates.
func (s *session) capabilities() []string {
	set := map[string]bool{protocol.CapPing: true}
	for typ := range s.cfg.Handlers {
		if c := CapabilityOf(typ); c != "" {
			set[c] = true
		}
	}
	for _, c := range s.cfg.Capabilities {
		set[c] = true
	}
	caps := make([]string, 0, len(set))
	for c := range set {
		caps = append(caps, c)
	}
	sort.Strings(caps)
	return caps
}
