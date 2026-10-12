// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
)

// --- parsers, fed with samples from real machines ---------------------------------------------------------------

const cpuinfoIntel = `processor	: 0
vendor_id	: GenuineIntel
model name	: Intel(R) Core(TM) i7-10870H CPU @ 2.20GHz
flags		: fpu vme de pse tsc msr pae mce cx8 apic sep vmx ssbd
processor	: 1
model name	: Intel(R) Core(TM) i7-10870H CPU @ 2.20GHz
`

const cpuinfoWSL = `processor	: 0
model name	: Intel(R) Core(TM) i7-10870H CPU @ 2.20GHz
flags		: fpu vme de pse tsc msr pae mce cx8 apic sep hypervisor lahf_lm
`

func TestParseCPUInfo(t *testing.T) {
	if m, flag, vm := parseCPUInfo(cpuinfoIntel); m != "Intel(R) Core(TM) i7-10870H CPU @ 2.20GHz" || flag != "vmx" || vm {
		t.Errorf("%q %q %v", m, flag, vm)
	}
	if m, flag, vm := parseCPUInfo(cpuinfoWSL); m == "" || flag != "" || !vm {
		t.Errorf("a guest shows the hypervisor flag and no vmx: %q %q %v", m, flag, vm)
	}
	if _, flag, _ := parseCPUInfo("flags : fpu svm npt\n"); flag != "svm" {
		t.Errorf("AMD: %q", flag)
	}
	if m, flag, vm := parseCPUInfo("Features : fp asimd evtstrm\nHardware : BCM2835\n"); m != "BCM2835" || flag != "" || vm {
		t.Errorf("arm: %q %q %v", m, flag, vm)
	}
	if m, flag, vm := parseCPUInfo(""); m != "" || flag != "" || vm {
		t.Error("empty")
	}
}

func TestParseOSRelease(t *testing.T) {
	if got := parseOSRelease("NAME=\"Ubuntu\"\nPRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\nID=ubuntu\n"); got != "Ubuntu 24.04.1 LTS" {
		t.Errorf("%q", got)
	}
	if parseOSRelease("ID=x\n") != "" {
		t.Error("absent")
	}
}

// The JSON the Windows collector's PowerShell script printed on the development machine.
const cimSample = `{"hypervisorPresent":true,"cpuName":"Intel(R) Core(TM) i7-10870H CPU @ 2.20GHz","firmwareVirt":false,"features":[{"name":"HypervisorPlatform","state":2},{"name":"VirtualMachinePlatform","state":1},{"name":"Microsoft-Hyper-V-All","state":2}]}`

func TestApplyCIM(t *testing.T) {
	w := &WindowsFacts{}
	cpu, problems, err := applyCIM([]byte(cimSample), w)
	if err != nil || len(problems) != 0 || !strings.Contains(cpu, "i7-10870H") {
		t.Fatalf("%q %v %v", cpu, problems, err)
	}
	if w.HypervisorPresent == nil || !*w.HypervisorPresent || w.FirmwareVirtualization == nil || *w.FirmwareVirtualization ||
		w.HypervisorPlatform != FeatureDisabled || w.VirtualMachinePlat != FeatureEnabled || w.HyperV != FeatureDisabled || w.QueryDenied {
		t.Errorf("%+v", w)
	}

	// Individual queries may be denied; the rest is kept and the problem is reported, not fatal.
	w = &WindowsFacts{}
	_, problems, err = applyCIM([]byte(`{"hypervisorPresent":false,"featError":"Access denied\r\nat line 4","features":[]}`), w)
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0], "Win32_OptionalFeature: Access denied at line 4") || !w.QueryDenied {
		t.Errorf("%v %v %+v", problems, err, w)
	}
	if w.HypervisorPlatform != FeatureUnknown || w.HypervisorPresent == nil || *w.HypervisorPresent {
		t.Errorf("%+v", w)
	}
	if _, _, err := applyCIM([]byte("not json"), &WindowsFacts{}); err == nil {
		t.Error("garbage")
	}
	for state, want := range map[int]FeatureState{1: FeatureEnabled, 2: FeatureDisabled, 3: FeatureAbsent, 0: FeatureUnknown, 7: FeatureUnknown} {
		if featureState(state) != want {
			t.Errorf("state %d", state)
		}
	}
	if !hasHypervisorEntitlement("<key>com.apple.security.hypervisor</key><true/>") || hasHypervisorEntitlement("<key>com.apple.security.cs.debugger</key>") {
		t.Error("entitlement")
	}
}

