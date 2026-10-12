// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
)

// Check is one more thing the doctor looks at (the base image, browsers for the viewer, local model servers). The
// packages that own those things add theirs as they land; the doctor only runs them and prints the outcome.
type Check struct {
	Name string
	Run  func(ctx context.Context) CheckResult
}

// CheckResult is what a Check found.
type CheckResult struct {
	Status string `json:"status"` // LevelOK, LevelInfo, LevelWarn or LevelFail
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// CheckOutcome is a finished Check, as it appears in the report.
type CheckOutcome struct {
	Name string `json:"name"`
	CheckResult
}

// QEMUReport describes the QEMU the doctor found.
type QEMUReport struct {
	QEMUState
	Source   string         `json:"source,omitempty"`
	System   string         `json:"system,omitempty"`
	Img      string         `json:"img,omitempty"`
	Banner   string         `json:"banner,omitempty"`
	Accels   []string       `json:"accelerators,omitempty"`
	Sandbox  bool           `json:"sandbox"`
	Firmware *qemu.Firmware `json:"firmware,omitempty"`
	Attempts []qemu.Attempt `json:"searched,omitempty"`
}

// Report is everything `doctor` knows. `--json` prints it as it is.
type Report struct {
	Host   Facts          `json:"host"`
	QEMU   QEMUReport     `json:"qemu"`
	Accel  AccelResult    `json:"accel"`
	Checks []CheckOutcome `json:"checks,omitempty"`
	Diagnosis
}

// DoctorService is the Doctor of the running machine.
type DoctorService struct {
	Host qemu.Host
	// Collect gathers the facts; the default reads the operating system (doctor_{linux,windows,darwin}.go).
	Collect func(ctx context.Context, h qemu.Host, inst *qemu.Install) Facts
	// Probe builds the accelerator probe from the QEMU found; the default is the real one (NewHostProbe).
	Probe func(h qemu.Host, inst qemu.Install) HostProbe
	// Checks are run after the virtualization checks; each gets a few seconds.
	Checks []Check
}

// NewDoctor returns the real doctor.
func NewDoctor() Doctor { return &DoctorService{Host: qemu.OSHost{}} }

// checkTimeout bounds one extra check.
const checkTimeout = 10 * time.Second

// Build gathers the report. It is read-only: it starts QEMU for two empty, paused machines (the tcg probe and, where
// the OS has one, the hardware probe `run` would also make) and changes nothing on the system.
func (s *DoctorService) Build(ctx context.Context, cfg config.Config) (Report, error) {
	if s.Host == nil {
		s.Host = qemu.OSHost{}
	}
	var rep Report

	inst, err := qemu.Discover(ctx, s.Host, qemu.Options{Dir: cfg.VM.QemuDir})
	if cerr := ctx.Err(); cerr != nil {
		return rep, cerr
	}
	state := describeQEMU(inst, err)
	rep.QEMU = QEMUReport{QEMUState: state, Source: string(inst.Source), System: inst.System, Img: inst.Img, Banner: inst.Banner,
		Accels: inst.Accels, Sandbox: inst.Sandbox, Firmware: inst.Firmware, Attempts: inst.Attempts}
	var instPtr *qemu.Install
	if state.Found {
		instPtr = &inst
	}

	if state.Found && state.Problem == "" {
		if perr := qemu.ProbeAccel(ctx, s.Host, inst, qemu.AccelTCG); perr != nil {
			if cerr := ctx.Err(); cerr != nil {
				return rep, cerr
			}
			state.TCGProbeErr = perr.Error()
			state.Problem = "an empty machine could not be started with software emulation: " + perr.Error()
		} else {
			state.Usable = true
		}
		rep.QEMU.QEMUState = state
	}

	collect := s.Collect
	if collect == nil {
		collect = collectFacts
	}
	rep.Host = collect(ctx, s.Host, instPtr)

	if state.Usable {
		newProbe := s.Probe
		if newProbe == nil {
			newProbe = NewHostProbe
		}
		acc, aerr := ResolveAccel(ctx, newProbe(s.Host, inst), cfg.VM.Accel, cfg.VM.Slow)
		switch {
		case aerr != nil && ctx.Err() != nil:
			return rep, ctx.Err()
		case aerr != nil && exitcode.Of(aerr) == exitcode.Usage:
			return rep, aerr // --accel names an accelerator this OS does not have, or conflicts with --slow
		case aerr != nil:
			state.Usable = false
			state.Problem = aerr.Error()
			rep.QEMU.QEMUState = state
		}
		rep.Accel = acc
	}

	for _, c := range s.Checks {
		cctx, cancel := context.WithTimeout(ctx, checkTimeout)
		res := c.Run(cctx)
		cancel()
		rep.Checks = append(rep.Checks, CheckOutcome{Name: c.Name, CheckResult: res})
	}

	rep.Diagnosis = Diagnose(rep.Host, state, rep.Accel)
	return rep, nil
}

// describeQEMU turns the outcome of discovery into the state the diagnosis works from.
func describeQEMU(inst qemu.Install, err error) QEMUState {
	st := QEMUState{Dir: inst.Dir}
	if inst.Version != (qemu.Version{}) {
		st.Version = inst.Version.String()
	}
	var nf *qemu.NotFoundError
	var ve *qemu.VersionError
	var fe *qemu.FeatureError
	switch {
	case err == nil:
		st.Found = true
	case errors.As(err, &nf):
		st.Problem = err.Error()
	case errors.As(err, &ve), errors.As(err, &fe):
		st.Found, st.Problem = true, err.Error()
	default:
		st.Found, st.Problem = inst.System != "", err.Error()
	}
	return st
}

// Run prints the report and returns an exit-code-3 error when no VM can run.
func (s *DoctorService) Run(ctx context.Context, w io.Writer, cfg config.Config, asJSON bool) error {
	rep, err := s.Build(ctx, cfg)
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else if err := RenderReport(w, rep); err != nil {
		return err
	}
	if !rep.CanRun {
		return exitcode.New(exitcode.Environment, errors.New("no VM can run on this machine yet: "+rep.Verdict))
	}
	return nil
}

// RenderReport writes the human-readable report. The verdict and the numbered steps are always the last block.
func RenderReport(w io.Writer, rep Report) error {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	kv := func(k, v string) {
		if v != "" {
			line("  %-22s %s", k, v)
		}
	}

	line("controlsnack doctor (read-only: nothing is changed on this machine)")
	line("")
	line("Host")
	kv("system", strings.TrimSpace(rep.Host.OSVersion+", "+rep.Host.Arch))
	kv("cpu", rep.Host.CPUModel)
	renderOS(kv, rep.Host)

	line("")
	line("QEMU")
	if rep.QEMU.Found {
		kv("version", rep.QEMU.Banner)
		kv("emulator", rep.QEMU.System)
		kv("qemu-img", rep.QEMU.Img)
		kv("found via", rep.QEMU.Source)
		kv("accelerators", strings.Join(rep.QEMU.Accels, ", "))
		if rep.QEMU.Firmware != nil {
			kv("uefi firmware", rep.QEMU.Firmware.Code)
		}
		if runtimeLinux(rep.Host) {
			kv("-sandbox", map[bool]string{true: "supported", false: "not available"}[rep.QEMU.Sandbox])
		}
	}
	if !rep.QEMU.Usable {
		line("  not usable: see the notes below")
	}

	if len(rep.Accel.Tried) > 0 {
		line("")
		line("Accelerators")
		for _, t := range rep.Accel.Tried {
			mark := "usable"
			if !t.Usable {
				mark = "not usable"
			}
			if t.Reason != "" {
				line("  %-6s %s: %s", t.Accel, mark, t.Reason)
			} else {
				line("  %-6s %s", t.Accel, mark)
			}
		}
	}

	if len(rep.Checks) > 0 {
		line("")
		line("Other checks")
		for _, c := range rep.Checks {
			line("  [%s] %s: %s", c.Status, c.Name, c.Detail)
			if c.Fix != "" {
				line("         fix: %s", c.Fix)
			}
		}
	}

	if len(rep.Notes) > 0 {
		line("")
		line("Notes")
		for _, n := range rep.Notes {
			line("  [%s] %s", n.Level, n.Text)
		}
	}

	line("")
	line("Verdict")
	line("  %s", rep.Verdict)
	if len(rep.Steps) > 0 {
		line("")
		if rep.CanRun {
			line("To do better:")
		} else {
			line("To fix this:")
		}
		for i, s := range rep.Steps {
			line("  %d. %s", i+1, s)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func runtimeLinux(f Facts) bool { return f.OS == "linux" }

func renderOS(kv func(k, v string), f Facts) {
	yesNo := func(b *bool) string {
		switch {
		case b == nil:
			return "unknown"
		case *b:
			return "yes"
		}
		return "no"
	}
	switch {
	case f.Linux != nil:
		l := f.Linux
		kv("virtualization", firstNonEmpty(l.VirtFlag, "none reported"))
		if l.InVM {
			kv("inside a VM", "yes")
		}
		kv("kvm module", firstNonEmpty(l.KVMModule, "not a loadable module (built in, or not available)"))
		if l.DevKVM {
			kv("/dev/kvm", fmt.Sprintf("%s %s:%s, this user can open it: %v", l.DevKVMMode, l.DevKVMOwner, l.DevKVMGroup, l.UserCanOpen))
		} else {
			kv("/dev/kvm", "does not exist")
		}
		kv("kvm group", fmt.Sprintf("this user is a member: %v", l.UserInKVMGroup))
	case f.Windows != nil:
		w := f.Windows
		kv("edition id", fmt.Sprintf("%s (Windows Home: %v)", w.EditionID, w.Home))
		kv("hypervisor", "running: "+yesNo(w.HypervisorPresent))
		kv("firmware virt.", yesNo(w.FirmwareVirtualization))
		kv("Hypervisor Platform", string(w.HypervisorPlatform))
		kv("Virtual Machine Plat.", string(w.VirtualMachinePlat))
		kv("Hyper-V", string(w.HyperV))
		kv("WHvGetCapability", "hypervisor present: "+yesNo(w.WHvHypervisor))
		if w.WHvError != "" {
			kv("", "("+w.WHvError+")")
		}
	case f.Darwin != nil:
		d := f.Darwin
		kv("chip", map[bool]string{true: "Apple Silicon", false: "Intel"}[d.AppleSilicon])
		kv("kern.hv_support", yesNo(d.HVSupport))
		kv("hypervisor entitlement", yesNo(d.Entitlement))
	}
}
