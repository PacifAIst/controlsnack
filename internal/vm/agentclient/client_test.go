// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agentclient

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/agent/server"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentbin"
)

var testAgent = func() agentbin.Agent {
	bin := []byte("this stands in for the agent binary")
	sum := sha256.Sum256(bin)
	return agentbin.Agent{Arch: protocol.ArchX86_64, Binary: bin, SHA256: hex.EncodeToString(sum[:])}
}()

// script says what the fake guest does with one connection.
type script struct {
	reply string // how the stub answers the push: ok (default), err, badsha, garbage, silent
	// raw, when set, takes over after the stub's OK: the test plays the agent itself.
	raw func(pc *protocol.Conn, push protocol.Push)
	// tweak changes the real agent's configuration before it serves.
	tweak func(*server.Config)
}

// guest is a fake VM: each dial gets a stub that checks the push like the real one and then runs the real agent
// dispatcher (or the script's raw peer) on the other end of a pipe.
type guest struct {
	t        *testing.T
	scripts  func(n int) script
	handlers map[string]server.Handler
	caps     []string

	dials  atomic.Int32
	pushes atomic.Int32
	mu     sync.Mutex
	conns  map[int]net.Conn // guest ends by connection number
}

func newGuest(t *testing.T, scripts func(n int) script) *guest {
	return &guest{t: t, scripts: scripts, conns: map[int]net.Conn{}}
}

func (g *guest) dial(ctx context.Context) (net.Conn, error) {
	n := int(g.dials.Add(1))
	host, guestEnd := net.Pipe()
	g.mu.Lock()
	g.conns[n] = guestEnd
	g.mu.Unlock()
	go g.stub(n, guestEnd)
	return host, nil
}

// kill drops connection n from the guest's side, as if the agent had died.
func (g *guest) kill(n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	_ = g.conns[n].Close()
}

func (g *guest) stub(n int, conn net.Conn) {
	defer conn.Close()
	s := script{}
	if g.scripts != nil {
		s = g.scripts(n)
	}
	br := bufio.NewReader(conn)
	line, err := protocol.ReadBootLine(br)
	if err != nil {
		return
	}
	push, err := protocol.ParsePush(line)
	if err != nil {
		_, _ = io.WriteString(conn, protocol.Reply{Reason: err.Error()}.Line())
		return
	}
	bin := make([]byte, push.Size)
	if _, err := io.ReadFull(br, bin); err != nil {
		return
	}
	g.pushes.Add(1)
	sum := sha256.Sum256(bin)
	got := hex.EncodeToString(sum[:])
	switch s.reply {
	case "err":
		_, _ = io.WriteString(conn, protocol.Reply{Reason: "no space left"}.Line())
		return
	case "badsha":
		_, _ = io.WriteString(conn, protocol.Reply{OK: true, SHA256: strings.Repeat("ab", 32)}.Line())
		return
	case "garbage":
		_, _ = io.WriteString(conn, "hello there\n")
		return
	case "silent":
		_, _ = io.Copy(io.Discard, br)
		return
	}
	if got != push.SHA256 {
		_, _ = io.WriteString(conn, protocol.Reply{Reason: "hash mismatch"}.Line())
		return
	}
	_, _ = io.WriteString(conn, protocol.Reply{OK: true, SHA256: got}.Line())
	if s.raw != nil {
		s.raw(protocol.NewConn(br, conn, conn), push)
		return
	}
	cfg := server.Config{Version: "v0.1.0", SelfSHA256: got, Arch: push.Arch, User: "cs", Nonce: push.Nonce,
		Display: protocol.Display{W: 1280, H: 800}, Handlers: g.handlers, Capabilities: g.caps}
	if s.tweak != nil {
		s.tweak(&cfg)
	}
	_ = server.Serve(context.Background(), struct {
		io.Reader
		io.Writer
	}{br, conn}, conn, cfg)
}

// --- helpers -------------------------------------------------------------------------------------------------------

type states struct {
	mu  sync.Mutex
	log []State
}

