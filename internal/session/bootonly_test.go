// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/internal/platform"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/safety/audit"
	"github.com/PacifAIst/controlsnack-local/internal/safety/killswitch"
	"github.com/PacifAIst/controlsnack-local/internal/vm"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentbin"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentclient"
	vmimage "github.com/PacifAIst/controlsnack-local/internal/vm/image"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
)

type fakeProc struct {
	exited  chan struct{}
	once    sync.Once
	code    int
	killed  atomic.Bool
	killErr error
}

func newProc() *fakeProc { return &fakeProc{exited: make(chan struct{})} }

func (p *fakeProc) exit(code int) { p.once.Do(func() { p.code = code; close(p.exited) }) }
func (p *fakeProc) Pid() int      { return 31337 }
func (p *fakeProc) Kill() error   { p.killed.Store(true); p.exit(137); return p.killErr }
func (p *fakeProc) Exited() <-chan struct{} {
	return p.exited
}
func (p *fakeProc) Result() (int, error) { return p.code, nil }
func (p *fakeProc) Output() string       { return "" }
func (p *fakeProc) Stop(context.Context, func(context.Context) error, time.Duration) error {
	p.exit(0)
	return nil
}

type fakeMachine struct {
	proc        *fakeProc
	fallback    *vm.Fallback
	closedAt    atomic.Int64
	shotErr     error
	agentStoped atomic.Bool
	// for the full session
	agent   AgentLink
	monitor Monitor
	closed  atomic.Bool
	netConn net.Conn
	redial  func(context.Context) (net.Conn, error)
}

func (m *fakeMachine) Proc() vm.Process { return m.proc }
func (m *fakeMachine) Report() vm.BootReport {
	return vm.BootReport{
		Accel: vm.AccelResult{Requested: "auto", Chosen: "tcg", Slow: true},
		Agent: protocol.HelloAck{AgentVersion: "v0.1.0", AgentSHA256: strings.Repeat("ab", 32), User: "cs", Display: protocol.Display{W: 1280, H: 800},
			Capabilities: []string{protocol.CapScreenshot, protocol.CapInput, protocol.CapExec, protocol.CapA11y, protocol.CapOCR, protocol.CapFilePeek}},
		Timings: vm.Timings{Launch: 12 * time.Millisecond, PortOpen: 40 * time.Second, Connected: 42 * time.Second},
		QEMUPID: 31337, Fallback: m.fallback,
	}
}
func (m *fakeMachine) Screendump(_ context.Context, path string) error {
	if m.shotErr != nil {
		return m.shotErr
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 64, 40))); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}
func (m *fakeMachine) StopAgent()       { m.agentStoped.Store(true) }
func (m *fakeMachine) Agent() AgentLink { return m.agent }
func (m *fakeMachine) Monitor() Monitor { return m.monitor }
func (m *fakeMachine) Close(context.Context) error {
	m.closed.Store(true)
	m.closedAt.Store(time.Now().UnixNano())
	m.proc.exit(0)
	return nil
}
func (m *fakeMachine) NetPort() (net.Conn, func(context.Context) (net.Conn, error)) {
	return m.netConn, m.redial
}

