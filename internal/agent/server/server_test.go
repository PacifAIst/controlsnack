// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

const (
	nonce = "0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f"
	sum   = "abababababababababababababababababababababababababababababababab"
)

func baseConfig() Config {
	return Config{Version: "v0.1.0", SelfSHA256: sum, Arch: protocol.ArchX86_64, User: "cs", Nonce: nonce, Display: protocol.Display{W: 1280, H: 800}}
}

// host is the host's end of a pipe to a running Serve.
type host struct {
	t    *testing.T
	conn *protocol.Conn
	done chan error
	cut  context.CancelFunc
}

func start(t *testing.T, cfg Config) *host {
	t.Helper()
	a, b := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	h := &host{t: t, conn: protocol.NewConn(bufio.NewReader(a), a, a), done: make(chan error, 1), cut: cancel}
	go func() { h.done <- Serve(ctx, b, b, cfg) }()
	t.Cleanup(func() { cancel(); _ = a.Close() })
	return h
}

func (h *host) send(id uint64, typ string, body any, blob []byte) {
	h.t.Helper()
	f, err := protocol.NewFrame(id, typ, body, blob)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.conn.Send(f); err != nil {
		h.t.Fatal(err)
	}
}

func (h *host) recv() protocol.Frame {
	h.t.Helper()
	type result struct {
		f   protocol.Frame
		err error
	}
	ch := make(chan result, 1)
	go func() { f, err := h.conn.Recv(); ch <- result{f, err} }()
	select {
	case r := <-ch:
		if r.err != nil {
			h.t.Fatalf("receiving: %v", r.err)
		}
		return r.f
	case <-time.After(5 * time.Second):
		h.t.Fatal("no frame within 5 s")
		return protocol.Frame{}
	}
}

func (h *host) hello(n string) {
	h.t.Helper()
	h.send(1, protocol.TypeHello, protocol.Hello{Proto: protocol.ProtoVersion, HostVersion: "v1", SessionID: "s_1", Nonce: n}, nil)
}

func (h *host) greeted() *host {
	h.t.Helper()
	h.hello(nonce)
	if f := h.recv(); f.Header.Type != protocol.TypeHelloAck {
		h.t.Fatalf("%+v", f.Header)
	}
	return h
}

func (h *host) errorFrame(id uint64) protocol.ErrorBody {
	h.t.Helper()
	f := h.recv()
	if f.Header.Type != protocol.TypeError || f.Header.ID != id {
		h.t.Fatalf("expected an error for %d, got %+v", id, f.Header)
	}
	var e protocol.ErrorBody
	if err := protocol.DecodeBody(f, &e); err != nil {
		h.t.Fatal(err)
	}
	return e
}

func (h *host) result(d time.Duration) error {
	h.t.Helper()
	select {
	case err := <-h.done:
		return err
	case <-time.After(d):
		h.t.Fatal("Serve did not return")
		return nil
	}
}

// --- handshake -----------------------------------------------------------------------------------------------------