func (s *states) add(st State, _ error) {
	s.mu.Lock()
	s.log = append(s.log, st)
	s.mu.Unlock()
}

func (s *states) snapshot() []State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]State(nil), s.log...)
}

func (s *states) count(st State) int {
	n := 0
	for _, x := range s.snapshot() {
		if x == st {
			n++
		}
	}
	return n
}

func eventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func options(g *guest) Options {
	return Options{
		Dial: g.dial, Agent: testAgent, HostVersion: "v1", SessionID: "s_1", User: "cs", Required: []string{protocol.CapPing},
		Interval: 40 * time.Millisecond, Misses: 3, InstallTimeout: 2 * time.Second, HelloTimeout: 2 * time.Second,
		WriteTimeout: 300 * time.Millisecond, RetryMin: 5 * time.Millisecond, RetryMax: 20 * time.Millisecond,
	}
}

func connect(t *testing.T, g *guest, tweak func(*Options)) (*Client, *states) {
	t.Helper()
	opt := options(g)
	st := &states{}
	opt.OnState = st.add
	if tweak != nil {
		tweak(&opt)
	}
	c, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return c, st
}

func tryConnect(t *testing.T, g *guest, tweak func(*Options)) error {
	t.Helper()
	opt := options(g)
	if tweak != nil {
		tweak(&opt)
	}
	c, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.Connect(ctx)
}

// readHello reads the host's hello from a raw peer and returns the acknowledgement a correct agent would send.
func readHello(t *testing.T, pc *protocol.Conn, push protocol.Push) (protocol.Frame, protocol.HelloAck) {
	t.Helper()
	f, err := pc.Recv()
	if err != nil || f.Header.Type != protocol.TypeHello {
		t.Errorf("expected a hello: %+v %v", f.Header, err)
	}
	return f, protocol.HelloAck{Proto: protocol.ProtoVersion, AgentVersion: "v0.1.0", AgentSHA256: push.SHA256, Nonce: push.Nonce,
		Arch: push.Arch, User: "cs", Display: protocol.Display{W: 1280, H: 800}, Capabilities: []string{protocol.CapPing, protocol.CapExec}}
}

func sendAck(t *testing.T, pc *protocol.Conn, id uint64, ack protocol.HelloAck) {
	t.Helper()
	f, err := protocol.NewFrame(id, protocol.TypeHelloAck, ack, nil)
	if err != nil {
		t.Error(err)
		return
	}
	_ = pc.Send(f)
}

func echoHandlers() map[string]server.Handler {
	return map[string]server.Handler{protocol.TypeExec: func(ctx context.Context, req protocol.Frame) (any, []byte, error) {
		var r protocol.ExecReq
		if err := protocol.DecodeBody(req, &r); err != nil {
			return nil, nil, server.Fail(protocol.CodeInvalidArgs, "%v", err)
		}
		switch r.Command {
		case "fail":
			return nil, nil, server.Fail(protocol.CodeUnsupported, "not here")
		case "wait":
			<-ctx.Done()
			return nil, nil, nil
		}
		return protocol.ExecRes{Stdout: r.Command}, append([]byte("blob:"), r.Command...), nil
	}}
}

// --- tests ---------------------------------------------------------------------------------------------------------

