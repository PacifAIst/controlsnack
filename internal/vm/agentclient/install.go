// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agentclient

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/PacifAIst/controlsnack/internal/protocol"
	"github.com/PacifAIst/controlsnack/internal/vm/agentbin"
)

// InstallError means the guest stub refused the pushed agent (or answered something other than OK).
type InstallError struct{ Reason string }

func (e *InstallError) Error() string { return "the guest refused the agent: " + e.Reason }

// ErrInstall wraps every failure to get the agent running that is not the stub's own refusal.
var ErrInstall = errors.New("installing the guest agent failed")

// bound limits how long conn may block: until ctx ends or timeout passes, whichever is first. The returned function
// lifts the limit again. A blocked Read or Write returns a timeout error when the limit is reached.
func bound(ctx context.Context, conn net.Conn, timeout time.Duration) (release func()) {
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	return func() {
		stop()
		_ = conn.SetDeadline(time.Time{})
	}
}

// ioError reports why an operation on a bounded connection failed: the context's error if it ended, else err.
func ioError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// install pushes the agent (PLAN.md section 5.1) and waits for the stub's verdict. The stub has checked the
// architecture, the size and the hash before it answers OK; the hash it reports must be the one pushed.
func install(ctx context.Context, conn net.Conn, br *bufio.Reader, agent agentbin.Agent, nonce string, timeout time.Duration) error {
	push := protocol.Push{Arch: agent.Arch, Size: int64(len(agent.Binary)), SHA256: agent.SHA256, Nonce: nonce}
	line, err := push.Line()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInstall, err)
	}
	release := bound(ctx, conn, timeout)
	defer release()
	if _, err := io.WriteString(conn, line); err != nil {
		return fmt.Errorf("%w: sending the push line: %w", ErrInstall, ioError(ctx, err))
	}
	if _, err := conn.Write(agent.Binary); err != nil {
		return fmt.Errorf("%w: sending the agent: %w", ErrInstall, ioError(ctx, err))
	}
	reply, err := protocol.ReadBootLine(br)
	if err != nil {
		return fmt.Errorf("%w: waiting for the guest's answer: %w", ErrInstall, ioError(ctx, err))
	}
	r, err := protocol.ParseReply(reply)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInstall, err)
	}
	if !r.OK {
		return &InstallError{Reason: r.Reason}
	}
	if r.SHA256 != agent.SHA256 {
		return fmt.Errorf("%w: the guest stored a different agent than the one pushed", ErrInstall)
	}
	return nil
}

// handshake performs the hello exchange and checks the acknowledgement against what was pushed.
func handshake(ctx context.Context, conn net.Conn, pc *protocol.Conn, hello protocol.Hello, expect protocol.Expect, timeout time.Duration) (protocol.HelloAck, error) {
	release := bound(ctx, conn, timeout)
	defer release()
	req, err := protocol.NewFrame(1, protocol.TypeHello, hello, nil)
	if err != nil {
		return protocol.HelloAck{}, err
	}
	if err := pc.Send(req); err != nil {
		return protocol.HelloAck{}, fmt.Errorf("%w: sending the hello: %w", protocol.ErrHandshake, ioError(ctx, err))
	}
	f, err := pc.Recv()
	if err != nil {
		return protocol.HelloAck{}, fmt.Errorf("%w: waiting for the agent's answer: %w", protocol.ErrHandshake, ioError(ctx, err))
	}
	switch {
	case f.Header.Type == protocol.TypeError && f.Header.ID == 1:
		var e protocol.ErrorBody
		if err := protocol.DecodeBody(f, &e); err != nil {
			return protocol.HelloAck{}, fmt.Errorf("%w: %w", protocol.ErrHandshake, err)
		}
		return protocol.HelloAck{}, fmt.Errorf("%w: the agent refused the hello: %s: %s", protocol.ErrHandshake, e.Code, clip(e.Message))
	case f.Header.Type != protocol.TypeHelloAck || f.Header.ID != 1:
		return protocol.HelloAck{}, fmt.Errorf("%w: expected a hello_ack for request 1, got %q for %d", protocol.ErrHandshake, f.Header.Type, f.Header.ID)
	}
	var ack protocol.HelloAck
	if err := protocol.DecodeBody(f, &ack); err != nil {
		return protocol.HelloAck{}, fmt.Errorf("%w: %w", protocol.ErrHandshake, err)
	}
	if err := ack.Validate(expect); err != nil {
		return protocol.HelloAck{}, err
	}
	return ack, nil
}

// newNonce returns 16 random bytes as hex: it ties a hello_ack to the push that started that very agent.
func newNonce(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	var b [16]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", fmt.Errorf("making a nonce: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// clip shortens text that came from the guest before it goes into an error message.
func clip(s string) string {
	const max = 300
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "..."
	}
	return s
}
