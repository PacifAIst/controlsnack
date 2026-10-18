// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/PacifAIst/controlsnack-local/guest"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentbin"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentclient"
)

// Timeouts of Boot that are not part of Launch; like the launch window they are multiplied by the accelerator's
// TimeoutFactor (four under tcg).
const (
	// ConnectWindow is how long pushing the agent and the handshake may take once the guest has opened the port.
	ConnectWindow = 90 * time.Second
	// HeartbeatInterval is the agent heartbeat period (PLAN.md section 5.1).
	HeartbeatInterval = 2 * time.Second
)

// BootOptions configure Boot.
type BootOptions struct {
	Prepare PrepareFunc
	// Resolve picks the accelerator (see LaunchOptions).
	Resolve func(ctx context.Context) (AccelResult, error)
	Backend Backend
	Events  Events

	Agent       agentbin.Agent
	HostVersion string
	SessionID   string
	Features    protocol.Features

	// Window and Stable are passed to Launch; PortWindow is how long the guest has to open the agent port after QEMU is
	// running (default BootCheckTimeout) and ConnectWindow how long the install and handshake may take. All are
	// multiplied by the accelerator's TimeoutFactor.
	Window, Stable, PortWindow, ConnectWindow time.Duration

	// OnAgentState and OnAgentEvent see what the agent connection does for as long as the machine lives.
	OnAgentState func(agentclient.State, error)
	OnAgentEvent func(protocol.EventBody)
	// Progress receives one human-readable line per stage.
	Progress func(format string, args ...any)
	Log      *slog.Logger
}

// Timings say where the time of a boot went.
type Timings struct {
	Launch    time.Duration // QEMU running and stable
	PortOpen  time.Duration // since the start: the guest opened the agent port (kernel, desktop and stub are up)
	Connected time.Duration // since the start: the agent is installed and has answered the handshake
}

// Machine is a running VM with a connected agent.
type Machine struct {
	*VM
	Client  *agentclient.Client
	Ack     protocol.HelloAck
	Timings Timings

	stopSupervise context.CancelFunc
	superDone     chan struct{}
	closeOnce     sync.Once
	redialNet     func(context.Context) (net.Conn, error)
}

// ErrNoNetPort means the session has no network port: it was not started with --allow-net.
var ErrNoNetPort = errors.New("the session has no network port (it was not started with --allow-net)")

// DialNet connects to the network port again, for the bridge to use when the connection it had has ended. The first
// connection is Machine.Net, which Launch made before the guest started.
func (m *Machine) DialNet(ctx context.Context) (net.Conn, error) {
	if m.redialNet == nil {
		return nil, ErrNoNetPort
	}
	return m.redialNet(ctx)
}

// Boot launches the VM, waits until the guest has started its desktop session and the bootstrap stub has opened the
// agent port, installs the agent, completes the handshake and keeps the connection alive. On any failure the VM is
// killed and nothing is left running.
func Boot(ctx context.Context, o BootOptions) (*Machine, error) {
	if o.Progress == nil {
		o.Progress = func(string, ...any) {}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.PortWindow <= 0 {
		o.PortWindow = BootCheckTimeout
	}
	if o.ConnectWindow <= 0 {
		o.ConnectWindow = ConnectWindow
	}

	begin := time.Now()
	v, err := Launch(ctx, LaunchOptions{Resolve: o.Resolve, Prepare: o.Prepare, Backend: o.Backend, Events: o.Events,
		Window: o.Window, Stable: o.Stable})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Machine, error) {
		_ = v.Kill()
		return nil, err
	}
	factor := time.Duration(v.Accel.TimeoutFactor())
	t := Timings{Launch: time.Since(begin)}
	o.Progress("QEMU is running with %s (%s)", v.Accel.Chosen, t.Launch.Round(time.Millisecond))
	if v.Fallback != nil {
		o.Progress("%s did not start, so this session runs with software emulation and will be slow: %s", v.Fallback.From, v.Fallback.Reason)
	}

	// The guest opens the agent port when its desktop session has started the stub; QEMU dying first ends the wait.
	o.Progress("waiting for the desktop to start (up to %s)", (o.PortWindow * factor).Round(time.Second))
	pctx, cancel := context.WithTimeout(ctx, o.PortWindow*factor)
	defer cancel()
	go func() {
		select {
		case <-v.Process.Exited():
			cancel()
		case <-pctx.Done():
		}
	}()
	if err := v.QMP.WaitPortOpen(pctx, "agent0"); err != nil {
		return fail(bootError(ctx, v, "the guest did not open the agent port", o.PortWindow*factor, err))
	}
	t.PortOpen = time.Since(begin)
	o.Progress("the desktop is up and the bootstrap is waiting (%s)", t.PortOpen.Round(time.Second))

	var mu sync.Mutex
	first := true
	dial := func(ctx context.Context) (net.Conn, error) {
		mu.Lock()
		useHeld := first
		first = false
		mu.Unlock()
		if useHeld {
			return v.Agent, nil // already connected by Launch: QEMU waits for it before it starts the guest
		}
		return o.Backend.DialStream(ctx, v.Spec.Agent)
	}
	client, err := agentclient.New(agentclient.Options{
		Dial: dial, Agent: o.Agent, HostVersion: o.HostVersion, SessionID: o.SessionID, Features: o.Features,
		User: guest.User, Required: []string{protocol.CapPing},
		Interval: HeartbeatInterval * factor, InstallTimeout: 60 * time.Second * factor, HelloTimeout: 10 * time.Second * factor,
		OnState: o.OnAgentState, OnEvent: o.OnAgentEvent, Log: o.Log,
	})
	if err != nil {
		return fail(err)
	}
	o.Progress("installing the guest agent (%s, %d bytes)", o.Agent.Arch, len(o.Agent.Binary))
	cctx, cancelConnect := context.WithTimeout(ctx, o.ConnectWindow*factor)
	defer cancelConnect()
	if err := client.Connect(cctx); err != nil {
		_ = client.Close()
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		return fail(bootError(ctx, v, "the guest agent could not be started", o.ConnectWindow*factor, err))
	}
	ack, _ := client.Ack()
	t.Connected = time.Since(begin)

	sctx, stop := context.WithCancel(context.WithoutCancel(ctx))
	m := &Machine{VM: v, Client: client, Ack: ack, Timings: t, stopSupervise: stop, superDone: make(chan struct{})}
	if v.Spec.Net != nil {
		ep := *v.Spec.Net
		m.redialNet = func(ctx context.Context) (net.Conn, error) { return o.Backend.DialStream(ctx, ep) }
	}
	go func() {
		defer close(m.superDone)
		client.Supervise(sctx)
	}()
	go func() { // QEMU gone: nothing left to supervise
		select {
		case <-v.Process.Exited():
			stop()
		case <-sctx.Done():
		}
	}()
	return m, nil
}

