// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package vm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
)

// realRig finds the QEMU of this machine (the test is skipped without one) and builds a PrepareFunc that gives
// every attempt a fresh blank disk and fresh sockets, the way the session will.
type realRig struct {
	inst    qemu.Install
	host    qemu.OSHost
	session string // the session directory of the latest attempt
}

func newRealRig(t *testing.T) *realRig {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	inst, err := qemu.Discover(ctx, qemu.OSHost{}, qemu.Options{})
	if errors.Is(err, qemu.ErrNotFound) {
		t.Skip("no QEMU installed here")
	}
	if err != nil {
		t.Fatal(err)
	}
	if inst.Arch != qemu.ArchX86_64 {
		t.Skip("the real launch test is written for x86_64 hosts")
	}
	return &realRig{inst: inst}
}

func (r *realRig) prepare(t *testing.T) PrepareFunc {
	return func(ctx context.Context, n int) (Prepared, error) {
		session, err := os.MkdirTemp("", "css-")
		if err != nil {
			return Prepared{}, err
		}
		sockDir, cleanSock, err := qemu.NewSocketDir()
		if err != nil {
			return Prepared{}, err
		}
		disk := filepath.Join(session, "disk.qcow2")
		if out, err := exec.CommandContext(ctx, r.inst.Img, "create", "-f", "qcow2", disk, "256M").CombinedOutput(); err != nil { //nolint:gosec // discovered qemu-img
			return Prepared{}, errors.New("qemu-img: " + string(out))
		}
		eps, err := qemu.AllocateEndpoints(ctx, runtime.GOOS, sockDir, "agent0", "qmp0")
		if err != nil {
			return Prepared{}, err
		}
		r.session = session
		return Prepared{
			Dir: session,
			Spec: qemu.RuntimeSpec{
				Install: r.inst, Name: "real", CPUs: 2, MemoryMiB: 512, Width: 1024, Height: 768,
				Disk: disk, Serial: filepath.Join(session, "serial.log"), Agent: eps[0], QMP: eps[1],
				AllowedDirs: []string{session, sockDir},
			},
			Cleanup: func() { _ = os.RemoveAll(session); cleanSock() },
		}, nil
	}
}

func (r *realRig) options(t *testing.T, accel string, slow bool) LaunchOptions {
	probe := NewHostProbe(r.host, r.inst)
	return LaunchOptions{
		Resolve: func(ctx context.Context) (AccelResult, error) { return ResolveAccel(ctx, probe, accel, slow) },
		Prepare: r.prepare(t), Backend: RealBackend(), Stable: 2 * time.Second,
	}
}

// TestLaunchRealQEMUWithTCG is the first run of the command line the builders produce, against the real program:
// every option and device must be accepted, the chardevs must be served in turn (wait=on), the VM must run for the
// stability period, and the control channel must work. A blank disk is enough for all of that; the BIOS just
// reports that there is nothing to boot.
func TestLaunchRealQEMUWithTCG(t *testing.T) {
	r := newRealRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	start := time.Now()
	vm, err := Launch(ctx, r.options(t, "", true))
	if err != nil {
		t.Fatalf("%v", err)
	}
	t.Logf("launched in %v with %s; sandbox=%v\nargv: %s", time.Since(start).Round(time.Millisecond), vm.Accel.Chosen, r.inst.Sandbox, strings.Join(vm.Argv, " "))
	if vm.Accel.Chosen != AccelTCG || !vm.Accel.Slow || vm.Fallback != nil || vm.Accel.Requested != "slow" {
		t.Errorf("%+v %+v", vm.Accel, vm.Fallback)
	}
	if vm.Agent == nil || vm.Net != nil {
		t.Error("the agent port is connected, the net port is not there")
	}

	st, err := vm.QMP.QueryStatus(ctx)
	if err != nil || !st.Running {
		t.Errorf("%+v %v", st, err)
	}
	shot := filepath.Join(r.session, "shot.png")
	if err := vm.QMP.Screendump(ctx, shot, "png"); err != nil {
		t.Fatalf("screendump: %v", err)
	}
	png, err := os.ReadFile(shot)
	if err != nil || len(png) < 100 || string(png[1:4]) != "PNG" {
		t.Errorf("screendump did not produce a PNG: %d bytes, %v", len(png), err)
	}
	t.Logf("screendump: %d bytes of PNG", len(png))

	stop := time.Now()
	if err := vm.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("stopped in %v", time.Since(stop).Round(time.Millisecond))
	if code, err := vm.Process.Result(); code != 0 || err != nil {
		t.Errorf("a polite stop ends QEMU cleanly: %d %v\n%s", code, err, vm.Process.Output())
	}
	if _, err := os.Stat(r.session); !os.IsNotExist(err) {
		t.Error("the session directory is removed with the VM")
	}
}

// TestLaunchRealQEMUKillIsFast measures the kill switch path on a real VM.
func TestLaunchRealQEMUKillIsFast(t *testing.T) {
	r := newRealRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	o := r.options(t, "", true)
	o.Stable = time.Second
	vm, err := Launch(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_ = vm.Kill()
	d := time.Since(start)
	t.Logf("kill -> a real VM is gone: %v", d.Round(time.Millisecond))
	if d > 500*time.Millisecond {
		t.Errorf("kill took %v; the target of PLAN.md section 6.8 is under 500 ms", d)
	}
}

// TestLaunchRealQEMUExplicitKVM asks for kvm. Where /dev/kvm is usable the VM runs on it; where it is not (this
// machine's user is not in the kvm group), QEMU refuses and the single retry with tcg must take over, with the
// real reason and QEMU's own words recorded.
func TestLaunchRealQEMUExplicitKVM(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("kvm is Linux only")
	}
	r := newRealRig(t)
	if !r.inst.HasAccel(AccelKVM) {
		t.Skip("this QEMU has no kvm")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ev := &events{}
	o := r.options(t, AccelKVM, false)
	o.Events = ev
	vm, err := Launch(ctx, o)
	if err != nil {
		t.Fatalf("%v", err)
	}
	defer func() { _ = vm.Kill() }()
	if vm.Fallback == nil {
		t.Logf("kvm works here: chosen %s", vm.Accel.Chosen)
		if vm.Accel.Chosen != AccelKVM {
			t.Errorf("%+v", vm.Accel)
		}
		return
	}
	t.Logf("kvm refused, fell back to tcg\n  reason: %s\n  QEMU said: %s", vm.Fallback.Reason, vm.Fallback.Stderr)
	if vm.Accel.Chosen != AccelTCG || vm.Fallback.From != AccelKVM || vm.Accel.Requested != AccelKVM {
		t.Errorf("%+v %+v", vm.Accel, vm.Fallback)
	}
	if !strings.Contains(vm.Fallback.Stderr, "KVM") && !strings.Contains(strings.ToLower(vm.Fallback.Stderr), "kvm") {
		t.Errorf("QEMU's own explanation must be kept: %q", vm.Fallback.Stderr)
	}
	if len(ev.fallbacks) != 1 || len(ev.selected) != 1 {
		t.Errorf("events: %d selected, %d fallbacks", len(ev.selected), len(ev.fallbacks))
	}
	st, err := vm.QMP.QueryStatus(ctx)
	if err != nil || !st.Running {
		t.Errorf("the fallback VM runs: %+v %v", st, err)
	}
}
