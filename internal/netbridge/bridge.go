// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Package netbridge is the host's side of the guest's way out (PLAN.md section 7.8, D-07): when a session is started with
// --allow-net, the guest asks for each connection over one multiplexed virtio-serial port, and the host decides with the
// egress policy, resolves the name, connects, and carries the bytes. The guest has no network device and never resolves a
// name; nothing is connected that the policy has not vetted.
package netbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/xtaci/smux"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/safety/audit"
	"github.com/PacifAIst/controlsnack-local/internal/safety/egress"
)

// Dialer connects to host:port after deciding whether it may; *egress.Policy is the real one. The Decision says what was
// decided and why, whether or not a connection was made.
type Dialer interface {
	Dial(ctx context.Context, host string, port int) (net.Conn, egress.Decision, error)
}

// Phases of an Event.
const (
	PhaseDecision = "decision" // the policy has decided; for an allowed destination the connection is made or has failed
	PhaseClosed   = "closed"   // a connection that was made has ended
)

// Event is what the bridge tells the audit log: every decision, and the bytes of every connection that was made.
type Event struct {
	Time     time.Time
	Phase    string
	Decision egress.Decision
	// Error is why a connection was not made although the policy allowed it (the server did not answer), or why a
	// request was refused for another reason (too many connections, a bad request).
	Error string
	// Up and Down are the bytes carried from the guest and to it, set in the closed phase.
	Up, Down int64
	Duration time.Duration
}

// Bridge serves the guest's requests for connections.
type Bridge struct {
	Dialer  Dialer
	Tracker *egress.Tracker
	// Audit, if set, is called for every event. It must not block for long.
	Audit func(Event)
	Log   *slog.Logger
	// RequestTimeout bounds the wait for the first line of a stream (10 s when zero).
	RequestTimeout time.Duration
}

const (
	defaultRequestTimeout = 10 * time.Second
	maxReasonRunes        = 200
	retryPause            = 500 * time.Millisecond
	// closeGrace is how long a connection's second direction gets to notice that the first has ended.
	closeGrace = 3 * time.Second
)

