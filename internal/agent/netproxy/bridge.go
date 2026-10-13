// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package netproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/xtaci/smux"

	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

// DisabledMessage is what a program in the guest is told when the session has no network: the sentence a model can act on.
const DisabledMessage = "Network is disabled for this session (start with --allow-net)"

// Errors of dialing through the bridge.
var (
	// ErrDisabled means there is no bridge: the session was not started with --allow-net, so the port does not exist.
	ErrDisabled = errors.New("network is disabled for this session (start with --allow-net)")
	// ErrBridgeDown means the bridge exists but the host is not answering on it.
	ErrBridgeDown = errors.New("the network bridge to the host is not connected")
)

// DeniedError means the host refused a connection, and says why in a sentence.
type DeniedError struct{ Reason string }

func (e *DeniedError) Error() string { return e.Reason }

// handshakeTimeout is how long the host has to answer a request for a connection. The host resolves the name and
// connects before it answers, so this includes the time to reach the server.
const handshakeTimeout = 30 * time.Second

// Bridge opens connections to the outside through the host: one multiplexed stream for each, over the one port.
type Bridge struct {
	// Open opens the port to the host. It returns ErrDisabled when there is no port.
	Open func() (io.ReadWriteCloser, error)
	// Handshake bounds the wait for the host's answer (handshakeTimeout when zero).
	Handshake time.Duration

	mu   sync.Mutex
	sess *smux.Session
}

// muxConfig is the multiplexer's setting, the same on both sides. Version 2 has the half-close that tunnels need.
func muxConfig() *smux.Config {
	c := smux.DefaultConfig()
	c.Version = 2
	return c
}

// MuxConfig returns the setting the host's side must use as well.
func MuxConfig() *smux.Config { return muxConfig() }

// session returns the live connection to the host, making one if there is none.
func (b *Bridge) session() (*smux.Session, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sess != nil && !b.sess.IsClosed() {
		return b.sess, nil
	}
	conn, err := b.Open()
	if err != nil {
		return nil, err
	}
	s, err := smux.Client(conn, muxConfig())
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %w", ErrBridgeDown, err)
	}
	b.sess = s
	return s, nil
}

// drop forgets a session that failed, if it is still the current one.
func (b *Bridge) drop(s *smux.Session) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sess == s {
		b.sess = nil
	}
	_ = s.Close()
}

// Close ends the connection to the host.
func (b *Bridge) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sess == nil {
		return nil
	}
	err := b.sess.Close()
	b.sess = nil
	return err
}

// Dial asks the host for a connection to host:port. It returns the stream that carries it once the host has accepted;
// a refusal is a *DeniedError with the host's reason.
func (b *Bridge) Dial(ctx context.Context, host string, port int) (net.Conn, error) {
	s, err := b.session()
	if err != nil {
		return nil, err
	}
	st, err := s.OpenStream()
	if err != nil {
		b.drop(s)
		return nil, fmt.Errorf("%w: %w", ErrBridgeDown, err)
	}
	wait := b.Handshake
	if wait <= 0 {
		wait = handshakeTimeout
	}
	// The wait ends when the request does, or when the host takes too long.
	_ = st.SetDeadline(time.Now().Add(wait))
	stop := context.AfterFunc(ctx, func() { _ = st.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	rep, err := b.ask(st, host, port)
	if ctx.Err() != nil {
		_ = st.Close()
		return nil, ctx.Err()
	}
	if err != nil {
		_ = st.Close()
		if s.IsClosed() {
			b.drop(s)
		}
		return nil, fmt.Errorf("%w: %w", ErrBridgeDown, err)
	}
	if !rep.OK {
		_ = st.Close()
		return nil, &DeniedError{Reason: rep.Error}
	}
	_ = st.SetDeadline(time.Time{})
	return fullClose{st}, nil
}

// fullClose hides the stream's CloseWrite. smux's half-close loses the last bytes the peer sent in a few exchanges of a
// hundred (found by the tests: the answer arrives as an empty stream), while a full Close after the data loses nothing,
// so a stream is only ever closed whole; the end of a direction is told to the browser or the server over the TCP
// connection they have, which does have a half-close.
type fullClose struct{ net.Conn }

func (b *Bridge) ask(st *smux.Stream, host string, port int) (protocol.NetReply, error) {
	if err := protocol.WriteNetRequest(st, protocol.NetRequest{Host: host, Port: port}); err != nil {
		return protocol.NetReply{}, err
	}
	return protocol.ReadNetReply(st)
}
