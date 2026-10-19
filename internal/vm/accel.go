// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/PacifAIst/controlsnack/internal/exitcode"
	"github.com/PacifAIst/controlsnack/internal/vm/qemu"
)

// Accelerator names. AccelAuto is only ever a request.
const (
	AccelAuto = "auto"
	AccelKVM  = qemu.AccelKVM
	AccelWHPX = qemu.AccelWHPX
	AccelHVF  = qemu.AccelHVF
	AccelTCG  = qemu.AccelTCG
)

// slowFactor multiplies the VM-layer timeouts under software emulation (D-32).
const slowFactor = 4

// ErrNoAccelerator means not even software emulation is available.
var ErrNoAccelerator = errors.New("no usable accelerator")

// AccelTry is one accelerator the detector considered.
type AccelTry struct {
	Accel  string `json:"accel"`
	Usable bool   `json:"usable"`
	Reason string `json:"reason,omitempty"` // why it is not usable, or why it was chosen without checking
}

// AccelResult is the outcome of choosing an accelerator. It feeds `run`, `doctor`, the viewer's hello and the
// audit log.
type AccelResult struct {
	Requested string     `json:"requested"` // auto, kvm, whpx, hvf, tcg, or slow for --slow
	Chosen    string     `json:"chosen"`
	Tried     []AccelTry `json:"tried"`
	Slow      bool       `json:"slow"` // Chosen is tcg
}

// TimeoutFactor is what the VM-layer timeouts (launch window, boot-to-agent wait, agent push, handshake) are
// multiplied by: 4 under software emulation, 1 otherwise. Safety budgets, LLM timeouts and agent tool timeouts
// never change.
func (r AccelResult) TimeoutFactor() int {
	if r.Chosen == AccelTCG {
		return slowFactor
	}
	return 1
}

// HostProbe is everything the detector asks the machine, so every branch is tested with fakes on any OS.
type HostProbe interface {
	GOOS() string
	// QEMUAccels lists the accelerators the located QEMU build supports (`-accel help`).
	QEMUAccels() []string
	// OpenKVM opens /dev/kvm for reading and writing (Linux).
	OpenKVM() error
	// WHPXPresent asks WHvGetCapability whether the Windows Hypervisor Platform has a hypervisor (Windows).
	WHPXPresent() (bool, error)
	// HVFSupported reads sysctl kern.hv_support (macOS).
	HVFSupported() (bool, error)
	// ProbeQEMU starts an empty machine with the accelerator and quits it.
	ProbeQEMU(ctx context.Context, accel string) error
}

// hardwareAccel is the one hardware accelerator an OS offers.
func hardwareAccel(goos string) string {
	switch goos {
	case "linux":
		return AccelKVM
	case "windows":
		return AccelWHPX
	case "darwin":
		return AccelHVF
	}
	return ""
}

// DetectAccel returns the hardware accelerator of this OS when it is usable and software emulation otherwise
// (PLAN.md section 7.1; D-31). Only the current OS's accelerator is checked. The error is the context's, or
// ErrNoAccelerator when even TCG is missing from the QEMU build.
func DetectAccel(ctx context.Context, p HostProbe) (AccelResult, error) {
	res := AccelResult{Requested: AccelAuto}
	if hw := hardwareAccel(p.GOOS()); hw != "" {
		try := tryHardware(ctx, p, hw)
		if err := ctx.Err(); err != nil {
			return AccelResult{}, err
		}
		res.Tried = append(res.Tried, try)
		if try.Usable {
			res.Chosen = hw
			return res, nil
		}
	}
	if !supports(p, AccelTCG) {
		res.Tried = append(res.Tried, AccelTry{Accel: AccelTCG, Reason: "this QEMU build has no tcg accelerator"})
		return res, fmt.Errorf("%w: no hardware accelerator works here and this QEMU build lacks software emulation", ErrNoAccelerator)
	}
	res.Tried = append(res.Tried, AccelTry{Accel: AccelTCG, Usable: true, Reason: "software emulation, always available"})
	res.Chosen, res.Slow = AccelTCG, true
	return res, nil
}

// supports reports whether the QEMU build lists the accelerator; a probe that lists nothing is taken as unknown.
func supports(p HostProbe, accel string) bool {
	have := p.QEMUAccels()
	return len(have) == 0 || slices.Contains(have, accel)
}

