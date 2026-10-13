// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentclient"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
)

var bootAgent = func() agentbin.Agent {
	bin := []byte("a stand-in for the guest agent")
	sum := sha256.Sum256(bin)
	return agentbin.Agent{Arch: protocol.ArchX86_64, Binary: bin, SHA256: hex.EncodeToString(sum[:])}
}()

// guestStub plays the bootstrap stub and the agent on the guest's end of the agent port: it reads the push, answers
// like the real stub and then serves the real dispatcher. refuse makes it answer ERR instead.
type guestStub struct {
	refuse  bool
	pushes  atomic.Int32
	serving atomic.Int32
}

func (g *guestStub) serve(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	line, err := protocol.ReadBootLine(br)
	if err != nil {
		return
	}
	push, err := protocol.ParsePush(line)
	if err != nil {
		return
	}
	if _, err := io.CopyN(io.Discard, br, push.Size); err != nil {
		return
	}
	g.pushes.Add(1)
	if g.refuse {
		_, _ = io.WriteString(conn, protocol.Reply{Reason: "no space left"}.Line())
		return
	}
	_, _ = io.WriteString(conn, protocol.Reply{OK: true, SHA256: push.SHA256}.Line())
	g.serving.Add(1)
	defer g.serving.Add(-1)
	_ = server.Serve(context.Background(), struct {
		io.Reader
		io.Writer
	}{br, conn}, conn, server.Config{Version: "v0.1.0", SelfSHA256: push.SHA256, Arch: push.Arch, User: "cs", Nonce: push.Nonce,
		Display: protocol.Display{W: 1280, H: 800}})
}

// bootRig is a launch rig whose agent port is served by a guestStub.
type bootRig struct {
	*rig
	stub *guestStub
	// agentConns are the guest ends handed out, in order, so a test can cut one.
	mu         sync.Mutex
	agentConns []net.Conn
	progress   []string
	states     []agentclient.State
}

func newBootRig(t *testing.T, sc map[string]scenario) *bootRig {
	b := &bootRig{rig: newRig(t, sc), stub: &guestStub{}}
	b.onStream = func(ep qemu.Endpoint, guestEnd net.Conn) {
		if !strings.HasSuffix(ep.Path, "agent0.sock") {
			_ = guestEnd.Close()
			return
		}
		b.mu.Lock()
		b.agentConns = append(b.agentConns, guestEnd)
		b.mu.Unlock()
		b.stub.serve(guestEnd)
	}
	return b
}

func (b *bootRig) options(accel string) BootOptions {
	return BootOptions{
		Prepare: b.prepare(false), Resolve: resolveTo(accel, "auto"), Backend: b.backend(),
		Agent: bootAgent, HostVersion: "v1", SessionID: "s_1",
		Window: 400 * time.Millisecond, Stable: 40 * time.Millisecond, PortWindow: 2 * time.Second, ConnectWindow: 5 * time.Second,
		OnAgentState: func(s agentclient.State, _ error) {
			b.mu.Lock()
			b.states = append(b.states, s)
			b.mu.Unlock()
		},
		Progress: func(format string, args ...any) {
			b.mu.Lock()
			b.progress = append(b.progress, format)
			b.mu.Unlock()
		},
	}
}

func (b *bootRig) cut(n int) {
	b.mu.Lock()
	c := b.agentConns[n]
	b.mu.Unlock()
	_ = c.Close()
}

func (b *bootRig) streamCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.agentConns)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBootConnectsTheAgent(t *testing.T) {
	b := newBootRig(t, map[string]scenario{"kvm": {}})
	m, err := Boot(context.Background(), b.options("kvm"))
	if err != nil {
		t.Fatal(err)
	}
	if !m.Client.Up() || m.Ack.AgentSHA256 != bootAgent.SHA256 || m.Ack.User != "cs" || m.Ack.Display.W != 1280 {
		t.Errorf("%v %+v", m.Client.Up(), m.Ack)
	}
	if m.Timings.Launch <= 0 || m.Timings.PortOpen < m.Timings.Launch || m.Timings.Connected < m.Timings.PortOpen {
		t.Errorf("timings must grow stage by stage: %+v", m.Timings)
	}
	if _, err := m.Client.Call(context.Background(), protocol.TypePing, nil, nil, nil); err != nil {
		t.Errorf("ping: %v", err)
	}
	if m.Accel.Chosen != "kvm" || b.stub.pushes.Load() != 1 {
		t.Errorf("%+v pushes %d", m.Accel, b.stub.pushes.Load())
	}
	if err := m.Close(context.Background()); err != nil {
		t.Errorf("close: %v", err)
	}
	if !b.procs[0].stopped.Load() || m.Client.Up() {
		t.Error("closing stops the VM politely and the agent connection")
	}
	if err := m.Kill(); err != nil || m.Close(context.Background()) != nil {
		t.Errorf("a second ending is a no-op: %v", err)
	}
}