func TestConnectAndCall(t *testing.T) {
	g := newGuest(t, nil)
	g.handlers, g.caps = echoHandlers(), []string{protocol.CapExec}
	c, st := connect(t, g, nil)

	ack, ok := c.Ack()
	if !ok || ack.AgentSHA256 != testAgent.SHA256 || ack.Arch != "x86_64" || ack.User != "cs" || ack.Display.W != 1280 {
		t.Errorf("%v %+v", ok, ack)
	}
	if !c.Up() {
		t.Error("not up")
	}
	var res protocol.ExecRes
	blob, err := c.Call(context.Background(), protocol.TypeExec, protocol.ExecReq{Command: "hello"}, nil, &res)
	if err != nil || res.Stdout != "hello" || string(blob) != "blob:hello" {
		t.Fatalf("%v %+v %q", err, res, blob)
	}
	// A call with no result wanted, and a plain ping.
	if _, err := c.Call(context.Background(), protocol.TypeExec, protocol.ExecReq{Command: "x"}, nil, nil); err != nil {
		t.Error(err)
	}
	if _, err := c.Call(context.Background(), protocol.TypePing, nil, nil, nil); err != nil {
		t.Error(err)
	}
	if got := st.snapshot(); len(got) != 2 || got[0] != StateConnecting || got[1] != StateUp {
		t.Errorf("states: %v", got)
	}
	if g.pushes.Load() != 1 {
		t.Errorf("pushes: %d", g.pushes.Load())
	}
}

func TestRemoteErrors(t *testing.T) {
	g := newGuest(t, nil)
	g.handlers, g.caps = echoHandlers(), []string{protocol.CapExec}
	c, _ := connect(t, g, nil)
	_, err := c.Call(context.Background(), protocol.TypeExec, protocol.ExecReq{Command: "fail"}, nil, nil)
	var re *RemoteError
	if !errors.As(err, &re) || re.Code != protocol.CodeUnsupported || re.Message != "not here" {
		t.Errorf("%v", err)
	}
	_, err = c.Call(context.Background(), protocol.TypeScreenshot, protocol.ScreenshotReq{}, nil, nil) // no handler
	if !errors.As(err, &re) || re.Code != protocol.CodeUnknownType {
		t.Errorf("%v", err)
	}
	if !c.Up() {
		t.Error("an error response must not drop the connection")
	}
}

func TestInstallRefusals(t *testing.T) {
	for _, mode := range []string{"err", "badsha", "garbage", "silent"} {
		t.Run(mode, func(t *testing.T) {
			g := newGuest(t, func(int) script { return script{reply: mode} })
			start := time.Now()
			err := tryConnect(t, g, func(o *Options) { o.InstallTimeout = 200 * time.Millisecond })
			var ie *InstallError
			switch mode {
			case "err":
				if !errors.As(err, &ie) || ie.Reason != "no space left" {
					t.Errorf("%v", err)
				}
			default:
				if !errors.Is(err, ErrInstall) {
					t.Errorf("%v", err)
				}
			}
			if time.Since(start) > 3*time.Second {
				t.Errorf("took %v", time.Since(start))
			}
		})
	}
}

func TestContextEndsAnInstall(t *testing.T) {
	g := newGuest(t, func(int) script { return script{reply: "silent"} })
	c, _ := New(options(g))
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	err := c.Connect(ctx)
	if !errors.Is(err, context.Canceled) || time.Since(start) > 2*time.Second {
		t.Errorf("%v after %v", err, time.Since(start))
	}
}

