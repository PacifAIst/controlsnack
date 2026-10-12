// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
)

// fakeProbe is a HostProbe whose answers are set per test; it records what was asked.
type fakeProbe struct {
	goos   string
	accels []string // nil = not listed (unknown)

	kvmErr      error
	whpx        bool
	whpxErr     error
	hvf         bool
	hvfErr      error
	probeErr    error
	probeCancel context.CancelFunc // called during the probe launch

	asked  []string // "kvm", "whpx", "hvf" checks, in order
	probed []string // accelerators a QEMU was launched with
}

func (f *fakeProbe) GOOS() string         { return f.goos }
func (f *fakeProbe) QEMUAccels() []string { return f.accels }
func (f *fakeProbe) OpenKVM() error       { f.asked = append(f.asked, "kvm"); return f.kvmErr }
func (f *fakeProbe) WHPXPresent() (bool, error) {
	f.asked = append(f.asked, "whpx")
	return f.whpx, f.whpxErr
}
func (f *fakeProbe) HVFSupported() (bool, error) {
	f.asked = append(f.asked, "hvf")
	return f.hvf, f.hvfErr
}
func (f *fakeProbe) ProbeQEMU(ctx context.Context, accel string) error {
	f.probed = append(f.probed, accel)
	if f.probeCancel != nil {
		f.probeCancel()
		return ctx.Err()
	}
	return f.probeErr
}

func detect(t *testing.T, f *fakeProbe) AccelResult {
	t.Helper()
	res, err := DetectAccel(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestDetectAccelBranches(t *testing.T) {
	probeFail := &qemu.AccelProbeError{Accel: "whpx", ExitCode: 1, Stderr: "Failed to create partition"}
	tests := []struct {
		name       string
		probe      fakeProbe
		chosen     string
		reason     string   // part of the reason the hardware accelerator was refused
		asked      []string // checks made, in order
		wantProbed []string
	}{
		{name: "linux, /dev/kvm opens", probe: fakeProbe{goos: "linux"}, chosen: "kvm", asked: []string{"kvm"}},
		{name: "linux, no permission", probe: fakeProbe{goos: "linux", kvmErr: errors.New("no permission to open /dev/kvm: this user is not in the kvm group")},
			chosen: "tcg", reason: "kvm group", asked: []string{"kvm"}},
		{name: "linux, no /dev/kvm", probe: fakeProbe{goos: "linux", kvmErr: errors.New("/dev/kvm does not exist")},
			chosen: "tcg", reason: "does not exist", asked: []string{"kvm"}},
		{name: "linux, QEMU built without kvm", probe: fakeProbe{goos: "linux", accels: []string{"tcg"}},
			chosen: "tcg", reason: "compiled without kvm"},

		{name: "windows, whpx works", probe: fakeProbe{goos: "windows", whpx: true}, chosen: "whpx", asked: []string{"whpx"}, wantProbed: []string{"whpx"}},
		{name: "windows, DLL missing", probe: fakeProbe{goos: "windows", whpxErr: errors.New("WinHvPlatform.dll cannot be used")},
			chosen: "tcg", reason: "WinHvPlatform.dll", asked: []string{"whpx"}},
		{name: "windows, feature off", probe: fakeProbe{goos: "windows", whpx: false},
			chosen: "tcg", reason: "optional feature is off", asked: []string{"whpx"}},
		{name: "windows, API yes, QEMU says no", probe: fakeProbe{goos: "windows", whpx: true, probeErr: probeFail},
			chosen: "tcg", reason: "Failed to create partition", asked: []string{"whpx"}, wantProbed: []string{"whpx"}},
		{name: "windows, QEMU without whpx", probe: fakeProbe{goos: "windows", whpx: true, accels: []string{"tcg"}},
			chosen: "tcg", reason: "compiled without whpx"},

		{name: "macOS, hvf works", probe: fakeProbe{goos: "darwin", hvf: true}, chosen: "hvf", asked: []string{"hvf"}, wantProbed: []string{"hvf"}},
		{name: "macOS, kern.hv_support 0", probe: fakeProbe{goos: "darwin"}, chosen: "tcg", reason: "kern.hv_support is 0", asked: []string{"hvf"}},
		{name: "macOS, sysctl fails", probe: fakeProbe{goos: "darwin", hvfErr: errors.New("sysctl: no such key")}, chosen: "tcg", reason: "no such key", asked: []string{"hvf"}},
		{name: "macOS, no entitlement", probe: fakeProbe{goos: "darwin", hvf: true, probeErr: errors.New("hvf: Hypervisor entitlement missing")},
			chosen: "tcg", reason: "entitlement", asked: []string{"hvf"}, wantProbed: []string{"hvf"}},

		{name: "an OS without hardware acceleration", probe: fakeProbe{goos: "freebsd"}, chosen: "tcg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.probe
			res := detect(t, &f)
			if res.Chosen != tt.chosen || res.Requested != AccelAuto || res.Slow != (tt.chosen == "tcg") {
				t.Fatalf("%+v", res)
			}
			if !slices.Equal(f.asked, tt.asked) {
				t.Errorf("checks made: %v, want %v (only this OS's accelerator is checked)", f.asked, tt.asked)
			}
			if !slices.Equal(f.probed, tt.wantProbed) {
				t.Errorf("QEMU probe launches: %v, want %v", f.probed, tt.wantProbed)
			}
			if tt.chosen == "tcg" {
				last := res.Tried[len(res.Tried)-1]
				if last.Accel != "tcg" || !last.Usable {
					t.Errorf("tcg closes the list: %+v", res.Tried)
				}
				if tt.reason != "" {
					first := res.Tried[0]
					if first.Usable || !strings.Contains(first.Reason, tt.reason) {
						t.Errorf("hardware try %+v, want a refusal mentioning %q", first, tt.reason)
					}
				}
				if tt.probe.goos == "freebsd" && len(res.Tried) != 1 {
					t.Errorf("nothing to try on freebsd: %+v", res.Tried)
				}
			} else if len(res.Tried) != 1 || !res.Tried[0].Usable || res.Tried[0].Accel != tt.chosen {
				t.Errorf("a usable hardware accelerator is chosen at once: %+v", res.Tried)
			}
			if got, want := res.TimeoutFactor(), map[bool]int{true: 4, false: 1}[tt.chosen == "tcg"]; got != want {
				t.Errorf("timeout factor %d, want %d", got, want)
			}
		})
	}
}

func TestDetectAccelNeedsSomeAccelerator(t *testing.T) {
	// A QEMU build with neither the hardware accelerator nor tcg cannot run anything.
	f := &fakeProbe{goos: "linux", accels: []string{"xen"}}
	res, err := DetectAccel(context.Background(), f)
	if !errors.Is(err, ErrNoAccelerator) {
		t.Fatalf("%v", err)
	}
	if len(res.Tried) != 2 || res.Chosen != "" {
		t.Errorf("%+v", res)
	}
}

func TestDetectAccelCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeProbe{goos: "windows", whpx: true, probeCancel: cancel}
	if _, err := DetectAccel(ctx, f); !errors.Is(err, context.Canceled) {
		t.Errorf("a canceled detection is an error, not a quiet fall back to tcg: %v", err)
	}
}