func TestHandshake(t *testing.T) {
	cfg := baseConfig()
	cfg.Capabilities = []string{protocol.CapExec, protocol.CapScreenshot}
	h := start(t, cfg)
	h.hello(nonce)
	f := h.recv()
	if f.Header.Type != protocol.TypeHelloAck || f.Header.ID != 1 {
		t.Fatalf("%+v", f.Header)
	}
	var ack protocol.HelloAck
	if err := protocol.DecodeBody(f, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Proto != protocol.ProtoVersion || ack.AgentVersion != "v0.1.0" || ack.AgentSHA256 != sum || ack.Nonce != nonce ||
		ack.Arch != "x86_64" || ack.User != "cs" || ack.Display != (protocol.Display{W: 1280, H: 800}) {
		t.Errorf("%+v", ack)
	}
	if strings.Join(ack.Capabilities, ",") != "exec,ping,screenshot" {
		t.Errorf("ping plus the handlers' capabilities, sorted: %v", ack.Capabilities)
	}
	// The host's own check accepts it.
	if err := ack.Validate(protocol.Expect{Nonce: nonce, AgentSHA256: sum, Arch: "x86_64", User: "cs", Required: []string{protocol.CapPing}}); err != nil {
		t.Error(err)
	}
}

func TestHandshakeRefusals(t *testing.T) {
	tests := []struct {
		name string
		send func(h *host)
		want string
	}{
		{"a request before hello", func(h *host) { h.send(1, protocol.TypePing, nil, nil) }, "must be a hello"},
		{"hello with id 0", func(h *host) {
			h.send(0, protocol.TypeHello, protocol.Hello{Proto: "1.0", Nonce: nonce}, nil)
		}, "must be a hello"},
		{"another major version", func(h *host) {
			h.send(1, protocol.TypeHello, protocol.Hello{Proto: "2.0", Nonce: nonce}, nil)
		}, "protocol"},
		{"no version", func(h *host) { h.send(1, protocol.TypeHello, protocol.Hello{Nonce: nonce}, nil) }, "protocol"},
		{"wrong nonce", func(h *host) {
			h.send(1, protocol.TypeHello, protocol.Hello{Proto: "1.0", Nonce: strings.Repeat("11", 16)}, nil)
		}, "nonce"},
		{"no nonce", func(h *host) { h.send(1, protocol.TypeHello, protocol.Hello{Proto: "1.0"}, nil) }, "nonce"},
		{"unknown fields", func(h *host) {
			h.send(1, protocol.TypeHello, map[string]any{"proto": "1.0", "nonce": nonce, "sudo": true}, nil)
		}, "unknown field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := start(t, baseConfig())
			tt.send(h)
			wantID := uint64(1)
			if tt.name == "hello with id 0" {
				wantID = 0 // the refusal echoes the id it was given
			}
			e := h.errorFrame(wantID)
			if e.Code != protocol.CodeInvalidArgs || !strings.Contains(e.Message, tt.want) {
				t.Errorf("%+v", e)
			}
			if err := h.result(3 * time.Second); !errors.Is(err, ErrHandshake) {
				t.Errorf("the session ends with the handshake error: %v", err)
			}
		})
	}
	// Hosts that go away before saying hello.
	h := start(t, baseConfig())
	_ = h.conn.Close()
	if err := h.result(3 * time.Second); !errors.Is(err, ErrHandshake) {
		t.Errorf("%v", err)
	}
}

func TestHelloWithoutAConfiguredNonce(t *testing.T) {
	cfg := baseConfig()
	cfg.Nonce = "" // the agent was started without --nonce: it echoes whatever it is given
	h := start(t, cfg)
	h.hello("1234567890abcdef")
	f := h.recv()
	var ack protocol.HelloAck
	if err := protocol.DecodeBody(f, &ack); err != nil || ack.Nonce != "1234567890abcdef" {
		t.Errorf("%+v %v", ack, err)
	}
}

// --- requests ------------------------------------------------------------------------------------------------------

func TestPingAndUnknownTypes(t *testing.T) {
	h := start(t, baseConfig()).greeted()
	for _, id := range []uint64{7, 8, 9} {
		h.send(id, protocol.TypePing, nil, nil)
		if f := h.recv(); f.Header.Type != protocol.TypePong || f.Header.ID != id {
			t.Errorf("ping %d: %+v", id, f.Header)
		}
	}
	h.send(20, "reboot", nil, nil)
	if e := h.errorFrame(20); e.Code != protocol.CodeUnknownType || !strings.Contains(e.Message, `"reboot"`) {
		t.Errorf("%+v", e)
	}
	h.send(21, "exec", protocol.ExecReq{Command: "ls"}, nil) // a known type with no handler registered here
	if e := h.errorFrame(21); e.Code != protocol.CodeUnknownType {
		t.Errorf("%+v", e)
	}
	h.send(22, strings.Repeat("a", 60), nil, nil)
	if e := h.errorFrame(22); len(e.Message) > 100 {
		t.Errorf("the echoed type is cut: %d bytes", len(e.Message))
	}
	h.send(23, protocol.TypePing, nil, nil) // still alive after the errors
	if f := h.recv(); f.Header.Type != protocol.TypePong {
		t.Error(f.Header)
	}
	// ID 0 is for the agent's events; a request with it is dropped without an answer, and a second hello is refused.
	h.send(0, protocol.TypePing, nil, nil)
	h.send(30, protocol.TypeHello, protocol.Hello{Proto: "1.0", Nonce: nonce}, nil)
	if e := h.errorFrame(30); e.Code != protocol.CodeInvalidArgs {
		t.Errorf("%+v", e)
	}
}

