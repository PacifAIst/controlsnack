// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qmp"
)

// --- fakes -----------------------------------------------------------------------------------------------------

type fakeProc struct {
	exited   chan struct{}
	once     sync.Once
	code     int
	out      string
	stubborn bool // ignores Kill
	killed   atomic.Bool
	stopped  atomic.Bool
}

func newProc(out string) *fakeProc { return &fakeProc{exited: make(chan struct{}), out: out} }

func (p *fakeProc) exit(code int) { p.once.Do(func() { p.code = code; close(p.exited) }) }
func (p *fakeProc) Pid() int      { return 4242 }
func (p *fakeProc) Kill() error {
	p.killed.Store(true)
	if !p.stubborn {
		p.exit(137)
	}
	return nil
}
func (p *fakeProc) Exited() <-chan struct{} { return p.exited }
func (p *fakeProc) Result() (int, error)    { return p.code, nil }
func (p *fakeProc) Output() string          { return p.out }
func (p *fakeProc) Stop(ctx context.Context, quit func(context.Context) error, _ time.Duration) error {
	p.stopped.Store(true)
	if quit != nil {
		_ = quit(ctx)
	}
	p.exit(0)
	return nil
}

type fakeQMP struct {
	proc    *fakeProc
	status  func() qmp.Status
	done    chan struct{}
	once    sync.Once
	closed  atomic.Bool
	quitted atomic.Bool
	// portWait, when set, decides what WaitPortOpen does (default: the port is open at once).
	portWait func(ctx context.Context) error
}

func newQMP(proc *fakeProc, status func() qmp.Status) *fakeQMP {
	return &fakeQMP{proc: proc, status: status, done: make(chan struct{})}
}

func (q *fakeQMP) QueryStatus(ctx context.Context) (qmp.Status, error) {
	if err := ctx.Err(); err != nil {
		return qmp.Status{}, err
	}
	select {
	case <-q.proc.exited:
		return qmp.Status{}, errors.New("connection lost")
	default:
	}
	return q.status(), nil
}
func (q *fakeQMP) Quit(context.Context) error                             { q.quitted.Store(true); return nil }
func (q *fakeQMP) Screendump(context.Context, string, string) error       { return nil }
func (q *fakeQMP) InputSendEvent(context.Context, []qmp.InputEvent) error { return nil }
func (q *fakeQMP) WaitPortOpen(ctx context.Context, _ string) error {
	if q.portWait != nil {
		return q.portWait(ctx)
	}
	return nil
}
func (q *fakeQMP) WaitEvent(context.Context, func(qmp.Event) bool) (qmp.Event, error) {
	return qmp.Event{}, errors.New("not used")
}
func (q *fakeQMP) Done() <-chan struct{} {
	go func() {
		select {
		case <-q.proc.exited:
			q.once.Do(func() { close(q.done) })
		case <-q.done:
		}
	}()
	return q.done
}
func (q *fakeQMP) Err() error { return errors.New("QMP connection lost: EOF") }
func (q *fakeQMP) Close() error {
	q.closed.Store(true)
	q.once.Do(func() { close(q.done) })
	return nil
}

var running = func() qmp.Status { return qmp.Status{Status: "running", Running: true} }

// scenario describes what the fake QEMU does for one accelerator.
type scenario struct {
	exitAfter time.Duration // the process exits by itself this long after starting (0 = never)
	exitCode  int
	out       string
	status    func() qmp.Status // nil = running
	qmpHangs  bool              // QMP never becomes ready
	stubborn  bool
	startErr  error
	portWait  func(ctx context.Context) error // what WaitPortOpen does (nil = opens at once)
}

type rig struct {
	t         *testing.T
	scenarios map[string]scenario // keyed by accelerator

	mu        sync.Mutex
	procs     []*fakeProc
	qmps      []*fakeQMP
	streams   []net.Conn
	argvs     [][]string
	prepared  []int
	cleaned   []int
	order     []string
	deadlines []time.Duration // remaining time of the context handed to DialQMP, per attempt
	// onStream, when set, serves the guest's end of every chardev stream the backend dials.
	onStream func(ep qemu.Endpoint, guestEnd net.Conn)
}