func TestHandshakeRefusals(t *testing.T) {
	tests := []struct {
		name string
		mut  func(a *protocol.HelloAck)
		want string
	}{
		{"wrong nonce", func(a *protocol.HelloAck) { a.Nonce = strings.Repeat("11", 16) }, "nonce"},
		{"different binary", func(a *protocol.HelloAck) { a.AgentSHA256 = strings.Repeat("22", 32) }, "different binary"},
		{"wrong user", func(a *protocol.HelloAck) { a.User = "root" }, "runs as"},
		{"wrong arch", func(a *protocol.HelloAck) { a.Arch = "aarch64" }, "architecture"},
		{"missing capability", func(a *protocol.HelloAck) { a.Capabilities = []string{protocol.CapExec} }, "capabilities"},
		{"other protocol", func(a *protocol.HelloAck) { a.Proto = "2.0" }, "protocol"},
		{"no display", func(a *protocol.HelloAck) { a.Display = protocol.Display{} }, "display"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newGuest(t, func(int) script {
				return script{raw: func(pc *protocol.Conn, push protocol.Push) {
					f, ack := readHello(t, pc, push)
					tt.mut(&ack)
					sendAck(t, pc, f.Header.ID, ack)
				}}
			})
			err := tryConnect(t, g, nil)
			if !errors.Is(err, protocol.ErrHandshake) || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%v", err)
			}
		})
	}

	t.Run("the agent refuses the hello", func(t *testing.T) {
		g := newGuest(t, func(int) script {
			return script{tweak: func(c *server.Config) { c.Nonce = strings.Repeat("ee", 16) }} // expects another nonce
		})
		err := tryConnect(t, g, nil)
		if !errors.Is(err, protocol.ErrHandshake) || !strings.Contains(err.Error(), "refused the hello") {
			t.Errorf("%v", err)
		}
	})
	t.Run("not an acknowledgement", func(t *testing.T) {
		g := newGuest(t, func(int) script {
			return script{raw: func(pc *protocol.Conn, push protocol.Push) {
				f, _ := readHello(t, pc, push)
				pong, _ := protocol.NewFrame(f.Header.ID, protocol.TypePong, nil, nil)
				_ = pc.Send(pong)
			}}
		})
		if err := tryConnect(t, g, nil); !errors.Is(err, protocol.ErrHandshake) {
			t.Errorf("%v", err)
		}
	})
	t.Run("a body with unknown fields", func(t *testing.T) {
		g := newGuest(t, func(int) script {
			return script{raw: func(pc *protocol.Conn, push protocol.Push) {
				f, _ := readHello(t, pc, push)
				odd, _ := protocol.NewFrame(f.Header.ID, protocol.TypeHelloAck, map[string]any{"proto": "1.0", "root": true}, nil)
				_ = pc.Send(odd)
			}}
		})
		if err := tryConnect(t, g, nil); !errors.Is(err, protocol.ErrHandshake) {
			t.Errorf("%v", err)
		}
	})
	t.Run("silence", func(t *testing.T) {
		g := newGuest(t, func(int) script {
			return script{raw: func(pc *protocol.Conn, push protocol.Push) { _, _ = pc.Recv(); time.Sleep(time.Second) }}
		})
		start := time.Now()
		err := tryConnect(t, g, func(o *Options) { o.HelloTimeout = 100 * time.Millisecond })
		if !errors.Is(err, protocol.ErrHandshake) || time.Since(start) > 2*time.Second {
			t.Errorf("%v after %v", err, time.Since(start))
		}
	})
}

func TestDialFailure(t *testing.T) {
	c, err := New(Options{Dial: func(context.Context) (net.Conn, error) { return nil, errors.New("no such socket") }, Agent: testAgent})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background()); err == nil || !strings.Contains(err.Error(), "no such socket") {
		t.Errorf("%v", err)
	}
	if c.Up() {
		t.Error("up")
	}
}

func TestOptionsAreChecked(t *testing.T) {
	if _, err := New(Options{Agent: testAgent}); err == nil {
		t.Error("no dialer")
	}
	if _, err := New(Options{Dial: func(context.Context) (net.Conn, error) { return nil, nil }}); err == nil {
		t.Error("no agent")
	}
}

func TestNotConnectedAndClosed(t *testing.T) {
	g := newGuest(t, nil)
	c, err := New(options(g))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call(context.Background(), protocol.TypePing, nil, nil, nil); !errors.Is(err, ErrAgentDown) {
		t.Errorf("%v", err)
	}
	select {
	case <-c.Down():
	default:
		t.Error("before connecting the client counts as down")
	}
	if _, ok := c.Ack(); ok {
		t.Error("ack")
	}
	c2, _ := connect(t, g, nil)
	_ = c2.Close()
	if _, err := c2.Call(context.Background(), protocol.TypePing, nil, nil, nil); !errors.Is(err, ErrClosed) {
		t.Errorf("%v", err)
	}
	if err := c2.Connect(context.Background()); !errors.Is(err, ErrClosed) {
		t.Errorf("a closed client does not reconnect: %v", err)
	}
	if c2.Up() {
		t.Error("up after Close")
	}
}