func TestHandlers(t *testing.T) {
	cfg := baseConfig()
	cfg.Handlers = map[string]Handler{
		protocol.TypeExec: func(ctx context.Context, req protocol.Frame) (any, []byte, error) {
			var r protocol.ExecReq
			if err := protocol.DecodeBody(req, &r); err != nil {
				return nil, nil, Fail(protocol.CodeInvalidArgs, "%v", err)
			}
			switch r.Command {
			case "ok":
				return protocol.ExecRes{ExitCode: 0, Stdout: "hello"}, []byte("blob-bytes"), nil
			case "denied":
				return nil, nil, Fail(protocol.CodeUnsupported, "not allowed here")
			case "plain":
				return nil, nil, errors.New("something broke inside")
			case "long":
				return nil, nil, errors.New(strings.Repeat("é", 2000))
			case "panic":
				panic("handler bug")
			}
			return nil, nil, nil
		},
	}
	cfg.Capabilities = []string{protocol.CapExec}
	h := start(t, cfg).greeted()

	h.send(2, protocol.TypeExec, protocol.ExecReq{Command: "ok"}, nil)
	f := h.recv()
	var res protocol.ExecRes
	if f.Header.Type != "exec.res" || f.Header.ID != 2 || protocol.DecodeBody(f, &res) != nil || res.Stdout != "hello" || string(f.Blob) != "blob-bytes" {
		t.Errorf("%+v %+v %q", f.Header, res, f.Blob)
	}
	h.send(3, protocol.TypeExec, protocol.ExecReq{Command: "denied"}, nil)
	if e := h.errorFrame(3); e.Code != protocol.CodeUnsupported || e.Message != "not allowed here" {
		t.Errorf("%+v", e)
	}
	h.send(4, protocol.TypeExec, protocol.ExecReq{Command: "plain"}, nil)
	if e := h.errorFrame(4); e.Code != protocol.CodeInternal || !strings.Contains(e.Message, "something broke") {
		t.Errorf("%+v", e)
	}
	h.send(5, protocol.TypeExec, protocol.ExecReq{Command: "long"}, nil)
	if e := h.errorFrame(5); len(e.Message) > 1100 || strings.ToValidUTF8(e.Message, "?") != e.Message {
		t.Errorf("error text is capped and stays valid UTF-8: %d bytes", len(e.Message))
	}
	h.send(6, protocol.TypeExec, protocol.ExecReq{Command: "panic"}, nil)
	if e := h.errorFrame(6); e.Code != protocol.CodeInternal || strings.Contains(e.Message, "handler bug") {
		t.Errorf("a panic is reported as an internal error without its details: %+v", e)
	}
	h.send(7, protocol.TypeExec, map[string]any{"command": "ok", "sudo": true}, nil) // strict decoding in the handler
	if e := h.errorFrame(7); e.Code != protocol.CodeInvalidArgs {
		t.Errorf("%+v", e)
	}
	h.send(8, protocol.TypePing, nil, nil) // the agent survived all of it
	if f := h.recv(); f.Header.Type != protocol.TypePong {
		t.Error(f.Header)
	}
}

func TestHeartbeatIsNotBlockedBySlowWork(t *testing.T) {
	release := make(chan struct{})
	cfg := baseConfig()
	cfg.Handlers = map[string]Handler{protocol.TypeExec: func(ctx context.Context, req protocol.Frame) (any, []byte, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return protocol.ExecRes{}, nil, nil
	}}
	h := start(t, cfg).greeted()
	h.send(2, protocol.TypeExec, protocol.ExecReq{Command: "slow"}, nil)
	time.Sleep(50 * time.Millisecond)
	h.send(3, protocol.TypePing, nil, nil)
	if f := h.recv(); f.Header.Type != protocol.TypePong || f.Header.ID != 3 {
		t.Fatalf("the pong must not wait for the slow request: %+v", f.Header)
	}
	close(release)
	if f := h.recv(); f.Header.ID != 2 || f.Header.Type != "exec.res" {
		t.Errorf("%+v", f.Header)
	}
}