// --- the diagnosis: every case of PLAN.md section 7.10 -----------------------------------------------------------

func accelTCGBecause(hw, reason string) AccelResult {
	return AccelResult{Requested: "auto", Chosen: "tcg", Slow: true, Tried: []AccelTry{{Accel: hw, Reason: reason}, {Accel: "tcg", Usable: true}}}
}

func accelOK(a string) AccelResult {
	return AccelResult{Requested: "auto", Chosen: a, Tried: []AccelTry{{Accel: a, Usable: true}}, Slow: a == "tcg"}
}

var okQEMUState = QEMUState{Found: true, Usable: true, Version: "9.0.0"}

func tru() *bool  { return boolPtr(true) }
func fals() *bool { return boolPtr(false) }

func win(mut func(*WindowsFacts)) Facts {
	w := &WindowsFacts{Edition: "Windows 10 Pro", EditionID: "Professional", HypervisorPlatform: FeatureEnabled, VirtualMachinePlat: FeatureEnabled, HyperV: FeatureDisabled,
		HypervisorPresent: tru(), FirmwareVirtualization: fals(), WHvHypervisor: tru()}
	if mut != nil {
		mut(w)
	}
	return Facts{OS: "windows", Arch: "amd64", Windows: w}
}

func joined(d Diagnosis) string {
	var b strings.Builder
	for _, n := range d.Notes {
		b.WriteString(n.Level + ": " + n.Text + "\n")
	}
	for _, s := range d.Steps {
		b.WriteString("step: " + s + "\n")
	}
	return b.String()
}

func TestDiagnoseWindows(t *testing.T) {
	probeFailed := "QEMU with -accel whpx exited with status 1: Failed to create partition"
	tests := []struct {
		name     string
		facts    Facts
		acc      AccelResult
		verdict  string
		steps    int      // number of remediation steps
		contains []string // in notes and steps
		absent   []string
	}{
		{name: "whpx works", facts: win(nil), acc: accelOK("whpx"), verdict: "will use whpx", contains: []string{"whpx works"}},
		{name: "whpx works although the feature reads disabled (D-44)", facts: win(func(w *WindowsFacts) { w.HypervisorPlatform = FeatureDisabled }),
			acc: accelOK("whpx"), verdict: "will use whpx", steps: 0, contains: []string{"nothing to fix"}, absent: []string{"Enable"}},
		{name: "case 1: virtualization off in the firmware, nothing running", facts: win(func(w *WindowsFacts) {
			w.HypervisorPresent, w.FirmwareVirtualization, w.WHvHypervisor, w.HypervisorPlatform = fals(), fals(), fals(), FeatureDisabled
		}), acc: accelTCGBecause("whpx", "no hypervisor"), verdict: "will use tcg (slow)", steps: 2, contains: []string{"VT-x or AMD-V", "HypervisorPlatform", "restart Windows"}},
		{name: "case 2: a hypervisor runs, the platform feature is off", facts: win(func(w *WindowsFacts) { w.HypervisorPlatform = FeatureDisabled }),
			acc: accelTCGBecause("whpx", probeFailed), verdict: "will use tcg (slow)", steps: 2,
			contains: []string{"Enable-WindowsOptionalFeature", "DISM", "Do not disable WSL2 or Hyper-V", "holds VT-x"}, absent: []string{"BIOS"}},
		{name: "case 2 without a running hypervisor", facts: win(func(w *WindowsFacts) {
			w.HypervisorPlatform, w.HypervisorPresent, w.WHvHypervisor, w.FirmwareVirtualization = FeatureDisabled, fals(), fals(), tru()
		}), acc: accelTCGBecause("whpx", probeFailed), steps: 1, contains: []string{"HypervisorPlatform"}, absent: []string{"Do not disable", "BIOS"}},
		{name: "case 3: feature on, no hypervisor reported", facts: win(func(w *WindowsFacts) { w.WHvHypervisor = fals() }),
			acc: accelTCGBecause("whpx", "the Windows Hypervisor Platform reports no hypervisor"), steps: 1, contains: []string{"hypervisorlaunchtype auto", "Restart Windows"}},
		{name: "case 4: everything on, the QEMU launch fails", facts: win(nil), acc: accelTCGBecause("whpx", probeFailed), steps: 1,
			contains: []string{"too old or lack WHPX", "Failed to create partition"}},
		{name: "case 4: this QEMU has no whpx", facts: win(nil), acc: accelTCGBecause("whpx", "this QEMU build was compiled without whpx support"), steps: 1,
			contains: []string{"no WHPX support", "winget install"}},
		{name: "hypervisor running: never 'off in the BIOS'", facts: win(func(w *WindowsFacts) { w.HypervisorPlatform = FeatureDisabled }),
			acc: accelTCGBecause("whpx", probeFailed), steps: 2, absent: []string{"BIOS", "UEFI"}},
		{name: "queries denied", facts: win(func(w *WindowsFacts) {
			w.QueryDenied, w.HypervisorPlatform, w.WHvHypervisor = true, FeatureUnknown, nil
		}),
			acc: accelTCGBecause("whpx", probeFailed), steps: 1, contains: []string{"could not be read", "Turn Windows features on or off"}},
		{name: "Windows Home is fine", facts: win(func(w *WindowsFacts) { w.Home, w.HypervisorPlatform = true, FeatureDisabled }),
			acc: accelTCGBecause("whpx", probeFailed), steps: 2, contains: []string{"Windows Home is fine"}},
		{name: "--slow asks for tcg: no fix is suggested", facts: win(nil), acc: AccelResult{Requested: "slow", Chosen: "tcg", Slow: true},
			verdict: "will use tcg (slow)", steps: 0, contains: []string{"requested"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Diagnose(tt.facts, okQEMUState, tt.acc)
			want := tt.verdict
			if want == "" {
				want = "will use tcg (slow)"
			}
			if d.Verdict != want || !d.CanRun || len(d.Steps) != tt.steps {
				t.Errorf("verdict %q can_run %v, %d steps; want %q, %d\n%s", d.Verdict, d.CanRun, len(d.Steps), want, tt.steps, joined(d))
			}
			text := joined(d)
			for _, c := range tt.contains {
				if !strings.Contains(text, c) {
					t.Errorf("missing %q in\n%s", c, text)
				}
			}
			for _, c := range tt.absent {
				if strings.Contains(text, c) {
					t.Errorf("must not say %q:\n%s", c, text)
				}
			}
		})
	}
}