func TestCancelReachesTheAgent(t *testing.T) {
	g := newGuest(t, nil)
	g.handlers, g.caps = echoHandlers(), []string{protocol.CapExec}
	stopped := make(chan struct{})
	inner := g.handlers[protocol.TypeExec]
	g.handlers[protocol.TypeExec] = func(ctx context.Context, req protocol.Frame) (any, []byte, error) {
		res, blob, err := inner(ctx, req)
		if ctx.Err() != nil {
			close(stopped)
		}
		return res, blob, err
	}
	c, _ := connect(t, g, nil)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err := c.Call(ctx, protocol.TypeExec, protocol.ExecReq{Command: "wait"}, nil, nil)
	if !errors.Is(err, context.Canceled) || time.Since(start) > 2*time.Second {
		t.Errorf("%v after %v", err, time.Since(start))
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Error("the agent was not told to cancel")
	}
	if _, err := c.Call(context.Background(), protocol.TypePing, nil, nil, nil); err != nil {
		t.Errorf("the connection survives a canceled call: %v", err)
	}
}

func TestCallDeadline(t *testing.T) {
	g := newGuest(t, nil)
	g.handlers, g.caps = echoHandlers(), []string{protocol.CapExec}
	c, _ := connect(t, g, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, err := c.Call(ctx, protocol.TypeExec, protocol.ExecReq{Command: "wait"}, nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("%v", err)
	}
	if !c.Up() {
		t.Error("a slow call is not a dead agent")
	}
}

func TestConcurrentCalls(t *testing.T) {
	g := newGuest(t, nil)
	g.handlers, g.caps = echoHandlers(), []string{protocol.CapExec}
	c, _ := connect(t, g, nil)
	var wg sync.WaitGroup
	for i := range 60 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := fmt.Sprintf("job-%d", i)
			var res protocol.ExecRes
			blob, err := c.Call(context.Background(), protocol.TypeExec, protocol.ExecReq{Command: cmd}, nil, &res)
			if err != nil || res.Stdout != cmd || string(blob) != "blob:"+cmd {
				t.Errorf("%s: %v %+v %q", cmd, err, res, blob)
			}
		}()
	}
	wg.Wait()
}

// --- heartbeat and reconnection -------------------------------------------------------------------------------------

func TestHeartbeatDeclaresTheAgentDown(t *testing.T) {
	g := newGuest(t, func(int) script {
		return script{raw: func(pc *protocol.Conn, push protocol.Push) {
			f, ack := readHello(t, pc, push)
			sendAck(t, pc, f.Header.ID, ack)
			answered := 0
			for {
				req, err := pc.Recv()
				if err != nil {
					return
				}
				if req.Header.Type == protocol.TypePing && answered < 2 { // then it goes quiet
					answered++
					pong, _ := protocol.NewFrame(req.Header.ID, protocol.TypePong, nil, nil)
					_ = pc.Send(pong)
				}
			}
		}}
	})
	c, st := connect(t, g, nil)
	start := time.Now()
	select {
	case <-c.Down():
	case <-time.After(3 * time.Second):
		t.Fatal("never declared down")
	}
	if took := time.Since(start); took < 3*40*time.Millisecond {
		t.Errorf("declared down after %v, before three missed beats", took)
	}
	_, err := c.Call(context.Background(), protocol.TypePing, nil, nil, nil)
	if !errors.Is(err, ErrAgentDown) || !strings.Contains(err.Error(), "heartbeats") {
		t.Errorf("%v", err)
	}
	if c.Up() {
		t.Error("up")
	}
	_ = st
}

func TestSlowButAnsweringAgentStaysUp(t *testing.T) {
	g := newGuest(t, nil) // the real dispatcher answers pings inline, even while a request is running
	g.handlers, g.caps = echoHandlers(), []string{protocol.CapExec}
	c, _ := connect(t, g, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond) // ten heartbeat periods
	defer cancel()
	_, _ = c.Call(ctx, protocol.TypeExec, protocol.ExecReq{Command: "wait"}, nil, nil)
	if !c.Up() {
		t.Error("an agent busy with a long request is still up")
	}
}

