// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	// exec answers the agent's exec requests with a command's exit status, or an error; nil leaves the agent without exec.
	exec func(command string) (int, error)

	mu       sync.Mutex
	commands []string
}

// asked is the commands the agent was asked to run.
func (g *guestStub) asked() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.commands...)
}

func (g *guestStub) handleExec(_ context.Context, f protocol.Frame) (any, []byte, error) {
	var req protocol.ExecReq
	if err := json.Unmarshal(f.Header.Body, &req); err != nil {
		return nil, nil, err
	}
	g.mu.Lock()
	g.commands = append(g.commands, req.Command)
	g.mu.Unlock()
	code, err := g.exec(req.Command)
	if err != nil {
		return nil, nil, err
	}
	return protocol.ExecRes{ExitCode: code}, nil, nil
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
	cfg := server.Config{Version: "v0.1.0", SelfSHA256: push.SHA256, Arch: push.Arch, User: "cs", Nonce: push.Nonce,
		Display: protocol.Display{W: 1280, H: 800}}
	if g.exec != nil {
		cfg.Handlers = map[string]server.Handler{protocol.TypeExec: g.handleExec}
	}
	_ = server.Serve(context.Background(), struct {
		io.Reader
		io.Writer
	}{br, conn}, conn, cfg)
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

func TestShutdownAsksTheGuestThroughTheAgentAndWaitsForItToGo(t *testing.T) {
	quickGrace(t)
	b := newBootRig(t, map[string]scenario{"kvm": {}})
	b.stub.exec = func(command string) (int, error) {
		if strings.Contains(command, "poweroff") {
			b.procs[0].exit(0) // the guest goes
		}
		return 0, nil
	}
	m, err := Boot(context.Background(), b.options("kvm"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	// The disk is written, then the system goes down without waiting for the job (the answer must come before the connection goes).
	if cmds := b.stub.asked(); len(cmds) != 1 || !strings.HasPrefix(cmds[0], "sync;") || !strings.Contains(cmds[0], "systemctl --no-block poweroff") {
		t.Errorf("the agent was asked %q", cmds)
	}
	if b.qmps[0].pressed.Load() {
		t.Error("the button is for a guest that cannot be asked")
	}
	select {
	case <-b.procs[0].Exited():
	default:
		t.Error("Shutdown returned before QEMU had exited")
	}
	if b.procs[0].killed.Load() {
		t.Error("a guest that powers off by itself is not killed")
	}
	if m.Client.Up() {
		t.Error("the agent connection is still there after the guest was asked to go")
	}
	if err := m.Close(context.Background()); err != nil {
		t.Errorf("Close after Shutdown: %v", err)
	}
}

func TestShutdownPressesThePowerButtonWhenTheAgentCannotBeAsked(t *testing.T) {
	quickGrace(t)
	for name, c := range map[string]struct {
		exec      func(string) (int, error)
		stopAgent bool
	}{
		"the agent is gone":         {nil, true},
		"the agent has no exec":     {nil, false},
		"the command fails":         {func(string) (int, error) { return 3, nil }, false},
		"the command cannot be run": {func(string) (int, error) { return 0, errors.New("cannot start a shell") }, false},
	} {
		t.Run(name, func(t *testing.T) {
			b := newBootRig(t, map[string]scenario{"kvm": {}})
			b.stub.exec = c.exec
			m, err := Boot(context.Background(), b.options("kvm"))
			if err != nil {
				t.Fatal(err)
			}
			if c.stopAgent {
				m.StopAgent()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := m.Shutdown(ctx); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
			if !b.qmps[0].pressed.Load() {
				t.Error("the power button was not pressed")
			}
			if err := m.Close(context.Background()); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
	}
}

func TestShutdownGivesUpWhenTheGuestDoesNotGoAndSaysSo(t *testing.T) {
	quickGrace(t)
	b := newBootRig(t, map[string]scenario{"kvm": {}})
	b.stub.exec = func(string) (int, error) { return 0, nil } // the agent is asked and the guest never goes
	m, err := Boot(context.Background(), b.options("kvm"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	began := time.Now()
	if err := m.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown: %v", err)
	}
	if took := time.Since(began); took < 250*time.Millisecond || took > 3*time.Second {
		t.Errorf("it waited %s", took)
	}
	select {
	case <-b.procs[0].Exited():
		t.Error("QEMU was ended by Shutdown; ending it is Close's")
	default:
	}
	if err := m.Close(context.Background()); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestShutdownReportsAButtonThatCannotBePressed(t *testing.T) {
	quickGrace(t)
	b := newBootRig(t, map[string]scenario{"kvm": {}})
	m, err := Boot(context.Background(), b.options("kvm"))
	if err != nil {
		t.Fatal(err)
	}
	m.StopAgent()
	b.qmps[0].powerdown = func() error { return errors.New("QMP: no such command") }
	if err := m.Shutdown(context.Background()); err == nil || !strings.Contains(err.Error(), "no such command") {
		t.Errorf("Shutdown: %v", err)
	}
	_ = m.Close(context.Background())
}

// quickGrace shortens how long Shutdown waits to see whether a guest is going down by itself.
func quickGrace(t *testing.T) {
	t.Helper()
	old := shutdownGrace
	shutdownGrace = 20 * time.Millisecond
	t.Cleanup(func() { shutdownGrace = old })
}

func TestShutdownTakesAGuestThatWentDownTooFastToAnswerAsGone(t *testing.T) {
	quickGrace(t)
	b := newBootRig(t, map[string]scenario{"kvm": {}})
	b.stub.exec = func(string) (int, error) {
		b.procs[0].exit(0)                      // the guest is gone before it can say so
		return 0, errors.New("connection lost") // and what it was going to say is lost
	}
	m, err := Boot(context.Background(), b.options("kvm"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if b.qmps[0].pressed.Load() {
		t.Error("the button was pressed on a guest that was gone")
	}
	_ = m.Close(context.Background())
}

func TestShutdownTakesAMonitorThatWentWithTheGuestAsAGuestThatWent(t *testing.T) {
	quickGrace(t)
	b := newBootRig(t, map[string]scenario{"kvm": {}})
	m, err := Boot(context.Background(), b.options("kvm"))
	if err != nil {
		t.Fatal(err)
	}
	m.StopAgent()
	b.qmps[0].powerdown = func() error {
		b.procs[0].exit(0)
		return errors.New("QMP connection lost: EOF")
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	_ = m.Close(context.Background())
}
