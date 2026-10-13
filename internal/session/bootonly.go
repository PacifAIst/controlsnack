// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/buildinfo"
	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/internal/platform"
	"github.com/PacifAIst/controlsnack-local/internal/safety/audit"
	"github.com/PacifAIst/controlsnack-local/internal/safety/killswitch"
	"github.com/PacifAIst/controlsnack-local/internal/vm"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentbin"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentclient"
	"github.com/PacifAIst/controlsnack-local/internal/vm/image"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
)

// machine is the part of *vm.Machine the runner uses; tests substitute a fake.
type machine interface {
	Proc() vm.Process
	Report() vm.BootReport
	Screendump(ctx context.Context, path string) error
	StopAgent()
}

// deps are the runner's connections to the machine it runs on.
type deps struct {
	dirs     func() (platform.Dirs, error)
	discover func(ctx context.Context, qemuDir string) (qemu.Install, error)
	image    func(dirs platform.Dirs, spec, arch string) (image.Image, error)
	agent    func(arch string) (agentbin.Agent, error)
	boot     func(ctx context.Context, o vm.BootOptions) (machine, error)
	procs    platform.ProcessInspector
	pid      func() int
	random   io.Reader
}

func realDeps() deps {
	return deps{
		dirs: platform.DefaultDirs,
		discover: func(ctx context.Context, dir string) (qemu.Install, error) {
			return qemu.Discover(ctx, qemu.OSHost{}, qemu.Options{Dir: dir})
		},
		image: func(dirs platform.Dirs, spec, arch string) (image.Image, error) {
			store := image.Store{Dir: dirs.ImagesDir()}
			im, err := store.Resolve(spec, arch)
			if err != nil {
				return image.Image{}, err
			}
			return im, store.QuickCheck(im)
		},
		agent: agentbin.For,
		boot: func(ctx context.Context, o vm.BootOptions) (machine, error) {
			m, err := vm.Boot(ctx, o)
			if err != nil {
				return nil, err // not a typed nil inside the interface
			}
			return m, nil
		},
		procs:  platform.OSProcesses{},
		pid:    os.Getpid,
		random: rand.Reader,
	}
}

