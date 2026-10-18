// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/internal/platform"
	"github.com/PacifAIst/controlsnack-local/internal/safety/audit"
	"github.com/PacifAIst/controlsnack-local/internal/vm"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentclient"
	"github.com/PacifAIst/controlsnack-local/internal/vm/image"
	"github.com/PacifAIst/controlsnack-local/internal/vm/overlay"
)

func (f *fullRig) library() overlay.Library { return testLibrary(f.dirs) }

func (f *fullRig) base() image.Image {
	im, err := f.r.deps.image(f.dirs, "", "x86_64")
	if err != nil {
		f.t.Fatal(err)
	}
	return im
}

func TestAVmKeptUnderANameStaysAndIsRunOnItsOwnDisk(t *testing.T) {
	f := newFullRig(t)
	f.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	cfg := headlessConfig("Make a file")
	cfg.Session.KeepVM = "workbench"
	if err := f.run(context.Background(), cfg); err != nil {
		t.Fatalf("%v\n%s", err, f.out.String())
	}
	vms, _, err := f.library().List()
	if err != nil || len(vms) != 1 || vms[0].Name != "workbench" {
		t.Fatalf("kept VMs %+v (%v)", vms, err)
	}
	// The VM ran on that disk, with the cache a disk that stays needs, and was free for the next session at the end.
	f.mu.Lock()
	o := f.options[0]
	f.mu.Unlock()
	prep, err := o.Prepare(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer prep.Cleanup()
	if prep.Spec.Disk != vms[0].Disk || prep.Spec.Cache != "writeback" {
		t.Errorf("disk %q cache %q, want %q and writeback", prep.Spec.Disk, prep.Spec.Cache, vms[0].Disk)
	}
	found := false
	for _, d := range prep.Spec.AllowedDirs {
		found = found || d == vms[0].Dir
	}
	if !found {
		t.Errorf("QEMU may not touch the kept VM's directory: %v", prep.Spec.AllowedDirs)
	}
	if !strings.Contains(f.out.String(), "Kept VM: workbench (resume it with: controlsnack run --vm workbench)") {
		t.Errorf("the output:\n%s", f.out.String())
	}
	start := recordsOf(f.auditRecords(), "lifecycle", "start")
	if len(start) != 1 || !strings.Contains(string(start[0].Data), `"kept_vm":"workbench"`) || !strings.Contains(string(start[0].Data), `"resumed":false`) {
		t.Errorf("start %+v", start)
	}
	if !vms[0].LastUsed.After(vms[0].Created) {
		t.Errorf("the VM was not marked as used: %+v", vms[0])
	}
	if _, err := f.library().Acquire("workbench"); err != nil {
		t.Errorf("the VM is still in use after the session: %v", err)
	}
}

func TestABareKeepVmIsGivenANameAndAKeptVmCanBeResumed(t *testing.T) {
	f := newFullRig(t)
	f.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	cfg := headlessConfig("Make a file")
	cfg.Session.KeepVM = "auto"
	if err := f.run(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	vms, _, _ := f.library().List()
	if len(vms) != 1 || !strings.HasPrefix(vms[0].Name, "vm-") || len(vms[0].Name) != 11 {
		t.Fatalf("kept VMs %+v", vms)
	}
	name := vms[0].Name

	// And now that VM is resumed.
	g := newFullRig(t)
	g.r.deps.dirs, g.r.deps.image, g.r.deps.random = f.r.deps.dirs, f.r.deps.image, f.r.deps.random
	g.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"again"}`))}
	cfg2 := headlessConfig("Go on")
	cfg2.Session.VM = name
	if err := g.run(context.Background(), cfg2); err != nil {
		t.Fatalf("%v\n%s", err, g.out.String())
	}
	start := startsIn(t, f.dirs)
	if len(start) != 2 || !strings.Contains(string(start[1].Data), `"resumed":true`) {
		t.Errorf("start %+v", start)
	}
}

func TestAKeptVmThatCannotBeUsedIsRefusedBeforeAnythingStarts(t *testing.T) {
	f := newFullRig(t)
	cfg := headlessConfig("Anything")
	cfg.Session.VM = "nothing-by-that-name"
	if err := f.run(context.Background(), cfg); err == nil || f.boots() != 0 {
		t.Errorf("error %v, %d boots", err, f.boots())
	}
	// A VM made from another image.
	other := f.base()
	other.Manifest.ID = "0123456789ab"
	if _, err := f.library().Create(context.Background(), "qemu-img", "old", other, "test"); err != nil {
		t.Fatal(err)
	}
	cfg.Session.VM = "old"
	err := f.run(context.Background(), cfg)
	var mm *overlay.BaseMismatchError
	if !errors.As(err, &mm) || f.boots() != 0 {
		t.Errorf("error %v, %d boots", err, f.boots())
	}
	// A name that is already taken.
	cfg = headlessConfig("Anything")
	cfg.Session.KeepVM = "old"
	if err := f.run(context.Background(), cfg); err == nil || f.boots() != 0 {
		t.Errorf("a name already taken: %v", err)
	}
	// A VM that another session is using.
	if _, err := f.library().Create(context.Background(), "qemu-img", "busy", f.base(), "test"); err != nil {
		t.Fatal(err)
	}
	release, err := f.library().Acquire("busy")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cfg = headlessConfig("Anything")
	cfg.Session.VM = "busy"
	if err := f.run(context.Background(), cfg); err == nil || f.boots() != 0 {
		t.Errorf("a VM in use: %v", err)
	}
}

func TestAKeptVmThatNeverStartedIsNotKept(t *testing.T) {
	f := newFullRig(t)
	f.bootErr = errors.New("QEMU could not start")
	cfg := headlessConfig("Anything")
	cfg.Session.KeepVM = "workbench"
	if err := f.run(context.Background(), cfg); err == nil {
		t.Fatal("a session started although the VM could not")
	}
	if vms, _, _ := f.library().List(); len(vms) != 0 {
		t.Errorf("a VM that never ran was kept: %+v", vms)
	}
	// A VM that was kept before is not removed because a later start failed.
	g := newFullRig(t)
	if _, err := g.library().Create(context.Background(), "qemu-img", "precious", g.base(), "test"); err != nil {
		t.Fatal(err)
	}
	g.bootErr = errors.New("QEMU could not start")
	cfg.Session.KeepVM, cfg.Session.VM = "", "precious"
	_ = g.run(context.Background(), cfg)
	if vms, _, _ := g.library().List(); len(vms) != 1 {
		t.Errorf("the VM that was resumed is gone: %+v", vms)
	}
}

func TestAKillStillKeepsTheVm(t *testing.T) {
	f := newFullRig(t)
	f.model.replies = []reply{{hook: func(ctx context.Context) { <-ctx.Done() }, err: context.Canceled}}
	cfg := headlessConfig("Wait")
	cfg.Session.KeepVM = "workbench"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.run(ctx, cfg) }()
	eventually(t, "the model to be asked", func() bool { return len(f.model.requests()) == 1 })
	cancel()
	select {
	case err := <-done:
		if exitcode.Of(err) != exitcode.Killed {
			t.Errorf("error %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end")
	}
	if vms, _, _ := f.library().List(); len(vms) != 1 {
		t.Errorf("kept VMs %+v", vms)
	}
	if n := f.mach.shutdowns.Load(); n != 0 {
		t.Errorf("a kill does not wait for the guest: the power button was pressed %d times", n)
	}
}

// startsIn reads the start records of every session log in the directories.
func startsIn(t *testing.T, dirs platform.Dirs) []audit.Record {
	t.Helper()
	entries, err := os.ReadDir(dirs.AuditDir())
	if err != nil {
		t.Fatal(err)
	}
	var out []audit.Record
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dirs.AuditDir(), e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var r audit.Record
			if json.Unmarshal([]byte(ln), &r) == nil && r.Kind == "lifecycle" && r.Event == "start" {
				out = append(out, r)
			}
		}
	}
	return out
}

func TestTheGuestOfAKeptVmIsAskedToPowerOffBeforeQEMUIsEnded(t *testing.T) {
	old := keptShutdown
	keptShutdown = 3 * time.Second
	defer func() { keptShutdown = old }()
	f := newFullRig(t)
	f.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	cfg := headlessConfig("Make a file")
	cfg.Session.KeepVM = "workbench"
	if err := f.run(context.Background(), cfg); err != nil {
		t.Fatalf("%v\n%s", err, f.out.String())
	}
	if n := f.mach.shutdowns.Load(); n != 1 {
		t.Fatalf("the power button was pressed %d times", n)
	}
	if f.mach.shutdownAfterClose.Load() || !f.mach.closed.Load() {
		t.Errorf("the guest is asked first and QEMU ended after (asked after close: %v, closed: %v)", f.mach.shutdownAfterClose.Load(), f.mach.closed.Load())
	}
	if given := time.Duration(f.mach.shutdownGiven.Load()); given < 2*time.Second || given > 3*time.Second {
		t.Errorf("the guest was given %s", given)
	}
	if out := f.out.String(); strings.Contains(out, "did not power off") || !strings.Contains(out, "The guest of the kept VM workbench powered off in ") {
		t.Errorf("a guest that powered off is not a warning, and the person hears that it did:\n%s", out)
	}
}

func TestAVmThatIsNotKeptIsNotAskedToPowerOff(t *testing.T) {
	f := newFullRig(t)
	f.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	if err := f.run(context.Background(), headlessConfig("Anything")); err != nil {
		t.Fatal(err)
	}
	if n := f.mach.shutdowns.Load(); n != 0 || !f.mach.closed.Load() {
		t.Errorf("%d presses of the power button; closed %v", n, f.mach.closed.Load())
	}
}

func TestAGuestThatDoesNotPowerOffIsStoppedAndThePersonIsTold(t *testing.T) {
	f := newFullRig(t)
	f.mach.shutdownErr = context.DeadlineExceeded
	f.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	cfg := headlessConfig("Make a file")
	cfg.Session.KeepVM = "workbench"
	if err := f.run(context.Background(), cfg); err != nil {
		t.Fatalf("a guest that does not power off is not a failure of the task: %v", err)
	}
	if !f.mach.closed.Load() {
		t.Error("QEMU was not ended")
	}
	if out := f.out.String(); !strings.Contains(out, "WARNING: the guest of the kept VM workbench did not power off (context deadline exceeded)") || !strings.Contains(out, "power cut") || strings.Contains(out, "powered off in") {
		t.Errorf("the person is told it failed, and not also that it worked:\n%s", out)
	}
	if vms, _, _ := f.library().List(); len(vms) != 1 {
		t.Errorf("the VM is kept all the same: %+v", vms)
	}
}

func TestAKeptGuestHasTwentySecondsToPowerOffAndWhatItSaysIsCleaned(t *testing.T) {
	if keptShutdown != 20*time.Second {
		t.Errorf("the guest of a kept VM is given %s", keptShutdown)
	}
	f := newFullRig(t)
	f.mach.shutdownErr = errors.New("odd\x1b[2J news")
	f.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	cfg := headlessConfig("Make a file")
	cfg.Session.KeepVM = "workbench"
	if err := f.run(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if out := f.out.String(); strings.Contains(out, "\x1b") || !strings.Contains(out, "odd[2J news") {
		t.Errorf("the output:\n%q", out)
	}
}

func TestTheAgentGoingDownWithTheGuestAtTheEndIsNotNews(t *testing.T) {
	f := newFullRig(t)
	var stateFn func(agentclient.State, error)
	boot := f.r.deps.boot
	f.r.deps.boot = func(ctx context.Context, o vm.BootOptions) (machine, error) {
		stateFn = o.OnAgentState
		return boot(ctx, o)
	}
	// As the guest powers off, the agent's connection ends.
	f.mach.onShutdown = func() { stateFn(agentclient.StateDown, errors.New("EOF")) }
	f.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	cfg := headlessConfig("Make a file")
	cfg.Session.KeepVM = "workbench"
	if err := f.run(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	if !strings.Contains(out, "powered off in") || strings.Contains(out, "agent: connection lost") || strings.Contains(out, "not answering") {
		t.Errorf("the output:\n%s", out)
	}
}
