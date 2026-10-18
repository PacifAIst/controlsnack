// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/buildinfo"
	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/internal/llm/detect"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/platform"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/safety/audit"
	"github.com/PacifAIst/controlsnack-local/internal/safety/killswitch"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
	"github.com/PacifAIst/controlsnack-local/internal/vm"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentclient"
	"github.com/PacifAIst/controlsnack-local/internal/vm/overlay"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
)

// live is a session that has booted: what the part after the boot needs of the part before it.
type live struct {
	cfg        config.Config
	id, token  string
	dirs       platform.Dirs
	ctx        context.Context
	killer     *killswitch.Killer
	log        *audit.Log
	rec        func(audit.Record) error
	end        func(map[string]any)
	sessionDir string
	mach       machine
	rep        vm.BootReport
	model      Model
	rules      []byte
	sm         *Machine
	started    time.Time
	teardown   func(context.Context) error
	// keptVM is the name of the VM that stays when the session is over, if there is one.
	keptVM string
	// keeper saves the transcript and the pictures when the person asked for --keep.
	keeper *Keeper
}

// session runs one session end to end (PLAN.md section 11): it takes the lock and opens the audit log, finds the model, boots the
// VM, and then either holds the machine for the person (`--no-llm`, P3-T11) or serves the model and the viewer.
func (r *runner) session(parent context.Context, cfg config.Config) (err error) {
	modeled := !cfg.Session.NoLLM
	if modeled && cfg.Viewer.Mode == viewer.ModeNone && cfg.Session.Task == "" {
		return exitcode.Usagef("with --viewer none there is no chat to type a task in: give one with --task")
	}
	d := r.deps
	dirs, err := d.dirs()
	if err != nil {
		return err
	}
	// What the person typed is checked before what the machine has: a mistake of theirs is the cheaper thing to fix.
	w, h, err := vm.ParseResolution(cfg.VM.Resolution)
	if err != nil {
		return exitcode.Usagef("vm.resolution: %v", err)
	}
	var rules []byte
	if cfg.Safety.Policy != "" {
		if rules, err = os.ReadFile(cfg.Safety.Policy); err != nil {
			return exitcode.Usagef("safety.policy: %v", err)
		}
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
	// The model is found before anything is started: a minute and a half of boot is a long time to learn there is none.
	var model Model
	if modeled {
		if model, err = ResolveModel(parent, d.models(cfg, dirs), cfg); err != nil {
			var ns *detect.NoServerError
			if errors.As(err, &ns) {
				fmt.Fprintln(r.out, ns.Instructions())
			}
			return err
		}
		fmt.Fprintf(r.out, "Model: %s (%s)\n", model.Target.Model, model.Setup.Badge())
		for _, n := range model.Notices {
			fmt.Fprintf(r.out, "NOTE: %s\n", n)
		}
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

	// `controlsnack kill` asks the session to die through a loopback endpoint; it serves nothing else.
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
	// A kept VM: the disk stays when the session is over, under a name, to be resumed with --vm.
	var kept *overlay.VM
	booted := false
	if cfg.Session.VM != "" || cfg.Session.KeepVM != "" {
		lib := d.library(dirs)
		created := false
		if cfg.Session.VM != "" {
			v, verr := lib.Open(cfg.Session.VM, im)
			if verr != nil {
				return verr
			}
			kept = &v
		} else {
			name := cfg.Session.KeepVM
			if name == "auto" {
				name = "vm-" + strings.TrimPrefix(id, "s_")
			}
			v, verr := lib.Create(ctx, inst.Img, name, im, buildinfo.Get().Version)
			if verr != nil {
				return verr
			}
			kept, created = &v, true
		}
		release, aerr := lib.Acquire(kept.Name)
		if aerr != nil {
			return aerr
		}
		defer func() {
			switch {
			case created && !booted: // a VM that never started is not worth keeping
				_, _ = lib.Remove(kept.Name)
			case booted:
				_ = lib.Touch(kept.Name)
			}
		}()
		defer release() // runs first: a VM that is in use cannot be removed
	}
	mode := "boot-only"
	start := map[string]any{
		"version": buildinfo.Get().Version, "image": im.Manifest.ID, "arch": inst.Arch,
		"accel": cfg.VM.Accel, "slow": cfg.VM.Slow, "memory_mib": cfg.VM.MemoryMiB, "cpus": cfg.VM.CPUs, "resolution": cfg.VM.Resolution,
	}
	if modeled {
		mode = "run"
		start["backend"], start["model"], start["remote_llm"] = model.Target.Backend, model.Target.Model, model.Target.Remote()
		start["vision"], start["tool_mode"], start["tools"] = model.Setup.Vision, string(model.Setup.Mode), model.Setup.Profile
		start["prompt_template_sha256"], start["prompt_version"] = prompt.TemplateSHA256(), prompt.SystemVersion
		start["unsafe"], start["step"], start["net"], start["viewer"] = cfg.Safety.Unsafe, cfg.Safety.Step, cfg.Net.Allow, cfg.Viewer.Mode
	}
	start["mode"] = mode
	if kept != nil {
		start["kept_vm"], start["resumed"] = kept.Name, cfg.Session.VM != ""
	}
	if err := rec(audit.Record{Kind: audit.KindLifecycle, Event: audit.EventStart, Actor: "system", Data: audit.ToRaw(start)}); err != nil {
		return err
	}
	if err := rec(audit.Record{Kind: audit.KindConfig, Event: "effective", Actor: "system", Data: audit.ToRaw(cfg.Redacted())}); err != nil {
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
	// What --keep keeps is the person's conversation and the pictures the model saw, in a directory of their own; everything else of
	// the session (the disk, the sockets, the browser profile) goes at its end, as it always does.
	var keeper *Keeper
	if cfg.Session.Keep {
		if keeper, err = NewKeeper(filepath.Join(dirs.State, "kept", id)); err != nil {
			return err
		}
	}
	var removed sync.Once // the teardown hook and the end of this function both call it
	removeSession := func() {
		removed.Do(func() {
			if keeper != nil {
				// A session that only booted has the one picture of the screen when it was ready.
				_ = os.Rename(filepath.Join(sessionDir, "ready.png"), filepath.Join(keeper.Dir(), "ready.png"))
				if kerr := keeper.Close(); kerr != nil {
					fmt.Fprintf(r.out, "Not everything that --keep was to keep could be saved: %v\n", kerr)
				}
				fmt.Fprintf(r.out, "Kept in %s: the transcript and %d pictures.\n", keeper.Dir(), keeper.Shots())
			}
			_ = os.RemoveAll(sessionDir)
		})
	}
	defer removeSession()
	po := vm.PrepareOptions{Install: inst, Base: im.Path, Name: vmName(id), Dir: sessionDir,
		CPUs: cfg.VM.CPUs, MemoryMiB: cfg.VM.MemoryMiB, Width: w, Height: h, Net: cfg.Net.Allow}
	if kept != nil {
		po.Disk, po.Cache, po.ExtraDirs = kept.Disk, "writeback", []string{kept.Dir}
	}
	prepare, err := vm.NewPrepare(po)
	if err != nil {
		return err
	}

	sm := NewMachine(nil)
	var linkMu sync.Mutex
	var link *Link
	events := &vm.AuditEvents{Log: log, Forward: terminalEvents{out: r.out}}
	fmt.Fprintf(r.out, "Starting a session (%s): image %s, %s\n", id, im.Manifest.ID, inst.Arch)
	mach, err := d.boot(ctx, vm.BootOptions{
		Prepare: prepare, Backend: r.backend,
		Resolve: func(ctx context.Context) (vm.AccelResult, error) {
			return vm.ResolveAccel(ctx, vm.NewHostProbe(qemu.OSHost{}, inst), cfg.VM.Accel, cfg.VM.Slow)
		},
		Events: events, Agent: agent, HostVersion: buildinfo.Get().Version, SessionID: id, Features: protocol.Features{Net: cfg.Net.Allow},
		Progress: func(format string, args ...any) { fmt.Fprintf(r.out, "  "+format+"\n", args...) },
		OnAgentState: func(s agentclient.State, aerr error) {
			// A connection that ends because the VM is being killed, or because the guest is being powered off at the end, is not news.
			if killer.Killed() || sm.State() == Ending || sm.State() == Ended {
				return
			}
			r.agentState(s, aerr)
			linkMu.Lock()
			l := link
			linkMu.Unlock()
			if l != nil {
				l.AgentState(s, aerr)
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

	booted = true
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

	l := &live{cfg: cfg, id: id, token: token, dirs: dirs, ctx: ctx, killer: killer, log: log, rec: rec, end: end, sessionDir: sessionDir, mach: mach, rep: rep,
		model: model, rules: rules, sm: sm, started: time.Now(), teardown: teardown, keeper: keeper}
	if kept != nil {
		l.keptVM = kept.Name
	}
	if modeled {
		return r.serve(l, func(lk *Link) { linkMu.Lock(); link = lk; linkMu.Unlock() })
	}
	return r.hold(l)
}