// rig is a runner with every outside connection faked and its directories in a temp dir.
type rig struct {
	t       *testing.T
	r       *runner
	out     *syncBuf
	dirs    platform.Dirs
	mach    *fakeMachine
	mu      sync.Mutex
	options []vm.BootOptions
	bootErr error
	// hold, when set, makes the fake boot wait for it (or the context) before it returns.
	hold chan struct{}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newRig(t *testing.T) *rig {
	root := t.TempDir()
	rg := &rig{t: t, out: &syncBuf{}, dirs: platform.Dirs{Config: filepath.Join(root, "config"), Cache: filepath.Join(root, "cache"), State: filepath.Join(root, "state")},
		mach: &fakeMachine{proc: newProc()}}
	rg.r = &runner{out: rg.out, deps: deps{
		dirs: func() (platform.Dirs, error) { return rg.dirs, nil },
		discover: func(context.Context, string) (qemu.Install, error) {
			return qemu.Install{Arch: qemu.ArchX86_64, System: "/usr/bin/qemu-system-x86_64", Img: "/usr/bin/qemu-img"}, nil
		},
		image: func(platform.Dirs, string, string) (vmimage.Image, error) {
			return vmimage.Image{Path: filepath.Join(root, "base.qcow2"), Manifest: vmimage.Manifest{ID: "abcdef123456"}}, nil
		},
		agent: func(arch string) (agentbin.Agent, error) {
			return agentbin.Agent{Arch: arch, Binary: []byte("x"), SHA256: strings.Repeat("cd", 32)}, nil
		},
		boot: func(ctx context.Context, o vm.BootOptions) (machine, error) {
			rg.mu.Lock()
			rg.options = append(rg.options, o)
			rg.mu.Unlock()
			if o.Events != nil {
				o.Events.AccelSelected(vm.AccelResult{Requested: "auto", Chosen: "tcg", Slow: true})
			}
			if o.Progress != nil {
				o.Progress("QEMU is running")
			}
			if rg.hold != nil {
				select {
				case <-rg.hold:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			if rg.bootErr != nil {
				return nil, rg.bootErr
			}
			return rg.mach, nil
		},
		procs: platform.OSProcesses{}, pid: os.Getpid, random: nil, library: testLibrary,
	}}
	rg.r.deps.random = &countingRand{}
	return rg
}

// countingRand is a deterministic source of bytes.
type countingRand struct{ n byte }

func (c *countingRand) Read(p []byte) (int, error) {
	for i := range p {
		c.n++
		p[i] = c.n
	}
	return len(p), nil
}

func bootOnlyConfig() config.Config {
	cfg := config.Defaults()
	cfg.Session.NoLLM = true
	cfg.Viewer.Mode = "none"
	return cfg
}

func (rg *rig) run(ctx context.Context, cfg config.Config) error { return rg.r.Run(ctx, cfg) }

func (rg *rig) auditLog() (*audit.Report, []string) {
	rg.t.Helper()
	entries, err := os.ReadDir(rg.dirs.AuditDir())
	if err != nil || len(entries) != 1 {
		rg.t.Fatalf("expected one audit log: %v %v", entries, err)
	}
	path := filepath.Join(rg.dirs.AuditDir(), entries[0].Name())
	rep, err := audit.Verify(path)
	if err != nil {
		rg.t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	return rep, strings.Split(strings.TrimSpace(string(data)), "\n")
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// --- the tests -------------------------------------------------------------------------------------------------------

func TestRunRefusesTheCombinationsThatMakeNoSense(t *testing.T) {
	rg := newRig(t)
	for name, mut := range map[string]func(*config.Config){
		"a model with no viewer and no task":  func(c *config.Config) { c.Session.NoLLM = false },
		"no model, but a viewer to look into": func(c *config.Config) { c.Viewer.Mode = "window" },
	} {
		cfg := bootOnlyConfig()
		mut(&cfg)
		if err := rg.run(context.Background(), cfg); exitcode.Of(err) != exitcode.Usage {
			t.Errorf("%s: %v", name, err)
		}
	}
	if rg.boots() != 0 {
		t.Error("nothing is booted for a combination that makes no sense")
	}
}

func TestInterruptedSessionIsKilledAndRecorded(t *testing.T) {
	rg := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rg.run(ctx, bootOnlyConfig()) }()
	eventually(t, "the session to be running", func() bool { return strings.Contains(rg.out.String(), "Running.") })
	cancel() // Ctrl+C
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not end")
	}
	if exitcode.Of(err) != exitcode.Killed {
		t.Errorf("an interrupt is exit code 5: %v (%d)", err, exitcode.Of(err))
	}
	out := rg.out.String()
	for _, want := range []string{"Starting a session (s_", "accelerator: tcg", "WARNING: no hardware acceleration", "Ready: agent v0.1.0",
		"running as cs, screen 1280x800", "Boot with tcg:", "Screendump works: 64x40 PNG", "Running. Press Ctrl+C", "ms after the kill (signal)"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	if !rg.mach.proc.killed.Load() || !rg.mach.agentStoped.Load() {
		t.Error("QEMU is killed and the agent connection stopped")
	}
	if _, serr := os.Stat(filepath.Join(rg.dirs.State, "run")); serr == nil {
		entries, _ := os.ReadDir(filepath.Join(rg.dirs.State, "run"))
		if len(entries) != 0 {
			t.Errorf("the session directory is removed: %v", entries)
		}
	}
	if _, serr := os.Stat(filepath.Join(rg.dirs.SessionsDir(), "active.json")); !os.IsNotExist(serr) {
		t.Errorf("the session lock is released: %v", serr)
	}

	rep, lines := rg.auditLog()
	if len(rep.Problems) != 0 || !rep.Complete {
		t.Errorf("the audit log is intact and complete: %+v", rep)
	}
	joined := strings.Join(lines, "\n")
	for _, ev := range []string{`"event":"start"`, `"event":"accel_selected"`, `"event":"ready"`, `"event":"kill"`, `"event":"end"`, `"source":"signal"`} {
		if !strings.Contains(joined, ev) {
			t.Errorf("the audit log lacks %s:\n%s", ev, joined)
		}
	}
}

func TestSessionTimeout(t *testing.T) {
	rg := newRig(t)
	cfg := bootOnlyConfig()
	cfg.Safety.Timeout = config.Duration(60 * time.Millisecond)
	err := rg.run(context.Background(), cfg)
	if exitcode.Of(err) != exitcode.Timeout || !strings.Contains(err.Error(), "session timeout") {
		t.Errorf("%v (%d)", err, exitcode.Of(err))
	}
	if !rg.mach.proc.killed.Load() {
		t.Error("a timed-out session is killed")
	}
	if rep, _ := rg.auditLog(); !rep.Complete {
		t.Errorf("%+v", rep)
	}
}

func TestGuestShutsDownByItself(t *testing.T) {
	rg := newRig(t)
	done := make(chan error, 1)
	go func() { done <- rg.run(context.Background(), bootOnlyConfig()) }()
	eventually(t, "the session to be running", func() bool { return strings.Contains(rg.out.String(), "Running.") })
	rg.mach.proc.exit(0)
	if err := <-done; err != nil {
		t.Errorf("a guest that powers off is a normal end: %v", err)
	}
	if !strings.Contains(rg.out.String(), "The guest shut down.") || rg.mach.proc.killed.Load() {
		t.Errorf("%s", rg.out.String())
	}
	if rep, _ := rg.auditLog(); !rep.Complete || len(rep.Problems) != 0 {
		t.Errorf("%+v", rep)
	}
}

func TestQEMUCrash(t *testing.T) {
	rg := newRig(t)
	done := make(chan error, 1)
	go func() { done <- rg.run(context.Background(), bootOnlyConfig()) }()
	eventually(t, "the session to be running", func() bool { return strings.Contains(rg.out.String(), "Running.") })
	rg.mach.proc.exit(1)
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "status 1") || exitcode.Of(err) != exitcode.Failure {
		t.Errorf("%v", err)
	}
}

func TestKeepLeavesTheSessionFiles(t *testing.T) {
	rg := newRig(t)
	cfg := bootOnlyConfig()
	cfg.Session.Keep = true
	cfg.Safety.Timeout = config.Duration(40 * time.Millisecond)
	_ = rg.run(context.Background(), cfg)
	matches, _ := filepath.Glob(filepath.Join(rg.dirs.State, "run", "s_*", "ready.png"))
	if len(matches) != 1 {
		t.Errorf("--keep keeps the screendump: %v", matches)
	}
}

func TestScreendumpFailureIsAWarning(t *testing.T) {
	rg := newRig(t)
	rg.mach.shotErr = errors.New("QMP: no such device")
	cfg := bootOnlyConfig()
	cfg.Safety.Timeout = config.Duration(40 * time.Millisecond)
	_ = rg.run(context.Background(), cfg)
	if !strings.Contains(rg.out.String(), "Warning: the screen could not be read through QMP: QMP: no such device") {
		t.Errorf("%s", rg.out.String())
	}
}

func TestFailuresBeforeTheBoot(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*rig, *config.Config)
		code int
		want string
	}{
		{"qemu missing", func(rg *rig, _ *config.Config) {
			rg.r.deps.discover = func(context.Context, string) (qemu.Install, error) {
				return qemu.Install{}, errors.New("qemu is not installed")
			}
		}, exitcode.Environment, "qemu is not installed"},
		{"no image", func(rg *rig, _ *config.Config) {
			rg.r.deps.image = func(platform.Dirs, string, string) (vmimage.Image, error) {
				return vmimage.Image{}, errors.New("no image yet")
			}
		}, exitcode.ImageMissing, "controlsnack image build"},
		{"no embedded agent", func(rg *rig, _ *config.Config) {
			rg.r.deps.agent = func(string) (agentbin.Agent, error) { return agentbin.Agent{}, agentbin.ErrMissing }
		}, exitcode.Failure, "no embedded guest agent"},
		{"bad resolution", func(_ *rig, c *config.Config) { c.VM.Resolution = "big" }, exitcode.Usage, "vm.resolution"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rg := newRig(t)
			cfg := bootOnlyConfig()
			tt.mut(rg, &cfg)
			err := rg.run(context.Background(), cfg)
			if err == nil || exitcode.Of(err) != tt.code || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%v (%d)", err, exitcode.Of(err))
			}
			if rg.boots() != 0 {
				t.Error("nothing is booted")
			}
			if _, serr := os.Stat(filepath.Join(rg.dirs.SessionsDir(), "active.json")); !os.IsNotExist(serr) {
				t.Errorf("no lock is taken before the checks pass: %v", serr)
			}
		})
	}
}

