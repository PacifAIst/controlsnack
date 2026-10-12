// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qemu

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Source says how an installation was found.
type Source string

// The places discovery looks, in this order (PLAN.md section 7.1).
const (
	SourceConfig Source = "config" // --qemu / vm.qemu_dir
	SourcePath   Source = "path"   // a directory on PATH
	SourceKnown  Source = "known"  // a well-known install directory
)

// probeTimeout bounds each program run during discovery. A cold QEMU start on Windows can take several seconds
// while the antivirus scans the binary.
const probeTimeout = 30 * time.Second

// ErrNotFound means no usable QEMU installation was found. The Attempts say where discovery looked and why each
// place did not qualify.
var ErrNotFound = errors.New("QEMU not found")

// NotFoundError carries ErrNotFound together with what was tried.
type NotFoundError struct {
	Binary   string
	Attempts []Attempt
}

// Error names every place that mattered. The directories on PATH that simply do not have the program are only
// counted, because PATH often holds dozens of them; a PATH directory that has the emulator but lacks qemu-img, and
// every explicit or well-known place, are listed with their reason. The full list is in Attempts.
func (e *NotFoundError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: could not find %s", ErrNotFound, e.Binary)
	plain := 0
	for _, a := range e.Attempts {
		if a.Source == SourcePath && strings.HasSuffix(a.Reason, ": not found") {
			plain++
			continue
		}
		fmt.Fprintf(&b, "\n  %s (%s): %s", a.Dir, a.Source, a.Reason)
	}
	if plain > 0 {
		fmt.Fprintf(&b, "\n  searched %d directories on PATH without finding it", plain)
	}
	return b.String()
}

// Is lets errors.Is(err, ErrNotFound) match.
func (e *NotFoundError) Is(target error) bool { return target == ErrNotFound }

// Attempt is one place discovery looked.
type Attempt struct {
	Dir    string
	Source Source
	Reason string // why it did not qualify; empty for the place that did
}

// Firmware is the UEFI firmware an aarch64 guest boots from: a read-only code image and a template for the
// per-session variable store.
type Firmware struct {
	Code string
	Vars string
}

// Install describes a QEMU installation that can run the guest architecture.
type Install struct {
	Arch    string // guest architecture: ArchX86_64 or ArchAarch64
	Dir     string // directory holding both programs
	System  string // qemu-system-<arch>
	Img     string // qemu-img
	Source  Source
	Version Version
	Banner  string // first line of `--version`

	Accels   []string // accelerators this build supports (`-accel help`), e.g. tcg, kvm
	Machines []string // `-machine help`
	Devices  []string // `-device help`, aliases included
	// Sandbox reports that `-sandbox SandboxArg` works here (Linux with seccomp). The argv builder only adds the
	// option when it does.
	Sandbox bool
	// Firmware is the aarch64 UEFI firmware, nil when none was found (and always nil for x86_64, which boots from
	// the BIOS built into QEMU).
	Firmware *Firmware

	Attempts []Attempt // where discovery looked, the winner last
}

// HasAccel reports whether this QEMU build supports the accelerator.
func (i Install) HasAccel(name string) bool { return slices.Contains(i.Accels, name) }

// HasDevice reports whether the build provides the device model.
func (i Install) HasDevice(name string) bool { return slices.Contains(i.Devices, name) }

// RequireFirmware returns the UEFI firmware or an error explaining what to install. x86_64 guests need none.
func (i Install) RequireFirmware() (Firmware, error) {
	if i.Arch != ArchAarch64 {
		return Firmware{}, nil
	}
	if i.Firmware == nil {
		return Firmware{}, errors.New("no UEFI firmware for aarch64 guests was found next to QEMU or in the standard " +
			"locations: install QEMU's edk2 firmware (Debian/Ubuntu: qemu-efi-aarch64; Homebrew: it ships with qemu)")
	}
	return *i.Firmware, nil
}