func lin(mut func(*LinuxFacts)) Facts {
	l := &LinuxFacts{VirtFlag: "vmx", KVMModule: "kvm_intel", DevKVM: true, DevKVMMode: "0660", DevKVMOwner: "root", DevKVMGroup: "kvm", UserCanOpen: true, UserInKVMGroup: true}
	if mut != nil {
		mut(l)
	}
	return Facts{OS: "linux", Arch: "amd64", Linux: l}
}

func TestDiagnoseLinux(t *testing.T) {
	denied := accelTCGBecause("kvm", "no permission to open /dev/kvm")
	tests := []struct {
		name     string
		facts    Facts
		acc      AccelResult
		steps    int
		contains []string
		absent   []string
	}{
		{name: "kvm works", facts: lin(nil), acc: accelOK("kvm"), contains: []string{"kvm works"}},
		{name: "not in the kvm group", facts: lin(func(l *LinuxFacts) { l.UserCanOpen, l.UserInKVMGroup = false, false }), acc: denied, steps: 1,
			contains: []string{"usermod -aG kvm $USER", "log out and back in", "0660 root:kvm"}},
		{name: "WSL2: /dev/kvm exists but there is no vmx flag; the group is the fix, not nested virtualization", facts: lin(func(l *LinuxFacts) {
			l.VirtFlag, l.InVM, l.KVMModule, l.UserCanOpen, l.UserInKVMGroup = "", true, "", false, false
		}), acc: denied, steps: 1, contains: []string{"usermod -aG kvm"}, absent: []string{"nested virtualization", "BIOS"}},
		{name: "in the group but a fresh login is needed", facts: lin(func(l *LinuxFacts) { l.UserCanOpen = false }), acc: denied, steps: 1, contains: []string{"log out and back in so the group applies"}},
		{name: "opens but QEMU fails", facts: lin(nil), acc: accelTCGBecause("kvm", "exited with status 1"), steps: 1, contains: []string{"opens but QEMU could not use it"}},
		{name: "a VM without nested virtualization", facts: lin(func(l *LinuxFacts) {
			l.VirtFlag, l.InVM, l.KVMModule, l.DevKVM, l.UserCanOpen = "", true, "", false, false
		}),
			acc: accelTCGBecause("kvm", "/dev/kvm does not exist"), steps: 0, contains: []string{"nested virtualization"}},
		{name: "no vmx or svm on bare metal", facts: lin(func(l *LinuxFacts) { l.VirtFlag, l.KVMModule, l.DevKVM, l.UserCanOpen = "", "", false, false }),
			acc: accelTCGBecause("kvm", "/dev/kvm does not exist"), steps: 1, contains: []string{"VT-x or AMD-V", "BIOS/UEFI"}},
		{name: "module not loaded (intel)", facts: lin(func(l *LinuxFacts) { l.KVMModule, l.DevKVM, l.UserCanOpen = "", false, false }),
			acc: accelTCGBecause("kvm", "/dev/kvm does not exist"), steps: 1, contains: []string{"sudo modprobe kvm_intel"}},
		{name: "module not loaded (amd)", facts: lin(func(l *LinuxFacts) { l.VirtFlag, l.KVMModule, l.DevKVM, l.UserCanOpen = "svm", "", false, false }),
			acc: accelTCGBecause("kvm", "/dev/kvm does not exist"), steps: 1, contains: []string{"sudo modprobe kvm_amd"}},
		{name: "arm64 without /dev/kvm", facts: func() Facts {
			f := lin(func(l *LinuxFacts) { l.VirtFlag, l.KVMModule, l.DevKVM, l.UserCanOpen = "", "", false, false })
			f.Arch = "arm64"
			return f
		}(), acc: accelTCGBecause("kvm", "/dev/kvm does not exist"), steps: 1, contains: []string{"modprobe kvm"}, absent: []string{"VT-x"}},
		{name: "QEMU without kvm", facts: lin(nil), acc: accelTCGBecause("kvm", "this QEMU build was compiled without kvm support"), steps: 1, contains: []string{"no KVM support"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Diagnose(tt.facts, okQEMUState, tt.acc)
			text := joined(d)
			wantVerdict := "will use " + tt.acc.Chosen
			if tt.acc.Chosen == "tcg" {
				wantVerdict = "will use tcg (slow)"
			}
			if d.Verdict != wantVerdict || !d.CanRun || len(d.Steps) != tt.steps {
				t.Errorf("verdict %q, %d steps; want %q, %d\n%s", d.Verdict, len(d.Steps), wantVerdict, tt.steps, text)
			}
			for _, c := range tt.contains {
				if !strings.Contains(text, c) {
					t.Errorf("missing %q in\n%s", c, text)
				}
			}
			for _, c := range tt.absent {
				if strings.Contains(text, c) {
					t.Errorf("must not say %q:\n%s", c, text)
				}
			}
		})
	}
}