func TestBootFailureIsReportedAndCleanedUp(t *testing.T) {
	rg := newRig(t)
	rg.bootErr = errors.New("the guest did not open the agent port")
	err := rg.run(context.Background(), bootOnlyConfig())
	if err == nil || !strings.Contains(err.Error(), "agent port") {
		t.Fatalf("%v", err)
	}
	if _, serr := os.Stat(filepath.Join(rg.dirs.SessionsDir(), "active.json")); !os.IsNotExist(serr) {
		t.Errorf("the lock is released: %v", serr)
	}
	rep, lines := rg.auditLog()
	if !rep.Complete || len(rep.Problems) != 0 {
		t.Errorf("even a failed session ends its log: %+v", rep)
	}
	if !strings.Contains(strings.Join(lines, "\n"), `"kind":"error"`) {
		t.Errorf("the failure is audited: %v", lines)
	}
}

func TestInterruptWhileBooting(t *testing.T) {
	rg := newRig(t)
	rg.hold = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rg.run(ctx, bootOnlyConfig()) }()
	eventually(t, "the boot to start", func() bool { return rg.boots() == 1 })
	cancel()
	err := <-done
	if exitcode.Of(err) != exitcode.Killed {
		t.Errorf("%v (%d)", err, exitcode.Of(err))
	}
	if _, serr := os.Stat(filepath.Join(rg.dirs.SessionsDir(), "active.json")); !os.IsNotExist(serr) {
		t.Errorf("the lock is released: %v", serr)
	}
	if rep, lines := rg.auditLog(); len(rep.Problems) != 0 || !strings.Contains(strings.Join(lines, "\n"), `"event":"kill"`) {
		t.Errorf("%+v %v", rep, lines)
	}
}

