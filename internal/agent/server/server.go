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
	// in the handshake are ping plus Capabilities.
	Handlers     map[string]Handler
	Capabilities []string
	// MaxInFlight bounds requests being served at once (default 8); more wait for a slot (up to 64 in all, then
	// they are answered "busy"), while pings and cancels are still read and served.
	MaxInFlight int
	Log         *slog.Logger
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
	maxIn := cfg.MaxInFlight
	if maxIn <= 0 {
		maxIn = 8
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn := protocol.NewConn(bufio.NewReaderSize(rw, 64<<10), rw, closer)
	defer func() {
		if closer != nil {
			_ = closer.Close()
		}
	}()

	s := &session{cfg: cfg, conn: conn, log: log, inflight: map[uint64]context.CancelFunc{}, sem: make(chan struct{}, maxIn)}
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

	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel() // before the wait: handlers see the end of the session
	for {
		f, err := conn.Recv()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
				return nil
			}
			return fmt.Errorf("reading from the host: %w", err)
		}
		s.dispatch(ctx, &wg, f)
	}
}

type session struct {
	cfg  Config
	conn *protocol.Conn
	log  *slog.Logger

	mu       sync.Mutex
	inflight map[uint64]context.CancelFunc
	sem      chan struct{}
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
	caps := append([]string{protocol.CapPing}, s.cfg.Capabilities...)
	sort.Strings(caps)
	ack := protocol.HelloAck{Proto: protocol.ProtoVersion, AgentVersion: s.cfg.Version, AgentSHA256: s.cfg.SelfSHA256,
		Nonce: hello.Nonce, Arch: s.cfg.Arch, User: s.cfg.User, Display: s.cfg.Display, Capabilities: caps}
	resp, err := protocol.NewFrame(f.Header.ID, protocol.TypeHelloAck, ack, nil)
	if err != nil {
		return err
	}
	return s.conn.Send(resp)
}

func (s *session) dispatch(ctx context.Context, wg *sync.WaitGroup, f protocol.Frame) {
	h := f.Header
	switch {
	case h.Type == protocol.TypeCancel:
		var c protocol.CancelBody
		if err := protocol.DecodeBody(f, &c); err != nil {
			s.log.Warn("bad cancel", "error", err)
			return
		}
		s.mu.Lock()
		cancel := s.inflight[c.ID]
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
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
	rctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if _, dup := s.inflight[h.ID]; dup {
		s.mu.Unlock()
		cancel()
		s.sendError(h.ID, protocol.CodeBusy, "a request with this id is already running")
		return
	}
	if len(s.inflight) >= maxQueued {
		s.mu.Unlock()
		cancel()
		s.sendError(h.ID, protocol.CodeBusy, "too many requests are waiting")
		return
	}
	s.inflight[h.ID] = cancel
	s.mu.Unlock()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.inflight, h.ID)
			s.mu.Unlock()
			cancel()
		}()
		// Requests beyond the concurrency limit wait here, not in the reader: pings and cancels must always get through.
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		case <-rctx.Done():
			s.sendError(h.ID, protocol.CodeCanceled, "canceled")
			return
		}
		defer func() {
			if r := recover(); r != nil { // a handler bug must not take the agent down
				s.log.Error("handler panicked", "type", h.Type, "panic", fmt.Sprint(r))
				s.sendError(h.ID, protocol.CodeInternal, "internal error")
			}
		}()
		body, blob, err := handler(rctx, f)
		switch {
		case rctx.Err() != nil && err == nil && s.canceled(rctx):
			s.sendError(h.ID, protocol.CodeCanceled, "canceled")
		case err != nil:
			var he *HandlerError
			if errors.As(err, &he) {
				s.sendError(h.ID, he.Code, he.Message)
			} else {
				s.log.Error("request failed", "type", h.Type, "error", err)
				s.sendError(h.ID, protocol.CodeInternal, err.Error())
			}
		default:
			s.reply(h, body, blob, nil)
		}
	}()
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