func TestCancel(t *testing.T) {
	started := make(chan struct{})
	cfg := baseConfig()
	cfg.Handlers = map[string]Handler{protocol.TypeExec: func(ctx context.Context, req protocol.Frame) (any, []byte, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, nil, nil // a handler that just stops when told to
	}}
	h := start(t, cfg).greeted()
	h.send(2, protocol.TypeExec, protocol.ExecReq{Command: "x"}, nil)
	<-started
	h.send(3, protocol.TypeExec, protocol.ExecReq{Command: "x"}, nil)
	<-started
	h.send(2, protocol.TypeExec, protocol.ExecReq{Command: "dup"}, nil) // same id while running
	if e := h.errorFrame(2); e.Code != protocol.CodeBusy {
		t.Errorf("%+v", e)
	}
	h.send(0, protocol.TypeCancel, protocol.CancelBody{ID: 99}, nil) // nothing to cancel: harmless
	h.send(1000, protocol.TypeCancel, protocol.CancelBody{ID: 2}, nil)
	if e := h.errorFrame(2); e.Code != protocol.CodeCanceled {
		t.Errorf("%+v", e)
	}
	h.send(1001, protocol.TypePing, nil, nil)
	if f := h.recv(); f.Header.Type != protocol.TypePong {
		t.Errorf("request 3 is still running and unaffected: %+v", f.Header)
	}
	h.send(1002, protocol.TypeCancel, map[string]any{"id": 3, "extra": 1}, nil) // malformed cancel is ignored
	h.send(1003, protocol.TypeCancel, protocol.CancelBody{ID: 3}, nil)
	if e := h.errorFrame(3); e.Code != protocol.CodeCanceled {
		t.Errorf("%+v", e)
	}
}

func TestConcurrencyIsBounded(t *testing.T) {
	var running, peak atomic.Int32
	var mu sync.Mutex
	started := map[uint64]bool{} // which scheduling wins the two slots is up to the runtime
	release := make(chan struct{})
	cfg := baseConfig()
	cfg.MaxInFlight = 2
	cfg.Handlers = map[string]Handler{protocol.TypeExec: func(ctx context.Context, req protocol.Frame) (any, []byte, error) {
		mu.Lock()
		started[req.Header.ID] = true
		mu.Unlock()
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		running.Add(-1)
		return protocol.ExecRes{}, nil, nil
	}}
	h := start(t, cfg).greeted()
	for id := uint64(2); id < 6; id++ {
		h.send(id, protocol.TypeExec, protocol.ExecReq{Command: "x"}, nil)
	}
	time.Sleep(200 * time.Millisecond)
	if got := running.Load(); got != 2 {
		t.Errorf("%d requests running, the limit is 2", got)
	}
	// With the limit reached the reader still serves pings and cancels.
	h.send(10, protocol.TypePing, nil, nil)
	if f := h.recv(); f.Header.Type != protocol.TypePong || f.Header.ID != 10 {
		t.Fatalf("a ping must be answered while requests wait: %+v", f.Header)
	}
	var waiting uint64
	mu.Lock()
	for id := uint64(2); id < 6; id++ {
		if !started[id] {
			waiting = id
		}
	}
	mu.Unlock()
	h.send(11, protocol.TypeCancel, protocol.CancelBody{ID: waiting}, nil) // one that is waiting for a slot
	if e := h.errorFrame(waiting); e.Code != protocol.CodeCanceled {
		t.Errorf("%+v", e)
	}
	close(release)
	got := map[uint64]bool{}
	for range 3 {
		f := h.recv()
		got[f.Header.ID] = f.Header.Type == "exec.res"
	}
	for id := uint64(2); id < 6; id++ {
		if id != waiting && !got[id] {
			t.Errorf("request %d should complete: %v", id, got)
		}
	}
	if peak.Load() > 2 {
		t.Errorf("peak concurrency %d", peak.Load())
	}
}