func TestOnlyOneSessionAtATime(t *testing.T) {
	rg := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { first <- rg.run(ctx, bootOnlyConfig()) }()
	eventually(t, "the first session to be running", func() bool { return strings.Contains(rg.out.String(), "Running.") })

	second := newRig(t)
	second.dirs = rg.dirs // the same user directories
	err := second.run(context.Background(), bootOnlyConfig())
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("%v", err)
	}
	if second.boots() != 0 {
		t.Error("the second session booted anyway")
	}
	cancel()
	<-first
}

func TestAccelFallbackIsShownAndAudited(t *testing.T) {
	rg := newRig(t)
	rg.r.deps.boot = func(ctx context.Context, o vm.BootOptions) (machine, error) {
		o.Events.AccelSelected(vm.AccelResult{Requested: "auto", Chosen: "kvm"})
		o.Events.AccelFallback(vm.Fallback{Requested: "auto", From: "kvm", Used: "tcg", Reason: "kvm: Permission denied"})
		return rg.mach, nil
	}
	cfg := bootOnlyConfig()
	cfg.Safety.Timeout = config.Duration(40 * time.Millisecond)
	_ = rg.run(context.Background(), cfg)
	if !strings.Contains(rg.out.String(), "kvm failed to start (kvm: Permission denied); falling back to software emulation") {
		t.Errorf("%s", rg.out.String())
	}
	if _, lines := rg.auditLog(); !strings.Contains(strings.Join(lines, "\n"), `"event":"accel_fallback"`) {
		t.Errorf("%v", lines)
	}
}