func TestDiagnoseMacOS(t *testing.T) {
	mac := func(silicon bool, hv, ent *bool) Facts {
		return Facts{OS: "darwin", Arch: "arm64", Darwin: &DarwinFacts{AppleSilicon: silicon, HVSupport: hv, Entitlement: ent}}
	}
	d := Diagnose(mac(true, tru(), tru()), okQEMUState, accelOK("hvf"))
	if d.Verdict != "will use hvf" || len(d.Steps) != 0 {
		t.Errorf("%s", joined(d))
	}
	d = Diagnose(mac(false, fals(), nil), okQEMUState, accelTCGBecause("hvf", "sysctl kern.hv_support is 0"))
	if d.Verdict != "will use tcg (slow)" || len(d.Steps) != 0 || !strings.Contains(joined(d), "does not offer the Hypervisor framework") {
		t.Errorf("an Intel Mac without hv_support has nothing to fix: %s", joined(d))
	}
	d = Diagnose(mac(true, tru(), fals()), okQEMUState, accelTCGBecause("hvf", "hvf: entitlement"))
	if len(d.Steps) != 1 || !strings.Contains(d.Steps[0], "brew install qemu") || !strings.Contains(d.Steps[0], "com.apple.security.hypervisor") {
		t.Errorf("%s", joined(d))
	}
	d = Diagnose(mac(true, tru(), nil), okQEMUState, accelTCGBecause("hvf", "x"))
	if len(d.Steps) != 1 || !strings.Contains(d.Steps[0], "codesign -d --entitlements") || !strings.Contains(d.Steps[0], "qemu-system-aarch64") {
		t.Errorf("%s", joined(d))
	}
}

