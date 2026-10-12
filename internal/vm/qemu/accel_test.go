// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qemu

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAccelArgsAndCPUModel(t *testing.T) {
	for accel, want := range map[string][]string{
		AccelKVM: {"-enable-kvm"}, AccelWHPX: {"-accel", "whpx"}, AccelHVF: {"-accel", "hvf"}, AccelTCG: {"-accel", "tcg"},
	} {
		if got := AccelArgs(accel); !slices.Equal(got, want) {
			t.Errorf("%s: %v", accel, got)
		}
	}
	// PLAN.md section 7.2: host for kvm and hvf, max for tcg, nothing for whpx (QEMU issue #1043).
	for accel, want := range map[string]string{AccelKVM: "host", AccelHVF: "host", AccelTCG: "max", AccelWHPX: ""} {
		if got := CPUModel(accel); got != want {
			t.Errorf("%s: -cpu %q, want %q", accel, got, want)
		}
	}
}

func TestAccelProbeArgs(t *testing.T) {
	got := AccelProbeArgs(ArchX86_64, AccelWHPX)
	want := []string{"-accel", "whpx", "-machine", "q35", "-m", "128", "-nodefaults", "-display", "none", "-S", "-qmp", "stdio"}
	if !slices.Equal(got, want) {
		t.Errorf("whpx: %v", got)
	}
	got = AccelProbeArgs(ArchAarch64, AccelHVF)
	if !slices.Contains(got, "virt") || !slices.Contains(got, "host") || !slices.Contains(got, "-S") {
		t.Errorf("aarch64 hvf: %v", got)
	}
	got = AccelProbeArgs(ArchX86_64, AccelTCG)
	if i := slices.Index(got, "-cpu"); i < 0 || got[i+1] != "max" {
		t.Errorf("tcg: %v", got)
	}
	for _, accel := range []string{AccelKVM, AccelWHPX, AccelHVF, AccelTCG} {
		for _, bad := range []string{"kernel-irqchip", "ssd=off", "-nic", "-netdev", "-monitor", "-vnc", "-gdb"} {
			if strings.Contains(strings.Join(AccelProbeArgs(ArchX86_64, accel), " "), bad) {
				t.Errorf("%s: the probe argv must not contain %q", accel, bad)
			}
		}
	}
}

func TestProbeAccel(t *testing.T) {
	inst := Install{Arch: ArchX86_64, System: "/x/qemu-system-x86_64"}
	run := func(q fakeQEMU) error {
		return ProbeAccel(context.Background(), &fakeHost{goos: "windows", q: q}, inst, AccelWHPX)
	}

	if err := run(okQEMU()); err != nil {
		t.Errorf("a machine that comes up and quits is a working accelerator: %v", err)
	}

	var pe *AccelProbeError
	failing := okQEMU()
	failing.probeExit, failing.probeStderr = 1, "qemu-system-x86_64.exe: -accel whpx: Failed to create partition\r\n\r\nsecond line\n"
	if err := run(failing); !errors.As(err, &pe) || pe.ExitCode != 1 || pe.Accel != AccelWHPX || pe.Timeout {
		t.Errorf("%v", err)
	} else if !strings.Contains(pe.Stderr, "Failed to create partition | second line") || strings.ContainsAny(pe.Stderr, "\r\n") {
		t.Errorf("stderr is kept as one tidy line: %q", pe.Stderr)
	} else if !strings.Contains(pe.Error(), "status 1") || !strings.Contains(pe.Error(), "-accel whpx") {
		t.Errorf("%v", pe)
	}

	// Exit status 0 with an accelerator complaint still counts as unusable.
	quiet := okQEMU()
	quiet.probeStderr = "qemu-system-x86_64: warning: WHPX: failed to set up the VM partition, falling back\n"
	if err := run(quiet); !errors.As(err, &pe) || pe.ExitCode != 0 || !strings.Contains(pe.Stderr, "WHPX") {
		t.Errorf("%v", err)
	}
	// ...but unrelated chatter, or a complaint about another accelerator, does not.
	for _, stderr := range []string{"", "warning: host doesn't support requested feature: CPUID.80000001H:ECX.svm\n", "kvm: failed to something\n", "whpx is great\n"} {
		q := okQEMU()
		q.probeStderr = stderr
		if err := run(q); err != nil {
			t.Errorf("stderr %q: %v", stderr, err)
		}
	}

	// The probe is bounded.
	defer func(d time.Duration) { accelProbeTimeout = d }(accelProbeTimeout)
	accelProbeTimeout = 50 * time.Millisecond
	hang := okQEMU()
	hang.probeHang = true
	if err := run(hang); !errors.As(err, &pe) || !pe.Timeout || !strings.Contains(err.Error(), "within") {
		t.Errorf("%v", err)
	}
	// A caller who gives up gets its own error, not a verdict about the accelerator.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ProbeAccel(ctx, &fakeHost{goos: "windows", q: okQEMU()}, inst, AccelWHPX); !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
	broken := okQEMU()
	broken.runErr = errors.New("exec format error")
	if err := run(broken); err == nil || errors.As(err, &pe) {
		t.Errorf("a QEMU that cannot even start is not an accelerator verdict: %v", err)
	}
}

func TestExcerpt(t *testing.T) {
	if got := excerpt("a\r\n\n  b  \n", 100); got != "a | b" {
		t.Errorf("%q", got)
	}
	long := strings.Repeat("é", 700) // 1400 bytes
	got := excerpt(long, 1024)
	if len(got) > 1024+3 || !strings.HasSuffix(got, "...") {
		t.Errorf("%d bytes", len(got))
	}
	if body := strings.TrimSuffix(got, "..."); strings.ToValidUTF8(body, "?") != body {
		t.Error("cut in the middle of a rune")
	}
}

// TestProbeAccelRealQEMU is the check against the real program, skipped where there is no QEMU. Software emulation
// must come up everywhere; a hardware accelerator must either work or fail with a verdict, never hang.
func TestProbeAccelRealQEMU(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	inst, err := Discover(ctx, OSHost{}, Options{})
	if errors.Is(err, ErrNotFound) {
		t.Skip("no QEMU installed here")
	}
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := ProbeAccel(ctx, OSHost{}, inst, AccelTCG); err != nil {
		t.Fatalf("tcg probe: %v", err)
	}
	t.Logf("tcg probe took %v", time.Since(start).Round(time.Millisecond))

	hardware := map[string]string{"linux": AccelKVM, "windows": AccelWHPX, "darwin": AccelHVF}[runtime.GOOS]
	if hardware == "" || !inst.HasAccel(hardware) {
		return
	}
	start = time.Now()
	err = ProbeAccel(ctx, OSHost{}, inst, hardware)
	var pe *AccelProbeError
	switch {
	case err == nil:
		t.Logf("%s probe: works (%v)", hardware, time.Since(start).Round(time.Millisecond))
	case errors.As(err, &pe):
		t.Logf("%s probe: not usable here (%v): %v", hardware, time.Since(start).Round(time.Millisecond), pe)
		if pe.Timeout {
			t.Errorf("%s: a missing accelerator must fail fast, not time out", hardware)
		}
	default:
		t.Errorf("%s probe: %v", hardware, err)
	}
}
