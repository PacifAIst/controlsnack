// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/netbridge"
	"github.com/PacifAIst/controlsnack-local/internal/safety"
	"github.com/PacifAIst/controlsnack-local/internal/safety/audit"
	"github.com/PacifAIst/controlsnack-local/internal/safety/egress"
	"github.com/PacifAIst/controlsnack-local/internal/safety/killswitch"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
)

// gracePeriod is how long the VM is given to stop by itself at a normal end before it is ended.
const gracePeriod = 10 * time.Second

// taskEnd is how a task ended.
type taskEnd struct {
	out  Outcome
	text string
}

// hello is what a page is told about the session when it connects.
func (r *runner) hello(l *live, prof safety.Profile) func() viewer.Hello {
	mode := "vision"
	if !l.model.Setup.Vision {
		mode = "text_only"
	}
	acc := viewer.Accel{Requested: l.rep.Accel.Requested, Used: l.rep.Accel.Chosen}
	if l.rep.Fallback != nil {
		acc.FallbackReason = textclean.Line(l.rep.Fallback.Reason, 200)
	}
	return func() viewer.Hello {
		return viewer.Hello{
			Session: l.id, Backend: l.model.Target.Backend, Model: l.model.Target.Model, Mode: mode, Net: l.cfg.Net.Allow, Unsafe: l.cfg.Safety.Unsafe,
			RemoteLLM: l.model.Target.Remote(), Accel: acc,
			Limits: viewer.Limits{MaxTurns: prof.MaxTurns, MaxActions: prof.ActionsPerTask, TimeoutSec: int(prof.SessionTimeout.Seconds()),
				ConfirmTimeoutSec: int(prof.ConfirmTimeout.Seconds()), IdleTimeoutSec: int(prof.IdleTimeout.Seconds())},
			Resolution: viewer.Resolution{W: l.rep.Agent.Display.W, H: l.rep.Agent.Display.H},
			StartedAt:  l.started.UTC().Format(time.RFC3339),
		}
	}
}