// bootError explains a failed stage with what the guest last said on its console.
func bootError(ctx context.Context, v *VM, what string, wait time.Duration, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var detail string
	select {
	case <-v.Process.Exited():
		code, _ := v.Process.Result()
		detail = fmt.Sprintf("QEMU exited with status %d\nQEMU said:\n%s", code, tailExcerpt(v.Process.Output(), excerptBytes))
	default:
		detail = fmt.Sprintf("gave up after %s", wait.Round(time.Second))
	}
	return fmt.Errorf("%s: %w (%s)\nlast lines of the serial console:\n%s", what, cause, detail, tailFile(v.Spec.Serial, 12))
}

// Close stops the agent connection and shuts the VM down politely (QMP quit, then kill).
func (m *Machine) Close(ctx context.Context) error {
	var err error
	m.closeOnce.Do(func() {
		m.stop()
		err = m.Stop(ctx)
	})
	return err
}

// poweroffCommand is what the agent is asked to run to power the guest off: the file systems are written and the system goes down.
// The agent runs in the desktop user's session, which the system lets power the machine off.
const poweroffCommand = "sync; exec systemctl --no-block poweroff"

// Shutdown powers the guest off and waits for QEMU to exit, until ctx ends. It is how a VM whose disk is kept is ended: the guest
// writes what it has in memory before the power goes. The guest is asked through the agent, because the desktop's power manager
// answers the power button with a question that nobody is there to answer; only when the agent cannot be reached is the button
// pressed (QMP system_powerdown). An error means the guest did not power off in time; the caller goes on to Close, which ends QEMU.
func (m *Machine) Shutdown(ctx context.Context) error {
	asked, wasUp := false, m.Client.Up()
	if wasUp {
		actx, cancel := context.WithTimeout(ctx, 10*time.Second)
		var res protocol.ExecRes
		_, err := m.Client.Call(actx, protocol.TypeExec, protocol.ExecReq{Command: poweroffCommand, TimeoutMS: 10000}, nil, &res)
		cancel()
		asked = err == nil && res.ExitCode == 0
	}
	// From here the agent goes down with the guest: nothing is left to supervise or to report.
	m.stop()
	if !asked {
		if wasUp {
			// An answer that never came may be a guest that went down too fast to send it.
			if m.exitedWithin(ctx, shutdownGrace) {
				return nil
			}
		}
		if err := m.QMP.SystemPowerdown(ctx); err != nil {
			if m.exitedWithin(ctx, shutdownGrace) {
				return nil // the monitor went with the guest
			}
			return err
		}
	}
	select {
	case <-m.Process.Exited():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// shutdownGrace is how long Shutdown lets a guest that may already be going down show it before it tries the power button, or
// before it believes that the button could not be pressed.
var shutdownGrace = 2 * time.Second

// exitedWithin says whether QEMU has exited by the end of d (or of ctx).
func (m *Machine) exitedWithin(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-m.Process.Exited():
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// Kill ends the machine at once: the kill path (PLAN.md section 6.8).
func (m *Machine) Kill() error {
	var err error
	m.closeOnce.Do(func() {
		m.stop()
		err = m.VM.Kill()
	})
	return err
}

func (m *Machine) stop() {
	m.stopSupervise()
	_ = m.Client.Close()
	<-m.superDone
}

// BootReport is what a finished boot knows, for the terminal, the audit log and the viewer.
type BootReport struct {
	Accel    AccelResult
	Fallback *Fallback
	Agent    protocol.HelloAck
	Timings  Timings
	QEMUPID  int
}

// Report describes the machine as it booted.
func (m *Machine) Report() BootReport {
	return BootReport{Accel: m.Accel, Fallback: m.Fallback, Agent: m.Ack, Timings: m.Timings, QEMUPID: m.Process.Pid()}
}

// Proc is the QEMU process; the kill switch ends it.
func (m *Machine) Proc() Process { return m.Process }

// Screendump saves the guest's screen as a PNG, through QMP: it works whether or not the agent does.
func (m *Machine) Screendump(ctx context.Context, path string) error {
	return m.QMP.Screendump(ctx, path, "png")
}

// StopAgent ends the agent connection and its supervisor, leaving the VM as it is (the kill switch has ended or is
// ending QEMU itself).
func (m *Machine) StopAgent() { m.stop() }