func TestDiagnoseWhenQEMUCannotRun(t *testing.T) {
	for _, tt := range []struct {
		os    string
		state QEMUState
		want  string
	}{
		{"windows", QEMUState{Problem: "QEMU not found: ..."}, "winget install SoftwareFreedomConservancy.QEMU"},
		{"linux", QEMUState{Problem: "QEMU not found: ..."}, "apt install qemu-system-x86"},
		{"darwin", QEMUState{Problem: "QEMU not found: ..."}, "brew install qemu"},
		{"linux", QEMUState{Found: true, Problem: "QEMU 7.2.0 is too old: 8.0 or newer is required"}, "8.0 or newer"},
		{"linux", QEMUState{Found: true, Problem: "this QEMU build lacks what the VM needs: device virtio-vga"}, "full QEMU build"},
		{"linux", QEMUState{Found: true, Problem: "an empty machine could not be started", TCGProbeErr: "exit 1"}, "could not start an empty machine"},
	} {
		d := Diagnose(Facts{OS: tt.os, Arch: "amd64"}, tt.state, AccelResult{})
		if d.CanRun || d.Verdict != "cannot run a VM" || len(d.Steps) != 1 || !strings.Contains(d.Steps[0], tt.want) {
			t.Errorf("%s %+v: %+v", tt.os, tt.state, d)
		}
		if !strings.Contains(joined(d), tt.state.Problem) {
			t.Errorf("the problem is part of the notes: %s", joined(d))
		}
	}
	// Facts that could not be read are reported, never fatal.
	d := Diagnose(Facts{OS: "linux", Arch: "amd64", Problems: []string{"/proc/cpuinfo: denied"}}, okQEMUState, accelOK("tcg"))
	if !d.CanRun || !strings.Contains(joined(d), "could not read: /proc/cpuinfo: denied") {
		t.Errorf("%s", joined(d))
	}
	// An OS without a hardware accelerator: tcg is simply what there is.
	d = Diagnose(Facts{OS: "freebsd", Arch: "amd64"}, okQEMUState, accelOK("tcg"))
	if d.Verdict != "will use tcg (slow)" || len(d.Steps) != 0 {
		t.Errorf("%s", joined(d))
	}
}

// --- the service -------------------------------------------------------------------------------------------------

const helpAccel = "Accelerators supported in QEMU binary:\ntcg\nkvm\n"
const helpMachine = "Supported machines are:\nq35  Standard PC\nnone empty machine\n"
const helpDevice = "name \"virtio-blk-pci\", bus PCI\nname \"qemu-xhci\", bus PCI\nname \"usb-kbd\", bus usb-bus\nname \"usb-tablet\", bus usb-bus\nname \"virtio-vga\", bus PCI\nname \"virtio-serial-pci\", bus PCI\nname \"virtserialport\", bus virtio-serial-bus\nname \"virtio-rng-pci\", bus PCI\n"

type docHost struct {
	env        map[string]string
	version    string
	probeExit  int
	probeErr   string
	missingDev bool
}

// GOOS is "windows" on every machine: Windows has no well-known QEMU directory unless ProgramFiles is set, so the
// search finds only what the test put on PATH and never the QEMU of the machine the test runs on.
func (h *docHost) GOOS() string           { return "windows" }
func (h *docHost) GOARCH() string         { return "amd64" }
func (h *docHost) Getenv(k string) string { return h.env[k] }
func (h *docHost) Run(_ context.Context, _ string, _ string, args ...string) (qemu.RunResult, error) {
	switch {
	case len(args) == 1 && args[0] == "--version":
		return qemu.RunResult{Stdout: h.version}, nil
	case slices.Equal(args, []string{"-accel", "help"}):
		return qemu.RunResult{Stdout: helpAccel}, nil
	case slices.Equal(args, []string{"-machine", "help"}):
		return qemu.RunResult{Stdout: helpMachine}, nil
	case slices.Equal(args, []string{"-device", "help"}):
		if h.missingDev {
			return qemu.RunResult{Stdout: strings.ReplaceAll(helpDevice, "name \"virtio-vga\", bus PCI\n", "")}, nil
		}
		return qemu.RunResult{Stdout: helpDevice}, nil
	case slices.Contains(args, "-sandbox"):
		return qemu.RunResult{}, nil
	case slices.Contains(args, "-S") && slices.Contains(args, "-qmp"):
		return qemu.RunResult{ExitCode: h.probeExit, Stderr: h.probeErr}, nil
	}
	return qemu.RunResult{}, errors.New("unexpected: " + strings.Join(args, " "))
}