// serve is a session once the VM is up: the model works on the person's tasks, the viewer shows it, and the session ends in one
// of the ways PLAN.md section 11 lists.
func (r *runner) serve(l *live, setLink func(*Link)) (outcome error) {
	cfg, killer, mach := l.cfg, l.killer, l.mach
	// Whatever goes wrong before the session is under way ends the VM with it: it is running, and nothing else would stop it.
	started := false
	defer func() {
		if outcome != nil && !started && !killer.Killed() {
			killer.Trigger(killswitch.SourceInternal, "the session could not start: "+textclean.Line(outcome.Error(), 200))
		}
	}()
	prof := safety.ProfileFor(cfg)
	tally := &Tally{Next: l.log}
	fatal := func(err error) { killer.TriggerAsync(killswitch.SourceAudit, err.Error()) }
	headless := cfg.Viewer.Mode == viewer.ModeNone

	var vs *viewerSide
	var sink Sink
	if !headless {
		var err error
		vs, err = newViewerSide(viewerArgs{Killer: killer, Token: l.token, Hello: r.hello(l, prof), Audit: func(rc audit.Record) {
			if _, err := tally.Append(rc); err != nil {
				fatal(err)
			}
		}})
		if err != nil {
			return err
		}
		sink = vs.hub
	}

	guest := prompt.Size{W: l.rep.Agent.Display.W, H: l.rep.Agent.Display.H}
	sess, err := Assemble(Parts{Cfg: cfg, Profile: prof, Model: l.model, Agent: mach.Agent(), Monitor: mach.Monitor(), Guest: guest, User: l.rep.Agent.User,
		Capabilities: l.rep.Agent.Capabilities, SlowBy: l.rep.Accel.TimeoutFactor(), Dir: l.sessionDir, Machine: l.sm, Viewer: sink, Audit: tally,
		Killed: killer.Killed, Fatal: fatal, Rules: l.rules})
	if err != nil {
		return err
	}
	setLink(sess.Link)
	if r.deps.assembled != nil {
		r.deps.assembled(sess)
	}
	if err := l.rec(audit.Record{Kind: audit.KindLLM, Event: "prompt", Actor: "system", Data: audit.ToRaw(map[string]any{
		"template_sha256": sess.System.TemplateSHA256, "text_sha256": sess.System.TextSHA256, "version": sess.System.Version,
		"tool_mode": string(l.model.Setup.Mode), "tools": l.model.Setup.Profile, "vision": l.model.Setup.Vision, "coords": l.model.Setup.Coords,
	})}); err != nil {
		killer.Trigger(killswitch.SourceAudit, err.Error())
		return err
	}
	sess.Console.Attach()

	// The session is ready for a task.
	if err := l.sm.To(Handshake, "the agent answered"); err != nil {
		return err
	}
	if err := l.sm.To(Ready, "ready for a task"); err != nil {
		return err
	}
	expired := make(chan Expiry, 1)
	sess.Watchdog.OnExpire = func(e Expiry) {
		select {
		case expired <- e:
		default:
		}
	}
	wdCtx, stopWatchdog := context.WithCancel(l.ctx)
	defer stopWatchdog()
	go sess.Watchdog.Run(wdCtx)

	tasks := make(chan string, 8)
	sess.Console.StartTask = func(text string) {
		select {
		case tasks <- text:
		default: // eight tasks are waiting already
		}
	}
	if cfg.Session.Task != "" {
		tasks <- cfg.Session.Task
	}

	// The viewer.
	var win *viewer.Window
	var deadman *viewer.Deadman
	if vs != nil {
		pump, err := viewer.NewPump(viewer.PumpOptions{Agent: sess.Link, Sink: vs.hub, Quality: cfg.Viewer.WebPQuality,
			ActiveFPS: float64(cfg.Viewer.FPSActive), IdleFPS: float64(cfg.Viewer.FPSIdle)})
		if err != nil {
			return err
		}
		sess.Dispatcher.Activity = func(starting bool) {
			if starting {
				pump.NoteAction()
			} else {
				pump.Activity()
			}
		}
		sess.Loop.Answered = pump.NoteLLM
		if deadman, err = NewDeadman(sess.Console, viewer.KillOnExpiry(killer, viewer.DefaultEndAfter), viewer.DeadmanOptions{AfterFunc: r.deps.afterFunc}); err != nil {
			return err
		}
		vs.in.set(sess.Console)
		vs.pres.add(pump.OnConnect, pump.OnDisconnect)
		vs.pres.add(sess.Console.Connected, sess.Console.Disconnected)
		vs.pres.add(deadman.Connected, deadman.Disconnected)
		for _, n := range l.model.Notices {
			vs.hub.Send(viewer.Notice{Level: viewer.LevelWarn, Text: n})
		}
		if err := vs.srv.Start(l.ctx); err != nil {
			return err
		}
		authURL, err := vs.srv.AuthURL()
		if err != nil {
			return err
		}
		if win, err = r.deps.launch(viewer.LaunchOptions{Mode: cfg.Viewer.Mode, URL: authURL, ProfileDir: filepath.Join(l.sessionDir, "viewer-profile")}); err != nil {
			return err
		}
		fmt.Fprintln(r.out, win.Notice)
		_ = killer.AddHook("viewer window", win.Kill)
		deadman.Arm() // a window that never connects ends the session as one that goes away does
	}

	// The network port, with its policy.
	bridgeCtx, stopBridge := context.WithCancel(l.ctx)
	defer stopBridge()
	if conn, redial := mach.NetPort(); conn != nil {
		pol, err := egress.New(egress.Config{ExtraPorts: cfg.Net.AllowPorts, AllowDomains: cfg.Net.AllowDomains, DenyDomains: cfg.Net.DenyDomains})
		if err != nil {
			return exitcode.Usagef("net: %v", err)
		}
		bridge := &netbridge.Bridge{Dialer: pol, Tracker: egress.NewTracker(egress.DefaultLimits()), Audit: func(e netbridge.Event) {
			if _, err := tally.Append(e.Record()); err != nil {
				fatal(err)
			}
		}}
		go func() { _ = bridge.Run(bridgeCtx, conn, redial) }()
	}

	if headless {
		fmt.Fprintf(r.out, "Running the task: %s\n", textclean.Line(cfg.Session.Task, 200))
	} else {
		fmt.Fprintln(r.out, "Ready. Give the model a task in the viewer. Press Ctrl+C to stop, or run `controlsnack kill` from another terminal.")
	}

	var (
		running bool
		ended   = make(chan taskEnd, 1)
		why     string // why the session is ending, when it is by its own decision
		cause   error  // what the exit code says
	)
	startTask := func(text string) {
		running = true
		ctxT, endT := sess.Console.Begin(l.ctx)
		go func() {
			out := sess.Loop.Run(ctxT, text)
			endT()
			ended <- taskEnd{out: out, text: text}
		}()
	}

	started = true

loop:
	for {
		var next <-chan string
		if !running {
			next = tasks
		}
		select {
		case text := <-next:
			startTask(text)
		case te := <-ended:
			running = false
			r.report(sink, te)
			switch te.out.End {
			case EndDone, EndStopped, EndFailed:
				if headless {
					why = "task " + string(te.out.End)
					switch {
					case te.out.End == EndDone && te.out.Success:
					case te.out.End == EndDone:
						cause = exitcode.New(exitcode.Failure, fmt.Errorf("the model gave up: %s", textclean.Line(te.out.Summary, 300)))
					case te.out.End == EndFailed:
						cause = exitcode.New(exitcode.Failure, te.out.Err)
					default:
						cause = exitcode.New(exitcode.Failure, errors.New("the task was stopped"))
					}
					break loop
				}
			case EndTimeout:
				why, cause = "session timeout", exitcode.New(exitcode.Timeout, fmt.Errorf("the session timeout of %s passed", prof.SessionTimeout))
				break loop
			case EndAudit:
				killer.Trigger(killswitch.SourceAudit, textclean.Line(fmt.Sprint(te.out.Err), 300))
				break loop
			case EndKilled, EndSession:
				break loop
			}
		case e := <-expired:
			switch e {
			case ExpiredSession:
				why, cause = "session timeout", exitcode.New(exitcode.Timeout, fmt.Errorf("the session timeout of %s passed", prof.SessionTimeout))
			default:
				why = "idle timeout"
				fmt.Fprintf(r.out, "Nobody did anything for %s, so the session ends.\n", prof.IdleTimeout)
			}
			break loop
		case <-mach.Proc().Exited():
			if !killer.Killed() {
				why = "the guest shut down"
			}
			break loop
		case <-l.ctx.Done():
			break loop
		}
	}

	// The end. A kill (the person's, the dead-man's, a signal, a log that cannot be written) is the kill switch's; everything else
	// is an orderly stop.
	if !killer.Killed() && why == "" {
		killer.Trigger(killswitch.SourceSignal, "interrupted")
	}
	if killer.Killed() {
		stopWatchdog()
		return r.killed(l, deadman, tally)
	}
	return r.orderly(l, sess, win, deadman, vs, tally, why, cause, &running, ended)
}

