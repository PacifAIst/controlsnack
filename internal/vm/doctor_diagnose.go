// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"fmt"
	"strings"
)

// Levels of a Note.
const (
	LevelOK   = "ok"
	LevelInfo = "info"
	LevelWarn = "warn"
	LevelFail = "fail"
)

// Note is one line of the report.
type Note struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// QEMUState is what the doctor found out about QEMU, as input to Diagnose.
type QEMUState struct {
	Found       bool   `json:"found"`
	Usable      bool   `json:"usable"` // found, new enough, has the devices, and the tcg probe launch worked
	Problem     string `json:"problem,omitempty"`
	Version     string `json:"version,omitempty"`
	Dir         string `json:"dir,omitempty"`
	TCGProbeErr string `json:"tcg_probe_error,omitempty"`
}

// Diagnosis is what the doctor concludes. Verdict is the last block of the report and comes from the same
// accelerator choice `run` makes, so the two cannot disagree (PLAN.md section 7.10, item 4).
type Diagnosis struct {
	Notes   []Note   `json:"notes"`
	Steps   []string `json:"steps,omitempty"` // numbered when printed
	Verdict string   `json:"verdict"`
	CanRun  bool     `json:"can_run"`
}

// Diagnose turns facts, the state of QEMU and the accelerator choice into notes, remediation steps and the verdict.
// It decides on what QEMU actually did (the probe in acc), not on what the operating system's feature switches
// claim: a machine may run WHPX with the optional feature reading "disabled" (D-44).
func Diagnose(f Facts, q QEMUState, acc AccelResult) Diagnosis {
	var d Diagnosis
	note := func(level, format string, args ...any) {
		d.Notes = append(d.Notes, Note{level, fmt.Sprintf(format, args...)})
	}
	step := func(format string, args ...any) { d.Steps = append(d.Steps, fmt.Sprintf(format, args...)) }

	for _, p := range f.Problems {
		note(LevelWarn, "could not read: %s", p)
	}

	if !q.Found || !q.Usable {
		qemuSteps(f, q, step)
		if q.Problem != "" {
			note(LevelFail, "%s", q.Problem)
		}
		d.Verdict = "cannot run a VM"
		return d
	}
	d.CanRun = true

	hardware := hardwareAccel(f.OS)
	if hardware == "" || acc.Chosen == hardware {
		if hardware != "" {
			note(LevelOK, "%s works: a session will be hardware accelerated", hardware)
		}
		if acc.Chosen == hardware && f.OS == "windows" && f.Windows != nil && f.Windows.HypervisorPlatform != FeatureEnabled {
			note(LevelInfo, "the optional feature \"Windows Hypervisor Platform\" reads %q, but QEMU started with WHPX, so there is nothing to fix", f.Windows.HypervisorPlatform)
		}
	} else if acc.Chosen == AccelTCG && acc.Requested != "slow" && acc.Requested != AccelTCG {
		reason := hardwareReason(acc, hardware)
		note(LevelWarn, "%s is not usable: %s", hardware, reason)
		said := len(d.Notes) + len(d.Steps)
		switch f.OS {
		case "windows":
			windowsSteps(f.Windows, reason, step, note)
		case "linux":
			linuxSteps(f, reason, step, note)
		case "darwin":
			darwinSteps(f.Darwin, reason, step, note)
		}
		if len(d.Notes)+len(d.Steps) == said {
			note(LevelInfo, "no specific fix was found; software emulation will be used")
		}
	}

	switch acc.Chosen {
	case AccelTCG:
		d.Verdict = "will use tcg (slow)"
		if acc.Requested == "slow" || acc.Requested == AccelTCG {
			note(LevelInfo, "software emulation was requested (--slow or --accel=tcg)")
		} else {
			note(LevelInfo, "software emulation works but can be 10 to 50 times slower; a first image build can take 30 minutes or more")
		}
	default:
		d.Verdict = "will use " + acc.Chosen
	}
	return d
}

// hardwareReason finds why the OS's hardware accelerator was refused.
func hardwareReason(acc AccelResult, hardware string) string {
	for _, t := range acc.Tried {
		if t.Accel == hardware && !t.Usable {
			return t.Reason
		}
	}
	return "it was not tried"
}