func TestSuperviseReconnects(t *testing.T) {
	g := newGuest(t, func(n int) script {
		if n == 2 {
			return script{reply: "err"} // the first reinstall is refused: the client keeps trying
		}
		return script{}
	})
	g.handlers, g.caps = echoHandlers(), []string{protocol.CapExec}
	c, st := connect(t, g, nil)
	first := c.Down()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Supervise(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	g.kill(1) // the agent dies
	select {
	case <-first:
	case <-time.After(3 * time.Second):
		t.Fatal("the loss was not noticed")
	}
	eventually(t, 5*time.Second, "the agent to be back", func() bool { return st.count(StateUp) == 2 && c.Up() })
	if g.pushes.Load() < 3 {
		t.Errorf("pushes: %d (the refused one counts)", g.pushes.Load())
	}
	var res protocol.ExecRes
	if _, err := c.Call(context.Background(), protocol.TypeExec, protocol.ExecReq{Command: "again"}, nil, &res); err != nil || res.Stdout != "again" {
		t.Errorf("%v %+v", err, res)
	}
	if st.count(StateDown) < 2 { // the loss, and the refused attempt
		t.Errorf("states: %v", st.snapshot())
	}
	select {
	case <-first:
	default:
		t.Error("the old connection's Down channel stays closed")
	}
	if c.Down() == first {
		t.Error("Down now refers to the new connection")
	}
}

func TestSuperviseStopsWhenClosed(t *testing.T) {
	g := newGuest(t, nil)
	c, _ := connect(t, g, nil)
	done := make(chan struct{})
	go func() { c.Supervise(context.Background()); close(done) }()
	_ = c.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Supervise kept running after Close")
	}
}

// --- a hostile agent --------------------------------------------------------------------------------------------------

// hostile runs a raw peer that answers the hello correctly and then calls act for the first request.
func hostile(t *testing.T, act func(pc *protocol.Conn, req protocol.Frame)) *guest {
	return newGuest(t, func(int) script {
		return script{raw: func(pc *protocol.Conn, push protocol.Push) {
			f, ack := readHello(t, pc, push)
			sendAck(t, pc, f.Header.ID, ack)
			for {
				req, err := pc.Recv()
				if err != nil {
					return
				}
				if req.Header.Type == protocol.TypePing {
					pong, _ := protocol.NewFrame(req.Header.ID, protocol.TypePong, nil, nil)
					_ = pc.Send(pong)
					continue
				}
				act(pc, req)
			}
		}}
	})
}

func TestHostileResponses(t *testing.T) {
	tests := []struct {
		name string
		act  func(pc *protocol.Conn, req protocol.Frame)
	}{
		{"the wrong type", func(pc *protocol.Conn, req protocol.Frame) {
			f, _ := protocol.NewFrame(req.Header.ID, "screenshot.res", nil, nil)
			_ = pc.Send(f)
		}},
		{"unknown fields in the body", func(pc *protocol.Conn, req protocol.Frame) {
			f, _ := protocol.NewFrame(req.Header.ID, "exec.res", map[string]any{"exit_code": 0, "root_shell": "yes"}, nil)
			_ = pc.Send(f)
		}},
		{"an error with a bad body", func(pc *protocol.Conn, req protocol.Frame) {
			f, _ := protocol.NewFrame(req.Header.ID, protocol.TypeError, map[string]any{"code": 5}, nil)
			_ = pc.Send(f)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := connect(t, hostile(t, tt.act), nil)
			var res protocol.ExecRes
			_, err := c.Call(context.Background(), protocol.TypeExec, protocol.ExecReq{Command: "x"}, nil, &res)
			if !errors.Is(err, ErrBadResponse) {
				t.Errorf("%v", err)
			}
			select {
			case <-c.Down():
			case <-time.After(2 * time.Second):
				t.Error("an agent that breaks the protocol is dropped")
			}
		})
	}

	t.Run("an oversize header ends the connection", func(t *testing.T) {
		var g *guest
		g = newGuest(t, func(int) script {
			return script{raw: func(pc *protocol.Conn, push protocol.Push) {
				f, ack := readHello(t, pc, push)
				sendAck(t, pc, f.Header.ID, ack)
				_, _ = pc.Recv()
				var n [4]byte
				binary.BigEndian.PutUint32(n[:], protocol.MaxHeaderBytes+1)
				g.mu.Lock()
				conn := g.conns[1]
				g.mu.Unlock()
				_, _ = conn.Write(n[:])
			}}
		})
		c, _ := connect(t, g, nil)
		_, err := c.Call(context.Background(), protocol.TypeExec, protocol.ExecReq{Command: "x"}, nil, nil)
		if !errors.Is(err, ErrAgentDown) {
			t.Errorf("%v", err)
		}
	})
}