// report tells the person how a task came out.
func (r *runner) report(sink Sink, te taskEnd) {
	out := te.out
	var text string
	level := viewer.LevelInfo
	switch out.End {
	case EndDone:
		if out.Success {
			text = fmt.Sprintf("Task finished in %d turns: %s", out.Turns, textclean.Line(out.Summary, 300))
		} else {
			level = viewer.LevelWarn
			text = fmt.Sprintf("The model gave up after %d turns: %s", out.Turns, textclean.Line(out.Summary, 300))
		}
	case EndStopped:
		text = fmt.Sprintf("Task stopped after %d turns.", out.Turns)
	case EndFailed:
		level = viewer.LevelError
		text = fmt.Sprintf("Task failed after %d turns: %s", out.Turns, textclean.Line(fmt.Sprint(out.Err), 300))
	default:
		return
	}
	fmt.Fprintln(r.out, text)
	if sink != nil {
		sink.Send(viewer.Notice{Level: level, Text: text})
	}
}

// summary is the line at the end of a session.
func (r *runner) summary(l *live, t *Tally, reason string) string {
	tot := t.Totals()
	rep := l.rep
	acc := rep.Accel.Chosen
	if rep.Fallback != nil {
		acc += fmt.Sprintf(" (%s failed: %s)", rep.Fallback.From, textclean.Line(rep.Fallback.Reason, 120))
	}
	line := fmt.Sprintf("Session %s ended (%s) after %s: %d turns, %d actions (%s). Accelerator: %s. Audit log: %s",
		l.id, reason, time.Since(l.started).Round(time.Second), tot.Answers, tot.Actions, tot, acc, l.log.Path())
	if l.keptVM != "" {
		line += fmt.Sprintf(". Kept VM: %s (resume it with: controlsnack run --vm %s)", l.keptVM, l.keptVM)
	}
	return line
}