func TestTooManyWaitingRequestsAreRefused(t *testing.T) {
	release := make(chan struct{})
	cfg := baseConfig()
	cfg.MaxInFlight = 1
	cfg.Handlers = map[string]Handler{protocol.TypeExec: func(ctx context.Context, req protocol.Frame) (any, []byte, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return protocol.ExecRes{}, nil, nil
	}}
	h := start(t, cfg).greeted()
	for id := uint64(2); id < 2+maxQueued; id++ {
		h.send(id, protocol.TypeExec, protocol.ExecReq{Command: "x"}, nil)
	}
	h.send(1000, protocol.TypeExec, protocol.ExecReq{Command: "one too many"}, nil)
	if e := h.errorFrame(1000); e.Code != protocol.CodeBusy {
		t.Errorf("%+v", e)
	}
	close(release)
}

// --- the end of a session ------------------------------------------------------------------------------------------

func TestEndings(t *testing.T) {
	t.Run("the host closes the port", func(t *testing.T) {
		h := start(t, baseConfig()).greeted()
		_ = h.conn.Close()
		if err := h.result(3 * time.Second); err != nil {
			t.Errorf("a host that goes away is not an error: %v", err)
		}
	})
	t.Run("the context ends and releases the handlers", func(t *testing.T) {
		stopped := make(chan struct{})
		cfg := baseConfig()
		cfg.Handlers = map[string]Handler{protocol.TypeExec: func(ctx context.Context, req protocol.Frame) (any, []byte, error) {
			<-ctx.Done()
			close(stopped)
			return nil, nil, nil
		}}
		h := start(t, cfg).greeted()
		h.send(2, protocol.TypeExec, protocol.ExecReq{Command: "x"}, nil)
		time.Sleep(50 * time.Millisecond)
		h.cut()
		if err := h.result(3 * time.Second); err != nil {
			t.Errorf("%v", err)
		}
		select {
		case <-stopped:
		case <-time.After(3 * time.Second):
			t.Error("the running handler was not told to stop")
		}
	})
	t.Run("an oversize header is a protocol violation", func(t *testing.T) {
		a, b := net.Pipe()
		done := make(chan error, 1)
		go func() { done <- Serve(context.Background(), b, b, baseConfig()) }()
		hc := protocol.NewConn(bufio.NewReader(a), a, a)
		f, _ := protocol.NewFrame(1, protocol.TypeHello, protocol.Hello{Proto: "1.0", Nonce: nonce}, nil)
		_ = hc.Send(f)
		_, _ = hc.Recv()
		var bad [4]byte
		binary.BigEndian.PutUint32(bad[:], protocol.MaxHeaderBytes+1)
		_, _ = a.Write(bad[:])
		select {
		case err := <-done:
			if !errors.Is(err, protocol.ErrFrameTooLarge) {
				t.Errorf("%v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("the agent kept running after a violation")
		}
	})
}

// --- identity ------------------------------------------------------------------------------------------------------

func TestIdentityHelpers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(p, []byte("agent bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := FileSHA256(p); err != nil || len(got) != 64 {
		t.Errorf("%q %v", got, err)
	}
	a, _ := FileSHA256(p)
	b, _ := FileSHA256(p)
	if a != b {
		t.Error("deterministic")
	}
	if _, err := FileSHA256(filepath.Join(t.TempDir(), "gone")); err == nil {
		t.Error("missing file")
	}
	for goarch, want := range map[string]string{"amd64": "x86_64", "arm64": "aarch64"} {
		if got, err := GuestArch(goarch); err != nil || got != want {
			t.Errorf("%s: %q %v", goarch, got, err)
		}
	}
	if _, err := GuestArch("386"); err == nil {
		t.Error("386")
	}
	if d, err := ParseDisplay(""); err != nil || d != DefaultDisplay {
		t.Errorf("%+v %v", d, err)
	}
	if d, err := ParseDisplay(" 1920X1080 "); err != nil || d != (protocol.Display{W: 1920, H: 1080}) {
		t.Errorf("%+v %v", d, err)
	}
	for _, bad := range []string{"1280", "x", "1280x", "ax800", "100x100", "9000x800", "1280x800x2", "-1280x800"} {
		if _, err := ParseDisplay(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