func accelOf(argv []string) string {
	if slices.Contains(argv, "-enable-kvm") {
		return "kvm"
	}
	for i, a := range argv {
		if a == "-accel" && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func (r *rig) backend() Backend {
	return Backend{
		Start: func(ctx context.Context, argv []string, opt qemu.StartOptions) (Process, error) {
			sc := r.scenarios[accelOf(argv)]
			r.mu.Lock()
			r.argvs = append(r.argvs, argv)
			r.mu.Unlock()
			if sc.startErr != nil {
				return nil, sc.startErr
			}
			p := newProc(sc.out)
			p.stubborn = sc.stubborn
			r.mu.Lock()
			r.procs = append(r.procs, p)
			r.mu.Unlock()
			if sc.exitAfter > 0 || sc.exitCode != 0 {
				go func() { time.Sleep(sc.exitAfter); p.exit(sc.exitCode) }()
			}
			return p, nil
		},
		DialQMP: func(ctx context.Context, ep qemu.Endpoint) (QMP, error) {
			r.mu.Lock()
			proc := r.procs[len(r.procs)-1]
			sc := r.scenarios[accelOf(r.argvs[len(r.argvs)-1])]
			if dl, ok := ctx.Deadline(); ok {
				r.deadlines = append(r.deadlines, time.Until(dl))
			}
			r.mu.Unlock()
			if sc.qmpHangs {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			st := sc.status
			if st == nil {
				st = running
			}
			q := newQMP(proc, st)
			q.portWait = sc.portWait
			r.mu.Lock()
			r.qmps = append(r.qmps, q)
			r.mu.Unlock()
			return q, nil
		},
		DialStream: func(ctx context.Context, ep qemu.Endpoint) (net.Conn, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			a, b := net.Pipe()
			if r.onStream != nil {
				go r.onStream(ep, b)
			}
			_ = b
			r.mu.Lock()
			r.streams = append(r.streams, a)
			r.mu.Unlock()
			return a, nil
		},
	}
}

func (r *rig) prepare(netPort bool) PrepareFunc {
	return func(ctx context.Context, n int) (Prepared, error) {
		r.mu.Lock()
		r.prepared = append(r.prepared, n)
		r.order = append(r.order, fmt.Sprintf("prepare%d", n))
		r.mu.Unlock()
		spec := qemu.RuntimeSpec{
			Install: qemu.Install{Arch: qemu.ArchX86_64, System: "/usr/bin/qemu-system-x86_64"}, Name: "abc", CPUs: 2, MemoryMiB: 2048,
			Width: 1280, Height: 800, Disk: "/run/cs/s/disk.qcow2", Serial: "/run/cs/s/serial.log",
			Agent:       qemu.Endpoint{Path: fmt.Sprintf("/tmp/cs-%d/agent0.sock", n)},
			QMP:         qemu.Endpoint{Path: fmt.Sprintf("/tmp/cs-%d/qmp0.sock", n)},
			AllowedDirs: []string{"/run/cs/s", "/tmp/cs-1", "/tmp/cs-2"},
		}
		if netPort {
			spec.Net = &qemu.Endpoint{Path: fmt.Sprintf("/tmp/cs-%d/net0.sock", n)}
		}
		return Prepared{Spec: spec, Dir: "/run/cs/s", Cleanup: func() {
			r.mu.Lock()
			r.cleaned = append(r.cleaned, n)
			r.order = append(r.order, fmt.Sprintf("cleanup%d", n))
			r.mu.Unlock()
		}}, nil
	}
}

type events struct {
	mu        sync.Mutex
	selected  []AccelResult
	fallbacks []Fallback
	seq       []string
}

func (e *events) AccelSelected(r AccelResult) {
	e.mu.Lock()
	e.selected = append(e.selected, r)
	e.seq = append(e.seq, "selected")
	e.mu.Unlock()
}
func (e *events) AccelFallback(f Fallback) {
	e.mu.Lock()
	e.fallbacks = append(e.fallbacks, f)
	e.seq = append(e.seq, "fallback")
	e.mu.Unlock()
}

func resolveTo(accel string, requested string) func(context.Context) (AccelResult, error) {
	return func(context.Context) (AccelResult, error) {
		r := AccelResult{Requested: requested, Chosen: accel, Slow: accel == AccelTCG,
			Tried: []AccelTry{{Accel: accel, Usable: true}}}
		return r, nil
	}
}

func opts(r *rig, accel string, ev Events) LaunchOptions {
	return LaunchOptions{Resolve: resolveTo(accel, "auto"), Prepare: r.prepare(false), Backend: r.backend(), Events: ev,
		Window: 400 * time.Millisecond, Stable: 60 * time.Millisecond, Poll: 10 * time.Millisecond}
}

func newRig(t *testing.T, sc map[string]scenario) *rig { return &rig{t: t, scenarios: sc} }

func hasFlags(argv []string, flags ...string) bool {
	for _, f := range flags {
		if !slices.Contains(argv, f) {
			return false
		}
	}
	return true
}

// --- the happy path ----------------------------------------------------------------------------------------------

func TestLaunchSucceedsOnTheFirstTry(t *testing.T) {
	r := newRig(t, map[string]scenario{"kvm": {}})
	ev := &events{}
	vm, err := Launch(context.Background(), opts(r, AccelKVM, ev))
	if err != nil {
		t.Fatal(err)
	}
	if vm.Accel.Chosen != "kvm" || vm.Fallback != nil || vm.Accel.Requested != "auto" {
		t.Errorf("%+v %+v", vm.Accel, vm.Fallback)
	}
	if !hasFlags(vm.Argv, "-enable-kvm") || vm.Spec.Accel != "kvm" || vm.Agent == nil || vm.Net != nil || vm.QMP == nil {
		t.Errorf("%v", vm.Argv)
	}
	if !slices.Equal(ev.seq, []string{"selected"}) || ev.selected[0].Chosen != "kvm" {
		t.Errorf("events %v", ev.seq)
	}
	if !slices.Equal(r.prepared, []int{1}) || len(r.cleaned) != 0 {
		t.Errorf("prepared %v cleaned %v", r.prepared, r.cleaned)
	}

	if err := vm.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	proc := r.procs[0]
	if !proc.stopped.Load() || !r.qmps[0].quitted.Load() || !r.qmps[0].closed.Load() || !slices.Equal(r.cleaned, []int{1}) {
		t.Errorf("Stop quits through QMP, closes the connections and cleans up: stopped=%v quit=%v closed=%v cleaned=%v",
			proc.stopped.Load(), r.qmps[0].quitted.Load(), r.qmps[0].closed.Load(), r.cleaned)
	}
	_ = vm.Stop(context.Background()) // twice is harmless
	if len(r.cleaned) != 1 {
		t.Errorf("cleanup ran %d times", len(r.cleaned))
	}
}

func TestLaunchWithoutEventsOrNetwork(t *testing.T) {
	r := newRig(t, map[string]scenario{"tcg": {}})
	vm, err := Launch(context.Background(), opts(r, AccelTCG, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vm.Kill() }()
	if !vm.Accel.Slow || vm.Accel.Chosen != "tcg" || !hasFlags(vm.Argv, "tcg", "max") {
		t.Errorf("%+v %v", vm.Accel, vm.Argv)
	}
	if len(r.streams) != 1 {
		t.Errorf("only the agent port is dialed without --allow-net: %d streams", len(r.streams))
	}
}

func TestLaunchDialsTheNetworkPortWhenAsked(t *testing.T) {
	r := newRig(t, map[string]scenario{"kvm": {}})
	o := opts(r, AccelKVM, nil)
	o.Prepare = r.prepare(true)
	vm, err := Launch(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vm.Kill() }()
	if vm.Net == nil || len(r.streams) != 2 {
		t.Errorf("--allow-net: agent and net ports are both connected: net=%v streams=%d", vm.Net, len(r.streams))
	}
	if !strings.Contains(strings.Join(vm.Argv, " "), "net0") {
		t.Errorf("%v", vm.Argv)
	}
}

// --- fallback --------------------------------------------------------------------------------------------------

func TestHardwareFailureFallsBackToTCGOnce(t *testing.T) {
	r := newRig(t, map[string]scenario{
		"kvm": {exitCode: 1, out: "qemu-system-x86_64: -accel kvm: Could not access KVM kernel module: Permission denied\nqemu-system-x86_64: failed to initialize kvm: Permission denied\n"},
		"tcg": {},
	})
	ev := &events{}
	vm, err := Launch(context.Background(), opts(r, AccelKVM, ev))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vm.Kill() }()

	if vm.Accel.Chosen != "tcg" || !vm.Accel.Slow || vm.Accel.Requested != "auto" || vm.Spec.Accel != "tcg" {
		t.Errorf("%+v", vm.Accel)
	}
	if !hasFlags(vm.Argv, "tcg", "max") || hasFlags(vm.Argv, "-enable-kvm") {
		t.Errorf("the second command line is the tcg one: %v", vm.Argv)
	}
	if !hasFlags(r.argvs[0], "-enable-kvm") || len(r.argvs) != 2 {
		t.Errorf("%d launches", len(r.argvs))
	}
	fb := vm.Fallback
	if fb == nil || fb.From != "kvm" || fb.Used != "tcg" || fb.Requested != "auto" ||
		!strings.Contains(fb.Reason, "exited with status 1") || !strings.Contains(fb.Stderr, "Permission denied") {
		t.Fatalf("%+v", fb)
	}
	if len(fb.Stderr) > excerptBytes+3 {
		t.Errorf("the excerpt is capped at 1 KiB: %d", len(fb.Stderr))
	}
	if !slices.Equal(ev.seq, []string{"selected", "fallback"}) || ev.fallbacks[0] != *fb {
		t.Errorf("events %v", ev.seq)
	}
	// Order: the failed attempt's resources are gone before the second attempt gets its own (fresh sockets, fresh overlay).
	if !slices.Equal(r.order, []string{"prepare1", "cleanup1", "prepare2"}) || !slices.Equal(r.prepared, []int{1, 2}) {
		t.Errorf("order %v", r.order)
	}
	if !r.procs[0].killed.Load() && !isClosed(r.procs[0].exited) {
		t.Error("the failed process must be gone")
	}
	if len(vm.Accel.Tried) < 3 || vm.Accel.Tried[len(vm.Accel.Tried)-2].Usable || !strings.Contains(vm.Accel.Tried[len(vm.Accel.Tried)-2].Reason, "failed at launch") {
		t.Errorf("the result records the failed attempt: %+v", vm.Accel.Tried)
	}
}

func isClosed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

func TestExplicitHardwareChoiceIsRetriedWithTCGToo(t *testing.T) {
	r := newRig(t, map[string]scenario{"whpx": {exitCode: 1, out: "whpx: Failed to create partition"}, "tcg": {}})
	o := opts(r, AccelWHPX, nil)
	o.Resolve = resolveTo(AccelWHPX, "whpx")
	vm, err := Launch(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vm.Kill() }()
	if vm.Accel.Requested != "whpx" || vm.Accel.Chosen != "tcg" || vm.Fallback.From != "whpx" {
		t.Errorf("%+v %+v", vm.Accel, vm.Fallback)
	}
}

func TestTCGFailureIsFinalAndNotRetried(t *testing.T) {
	r := newRig(t, map[string]scenario{"tcg": {exitCode: 1, out: "qemu: could not load BIOS"}})
	ev := &events{}
	_, err := Launch(context.Background(), opts(r, AccelTCG, ev))
	if err == nil || exitcode.Of(err) != exitcode.Environment {
		t.Fatalf("exit code 3 expected, got %v (%d)", err, exitcode.Of(err))
	}
	for _, want := range []string{"tcg", "exited with status 1", "could not load BIOS", "controlsnack doctor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message lacks %q:\n%v", want, err)
		}
	}
	if len(r.argvs) != 1 || len(ev.fallbacks) != 0 || !slices.Equal(r.cleaned, []int{1}) {
		t.Errorf("launches %d, fallbacks %d, cleaned %v: tcg is never retried", len(r.argvs), len(ev.fallbacks), r.cleaned)
	}
}

func TestBothLaunchesFailing(t *testing.T) {
	r := newRig(t, map[string]scenario{
		"kvm": {exitCode: 1, out: "kvm: permission denied"},
		"tcg": {exitCode: 2, out: "tcg: out of memory"},
	})
	ev := &events{}
	_, err := Launch(context.Background(), opts(r, AccelKVM, ev))
	if exitcode.Of(err) != exitcode.Environment {
		t.Fatalf("%v", err)
	}
	for _, want := range []string{"QEMU with kvm did not start", "kvm: permission denied", "fallback to software emulation failed", "QEMU with tcg did not start", "tcg: out of memory", "controlsnack doctor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message lacks %q:\n%v", want, err)
		}
	}
	if len(r.argvs) != 2 || !slices.Equal(r.cleaned, []int{1, 2}) || len(ev.fallbacks) != 1 {
		t.Errorf("never a third launch: %d launches, cleaned %v", len(r.argvs), r.cleaned)
	}
	var le *LaunchError
	if !errors.As(err, &le) {
		t.Error("the failure is inspectable")
	}
}