// qemuDir creates a directory with stand-in QEMU programs.
func qemuDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range []string{qemu.SystemBinary("windows", qemu.ArchX86_64), qemu.ImgBinary("windows")} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // an executable stand-in
			t.Fatal(err)
		}
	}
	return dir
}

func newDocHost(dir string) *docHost {
	return &docHost{env: map[string]string{"PATH": dir}, version: "QEMU emulator version 9.0.0\n"}
}

func cfgFor(accel string, slow bool) config.Config {
	c := config.Defaults()
	c.VM.Accel, c.VM.Slow = accel, slow
	return c
}

func serviceFor(h qemu.Host, facts Facts, probe *fakeProbe) *DoctorService {
	return &DoctorService{
		Host:    h,
		Collect: func(context.Context, qemu.Host, *qemu.Install) Facts { return facts },
		Probe:   func(qemu.Host, qemu.Install) HostProbe { return probe },
	}
}

func TestDoctorReportsAWorkingMachine(t *testing.T) {
	h := newDocHost(qemuDir(t))
	s := serviceFor(h, lin(nil), &fakeProbe{goos: "linux"})
	var out bytes.Buffer
	if err := s.Run(context.Background(), &out, cfgFor("auto", false), false); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	text := out.String()
	for _, want := range []string{"controlsnack doctor (read-only", "\nHost\n", "\nQEMU\n", "QEMU emulator version 9.0.0", "\nAccelerators\n", "kvm    usable", "\nVerdict\n  will use kvm\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if !strings.HasSuffix(strings.TrimRight(text, "\n"), "will use kvm") {
		t.Errorf("the verdict is the last block when nothing needs fixing:\n%s", text)
	}
}