func TestBootScalesItsWaitsUnderTCG(t *testing.T) {
	b := newBootRig(t, map[string]scenario{"tcg": {portWait: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}})
	o := b.options("tcg")
	o.PortWindow = 50 * time.Millisecond
	start := time.Now()
	_, err := Boot(context.Background(), o)
	took := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "did not open the agent port") {
		t.Fatalf("%v", err)
	}
	if took < 190*time.Millisecond { // 50 ms times the factor of four
		t.Errorf("gave up after %v: the window must be multiplied under tcg", took)
	}
	if !b.procs[0].killed.Load() {
		t.Error("a boot that fails leaves no VM running")
	}
}

func TestBootStopsWhenQEMUDiesWhileWaiting(t *testing.T) {
	var b *bootRig
	b = newBootRig(t, map[string]scenario{"kvm": {out: "qemu: fatal: guest panicked", portWait: func(ctx context.Context) error {
		b.mu.Lock()
		proc := b.procs[0]
		b.mu.Unlock()
		go func() { time.Sleep(60 * time.Millisecond); proc.exit(1) }()
		<-ctx.Done()
		return ctx.Err()
	}}})
	o := b.options("kvm")
	o.PortWindow = 30 * time.Second
	start := time.Now()
	_, err := Boot(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "QEMU exited with status 1") || !strings.Contains(err.Error(), "guest panicked") {
		t.Fatalf("%v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("waited %v for a VM that was already gone", time.Since(start))
	}
}

func TestBootRefusedAgent(t *testing.T) {
	b := newBootRig(t, map[string]scenario{"kvm": {}})
	b.stub.refuse = true
	_, err := Boot(context.Background(), b.options("kvm"))
	var ie *agentclient.InstallError
	if !errors.As(err, &ie) || ie.Reason != "no space left" || !strings.Contains(err.Error(), "could not be started") {
		t.Fatalf("%v", err)
	}
	if !b.procs[0].killed.Load() {
		t.Error("a boot that fails leaves no VM running")
	}
}

func TestBootCanceled(t *testing.T) {
	b := newBootRig(t, map[string]scenario{"kvm": {portWait: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(60*time.Millisecond, cancel)
	_, err := Boot(ctx, b.options("kvm"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
	if !b.procs[0].killed.Load() {
		t.Error("a canceled boot kills the VM")
	}
}

func TestBootReconnectsThroughTheChardev(t *testing.T) {
	b := newBootRig(t, map[string]scenario{"kvm": {}})
	o := b.options("kvm")
	m, err := Boot(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Kill() }()
	b.cut(0) // the agent dies; the stub is still there
	waitFor(t, "the second connection", func() bool { return b.streamCount() == 2 })
	waitFor(t, "the agent to be back", func() bool { return m.Client.Up() && b.stub.pushes.Load() == 2 })
	if _, err := m.Client.Call(context.Background(), protocol.TypePing, nil, nil, nil); err != nil {
		t.Errorf("%v", err)
	}
	b.mu.Lock()
	got := append([]agentclient.State(nil), b.states...)
	b.mu.Unlock()
	if len(got) < 4 || got[0] != agentclient.StateConnecting || got[1] != agentclient.StateUp {
		t.Errorf("states: %v", got)
	}
}

func TestBootKillIsQuickAndFinal(t *testing.T) {
	b := newBootRig(t, map[string]scenario{"kvm": {}})
	m, err := Boot(context.Background(), b.options("kvm"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := m.Kill(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("the kill path took %v", time.Since(start))
	}
	if !b.procs[0].killed.Load() || b.procs[0].stopped.Load() {
		t.Error("kill kills; it does not ask QEMU to quit")
	}
	if _, err := m.Client.Call(context.Background(), protocol.TypePing, nil, nil, nil); err == nil {
		t.Error("the agent connection is gone")
	}
}

func TestSuperviseStopsWhenQEMUExits(t *testing.T) {
	b := newBootRig(t, map[string]scenario{"kvm": {}})
	m, err := Boot(context.Background(), b.options("kvm"))
	if err != nil {
		t.Fatal(err)
	}
	b.procs[0].exit(0) // QEMU is gone
	select {
	case <-m.superDone:
	case <-time.After(3 * time.Second):
		t.Fatal("the agent supervisor kept trying to reach a dead VM")
	}
}

func TestBootNeedsItsParts(t *testing.T) {
	b := newBootRig(t, map[string]scenario{"kvm": {}})
	o := b.options("kvm")
	o.Prepare = nil
	if _, err := Boot(context.Background(), o); err == nil {
		t.Error("accepted options without Prepare")
	}
}