func TestLaunchWindowAndTCGFactor(t *testing.T) {
	r := newRig(t, map[string]scenario{"kvm": {qmpHangs: true}, "tcg": {}})
	o := opts(r, AccelKVM, nil)
	o.Window = 150 * time.Millisecond
	start := time.Now()
	vm, err := Launch(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vm.Kill() }()
	if time.Since(start) < 150*time.Millisecond {
		t.Error("the first attempt waited out its window")
	}
	if vm.Fallback == nil || !strings.Contains(vm.Fallback.Reason, "QMP was not ready within the launch window") {
		t.Fatalf("%+v", vm.Fallback)
	}
	// The windows handed to the dialers: 150 ms, then 4 times that for tcg (PLAN.md section 7.9, item 7).
	if len(r.deadlines) != 2 || r.deadlines[0] > 150*time.Millisecond || r.deadlines[1] < 450*time.Millisecond || r.deadlines[1] > 600*time.Millisecond {
		t.Errorf("windows %v", r.deadlines)
	}
}

func TestStateChangesDuringTheStabilityPeriodFailTheLaunch(t *testing.T) {
	for _, state := range []string{"internal-error", "shutdown", "guest-panicked", "io-error"} {
		t.Run(state, func(t *testing.T) {
			var calls atomic.Int32
			r := newRig(t, map[string]scenario{
				"kvm": {status: func() qmp.Status {
					if calls.Add(1) < 3 {
						return qmp.Status{Status: "running", Running: true}
					}
					return qmp.Status{Status: state}
				}},
				"tcg": {},
			})
			vm, err := Launch(context.Background(), func() LaunchOptions { o := opts(r, AccelKVM, nil); o.Stable = 200 * time.Millisecond; return o }())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = vm.Kill() }()
			if vm.Fallback == nil || !strings.Contains(vm.Fallback.Reason, state) {
				t.Errorf("%+v", vm.Fallback)
			}
		})
	}
}