func TestResolveAccel(t *testing.T) {
	ctx := context.Background()
	usage := func(t *testing.T, err error) {
		t.Helper()
		if err == nil || exitcode.Of(err) != exitcode.Usage {
			t.Errorf("want a usage error (exit 2), got %v", err)
		}
	}

	t.Run("slow", func(t *testing.T) {
		f := &fakeProbe{goos: "linux"}
		res, err := ResolveAccel(ctx, f, "auto", true)
		if err != nil || res.Chosen != "tcg" || !res.Slow || res.Requested != "slow" || len(f.asked)+len(f.probed) != 0 {
			t.Errorf("--slow does not detect: %+v %v %v", res, err, f.asked)
		}
		if res, err = ResolveAccel(ctx, f, "tcg", true); err != nil || res.Chosen != "tcg" {
			t.Errorf("--slow --accel=tcg agree: %+v %v", res, err)
		}
		if res, err = ResolveAccel(ctx, f, "", true); err != nil || res.Chosen != "tcg" {
			t.Errorf("%+v %v", res, err)
		}
		if !strings.Contains(res.Tried[0].Reason, "--slow") {
			t.Errorf("%+v", res.Tried)
		}
	})
	t.Run("tcg", func(t *testing.T) {
		f := &fakeProbe{goos: "windows", whpx: true}
		res, err := ResolveAccel(ctx, f, "tcg", false)
		if err != nil || res.Chosen != "tcg" || res.Requested != "tcg" || !res.Slow || len(f.asked) != 0 {
			t.Errorf("%+v %v", res, err)
		}
	})
	t.Run("auto and empty detect", func(t *testing.T) {
		for _, accel := range []string{"", "auto"} {
			f := &fakeProbe{goos: "linux"}
			res, err := ResolveAccel(ctx, f, accel, false)
			if err != nil || res.Chosen != "kvm" || res.Requested != "auto" || !slices.Equal(f.asked, []string{"kvm"}) {
				t.Errorf("%q: %+v %v", accel, res, err)
			}
		}
	})
	t.Run("explicit hardware skips detection", func(t *testing.T) {
		for goos, accel := range map[string]string{"linux": "kvm", "windows": "whpx", "darwin": "hvf"} {
			// The machine would refuse the accelerator; an explicit choice is still honored (and retried with tcg at launch).
			f := &fakeProbe{goos: goos, kvmErr: errors.New("no"), whpx: false, hvf: false}
			res, err := ResolveAccel(ctx, f, accel, false)
			if err != nil || res.Chosen != accel || res.Requested != accel || res.Slow || len(f.asked)+len(f.probed) != 0 {
				t.Errorf("%s: %+v %v %v", accel, res, err, f.asked)
			}
			if !strings.Contains(res.Tried[0].Reason, "detection skipped") {
				t.Errorf("%+v", res.Tried)
			}
		}
	})
	t.Run("usage errors", func(t *testing.T) {
		_, err := ResolveAccel(ctx, &fakeProbe{goos: "windows"}, "kvm", false)
		usage(t, err)
		if !strings.Contains(err.Error(), "--accel=kvm") || !strings.Contains(err.Error(), "windows") {
			t.Errorf("the message names the flag and the OS: %v", err)
		}
		_, err = ResolveAccel(ctx, &fakeProbe{goos: "linux"}, "hvf", false)
		usage(t, err)
		_, err = ResolveAccel(ctx, &fakeProbe{goos: "freebsd"}, "kvm", false)
		usage(t, err)
		for _, hw := range []string{"kvm", "whpx", "hvf"} {
			_, err = ResolveAccel(ctx, &fakeProbe{goos: "linux"}, hw, true)
			usage(t, err)
		}
		_, err = ResolveAccel(ctx, &fakeProbe{goos: "linux"}, "xen", false)
		usage(t, err)
		_, err = ResolveAccel(ctx, &fakeProbe{goos: "linux"}, "KVM", false)
		usage(t, err) // values are exact; config normalizes case before they get here
	})
}