func qemuSteps(f Facts, q QEMUState, step func(string, ...any)) {
	switch {
	case !q.Found:
		switch f.OS {
		case "windows":
			step("Install QEMU for Windows (winget install SoftwareFreedomConservancy.QEMU, or the installer from https://www.qemu.org/download/#windows), then run doctor again; or point to it with --qemu <directory>.")
		case "darwin":
			step("Install QEMU with Homebrew (brew install qemu): the Homebrew build carries the hypervisor entitlement.")
		default:
			step("Install QEMU (Debian/Ubuntu: sudo apt install qemu-system-x86 qemu-utils; Fedora: sudo dnf install qemu-system-x86 qemu-img), or point to it with --qemu <directory>.")
		}
	case strings.Contains(q.Problem, "too old"):
		step("Install a current QEMU (8.0 or newer) and run doctor again.")
	case strings.Contains(q.Problem, "lacks"):
		step("Install a full QEMU build: this one is missing devices the VM needs.")
	default:
		step("QEMU was found but could not start an empty machine (%s). Run it by hand to see why, or reinstall it.", firstNonEmpty(q.TCGProbeErr, q.Problem))
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func windowsSteps(w *WindowsFacts, reason string, step func(string, ...any), note func(string, string, ...any)) {
	if w == nil {
		return
	}
	const enableHP = "Enable the \"Windows Hypervisor Platform\" optional feature (Settings > Apps > Optional features > More Windows features; or in an administrator PowerShell: Enable-WindowsOptionalFeature -Online -FeatureName HypervisorPlatform; or DISM /Online /Enable-Feature /FeatureName:HypervisorPlatform /All), then restart Windows."
	hv, fw := w.HypervisorPresent, w.FirmwareVirtualization
	if w.Home {
		note(LevelInfo, "Windows Home is fine: WHPX needs the Windows Hypervisor Platform feature, not the Hyper-V role")
	}
	if w.QueryDenied {
		note(LevelWarn, "the optional features could not be read; check \"Turn Windows features on or off\" for Windows Hypervisor Platform")
	}
	switch {
	case strings.Contains(reason, "compiled without"):
		// Case 4: the OS side is fine but this QEMU cannot use WHPX.
		step("This QEMU build has no WHPX support. Install a current QEMU for Windows (winget install SoftwareFreedomConservancy.QEMU); software emulation is used until then.")
	case hv != nil && !*hv && fw != nil && !*fw:
		// Case 1: virtualization off in the firmware and nothing running.
		step("Turn on Intel VT-x or AMD-V (SVM) in the BIOS/UEFI setup.")
		step(enableHP)
	case w.HypervisorPlatform == FeatureDisabled || w.HypervisorPlatform == FeatureAbsent:
		// Case 2. A running hypervisor is no reason to remove it.
		step(enableHP)
		if hv != nil && *hv {
			note(LevelInfo, "a hypervisor is already running (Hyper-V, WSL2, Windows Sandbox or Memory Integrity) and holds VT-x; QEMU can only share it through WHPX")
			step("Do not disable WSL2 or Hyper-V for this: it would not hand VT-x to QEMU.")
		}
	case w.HypervisorPlatform == FeatureEnabled && w.WHvHypervisor != nil && !*w.WHvHypervisor:
		// Case 3: the feature is on but the platform reports no hypervisor.
		step("Restart Windows (a feature change is probably pending). If the problem stays, the hypervisor is switched off at boot: in an administrator prompt run bcdedit /set hypervisorlaunchtype auto, then restart.")
	case w.HypervisorPlatform == FeatureEnabled:
		// Case 4: everything is on but QEMU's launch failed.
		step("Windows is set up for WHPX but QEMU could not use it (%s). The QEMU build may be too old or lack WHPX: install a current QEMU for Windows.", reason)
	default:
		step("Check that \"Windows Hypervisor Platform\" is turned on in \"Turn Windows features on or off\", restart if you change it, then run doctor again.")
	}
}

func linuxSteps(f Facts, reason string, step func(string, ...any), note func(string, string, ...any)) {
	l := f.Linux
	if l == nil {
		return
	}
	if strings.Contains(reason, "compiled without") {
		step("This QEMU build has no KVM support. Install the distribution's QEMU package (Debian/Ubuntu: qemu-system-x86).")
		return
	}
	module := "kvm_intel"
	if l.VirtFlag == "svm" {
		module = "kvm_amd"
	}
	// What /dev/kvm says comes first: where the device exists (also in WSL2 and in nested setups, where /proc/cpuinfo
	// shows no vmx or svm flag), permissions are the thing to fix. CPU flags only matter when it does not exist.
	switch {
	case l.DevKVM && !l.UserCanOpen && !l.UserInKVMGroup:
		step("Let this user open /dev/kvm (%s %s:%s): sudo usermod -aG kvm $USER, then log out and back in.", l.DevKVMMode, l.DevKVMOwner, l.DevKVMGroup)
	case l.DevKVM && !l.UserCanOpen:
		step("/dev/kvm (%s %s:%s) cannot be opened by this user even though the user is in the kvm group: log out and back in so the group applies, or check the device's permissions.", l.DevKVMMode, l.DevKVMOwner, l.DevKVMGroup)
	case l.DevKVM:
		step("/dev/kvm opens but QEMU could not use it (%s). Check that no other hypervisor holds it, or run QEMU by hand to see its message.", reason)
	case f.Arch == "amd64" && l.VirtFlag == "" && l.InVM:
		note(LevelInfo, "this machine is itself a virtual machine without nested virtualization, so KVM is not available; software emulation will be used. Enable nested virtualization on the host to get hardware acceleration.")
	case f.Arch == "amd64" && l.VirtFlag == "":
		step("Turn on Intel VT-x or AMD-V (SVM) in the BIOS/UEFI setup: the CPU does not report vmx or svm.")
	case f.Arch == "arm64":
		step("Load the KVM module (sudo modprobe kvm) or use a kernel with KVM; /dev/kvm does not exist.")
	default:
		step("Load the KVM module: sudo modprobe %s (and make sure virtualization is on in the BIOS/UEFI); /dev/kvm does not exist.", module)
	}
}

func darwinSteps(d *DarwinFacts, reason string, step func(string, ...any), note func(string, string, ...any)) {
	if d == nil {
		return
	}
	switch {
	case d.HVSupport != nil && !*d.HVSupport:
		note(LevelInfo, "this Mac does not offer the Hypervisor framework (sysctl kern.hv_support is 0); software emulation will be used")
	case d.Entitlement != nil && !*d.Entitlement:
		step("This QEMU lacks the com.apple.security.hypervisor entitlement. Install QEMU from Homebrew (brew install qemu), whose build is entitled.")
	default:
		step("The Hypervisor framework is available but QEMU could not use it (%s). Install QEMU from Homebrew (brew install qemu) or check the binary's entitlement with: codesign -d --entitlements - $(which qemu-system-%s)", reason, map[bool]string{true: "aarch64", false: "x86_64"}[d.AppleSilicon])
	}
}