func TestLaunchNeedsToReachRunning(t *testing.T) {
	// A VM that stays in prelaunch never counts as started, and a running VM that pauses fails.
	r := newRig(t, map[string]scenario{"tcg": {status: func() qmp.Status { return qmp.Status{Status: "prelaunch"} }}})
	if _, err := Launch(context.Background(), opts(r, AccelTCG, nil)); err == nil || !strings.Contains(err.Error(), "did not reach the running state") {
		t.Errorf("%v", err)
	}
	var n atomic.Int32
	r2 := newRig(t, map[string]scenario{"tcg": {status: func() qmp.Status {
		if n.Add(1) < 3 {
			return qmp.Status{Status: "running", Running: true}
		}
		return qmp.Status{Status: "paused"}
	}}})
	o := opts(r2, AccelTCG, nil)
	o.Stable = 300 * time.Millisecond
	if _, err := Launch(context.Background(), o); err == nil || !strings.Contains(err.Error(), "stopped running") {
		t.Errorf("%v", err)
	}
}

func TestProcessExitingDuringTheStabilityPeriod(t *testing.T) {
	r := newRig(t, map[string]scenario{"kvm": {exitAfter: 30 * time.Millisecond, exitCode: 9, out: "boom"}, "tcg": {}})
	o := opts(r, AccelKVM, nil)
	o.Stable = 300 * time.Millisecond
	vm, err := Launch(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vm.Kill() }()
	if vm.Fallback == nil || !strings.Contains(vm.Fallback.Reason, "exited with status 9") || vm.Fallback.Stderr != "boom" {
		t.Errorf("%+v", vm.Fallback)
	}
}