func TestDoctorFixesComeAfterTheVerdictAndAreNumbered(t *testing.T) {
	h := newDocHost(qemuDir(t))
	facts := lin(func(l *LinuxFacts) { l.UserCanOpen, l.UserInKVMGroup = false, false })
	s := serviceFor(h, facts, &fakeProbe{goos: "linux", kvmErr: errors.New("no permission to open /dev/kvm: this user is not in the kvm group")})
	var out bytes.Buffer
	if err := s.Run(context.Background(), &out, cfgFor("auto", false), false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	i, j := strings.Index(text, "\nVerdict\n"), strings.Index(text, "To do better:\n  1. Let this user open /dev/kvm")
	if i < 0 || j < i || strings.Contains(text[i:], "\nNotes") {
		t.Errorf("Verdict, then numbered steps, last:\n%s", text)
	}
	if !strings.Contains(text, "will use tcg (slow)") {
		t.Errorf("%s", text)
	}
}

func TestDoctorJSON(t *testing.T) {
	h := newDocHost(qemuDir(t))
	s := serviceFor(h, lin(nil), &fakeProbe{goos: "linux"})
	var out bytes.Buffer
	if err := s.Run(context.Background(), &out, cfgFor("auto", false), true); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, k := range []string{"host", "qemu", "accel", "notes", "verdict", "can_run"} {
		if _, ok := m[k]; !ok {
			t.Errorf("--json lacks %q", k)
		}
	}
	if m["verdict"] != "will use kvm" || m["can_run"] != true {
		t.Errorf("%v %v", m["verdict"], m["can_run"])
	}
	if q := m["qemu"].(map[string]any); q["found"] != true || q["usable"] != true || q["accelerators"] == nil {
		t.Errorf("%v", q)
	}
}

func TestDoctorExitsThreeWhenNoVMCanRun(t *testing.T) {
	// QEMU is missing.
	h := &docHost{env: map[string]string{"PATH": t.TempDir()}}
	s := serviceFor(h, lin(nil), &fakeProbe{goos: "linux"})
	var out bytes.Buffer
	err := s.Run(context.Background(), &out, cfgFor("auto", false), false)
	if exitcode.Of(err) != exitcode.Environment || !strings.Contains(out.String(), "cannot run a VM") || !strings.Contains(out.String(), "1. Install QEMU") {
		t.Errorf("%v\n%s", err, out.String())
	}
	rep, berr := s.Build(context.Background(), cfgFor("auto", false))
	if berr != nil || rep.CanRun || rep.QEMU.Found || rep.QEMU.Usable {
		t.Errorf("%+v %v", rep.QEMU, berr)
	}

	// QEMU is found but too old.
	h = newDocHost(qemuDir(t))
	h.version = "QEMU emulator version 7.2.0\n"
	rep, _ = serviceFor(h, lin(nil), &fakeProbe{goos: "linux"}).Build(context.Background(), cfgFor("auto", false))
	if rep.CanRun || !rep.QEMU.Found || !strings.Contains(rep.QEMU.Problem, "too old") || rep.QEMU.Version != "7.2.0" {
		t.Errorf("%+v", rep.QEMU)
	}

	// QEMU lacks a device.
	h = newDocHost(qemuDir(t))
	h.missingDev = true
	rep, _ = serviceFor(h, lin(nil), &fakeProbe{goos: "linux"}).Build(context.Background(), cfgFor("auto", false))
	if rep.CanRun || !strings.Contains(rep.QEMU.Problem, "virtio-vga") {
		t.Errorf("%+v", rep.QEMU)
	}

	// QEMU is fine but cannot even start an empty machine with software emulation.
	h = newDocHost(qemuDir(t))
	h.probeExit, h.probeErr = 1, "qemu: could not load BIOS"
	s = serviceFor(h, lin(nil), &fakeProbe{goos: "linux"})
	err = s.Run(context.Background(), &out, cfgFor("auto", false), false)
	if exitcode.Of(err) != exitcode.Environment {
		t.Errorf("a failing tcg probe is exit 3, got %v", err)
	}
	rep, _ = s.Build(context.Background(), cfgFor("auto", false))
	if rep.CanRun || !strings.Contains(rep.QEMU.TCGProbeErr, "could not load BIOS") {
		t.Errorf("%+v", rep.QEMU)
	}
}

func TestDoctorAndRunChooseTheSameAccelerator(t *testing.T) {
	// The verdict comes from the same ResolveAccel call run uses, with the same probe, for every setting.
	dir := qemuDir(t)
	for _, tt := range []struct {
		name  string
		probe *fakeProbe
		accel string
		slow  bool
	}{
		{"auto, kvm usable", &fakeProbe{goos: "linux"}, "auto", false},
		{"auto, kvm refused", &fakeProbe{goos: "linux", kvmErr: errors.New("denied")}, "auto", false},
		{"slow", &fakeProbe{goos: "linux"}, "auto", true},
		{"tcg", &fakeProbe{goos: "linux"}, "tcg", false},
		{"explicit kvm", &fakeProbe{goos: "linux", kvmErr: errors.New("denied")}, "kvm", false},
		{"windows, whpx works", &fakeProbe{goos: "windows", whpx: true}, "auto", false},
		{"windows, probe fails", &fakeProbe{goos: "windows", whpx: true, probeErr: errors.New("exit 1")}, "auto", false},
		{"macOS", &fakeProbe{goos: "darwin", hvf: true}, "auto", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := serviceFor(newDocHost(dir), lin(nil), tt.probe)
			rep, err := s.Build(context.Background(), cfgFor(tt.accel, tt.slow))
			if err != nil {
				t.Fatal(err)
			}
			want, werr := ResolveAccel(context.Background(), &fakeProbe{goos: tt.probe.goos, kvmErr: tt.probe.kvmErr, whpx: tt.probe.whpx, hvf: tt.probe.hvf, probeErr: tt.probe.probeErr}, tt.accel, tt.slow)
			if werr != nil {
				t.Fatal(werr)
			}
			if rep.Accel.Chosen != want.Chosen || rep.Accel.Requested != want.Requested || rep.Accel.Slow != want.Slow {
				t.Errorf("doctor says %+v, run would use %+v", rep.Accel, want)
			}
			wantVerdict := "will use " + want.Chosen
			if want.Chosen == "tcg" {
				wantVerdict = "will use tcg (slow)"
			}
			if rep.Verdict != wantVerdict {
				t.Errorf("verdict %q, want %q", rep.Verdict, wantVerdict)
			}
		})
	}
}