func tryHardware(ctx context.Context, p HostProbe, accel string) AccelTry {
	t := AccelTry{Accel: accel}
	fail := func(format string, args ...any) AccelTry {
		t.Reason = fmt.Sprintf(format, args...)
		return t
	}
	if !supports(p, accel) {
		return fail("this QEMU build was compiled without %s support", accel)
	}
	switch accel {
	case AccelKVM:
		if err := p.OpenKVM(); err != nil {
			return fail("%v", err)
		}
		// No probe launch here: /dev/kvm opening is the test (PLAN.md section 7.1), and a KVM that still fails to
		// start falls back to TCG once at launch (section 7.9).
		t.Usable = true
		return t
	case AccelWHPX:
		present, err := p.WHPXPresent()
		if err != nil {
			return fail("%v", err)
		}
		if !present {
			return fail("the Windows Hypervisor Platform reports no hypervisor: the \"Windows Hypervisor Platform\" optional feature is off")
		}
	case AccelHVF:
		ok, err := p.HVFSupported()
		if err != nil {
			return fail("%v", err)
		}
		if !ok {
			return fail("sysctl kern.hv_support is 0: this Mac does not offer the Hypervisor framework")
		}
	}
	// The API says yes; whether QEMU can really use it (entitlement, a hypervisor held elsewhere) only a launch shows.
	if err := p.ProbeQEMU(ctx, accel); err != nil {
		return fail("%v", err)
	}
	t.Usable = true
	return t
}

// ResolveAccel applies --accel and --slow (D-32): --slow and `tcg` mean software emulation; `auto` or "" detects;
// a hardware accelerator must match the host OS, skips detection and still gets the single TCG retry at launch.
func ResolveAccel(ctx context.Context, p HostProbe, accel string, slow bool) (AccelResult, error) {
	return resolve(ctx, p, accel, slow, func(ctx context.Context) (AccelResult, error) { return DetectAccel(ctx, p) })
}

func resolve(ctx context.Context, p HostProbe, accel string, slow bool, detect func(context.Context) (AccelResult, error)) (AccelResult, error) {
	switch accel {
	case "", AccelAuto, AccelTCG, AccelKVM, AccelWHPX, AccelHVF:
	default:
		return AccelResult{}, exitcode.Usagef("unknown accelerator %q: use auto, kvm, whpx, hvf or tcg", accel)
	}
	if accel == "" {
		accel = AccelAuto
	}
	hardware := accel != AccelAuto && accel != AccelTCG
	if slow && hardware {
		return AccelResult{}, exitcode.Usagef("--slow means software emulation and conflicts with --accel=%s", accel)
	}
	switch {
	case slow:
		return forcedTCG("slow", "forced by --slow"), nil
	case accel == AccelTCG:
		return forcedTCG(AccelTCG, "forced by --accel=tcg"), nil
	case accel == AccelAuto:
		return detect(ctx)
	}
	if want := hardwareAccel(p.GOOS()); accel != want {
		return AccelResult{}, exitcode.Usagef("--accel=%s is not available on %s (use auto, or tcg for software emulation)", accel, p.GOOS())
	}
	return AccelResult{Requested: accel, Chosen: accel,
		Tried: []AccelTry{{Accel: accel, Usable: true, Reason: "forced by --accel; detection skipped"}}}, nil
}

func forcedTCG(requested, reason string) AccelResult {
	return AccelResult{Requested: requested, Chosen: AccelTCG, Slow: true,
		Tried: []AccelTry{{Accel: AccelTCG, Usable: true, Reason: reason}}}
}

// Detector caches the outcome of automatic detection for the life of the process: hardware and Windows features
// can change between runs, but not between the `run` that detects and the retry that follows it.
type Detector struct {
	Probe HostProbe

	mu   sync.Mutex
	done bool
	res  AccelResult
}

// Resolve is ResolveAccel with the cached detection.
func (d *Detector) Resolve(ctx context.Context, accel string, slow bool) (AccelResult, error) {
	return resolve(ctx, d.Probe, accel, slow, d.Detect)
}

// Detect runs DetectAccel once; errors (a canceled context) are not cached.
func (d *Detector) Detect(ctx context.Context) (AccelResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done {
		return d.res, nil
	}
	res, err := DetectAccel(ctx, d.Probe)
	if err != nil {
		return res, err
	}
	d.res, d.done = res, true
	return res, nil
}