// --- things that must not be retried -----------------------------------------------------------------------------

func TestAFailedProcessThatWillNotDieStopsEverything(t *testing.T) {
	defer func(d time.Duration) { killWait = d }(killWait)
	killWait = 50 * time.Millisecond
	r := newRig(t, map[string]scenario{"kvm": {qmpHangs: true, stubborn: true}, "tcg": {}})
	o := opts(r, AccelKVM, nil)
	o.Window = 60 * time.Millisecond
	_, err := Launch(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "could not be stopped, so no second attempt") {
		t.Fatalf("%v", err)
	}
	if len(r.argvs) != 1 {
		t.Errorf("a second QEMU must never start while the first is alive: %d launches", len(r.argvs))
	}
}

func TestPreparationAndCommandLineErrorsAreNotRetried(t *testing.T) {
	r := newRig(t, map[string]scenario{"tcg": {}})
	o := opts(r, AccelKVM, nil)
	o.Prepare = func(ctx context.Context, n int) (Prepared, error) {
		return Prepared{}, errors.New("no space left on device")
	}
	_, err := Launch(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "no space left") || exitcode.Of(err) != exitcode.Failure || len(r.argvs) != 0 {
		t.Errorf("a full disk is not an accelerator problem: %v (%d)", err, exitcode.Of(err))
	}

	o = opts(r, AccelKVM, nil)
	good := o.Prepare
	o.Prepare = func(ctx context.Context, n int) (Prepared, error) {
		p, _ := good(ctx, n)
		p.Spec.Disk = "/etc/disk.qcow2" // outside the allowed directories
		return p, nil
	}
	_, err = Launch(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "outside the directories") || len(r.argvs) != 0 || len(r.cleaned) != 1 {
		t.Errorf("a refused command line is not retried and its files are cleaned up: %v (cleaned %v)", err, r.cleaned)
	}

	o = opts(newRig(t, map[string]scenario{"kvm": {startErr: errors.New("fork/exec: no such file or directory")}}), AccelKVM, nil)
	_, err = Launch(context.Background(), o)
	if err == nil || exitcode.Of(err) != exitcode.Environment || !strings.Contains(err.Error(), "could not be started") {
		t.Errorf("QEMU that cannot be executed is an environment problem: %v", err)
	}
}