func TestStrayAndEarlyFramesAreIgnored(t *testing.T) {
	events := make(chan protocol.EventBody, 4)
	g := hostile(t, func(pc *protocol.Conn, req protocol.Frame) {
		stray, _ := protocol.NewFrame(9999, "exec.res", protocol.ExecRes{Stdout: "nobody asked"}, nil) // no such request
		_ = pc.Send(stray)
		ev, _ := protocol.NewFrame(0, protocol.TypeEvent, protocol.EventBody{Name: "display_changed", Data: "1920x1080"}, nil)
		_ = pc.Send(ev)
		bad, _ := protocol.NewFrame(0, protocol.TypeEvent, map[string]any{"name": "x", "evil": 1}, nil)
		_ = pc.Send(bad)
		ok, _ := protocol.NewFrame(req.Header.ID, "exec.res", protocol.ExecRes{Stdout: "real"}, []byte("b"))
		_ = pc.Send(ok)
	})
	c, _ := connect(t, g, func(o *Options) { o.OnEvent = func(e protocol.EventBody) { events <- e } })
	var res protocol.ExecRes
	blob, err := c.Call(context.Background(), protocol.TypeExec, protocol.ExecReq{Command: "x"}, nil, &res)
	if err != nil || res.Stdout != "real" || string(blob) != "b" {
		t.Fatalf("%v %+v %q", err, res, blob)
	}
	select {
	case e := <-events:
		if e.Name != "display_changed" || e.Data != "1920x1080" {
			t.Errorf("%+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Error("no event")
	}
	select {
	case e := <-events:
		t.Errorf("a malformed event was delivered: %+v", e)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAnAgentThatStopsReadingCannotBlockTheHost(t *testing.T) {
	g := newGuest(t, func(int) script {
		return script{raw: func(pc *protocol.Conn, push protocol.Push) {
			f, ack := readHello(t, pc, push)
			sendAck(t, pc, f.Header.ID, ack)
			time.Sleep(5 * time.Second) // alive but never reads again
		}}
	})
	c, _ := connect(t, g, func(o *Options) { o.Interval = time.Hour }) // keep the heartbeat out of it
	start := time.Now()
	_, err := c.Call(context.Background(), protocol.TypeExec, protocol.ExecReq{Command: "big"}, make([]byte, 1<<20), nil)
	if !errors.Is(err, ErrAgentDown) {
		t.Errorf("%v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("the write was not bounded: %v", time.Since(start))
	}
}

func TestOversizedRequestsAreRefusedLocally(t *testing.T) {
	g := newGuest(t, nil)
	g.handlers, g.caps = echoHandlers(), []string{protocol.CapExec}
	c, _ := connect(t, g, nil)
	_, err := c.Call(context.Background(), protocol.TypeExec, protocol.ExecReq{Command: "x"}, make([]byte, protocol.MaxBlobBytes+1), nil)
	if err == nil {
		t.Fatal("accepted")
	}
	if !c.Up() {
		t.Error("a request we refuse to build must not cost the connection")
	}
}