func TestDetectorCachesForTheProcess(t *testing.T) {
	f := &fakeProbe{goos: "windows", whpx: true}
	d := &Detector{Probe: f}
	for range 3 {
		res, err := d.Detect(context.Background())
		if err != nil || res.Chosen != "whpx" {
			t.Fatal(res, err)
		}
	}
	if len(f.probed) != 1 {
		t.Errorf("detection ran %d times, want once", len(f.probed))
	}
	res, err := d.Resolve(context.Background(), "auto", false)
	if err != nil || res.Chosen != "whpx" || len(f.probed) != 1 {
		t.Errorf("Resolve uses the cache: %+v %v %v", res, err, f.probed)
	}
	if res, _ := d.Resolve(context.Background(), "tcg", false); res.Chosen != "tcg" {
		t.Errorf("a forced tcg is not overridden by the cache: %+v", res)
	}

	// A failed detection is not cached.
	ctx, cancel := context.WithCancel(context.Background())
	g := &fakeProbe{goos: "windows", whpx: true, probeCancel: cancel}
	d2 := &Detector{Probe: g}
	if _, err := d2.Detect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	g.probeCancel = nil
	if res, err := d2.Detect(context.Background()); err != nil || res.Chosen != "whpx" {
		t.Errorf("%+v %v", res, err)
	}
}

func TestAccelResultJSON(t *testing.T) {
	res := detect(t, &fakeProbe{goos: "linux", kvmErr: errors.New("nope")})
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"requested", "chosen", "tried", "slow"} {
		if _, ok := m[k]; !ok {
			t.Errorf("%s missing from %s", k, data)
		}
	}
	if !strings.Contains(string(data), `"reason":"nope"`) {
		t.Errorf("%s", data)
	}
}

// --- the real machine ------------------------------------------------------------------------------------------

func TestOtherSystemsChecksAreNotApplicable(t *testing.T) {
	if runtime.GOOS != "linux" && !errors.Is(openKVM(), errNotHere) {
		t.Error("openKVM off Linux")
	}
	if runtime.GOOS != "windows" {
		if _, err := whpxPresent(); !errors.Is(err, errNotHere) {
			t.Error("whpxPresent off Windows")
		}
	}
	if runtime.GOOS != "darwin" {
		if _, err := hvfSupported(); !errors.Is(err, errNotHere) {
			t.Error("hvfSupported off macOS")
		}
	}
}

// TestDetectAccelOnThisMachine runs the real detector against the real QEMU, where there is one.
func TestDetectAccelOnThisMachine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	inst, err := qemu.Discover(ctx, qemu.OSHost{}, qemu.Options{})
	if errors.Is(err, qemu.ErrNotFound) {
		t.Skip("no QEMU installed here")
	}
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res, err := DetectAccel(ctx, NewHostProbe(qemu.OSHost{}, inst))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("detected in %v: chosen %s, slow %v", time.Since(start).Round(time.Millisecond), res.Chosen, res.Slow)
	for _, try := range res.Tried {
		t.Logf("  %-5s usable=%-5v %s", try.Accel, try.Usable, try.Reason)
	}
	if res.Chosen == "" || res.Slow != (res.Chosen == "tcg") {
		t.Errorf("%+v", res)
	}
}