// bootOnly is `run --no-llm --viewer none` (P3-T11): the whole VM side of a session without a model or a viewer. It
// takes the session lock, writes the audit log, boots the VM, installs the agent, proves that the screen can be
// read, and then waits until the user stops it (Ctrl+C or `controlsnack kill`), the session timeout passes, or the
// guest shuts down. Whatever ends it goes through the kill switch.
func (r *runner) bootOnly(parent context.Context, cfg config.Config) (err error) {
	if cfg.Session.VM != "" || cfg.Session.KeepVM != "" {
		return exitcode.Pending("P7-T06") // kept VMs belong to the full session
	}
	d := r.deps
	dirs, err := d.dirs()
	if err != nil {
		return err
	}
	inst, err := d.discover(parent, cfg.VM.QemuDir)
	if err != nil {
		return exitcode.New(exitcode.Environment, err)
	}
	im, err := d.image(dirs, cfg.VM.Image, inst.Arch)
	if err != nil {
		return exitcode.New(exitcode.ImageMissing, fmt.Errorf("%w\nbuild one with: controlsnack image build", err))
	}
	agent, err := d.agent(inst.Arch)
	if err != nil {
		return err
	}
	w, h, err := vm.ParseResolution(cfg.VM.Resolution)
	if err != nil {
		return exitcode.Usagef("vm.resolution: %v", err)
	}
	id, err := newID(d.random, "s_", 4)
	if err != nil {
		return err
	}

	token, err := newID(d.random, "", 16)
	if err != nil {
		return err
	}

	// The kill switch exists before anything else does, so no kill source can arrive before it is ready.
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	killer := killswitch.New(killswitch.Options{Cancel: cancel})

	// `controlsnack kill` asks the session to die through a loopback endpoint (the viewer mounts the same handler
	// later); without a viewer this tiny server is all there is. It serves nothing else.
	ln, err := platform.ListenLoopback(ctx, 0)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle(killswitch.ControlKillPath, killswitch.ControlHandler(killer, token))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	// One session at a time (D-20).
	started, _ := d.procs.StartTime(d.pid())
	lock, reaped, err := killswitch.NewRegistry(dirs.SessionsDir(), d.procs).Acquire(killswitch.Entry{
		SessionID: id, ControllerPID: d.pid(), ControllerStarted: started, KillToken: token,
		ControlURL: "http://" + ln.Addr().String() + "/"})
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	if reaped != nil {
		fmt.Fprintf(r.out, "cleaned up the leftovers of session %s, which was no longer running\n", reaped.Entry.SessionID)
	}
	sweepRunDirs(filepath.Join(dirs.State, "run")) // we hold the lock: none of these can belong to a live session

	log, err := audit.Open(dirs.AuditDir(), id, audit.Options{})
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	rec := func(rc audit.Record) error {
		if _, aerr := log.Append(rc); aerr != nil {
			return fmt.Errorf("the audit log can no longer be written, so the session stops: %w", aerr)
		}
		return nil
	}
	// Whatever way the session ends, its log ends with an `end` record: a log without one reads as a crash.
	ended := false
	end := func(data map[string]any) {
		if ended {
			return
		}
		ended = true
		rc := audit.Record{Kind: audit.KindLifecycle, Event: audit.EventEnd, Actor: "system"}
		if data != nil {
			rc.Data = audit.ToRaw(data)
		}
		_ = rec(rc)
	}
	defer func() {
		if err != nil {
			end(map[string]any{"error": err.Error()})
		} else {
			end(nil)
		}
	}()
	if err := rec(audit.Record{Kind: audit.KindLifecycle, Event: audit.EventStart, Actor: "system", Data: audit.ToRaw(map[string]any{
		"mode": "boot-only", "version": buildinfo.Get().Version, "image": im.Manifest.ID, "arch": inst.Arch,
		"accel": cfg.VM.Accel, "slow": cfg.VM.Slow, "memory_mib": cfg.VM.MemoryMiB, "cpus": cfg.VM.CPUs, "resolution": cfg.VM.Resolution,
	})}); err != nil {
		return err
	}

	sessionDir := filepath.Join(dirs.State, "run", id)
	if err := platform.EnsureDir(sessionDir); err != nil {
		return err
	}
	// The marker says "a session owns this directory": if the controller is killed hard, the next start sweeps it.
	marker := filepath.Join(sessionDir, runningMarker)
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		return err
	}
	removeSession := func() {
		if cfg.Session.Keep {
			_ = os.Remove(marker)
		} else {
			_ = os.RemoveAll(sessionDir)
		}
	}
	defer removeSession()
	prepare, err := vm.NewPrepare(vm.PrepareOptions{Install: inst, Base: im.Path, Name: vmName(id), Dir: sessionDir,
		CPUs: cfg.VM.CPUs, MemoryMiB: cfg.VM.MemoryMiB, Width: w, Height: h})
	if err != nil {
		return err
	}

	events := &vm.AuditEvents{Log: log, Forward: terminalEvents{out: r.out}}
	fmt.Fprintf(r.out, "Starting a session (%s): image %s, %s\n", id, im.Manifest.ID, inst.Arch)
	mach, err := d.boot(ctx, vm.BootOptions{
		Prepare: prepare, Backend: r.backend,
		Resolve: func(ctx context.Context) (vm.AccelResult, error) {
			return vm.ResolveAccel(ctx, vm.NewHostProbe(qemu.OSHost{}, inst), cfg.VM.Accel, cfg.VM.Slow)
		},
		Events: events, Agent: agent, HostVersion: buildinfo.Get().Version, SessionID: id,
		Progress: func(format string, args ...any) { fmt.Fprintf(r.out, "  "+format+"\n", args...) },
		OnAgentState: func(s agentclient.State, aerr error) {
			if !killer.Killed() { // a connection that ends because the VM is being killed is not news
				r.agentState(s, aerr)
			}
		},
	})
	if aerr := events.Err(); aerr != nil {
		if mach != nil {
			_ = mach.Proc().Kill()
		}
		return fmt.Errorf("the audit log can no longer be written, so the session stops: %w", aerr)
	}
	if err != nil {
		if parent.Err() != nil {
			_ = rec(audit.Record{Kind: audit.KindLifecycle, Event: audit.EventKill, Actor: "system", Reason: "interrupted while booting",
				Data: audit.ToRaw(map[string]any{"source": killswitch.SourceSignal})})
			err = exitcode.New(exitcode.Killed, errors.New("interrupted while the VM was starting"))
		}
		_ = rec(audit.Record{Kind: audit.KindError, Actor: "system", Reason: err.Error()})
		return err
	}

	killer.SetQEMU(mach.Proc())
	if err := lock.UpdateQEMU(mach.Proc().Pid(), qemuStart(d.procs, mach.Proc().Pid())); err != nil {
		killer.Trigger(killswitch.SourceInternal, "cannot record the VM in the session lock")
		return err
	}
	var once sync.Once
	teardown := func(context.Context) error {
		once.Do(func() { mach.StopAgent(); removeSession() })
		return nil
	}
	_ = killer.AddHook("agent and session files", teardown)

	rep := mach.Report()
	r.printReady(ctx, mach, rep, sessionDir)
	if err := rec(audit.Record{Kind: audit.KindLifecycle, Event: "ready", Actor: "system", Data: audit.ToRaw(map[string]any{
		"accel": rep.Accel.Chosen, "agent_version": rep.Agent.AgentVersion, "agent_sha256": rep.Agent.AgentSHA256,
		"launch_ms": rep.Timings.Launch.Milliseconds(), "port_open_ms": rep.Timings.PortOpen.Milliseconds(),
		"connected_ms": rep.Timings.Connected.Milliseconds(),
	})}); err != nil {
		killer.Trigger(killswitch.SourceAudit, err.Error())
		return err
	}

	fmt.Fprintln(r.out, "Running. Press Ctrl+C to stop the VM, or run `controlsnack kill` from another terminal.")
	var timeout <-chan time.Time
	if t := cfg.Safety.Timeout.D(); t > 0 {
		timer := time.NewTimer(t)
		defer timer.Stop()
		timeout = timer.C
	}

	timedOut := false
	select {
	case <-ctx.Done(): // Ctrl+C, SIGTERM or SIGHUP, or a kill that has already canceled the context
	case <-timeout:
		timedOut = true
	case <-mach.Proc().Exited():
	}
	// Who ended it? The kill switch sets its flag before it kills QEMU, so a kill that came from outside
	// (`controlsnack kill`) is recognized even when QEMU's exit is seen before the context ends. Only a QEMU that went
	// away with no kill switch fired is the guest's own doing.
	if !killer.Killed() {
		select {
		case <-mach.Proc().Exited():
			code, _ := mach.Proc().Result()
			_ = teardown(ctx)
			var outcome error
			if code != 0 {
				outcome = fmt.Errorf("QEMU stopped by itself with status %d", code)
			} else {
				fmt.Fprintln(r.out, "The guest shut down.")
			}
			end(map[string]any{"qemu_status": code})
			return outcome
		default:
		}
		if timedOut {
			killer.Trigger(killswitch.SourceInternal, "session timeout")
		} else {
			killer.Trigger(killswitch.SourceSignal, "interrupted")
		}
	}
	<-killer.Done()
	res, _ := killer.Result()
	outcome := exitcode.New(exitcode.Killed, fmt.Errorf("stopped (%s)", res.Source))
	if timedOut && res.Source == killswitch.SourceInternal {
		outcome = exitcode.New(exitcode.Timeout, fmt.Errorf("the session timeout of %s passed", cfg.Safety.Timeout.D()))
	}

	if res.QEMUKilled {
		fmt.Fprintf(r.out, "The VM was stopped %d ms after the kill (%s).\n", res.QEMUGoneAfter.Milliseconds(), res.Source)
	}
	_ = rec(audit.Record{Kind: audit.KindLifecycle, Event: audit.EventKill, Actor: "system", Reason: res.Reason, Data: audit.ToRaw(map[string]any{
		"source": res.Source, "qemu_killed": res.QEMUKilled, "qemu_gone_ms": res.QEMUGoneAfter.Milliseconds(), "teardown_ms": res.TeardownTook.Milliseconds(),
	})})
	end(nil)
	if !res.QEMUKilled || res.QEMUErr != nil {
		cause := res.QEMUErr
		if cause == nil {
			cause = errors.New("the kill did not reach the VM")
		}
		return fmt.Errorf("the VM may still be running: %w", cause)
	}
	return outcome
}