func TestCancelingEndsTheLaunchWithoutRetry(t *testing.T) {
	r := newRig(t, map[string]scenario{"kvm": {qmpHangs: true}, "tcg": {}})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	o := opts(r, AccelKVM, nil)
	o.Window = 5 * time.Second
	start := time.Now()
	_, err := Launch(ctx, o)
	if !errors.Is(err, context.Canceled) || time.Since(start) > 2*time.Second {
		t.Errorf("%v after %v", err, time.Since(start))
	}
	if len(r.argvs) != 1 || !r.procs[0].killed.Load() || !slices.Equal(r.cleaned, []int{1}) {
		t.Errorf("the half-started VM is killed and cleaned up: launches %d cleaned %v", len(r.argvs), r.cleaned)
	}
}

func TestResolveFailureIsAnEnvironmentError(t *testing.T) {
	o := opts(newRig(t, nil), AccelTCG, nil)
	o.Resolve = func(context.Context) (AccelResult, error) { return AccelResult{}, ErrNoAccelerator }
	if _, err := Launch(context.Background(), o); !errors.Is(err, ErrNoAccelerator) || exitcode.Of(err) != exitcode.Environment {
		t.Errorf("%v", err)
	}
	if _, err := Launch(context.Background(), LaunchOptions{}); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("%v", err)
	}
}