// boots is how many times the fake boot was called.
func (rg *rig) boots() int {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	return len(rg.options)
}

func TestSessionIdsMakeValidVMNames(t *testing.T) {
	for _, id := range []string{"s_01020304", "s_ffffffff", "s_00000000"} {
		if name := vmName(id); !qemu.ValidName(name) || strings.Contains(name, "_") {
			t.Errorf("%s -> %q is not a name QEMU accepts", id, name)
		}
	}
	rg := newRig(t)
	if got, _ := newID(rg.r.deps.random, "s_", 4); !qemu.ValidName(vmName(got)) {
		t.Errorf("a generated id gives %q", vmName(got))
	}
}

func TestControlsnackKillEndsTheSession(t *testing.T) {
	rg := newRig(t)
	done := make(chan error, 1)
	go func() { done <- rg.run(context.Background(), bootOnlyConfig()) }()
	eventually(t, "the session to be running", func() bool { return strings.Contains(rg.out.String(), "Running.") })

	// What `controlsnack kill` does from another terminal: read the lock, ask the session over loopback.
	reg := killswitch.NewRegistry(rg.dirs.SessionsDir(), platform.OSProcesses{})
	entry, err := reg.Read()
	if err != nil || entry == nil || !strings.HasPrefix(entry.ControlURL, "http://127.0.0.1:") {
		t.Fatalf("the lock must say how to ask the session to stop: %+v %v", entry, err)
	}
	var said bytes.Buffer
	if err := killswitch.NewControlClient(reg).Kill(context.Background(), &said); err != nil {
		t.Fatalf("kill: %v\n%s", err, said.String())
	}
	if !strings.Contains(said.String(), "killed session "+entry.SessionID) {
		t.Errorf("the session was asked, not shot from outside: %q", said.String())
	}
	var err2 error
	select {
	case err2 = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not end")
	}
	if exitcode.Of(err2) != exitcode.Killed || !rg.mach.proc.killed.Load() {
		t.Errorf("%v", err2)
	}
	if !strings.Contains(rg.out.String(), "after the kill (cli)") {
		t.Errorf("%s", rg.out.String())
	}
	rep, lines := rg.auditLog()
	if !rep.Complete || len(rep.Problems) != 0 || !strings.Contains(strings.Join(lines, "\n"), `"source":"cli"`) {
		t.Errorf("%+v %v", rep, lines)
	}
	if _, serr := os.Stat(filepath.Join(rg.dirs.SessionsDir(), "active.json")); !os.IsNotExist(serr) {
		t.Errorf("the lock is gone: %v", serr)
	}
}