// printReady tells the user what came up and proves the screen can be read.
func (r *runner) printReady(ctx context.Context, m machine, rep vm.BootReport, sessionDir string) {
	fmt.Fprintf(r.out, "Ready: agent %s (sha256 %.12s) running as %s, screen %dx%d.\n",
		rep.Agent.AgentVersion, rep.Agent.AgentSHA256, rep.Agent.User, rep.Agent.Display.W, rep.Agent.Display.H)
	fmt.Fprintf(r.out, "Boot with %s: QEMU running after %s, desktop up after %s, agent connected after %s.\n", rep.Accel.Chosen,
		rep.Timings.Launch.Round(time.Millisecond), rep.Timings.PortOpen.Round(time.Second), rep.Timings.Connected.Round(time.Second))
	shot := filepath.Join(sessionDir, "ready.png")
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := m.Screendump(sctx, shot); err != nil {
		fmt.Fprintf(r.out, "Warning: the screen could not be read through QMP: %v\n", err)
		return
	}
	f, err := os.Open(shot) //nolint:gosec // a file QEMU just wrote in the session directory
	if err != nil {
		fmt.Fprintf(r.out, "Warning: the screendump is missing: %v\n", err)
		return
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	fi, _ := f.Stat()
	if err != nil || fi == nil {
		fmt.Fprintf(r.out, "Warning: the screendump is not a PNG: %v\n", err)
		return
	}
	fmt.Fprintf(r.out, "Screendump works: %dx%d PNG, %s.\n", cfg.Width, cfg.Height, image.HumanSize(fi.Size()))
}

// terminalEvents prints the accelerator decisions (the audit log gets them through vm.AuditEvents).
type terminalEvents struct{ out io.Writer }

func (t terminalEvents) AccelSelected(a vm.AccelResult) {
	fmt.Fprintf(t.out, "  accelerator: %s\n", a.Chosen)
	if a.Chosen == vm.AccelTCG {
		fmt.Fprintln(t.out, "  WARNING: no hardware acceleration, so the guest runs slowly (see `controlsnack doctor`)")
	}
}

func (t terminalEvents) AccelFallback(f vm.Fallback) {
	fmt.Fprintf(t.out, "  WARNING: %s failed to start (%s); falling back to software emulation, which is slow\n", f.From, f.Reason)
}

func newID(r io.Reader, prefix string, n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("making a session id: %w", err)
	}
	return prefix + hex.EncodeToString(b), nil
}

func qemuStart(p platform.ProcessInspector, pid int) string {
	s, _ := p.StartTime(pid)
	return s
}

// agentState tells the user when the agent connection changes after the first connect.
func (r *runner) agentState(s agentclient.State, err error) {
	switch s {
	case agentclient.StateDown:
		fmt.Fprintf(r.out, "  agent: connection lost (%v); reconnecting\n", err)
	case agentclient.StateUp:
		fmt.Fprintln(r.out, "  agent: connected")
	}
}

// vmName turns a session id (s_1a2b3c4d) into a name QEMU accepts: ids may contain underscores, VM names may not.
func vmName(id string) string { return strings.ReplaceAll(id, "_", "-") }

// runningMarker is the file that marks a session directory as owned by a running session.
const runningMarker = ".running"

// sweepRunDirs removes the directories of sessions that were killed without a chance to clean up (the marker is still
// there). Directories without a marker are what --keep left on purpose.
func sweepRunDirs(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, runningMarker)); err == nil {
			_ = os.RemoveAll(dir)
		}
	}
}