func TestOneDialFailingEndsTheOthersBeforeCleanup(t *testing.T) {
	r := newRig(t, map[string]scenario{"tcg": {}})
	o := opts(r, AccelTCG, nil)
	be := r.backend()
	var qmpReturned atomic.Bool
	o.Backend = be
	o.Backend.DialStream = func(ctx context.Context, ep qemu.Endpoint) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}
	slowQMP := be.DialQMP
	o.Backend.DialQMP = func(ctx context.Context, ep qemu.Endpoint) (QMP, error) {
		<-ctx.Done() // only ends because the failed dial cancels the others
		qmpReturned.Store(true)
		return slowQMP(ctx, ep)
	}
	_, err := Launch(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "could not reach the agent port") {
		t.Fatalf("%v", err)
	}
	if !qmpReturned.Load() {
		t.Error("the launcher returned before the other dialer had finished")
	}
}

func TestTailExcerpt(t *testing.T) {
	if got := tailExcerpt("a\n\n  b  \r\nc\n", 100); got != "a | b | c" {
		t.Errorf("%q", got)
	}
	long := strings.Repeat("old line\n", 500) + "the real error\n"
	got := tailExcerpt(long, 100)
	if len(got) > 103 || !strings.HasSuffix(got, "the real error") || !strings.HasPrefix(got, "...") {
		t.Errorf("the end of the output is what matters: %q", got)
	}
	if got := tailExcerpt(strings.Repeat("é", 300), 101); !strings.HasPrefix(got, "...") || strings.ToValidUTF8(got, "?") != got {
		t.Errorf("never cut inside a character: %q", got)
	}
	if tailExcerpt("", 10) != "" {
		t.Error("empty")
	}
}

// --- endpoints ---------------------------------------------------------------------------------------------------

func TestEndpointAddr(t *testing.T) {
	if n, a, err := endpointAddr(qemu.Endpoint{Path: "/tmp/x.sock"}); n != "unix" || a != "/tmp/x.sock" || err != nil {
		t.Error(n, a, err)
	}
	if n, a, err := endpointAddr(qemu.Endpoint{Port: 4000}); n != "tcp" || a != "127.0.0.1:4000" || err != nil {
		t.Error(n, a, err)
	}
	for _, bad := range []qemu.Endpoint{{}, {Path: "/x", Port: 1}, {Port: 70000}, {Port: -1}} {
		if _, _, err := endpointAddr(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestExcerptDropsQEMUsConnectionChatter(t *testing.T) {
	out := "qemu-system-x86_64: -chardev socket,id=agent0,path=/tmp/a.sock,server=on,wait=on: info: QEMU waiting for connection on: disconnected:unix:/tmp/a.sock,server=on\n" +
		"qemu-system-x86_64: -chardev socket,id=qmp0,path=/tmp/q.sock,server=on,wait=on: info: QEMU waiting for connection on: disconnected:unix:/tmp/q.sock,server=on\n" +
		"Could not access KVM kernel module: Permission denied\n" +
		"qemu-system-x86_64: failed to initialize kvm: Permission denied\n"
	got := tailExcerpt(out, 1024)
	if got != "Could not access KVM kernel module: Permission denied | qemu-system-x86_64: failed to initialize kvm: Permission denied" {
		t.Errorf("%q", got)
	}
}