func (b *Bridge) log() *slog.Logger {
	if b.Log != nil {
		return b.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (b *Bridge) emit(e Event) {
	if b.Audit != nil {
		e.Time = time.Now()
		b.Audit(e)
	}
}

// Run serves the guest over the port until the context ends. first is the connection to the port that is already open
// (may be nil); dial makes another when the session on the current one ends: the guest's agent restarted, or the port
// was closed. Run does not return an error for a session that ended.
func (b *Bridge) Run(ctx context.Context, first net.Conn, dial func(context.Context) (net.Conn, error)) error {
	conn := first
	for ctx.Err() == nil {
		if conn == nil {
			var err error
			if conn, err = dial(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				b.log().Debug("the network port is not reachable", "error", err)
				if !sleep(ctx, retryPause) {
					return nil
				}
				continue
			}
		}
		err := b.Serve(ctx, conn)
		_ = conn.Close()
		conn = nil
		if err != nil && ctx.Err() == nil {
			b.log().Debug("the network session ended", "error", err)
		}
		if !sleep(ctx, retryPause) {
			return nil
		}
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// Serve runs one multiplexed session over conn: it accepts the guest's streams until the session ends or the context
// does. It closes the session before it returns.
func (b *Bridge) Serve(ctx context.Context, conn io.ReadWriteCloser) error {
	sess, err := smux.Server(conn, protocol.NetMuxConfig())
	if err != nil {
		return fmt.Errorf("starting the multiplexer: %w", err)
	}
	defer sess.Close()
	stop := context.AfterFunc(ctx, func() { _ = sess.Close() })
	defer stop()
	for {
		st, err := acceptStream(sess)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go b.handle(ctx, st)
	}
}

// acceptStream accepts the guest's next stream. smux hands out a wrapper whose finalizer closes the stream when the
// wrapper is garbage collected (the finalizer of OpenStream's wrapper was removed for that reason, smux issue 997, the
// one of AcceptStream's was not). The wrapper embeds the stream, so code that holds the stream only through a promoted
// method (`defer st.Close()`) leaves the wrapper unreachable: a collection then closes a stream that is still in use and
// the guest sees its connection end with nothing said (found as a rare failure of a test whose host kept a stream
// waiting). A stream here lives until its handler closes it.
func acceptStream(sess *smux.Session) (*smux.Stream, error) {
	st, err := sess.AcceptStream()
	if err != nil {
		return nil, err
	}
	runtime.SetFinalizer(st, nil)
	return st, nil
}

// refuse answers a request with a reason and records why.
func (b *Bridge) refuse(st *smux.Stream, d egress.Decision, reason, errText string) {
	_ = st.SetWriteDeadline(time.Now().Add(5 * time.Second))
	err := protocol.WriteNetReply(st, protocol.NetReply{Error: textclean.Line(reason, maxReasonRunes)})
	if errors.Is(err, protocol.ErrNetLine) {
		// A reason that does not fit the line (JSON can make a short text long): the guest still hears that it was refused.
		_ = protocol.WriteNetReply(st, protocol.NetReply{Error: "the connection was refused"})
	}
	b.emit(Event{Phase: PhaseDecision, Decision: d, Error: errText})
}

// handle serves one stream: read the request, decide, connect, answer, carry the bytes.
func (b *Bridge) handle(ctx context.Context, st *smux.Stream) {
	defer st.Close()
	wait := b.RequestTimeout
	if wait <= 0 {
		wait = defaultRequestTimeout
	}
	_ = st.SetReadDeadline(time.Now().Add(wait))
	req, err := protocol.ReadNetRequest(st)
	if err != nil {
		b.refuse(st, egress.Decision{Rule: "bad-request", Reason: "the request is not valid"}, "the request is not valid", err.Error())
		return
	}
	_ = st.SetReadDeadline(time.Time{})

	release, err := b.Tracker.Open()
	if err != nil {
		b.refuse(st, egress.Decision{Rule: "limit", Reason: err.Error(), Host: req.Host, Port: req.Port}, err.Error(), err.Error())
		return
	}
	defer release()

	begin := time.Now()
	up, dec, err := b.Dialer.Dial(ctx, req.Host, req.Port)
	if err != nil {
		reason := err.Error()
		if !dec.Allowed && dec.Reason != "" {
			reason = dec.Reason
		}
		errText := ""
		if dec.Allowed {
			errText = err.Error() // allowed, but the server did not answer
		}
		b.refuse(st, dec, reason, errText)
		return
	}
	defer up.Close()
	_ = st.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := protocol.WriteNetReply(st, protocol.NetReply{OK: true}); err != nil {
		return
	}
	_ = st.SetWriteDeadline(time.Time{})
	b.emit(Event{Phase: PhaseDecision, Decision: dec})

	var upBytes, downBytes atomic.Int64
	b.carry(st, up, &upBytes, &downBytes)
	b.emit(Event{Phase: PhaseClosed, Decision: dec, Up: upBytes.Load(), Down: downBytes.Load(), Duration: time.Since(begin)})
}

// carry copies bytes both ways until the connection is over, which is when either direction ends. A stream has no
// half-close (see protocol.NetMuxConfig, and a peer that has closed its end cannot be written to), so there is nothing to
// wait for in the other direction: the server's end of a finished answer is closed after its last byte, and a guest
// that has closed the stream cannot receive more.
func (b *Bridge) carry(st *smux.Stream, up net.Conn, upBytes, downBytes *atomic.Int64) {
	t := &traffic{idle: b.Tracker.IdleTimeout(), tracker: b.Tracker}
	t.touch()
	done := make(chan struct{}, 2)

	go func() { // guest -> server
		t.pump(up, st, upBytes)
		done <- struct{}{}
	}()
	go func() { // server -> guest
		t.pump(st, up, downBytes)
		_ = st.Close() // the end of the answer: the guest sees the end of the stream after the last byte
		done <- struct{}{}
	}()
	<-done
	_ = up.Close()
	_ = st.Close()
	select {
	case <-done:
	case <-time.After(closeGrace):
	}
}

// traffic is what the two directions of one connection share: when anything last moved, and the session's budget.
type traffic struct {
	idle    time.Duration
	tracker *egress.Tracker
	last    atomic.Int64 // unix nanoseconds of the last byte in either direction
	over    atomic.Bool  // the byte budget is used up
}

func (t *traffic) touch() { t.last.Store(time.Now().UnixNano()) }

func (t *traffic) quiet() time.Duration { return time.Since(time.Unix(0, t.last.Load())) }

// pump moves bytes from src to dst, charging them to the session. It ends when either side does, when the byte budget is
// used up, or when neither direction has moved for the idle time (a download the guest is not answering is not idle).
func (t *traffic) pump(dst, src interface {
	io.ReadWriter
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}, n *atomic.Int64) {
	buf := make([]byte, 32<<10)
	for {
		_ = src.SetReadDeadline(time.Now().Add(t.idle))
		nr, err := src.Read(buf)
		if nr > 0 {
			if t.over.Load() || t.tracker.AddBytes(int64(nr)) != nil {
				t.over.Store(true)
				return
			}
			t.touch()
			_ = dst.SetWriteDeadline(time.Now().Add(t.idle))
			nw, werr := dst.Write(buf[:nr])
			n.Add(int64(nw))
			if werr != nil {
				return
			}
			t.touch()
		}
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() && t.quiet() < t.idle && !t.over.Load() {
				continue // this direction was silent, the other was not
			}
			return
		}
	}
}

// Record is the audit log's line for an event (kind net): the decision with the rule that made it, and for a finished
// connection the bytes carried.
func (e Event) Record() audit.Record {
	verdict := "allowed"
	switch {
	case !e.Decision.Allowed:
		verdict = "denied"
	case e.Error != "":
		verdict = "failed"
	}
	data := map[string]any{"host": e.Decision.Host, "port": e.Decision.Port}
	if e.Phase == PhaseClosed {
		data["up"], data["down"], data["ms"] = e.Up, e.Down, e.Duration.Milliseconds()
	}
	if e.Error != "" {
		data["error"] = e.Error
	}
	r := audit.Record{Kind: audit.KindNet, Event: e.Phase, Actor: "system", Verdict: verdict, Reason: e.Decision.Reason, Data: audit.ToRaw(data)}
	if e.Decision.Rule != "" {
		r.Rules = []string{e.Decision.Rule}
	}
	return r
}