// Options are what the user chose.
type Options struct {
	// Dir is --qemu / vm.qemu_dir: a directory holding the programs, or one of the programs. An explicit choice is
	// never silently replaced by another installation.
	Dir string
	// Arch is the guest architecture; empty means the host's (the guest runs at native speed or not at all).
	Arch string

	// noKnownDirs skips the well-known install directories, so tests do not find the QEMU of the machine they run on.
	noKnownDirs bool
}

// GuestArch maps a Go architecture name to the guest architecture.
func GuestArch(goarch string) (string, error) {
	switch goarch {
	case "amd64", ArchX86_64:
		return ArchX86_64, nil
	case "arm64", ArchAarch64:
		return ArchAarch64, nil
	}
	return "", fmt.Errorf("unsupported architecture %q: guests run on amd64 and arm64 hosts", goarch)
}

// SystemBinary is the file name of the system emulator for the guest architecture on this OS.
func SystemBinary(goos, arch string) string { return exe(goos, "qemu-system-"+arch) }

// ImgBinary is the file name of qemu-img on this OS.
func ImgBinary(goos string) string { return exe(goos, "qemu-img") }

func exe(goos, name string) string {
	if goos == "windows" {
		return name + ".exe"
	}
	return name
}

// Discover locates QEMU, reads its version and asks it what it can do. Search order (PLAN.md section 7.1): the
// explicit Options.Dir, then PATH, then the well-known install directories; qemu-img must sit next to the
// emulator.
//
// When a program was found but is unusable (too old, lacks a device), the Install is returned together with a
// *VersionError or *FeatureError, so `doctor` can still show what it found.
func Discover(ctx context.Context, h Host, opt Options) (Install, error) {
	goos := h.GOOS()
	archName := opt.Arch
	if archName == "" {
		archName = h.GOARCH()
	}
	arch, err := GuestArch(archName)
	if err != nil {
		return Install{}, err
	}
	sysName, imgName := SystemBinary(goos, arch), ImgBinary(goos)

	cands, err := candidates(h, opt, sysName)
	if err != nil {
		return Install{}, err
	}
	var attempts []Attempt
	var inst *Install
	for _, c := range cands {
		if reason := c.check(goos, sysName, imgName); reason != "" {
			attempts = append(attempts, Attempt{Dir: c.dir, Source: c.source, Reason: reason})
			continue
		}
		attempts = append(attempts, Attempt{Dir: c.dir, Source: c.source})
		inst = &Install{Arch: arch, Dir: c.dir, Source: c.source,
			System: filepath.Join(c.dir, sysName), Img: filepath.Join(c.dir, imgName)}
		break
	}
	if inst == nil {
		return Install{}, &NotFoundError{Binary: sysName + " and " + imgName, Attempts: attempts}
	}
	inst.Attempts = attempts

	if err := inst.probe(ctx, h); err != nil {
		return *inst, err
	}
	inst.Firmware = findFirmware(goos, *inst)
	return *inst, nil
}

