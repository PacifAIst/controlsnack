// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qemu

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Accelerators, spelled as QEMU's `-accel` takes them.
const (
	AccelKVM  = "kvm"
	AccelWHPX = "whpx"
	AccelHVF  = "hvf"
	AccelTCG  = "tcg"
)

// AccelArgs are the command-line words that select the accelerator (PLAN.md section 7.2). Never
// `kernel-irqchip=` or `ssd=off` for WHPX: QEMU documents the first as a debugging option and the second weakens
// security mitigations.
func AccelArgs(accel string) []string {
	if accel == AccelKVM {
		return []string{"-enable-kvm"}
	}
	return []string{"-accel", accel}
}

// CPUModel is the `-cpu` value under the accelerator (the same for both guest architectures), "" meaning "leave
// -cpu out" (PLAN.md section 7.2): `host` is only valid with hardware acceleration, `max` is the best software
// model, and WHPX keeps QEMU's default because `max` has been reported to fail to boot under it (QEMU issue
// #1043).
func CPUModel(accel string) string {
	switch accel {
	case AccelKVM, AccelHVF:
		return "host"
	case AccelTCG:
		return "max"
	}
	return ""
}

// accelProbeTimeout is how long a hardware accelerator gets to start an empty machine (PLAN.md section 7.1). It is
// a variable only so that tests need not wait it out.
var accelProbeTimeout = 10 * time.Second

// quitScript answers QMP's greeting and ends the machine. QEMU does not exit when its QMP stdin simply ends.
const quitScript = `{"execute":"qmp_capabilities"}` + "\n" + `{"execute":"quit"}` + "\n"

// AccelProbeError says that QEMU could not start an empty machine with the accelerator.
type AccelProbeError struct {
	Accel    string
	ExitCode int
	Timeout  bool
	Stderr   string // an excerpt of what QEMU said, at most 1 KiB
}

func (e *AccelProbeError) Error() string {
	var why string
	switch {
	case e.Timeout:
		why = fmt.Sprintf("did not start an empty machine within %s", accelProbeTimeout)
	case e.ExitCode != 0:
		why = fmt.Sprintf("exited with status %d", e.ExitCode)
	default:
		why = "reported an accelerator error"
	}
	if e.Stderr != "" {
		why += ": " + e.Stderr
	}
	return fmt.Sprintf("QEMU with -accel %s %s", e.Accel, why)
}

// AccelProbeArgs is the command line of the probe: an empty, paused machine of the guest architecture that
// answers QMP on stdin and stdout (PLAN.md section 7.1; stdio instead of a socket needs no endpoint).
func AccelProbeArgs(arch, accel string) []string {
	args := append(AccelArgs(accel), "-machine", requiredMachine[arch])
	if cpu := CPUModel(accel); cpu != "" {
		args = append(args, "-cpu", cpu)
	}
	return append(args, "-m", "128", "-nodefaults", "-display", "none", "-S", "-qmp", "stdio")
}

// ProbeAccel starts QEMU briefly with the accelerator and quits it again. It returns nil when the machine came
// up and went down cleanly, an *AccelProbeError when the accelerator is not usable, and the context's error when
// ctx ended first.
func ProbeAccel(ctx context.Context, h Host, inst Install, accel string) error {
	pctx, cancel := context.WithTimeout(ctx, accelProbeTimeout)
	defer cancel()
	res, err := h.Run(pctx, inst.System, quitScript, AccelProbeArgs(inst.Arch, accel)...)
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		return &AccelProbeError{Accel: accel, Timeout: true}
	case err != nil:
		return fmt.Errorf("starting %s: %w", inst.System, err)
	}
	if res.ExitCode != 0 {
		return &AccelProbeError{Accel: accel, ExitCode: res.ExitCode, Stderr: excerpt(res.Stderr, 1024)}
	}
	// QEMU can start, complain about the accelerator and carry on with software emulation.
	if line := accelErrorLine(res.Stderr, accel); line != "" {
		return &AccelProbeError{Accel: accel, Stderr: excerpt(line, 1024)}
	}
	return nil
}

var reAccelFailure = regexp.MustCompile(`(?i)\b(fail(ed|ure)?|errors?|cannot|can't|could not|unable|not supported|unsupported|denied|invalid|not available)\b`)

// accelErrorLine returns the first stderr line that names the accelerator and a failure, or "".
func accelErrorLine(stderr, accel string) string {
	word := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(accel) + `\b`)
	for _, line := range strings.Split(stderr, "\n") {
		if word.MatchString(line) && reAccelFailure.MatchString(line) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// excerpt trims s, collapses it to its non-empty lines and cuts it to n bytes on a rune boundary.
func excerpt(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	out := strings.Join(lines, " | ")
	if len(out) <= n {
		return out
	}
	for n > 0 && out[n]&0xC0 == 0x80 { // do not cut a multi-byte rune in half
		n--
	}
	return out[:n] + "..."
}