func TestDoctorPassesUsageErrorsThrough(t *testing.T) {
	s := serviceFor(newDocHost(qemuDir(t)), lin(nil), &fakeProbe{goos: "linux"})
	for _, cfg := range []config.Config{cfgFor("whpx", false), cfgFor("kvm", true), cfgFor("xen", false)} {
		err := s.Run(context.Background(), &bytes.Buffer{}, cfg, false)
		if exitcode.Of(err) != exitcode.Usage {
			t.Errorf("%+v: %v", cfg.VM, err)
		}
	}
}

func TestDoctorRunsTheExtraChecks(t *testing.T) {
	s := serviceFor(newDocHost(qemuDir(t)), lin(nil), &fakeProbe{goos: "linux"})
	s.Checks = []Check{
		{Name: "base image", Run: func(context.Context) CheckResult {
			return CheckResult{Status: LevelWarn, Detail: "none built yet", Fix: "controlsnack image build"}
		}},
		{Name: "slow check", Run: func(ctx context.Context) CheckResult {
			<-ctx.Done()
			return CheckResult{Status: LevelFail, Detail: "gave up: " + ctx.Err().Error()}
		}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	rep, err := s.Build(ctx, cfgFor("auto", false))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Checks) != 2 || rep.Checks[0].Name != "base image" || rep.Checks[0].Fix == "" {
		t.Fatalf("%+v", rep.Checks)
	}
	var out bytes.Buffer
	if err := RenderReport(&out, rep); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "[warn] base image: none built yet") || !strings.Contains(out.String(), "fix: controlsnack image build") {
		t.Errorf("%s", out.String())
	}
}

func TestDoctorStopsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := serviceFor(newDocHost(qemuDir(t)), lin(nil), &fakeProbe{goos: "linux"})
	if err := s.Run(ctx, &bytes.Buffer{}, cfgFor("auto", false), false); !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
}

// --- the real machine --------------------------------------------------------------------------------------------

// TestCollectFactsOnThisMachine reads the real operating system. It must not fail whatever the machine looks like,
// and what it finds is logged because it decides what the doctor says here.
func TestCollectFactsOnThisMachine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f := collectFacts(ctx, qemu.OSHost{}, nil)
	data, _ := json.MarshalIndent(f, "", "  ")
	t.Logf("%s", data)
	if f.OS != runtime.GOOS || f.Arch != runtime.GOARCH {
		t.Errorf("%+v", f)
	}
	switch runtime.GOOS {
	case "windows":
		w := f.Windows
		if w == nil || w.Build == "" || w.EditionID == "" || f.CPUModel == "" {
			t.Fatalf("%+v", w)
		}
		if w.HypervisorPlatform == "" || w.WHvHypervisor == nil && w.WHvError == "" {
			t.Errorf("%+v", w)
		}
		if w.Home != strings.HasPrefix(w.EditionID, "Core") {
			t.Errorf("%+v", w)
		}
	case "linux":
		if f.Linux == nil || f.OSVersion == "" || f.CPUModel == "" {
			t.Fatalf("%+v", f)
		}
		if f.Linux.DevKVM && f.Linux.DevKVMMode == "" {
			t.Errorf("%+v", f.Linux)
		}
	}
}

// TestDoctorOnThisMachine runs the real doctor with whatever QEMU there is and prints the report.
func TestDoctorOnThisMachine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var out bytes.Buffer
	err := NewDoctor().Run(ctx, &out, config.Defaults(), false)
	t.Logf("exit %d\n%s", exitcode.Of(err), out.String())
	if err != nil && exitcode.Of(err) != exitcode.Environment {
		t.Errorf("the only failure a doctor may have is exit code 3: %v", err)
	}
	if !strings.Contains(out.String(), "\nVerdict\n") {
		t.Errorf("a report always ends with a verdict")
	}
}