// probe runs the programs and fills the version and feature fields.
func (i *Install) probe(ctx context.Context, h Host) error {
	run := func(stdin string, args ...string) (RunResult, error) {
		ctx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		return h.Run(ctx, i.System, stdin, args...)
	}

	res, err := run("", "--version")
	if err != nil {
		return fmt.Errorf("running %s --version: %w", i.System, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s --version exited with status %d: %s", i.System, res.ExitCode, truncate(strings.TrimSpace(res.Output()), 200))
	}
	v, banner, err := ParseVersion(res.Stdout)
	i.Banner = banner
	if err != nil {
		return fmt.Errorf("%s is not QEMU: %w", i.System, err)
	}
	i.Version = v
	if v.Major < MinMajor {
		return &VersionError{Have: v}
	}

	for _, p := range []struct {
		arg   string
		parse func(string) []string
		dst   *[]string
	}{
		{"-accel", ParseAccels, &i.Accels},
		{"-machine", ParseMachines, &i.Machines},
		{"-device", ParseDevices, &i.Devices},
	} {
		res, err := run("", p.arg, "help")
		if err != nil {
			return fmt.Errorf("running %s %s help: %w", i.System, p.arg, err)
		}
		*p.dst = p.parse(res.Output())
	}

	var missing []string
	if m := requiredMachine[i.Arch]; !slices.Contains(i.Machines, m) {
		missing = append(missing, "machine "+m)
	}
	for _, d := range requiredDevices[i.Arch] {
		if !i.HasDevice(d) {
			missing = append(missing, "device "+d)
		}
	}
	if len(missing) > 0 {
		return &FeatureError{Missing: missing}
	}

	i.Sandbox = i.probeSandbox(h, run)
	return nil
}

// probeSandbox starts an empty machine under the sandbox the VMs use and quits it. QEMU does not exit when its
// QMP stdin reaches end of file, so the quit command is sent explicitly; the run is also bounded by the timeout.
func (i *Install) probeSandbox(h Host, run func(stdin string, args ...string) (RunResult, error)) bool {
	if h.GOOS() != "linux" {
		return false
	}
	res, err := run(`{"execute":"qmp_capabilities"}`+"\n"+`{"execute":"quit"}`+"\n",
		"-sandbox", SandboxArg, "-machine", "none", "-nodefaults", "-display", "none", "-S", "-qmp", "stdio")
	return err == nil && res.ExitCode == 0
}

type candidate struct {
	dir    string
	source Source
}

// check reports why the directory does not qualify, or "" when it holds both programs.
func (c candidate) check(goos, sysName, imgName string) string {
	for _, name := range []string{sysName, imgName} {
		switch reason := fileProblem(goos, filepath.Join(c.dir, name)); {
		case reason == "not found" && name == imgName:
			return imgName + " is not next to " + sysName
		case reason != "":
			return name + ": " + reason
		}
	}
	return ""
}

// fileProblem says why path is not a runnable program; "" means it is one.
func fileProblem(goos, path string) string {
	fi, err := os.Stat(path) // follows symlinks: Homebrew links its binaries into /opt/homebrew/bin
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "not found"
	case err != nil:
		return err.Error()
	case !fi.Mode().IsRegular():
		return "not a regular file"
	case goos != "windows" && fi.Mode().Perm()&0o111 == 0:
		return "not executable"
	}
	return ""
}

// candidates lists the directories to try, in order.
func candidates(h Host, opt Options, sysName string) ([]candidate, error) {
	if opt.Dir != "" {
		dir := opt.Dir
		fi, err := os.Stat(dir)
		switch {
		case err != nil:
			return nil, &NotFoundError{Binary: sysName, Attempts: []Attempt{{Dir: dir, Source: SourceConfig, Reason: "does not exist"}}}
		case !fi.IsDir():
			dir = filepath.Dir(dir) // a path to one of the programs
		}
		return []candidate{{dir: dir, source: SourceConfig}}, nil
	}

	var out []candidate
	seen := map[string]bool{}
	add := func(dir string, src Source) {
		// A relative entry (".", "bin") would make the result depend on the directory discovery runs in.
		if dir == "" || !filepath.IsAbs(dir) {
			return
		}
		key := filepath.Clean(dir)
		if h.GOOS() == "windows" {
			key = strings.ToLower(key)
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, candidate{dir: filepath.Clean(dir), source: src})
		}
	}
	for _, dir := range filepath.SplitList(h.Getenv("PATH")) {
		add(dir, SourcePath)
	}
	if !opt.noKnownDirs {
		for _, dir := range KnownDirs(h.GOOS(), h.Getenv) {
			add(dir, SourceKnown)
		}
	}
	return out, nil
}

// KnownDirs lists the install locations tried after PATH (PLAN.md section 7.1).
func KnownDirs(goos string, getenv func(string) string) []string {
	switch goos {
	case "windows":
		var dirs []string
		for _, v := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"} {
			if base := getenv(v); base != "" {
				dirs = append(dirs, filepath.Join(base, "qemu"))
			}
		}
		return dirs
	case "darwin":
		return []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin"}
	default:
		return []string{"/usr/local/bin", "/usr/bin"}
	}
}