func TestControlEndpointNeedsTheToken(t *testing.T) {
	rg := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rg.run(ctx, bootOnlyConfig()) }()
	eventually(t, "the session to be running", func() bool { return strings.Contains(rg.out.String(), "Running.") })
	entry, _ := killswitch.NewRegistry(rg.dirs.SessionsDir(), platform.OSProcesses{}).Read()
	url := strings.TrimSuffix(entry.ControlURL, "/") + killswitch.ControlKillPath

	for _, tc := range []struct{ method, auth string }{{http.MethodPost, ""}, {http.MethodPost, "Bearer wrong"}, {http.MethodGet, "Bearer " + entry.KillToken}} {
		req, _ := http.NewRequestWithContext(context.Background(), tc.method, url, nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s with %q was accepted", tc.method, tc.auth)
		}
	}
	if rg.mach.proc.killed.Load() {
		t.Error("a request without the token must not kill anything")
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, strings.TrimSuffix(entry.ControlURL, "/")+"/anything-else", nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("the endpoint serves nothing but the kill: %d", resp.StatusCode)
		}
	}
	cancel()
	<-done
}

func TestLeftoversOfAKilledSessionAreSwept(t *testing.T) {
	rg := newRig(t)
	run := filepath.Join(rg.dirs.State, "run")
	crashed, kept := filepath.Join(run, "s_crashed"), filepath.Join(run, "s_kept")
	for _, d := range []string{crashed, kept} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(crashed, runningMarker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := bootOnlyConfig()
	cfg.Safety.Timeout = config.Duration(40 * time.Millisecond)
	_ = rg.run(context.Background(), cfg)
	if _, err := os.Stat(crashed); !os.IsNotExist(err) {
		t.Error("the directory of a session that never got to clean up is removed")
	}
	if _, err := os.Stat(kept); err != nil {
		t.Error("a directory without the marker is something --keep left on purpose")
	}
}

func TestKeepRemovesOnlyTheMarker(t *testing.T) {
	rg := newRig(t)
	cfg := bootOnlyConfig()
	cfg.Session.Keep = true
	cfg.Safety.Timeout = config.Duration(40 * time.Millisecond)
	_ = rg.run(context.Background(), cfg)
	dirs, _ := filepath.Glob(filepath.Join(rg.dirs.State, "run", "s_*"))
	if len(dirs) != 1 {
		t.Fatalf("%v", dirs)
	}
	if _, err := os.Stat(filepath.Join(dirs[0], runningMarker)); !os.IsNotExist(err) {
		t.Error("a finished session's directory no longer says it is running")
	}
}

func TestAgentStateChangesAreReportedUnlessTheVMIsBeingKilled(t *testing.T) {
	rg := newRig(t)
	var stateFn func(agentclient.State, error)
	rg.r.deps.boot = func(ctx context.Context, o vm.BootOptions) (machine, error) {
		stateFn = o.OnAgentState
		return rg.mach, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rg.run(ctx, bootOnlyConfig()) }()
	eventually(t, "the session to be running", func() bool { return strings.Contains(rg.out.String(), "Running.") })
	stateFn(agentclient.StateDown, errors.New("EOF"))
	stateFn(agentclient.StateUp, nil)
	out := rg.out.String()
	if !strings.Contains(out, "agent: connection lost (EOF); reconnecting") || !strings.Contains(out, "agent: connected") {
		t.Errorf("%s", out)
	}
	cancel()
	<-done
	before := rg.out.String()
	stateFn(agentclient.StateDown, errors.New("EOF after the kill"))
	if rg.out.String() != before {
		t.Error("nothing is said about the agent once the kill switch has fired")
	}
}