// killed is the end of a session that the kill switch ended.
func (r *runner) killed(l *live, dm *viewer.Deadman, t *Tally) error {
	if dm != nil {
		dm.Stop()
	}
	<-l.killer.Done()
	res, _ := l.killer.Result()
	if res.QEMUKilled {
		fmt.Fprintf(r.out, "The VM was stopped %d ms after the kill (%s).\n", res.QEMUGoneAfter.Milliseconds(), res.Source)
	}
	_ = l.rec(audit.Record{Kind: audit.KindLifecycle, Event: audit.EventKill, Actor: "system", Reason: res.Reason, Data: audit.ToRaw(map[string]any{
		"source": res.Source, "qemu_killed": res.QEMUKilled, "qemu_gone_ms": res.QEMUGoneAfter.Milliseconds(), "teardown_ms": res.TeardownTook.Milliseconds(),
	})})
	line := r.summary(l, t, "killed: "+string(res.Source))
	fmt.Fprintln(r.out, line)
	l.end(map[string]any{"reason": "killed", "source": res.Source, "summary": line})
	if !res.QEMUKilled || res.QEMUErr != nil {
		cause := res.QEMUErr
		if cause == nil {
			cause = errors.New("the kill did not reach the VM")
		}
		return fmt.Errorf("the VM may still be running: %w", cause)
	}
	return exitcode.New(exitcode.Killed, fmt.Errorf("stopped (%s)", res.Source))
}

// orderly is the end of a session that ends by its own decision: the task is stopped, the viewer closed, the VM asked to stop.
func (r *runner) orderly(l *live, sess *Session, win *viewer.Window, dm *viewer.Deadman, vs *viewerSide, t *Tally, why string, cause error,
	running *bool, ended chan taskEnd) error {
	_ = l.sm.To(Ending, why)
	if dm != nil {
		dm.Stop()
	}
	sess.Console.Stop() // ends the task that is running, if one is
	if *running {
		select {
		case <-ended:
		case <-time.After(gracePeriod):
		}
	}
	line := r.summary(l, t, why)
	if vs != nil {
		vs.hub.Send(viewer.Ended{Reason: why, Summary: line})
		time.Sleep(100 * time.Millisecond) // let the page read it before the connection goes
		_ = vs.srv.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), gracePeriod)
	defer cancel()
	if win != nil {
		_ = win.Kill(ctx)
	}
	err := l.mach.Close(ctx)
	_ = l.teardown(ctx)
	_ = l.sm.To(Ended, why)
	fmt.Fprintln(r.out, line)
	l.end(map[string]any{"reason": why, "summary": line})
	switch {
	case cause != nil:
		return cause
	case err != nil && !strings.Contains(err.Error(), "signal"):
		return fmt.Errorf("stopping the VM: %w", err)
	}
	return nil
}
