// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qemu

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

// --- parsers, fed with what QEMU 8.2.2 really prints ----------------------------------------------------------

const (
	realVersion = "QEMU emulator version 8.2.2 (Debian 1:8.2.2+ds-0ubuntu1.18)\nCopyright (c) 2003-2023 Fabrice Bellard and the QEMU Project developers\n"
	realAccel   = "Accelerators supported in QEMU binary:\ntcg\nkvm\n"
	realMachine = "Supported machines are:\nmicrovm              microvm (i386)\npc-i440fx-zesty      Ubuntu 17.04 PC (i440FX + PIIX, 1996)\nq35                  Standard PC (Q35 + ICH9, 2009) (alias of pc-q35-8.2)\npc-q35-8.2           Standard PC (Q35 + ICH9, 2009) (default)\nnone                 empty machine\n"
	realDevice  = "Storage devices:\nname \"virtio-blk-pci\", bus PCI, alias \"virtio-blk\"\n\nUSB devices:\nname \"qemu-xhci\", bus PCI\nname \"usb-kbd\", bus usb-bus\nname \"usb-tablet\", bus usb-bus\n\nDisplay devices:\nname \"virtio-vga\", bus PCI\nname \"virtio-gpu-pci\", bus PCI, alias \"virtio-gpu\"\n\nInput devices:\nname \"virtio-serial-pci\", bus PCI, alias \"virtio-serial\"\nname \"virtserialport\", bus virtio-serial-bus\nname \"virtio-rng-pci\", bus PCI, alias \"virtio-rng\"\n"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in     string
		want   Version
		banner string
	}{
		{realVersion, Version{8, 2, 2}, "QEMU emulator version 8.2.2 (Debian 1:8.2.2+ds-0ubuntu1.18)"},
		{"QEMU emulator version 9.0.0\n", Version{9, 0, 0}, "QEMU emulator version 9.0.0"},
		{"QEMU emulator version 10.0.0-rc2", Version{10, 0, 0}, "QEMU emulator version 10.0.0-rc2"},
		{"QEMU emulator version 9.2.0 (v9.2.0-11825-gb67e8c7)\r\nCopyright", Version{9, 2, 0}, "QEMU emulator version 9.2.0 (v9.2.0-11825-gb67e8c7)"},
		{"QEMU emulator version 8.0\n", Version{8, 0, 0}, "QEMU emulator version 8.0"},
		{"  QEMU Windows emulator version 8.1.5  \n", Version{8, 1, 5}, "QEMU Windows emulator version 8.1.5"},
	}
	for _, tt := range tests {
		got, banner, err := ParseVersion(tt.in)
		if err != nil || got != tt.want || banner != tt.banner {
			t.Errorf("ParseVersion(%q) = %v, %q, %v; want %v, %q", tt.in, got, banner, err, tt.want, tt.banner)
		}
	}
	for _, bad := range []string{"", "\n", "bash 5.2", "version 8.2.2", "QEMU", "QEMU emulator", "not QEMU emulator version 8.2.2", "QEMU emulator version x.y", "QEMU emulator version 99999999999999999999.0"} {
		if v, _, err := ParseVersion(bad); err == nil {
			t.Errorf("ParseVersion(%q) accepted %v", bad, v)
		}
	}
}

func TestVersionAtLeast(t *testing.T) {
	v := Version{8, 2, 2}
	for _, c := range []struct {
		maj, min int
		want     bool
	}{{8, 2, true}, {8, 0, true}, {7, 9, true}, {8, 3, false}, {9, 0, false}} {
		if v.AtLeast(c.maj, c.min) != c.want {
			t.Errorf("%v.AtLeast(%d, %d) should be %v", v, c.maj, c.min, c.want)
		}
	}
	if v.String() != "8.2.2" {
		t.Error(v.String())
	}
}

func TestParseHelpTexts(t *testing.T) {
	if got := ParseAccels(realAccel); !slices.Equal(got, []string{"kvm", "tcg"}) {
		t.Errorf("accels %v", got)
	}
	if got := ParseAccels("Accelerators supported in QEMU binary:\r\ntcg\r\nwhpx\r\n"); !slices.Equal(got, []string{"tcg", "whpx"}) {
		t.Errorf("CRLF accels %v", got)
	}
	if got := ParseAccels(""); len(got) != 0 {
		t.Errorf("%v", got)
	}
	m := ParseMachines(realMachine)
	for _, want := range []string{"q35", "microvm", "none", "pc-q35-8.2"} {
		if !slices.Contains(m, want) {
			t.Errorf("machines %v lack %q", m, want)
		}
	}
	if slices.Contains(m, "Supported") {
		t.Error("the header line is not a machine")
	}
	d := ParseDevices(realDevice)
	for _, want := range []string{"virtio-blk-pci", "virtio-blk", "qemu-xhci", "usb-tablet", "virtio-vga", "virtio-gpu", "virtserialport", "virtio-rng"} {
		if !slices.Contains(d, want) {
			t.Errorf("devices %v lack %q (aliases count as names)", d, want)
		}
	}
	if slices.Contains(d, "USB devices:") || slices.Contains(d, "PCI") {
		t.Errorf("only quoted names are devices: %v", d)
	}
	if !slices.IsSorted(d) {
		t.Error("devices come back sorted and unique")
	}
}

func TestGuestArchAndBinaryNames(t *testing.T) {
	for in, want := range map[string]string{"amd64": ArchX86_64, "x86_64": ArchX86_64, "arm64": ArchAarch64, "aarch64": ArchAarch64} {
		if got, err := GuestArch(in); err != nil || got != want {
			t.Errorf("%s: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"", "386", "riscv64", "s390x"} {
		if _, err := GuestArch(in); err == nil {
			t.Errorf("%q must be unsupported", in)
		}
	}
	if ArchX86_64 != protocol.ArchX86_64 || ArchAarch64 != protocol.ArchAarch64 {
		t.Error("the qemu and protocol packages must spell architectures alike")
	}
	if SystemBinary("windows", ArchX86_64) != "qemu-system-x86_64.exe" || SystemBinary("linux", ArchAarch64) != "qemu-system-aarch64" ||
		ImgBinary("windows") != "qemu-img.exe" || ImgBinary("darwin") != "qemu-img" {
		t.Error("binary names")
	}
}

func TestKnownDirs(t *testing.T) {
	env := map[string]string{"ProgramFiles": `C:\Program Files`, "ProgramW6432": `C:\Program Files`, "ProgramFiles(x86)": `C:\Program Files (x86)`}
	get := func(k string) string { return env[k] }
	win := KnownDirs("windows", get)
	if len(win) != 3 || !strings.HasSuffix(win[0], "qemu") || !strings.Contains(win[2], "(x86)") {
		t.Errorf("windows %v", win)
	}
	if got := KnownDirs("windows", func(string) string { return "" }); len(got) != 0 {
		t.Errorf("no Program Files variables, no directories: %v", got)
	}
	if got := KnownDirs("darwin", get); !slices.Equal(got, []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin"}) {
		t.Errorf("darwin %v", got)
	}
	if got := KnownDirs("linux", get); !slices.Contains(got, "/usr/bin") {
		t.Errorf("linux %v", got)
	}
}

// --- Discover, with real directories and a fake QEMU -----------------------------------------------------------

type fakeQEMU struct {
	version     string // output of --version; defaults to realVersion
	versionExit int
	accel       string
	machine     string
	device      string
	sandboxExit int
	runErr      error
}

func okQEMU() fakeQEMU {
	return fakeQEMU{version: realVersion, accel: realAccel, machine: realMachine, device: realDevice}
}

type fakeHost struct {
	goos, goarch string
	env          map[string]string
	q            fakeQEMU

	mu    sync.Mutex
	calls [][]string
}

func newHost(t *testing.T, path ...string) *fakeHost {
	t.Helper()
	return &fakeHost{goos: runtime.GOOS, goarch: "amd64", q: okQEMU(),
		env: map[string]string{"PATH": strings.Join(path, string(os.PathListSeparator))}}
}

func (h *fakeHost) GOOS() string             { return h.goos }
func (h *fakeHost) GOARCH() string           { return h.goarch }
func (h *fakeHost) Getenv(key string) string { return h.env[key] }
func (h *fakeHost) callCount() int           { h.mu.Lock(); defer h.mu.Unlock(); return len(h.calls) }
func (h *fakeHost) Run(ctx context.Context, path, stdin string, args ...string) (RunResult, error) {
	h.mu.Lock()
	h.calls = append(h.calls, append([]string{path}, args...))
	h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}
	if h.q.runErr != nil {
		return RunResult{}, h.q.runErr
	}
	switch {
	case len(args) == 1 && args[0] == "--version":
		return RunResult{Stdout: h.q.version, ExitCode: h.q.versionExit}, nil
	case slices.Equal(args, []string{"-accel", "help"}):
		return RunResult{Stdout: h.q.accel}, nil
	case slices.Equal(args, []string{"-machine", "help"}):
		return RunResult{Stdout: h.q.machine}, nil
	case slices.Equal(args, []string{"-device", "help"}):
		return RunResult{Stdout: h.q.device}, nil
	case slices.Contains(args, "-sandbox"):
		if !strings.Contains(stdin, `"quit"`) {
			return RunResult{}, errors.New("a QEMU that is not told to quit never exits")
		}
		return RunResult{ExitCode: h.q.sandboxExit}, nil
	}
	return RunResult{}, errors.New("unexpected probe: " + strings.Join(args, " "))
}

// install creates fake programs for arch in dir and returns their directory.
func install(t *testing.T, dir, arch string, withImg bool) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	names := []string{SystemBinary(runtime.GOOS, arch)}
	if withImg {
		names = append(names, ImgBinary(runtime.GOOS))
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // an executable stand-in
			t.Fatal(err)
		}
	}
	return dir
}

func TestDiscoverPathOrder(t *testing.T) {
	root := t.TempDir()
	noImg := install(t, filepath.Join(root, "a"), ArchX86_64, false)
	good := install(t, filepath.Join(root, "b"), ArchX86_64, true)
	other := install(t, filepath.Join(root, "c"), ArchX86_64, true)
	h := newHost(t, noImg, good, other, good) // the duplicate must be tried once
	inst, err := Discover(context.Background(), h, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Dir != good || inst.Source != SourcePath || inst.Arch != ArchX86_64 {
		t.Errorf("%+v", inst)
	}
	if inst.System != filepath.Join(good, SystemBinary(runtime.GOOS, ArchX86_64)) || inst.Img != filepath.Join(good, ImgBinary(runtime.GOOS)) {
		t.Errorf("paths %q %q", inst.System, inst.Img)
	}
	if inst.Version != (Version{8, 2, 2}) || !strings.HasPrefix(inst.Banner, "QEMU emulator version 8.2.2") {
		t.Errorf("%v %q", inst.Version, inst.Banner)
	}
	if !slices.Equal(inst.Accels, []string{"kvm", "tcg"}) || !inst.HasAccel("kvm") || inst.HasAccel("whpx") || !inst.HasDevice("virtio-vga") {
		t.Errorf("features %v", inst.Accels)
	}
	if len(inst.Attempts) != 2 || inst.Attempts[0].Reason == "" || !strings.Contains(inst.Attempts[0].Reason, "not next to") || inst.Attempts[1].Reason != "" {
		t.Errorf("attempts %+v", inst.Attempts)
	}
	if inst.Firmware != nil {
		t.Error("x86_64 guests use QEMU's built-in BIOS")
	}
	if fw, err := inst.RequireFirmware(); err != nil || fw != (Firmware{}) {
		t.Errorf("x86_64 needs no firmware: %+v %v", fw, err)
	}
}

func TestDiscoverIgnoresRelativePathEntries(t *testing.T) {
	root := t.TempDir()
	install(t, filepath.Join(root, "bin"), ArchX86_64, true)
	t.Chdir(filepath.Join(root, "bin")) // QEMU sits in the working directory
	h := newHost(t, ".", "bin", "")
	_, err := Discover(context.Background(), h, Options{noKnownDirs: true})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("a relative PATH entry must not be searched: %v", err)
	}
	if h.callCount() != 0 {
		t.Error("nothing found, nothing run")
	}
}

func TestDiscoverExplicitDir(t *testing.T) {
	root := t.TempDir()
	mine := install(t, filepath.Join(root, "mine"), ArchX86_64, true)
	onPath := install(t, filepath.Join(root, "path"), ArchX86_64, true)
	h := newHost(t, onPath)

	inst, err := Discover(context.Background(), h, Options{Dir: mine})
	if err != nil || inst.Dir != mine || inst.Source != SourceConfig {
		t.Fatalf("%+v %v", inst, err)
	}
	// A path to the emulator itself names its directory.
	inst, err = Discover(context.Background(), h, Options{Dir: filepath.Join(mine, SystemBinary(runtime.GOOS, ArchX86_64))})
	if err != nil || inst.Dir != mine {
		t.Fatalf("%+v %v", inst, err)
	}

	// An explicit choice that is wrong is an error, not a quiet switch to the installation on PATH.
	for name, opt := range map[string]Options{
		"missing dir":    {Dir: filepath.Join(root, "nope")},
		"no qemu in dir": {Dir: root},
		"no qemu-img":    {Dir: install(t, filepath.Join(root, "noimg"), ArchX86_64, false)},
	} {
		h := newHost(t, onPath)
		_, err := Discover(context.Background(), h, opt)
		var nf *NotFoundError
		if !errors.Is(err, ErrNotFound) || !errors.As(err, &nf) || len(nf.Attempts) != 1 || nf.Attempts[0].Source != SourceConfig {
			t.Errorf("%s: %v", name, err)
		}
		if h.callCount() != 0 {
			t.Errorf("%s: probed %v", name, h.calls)
		}
	}
}

func TestNotFoundErrorExplainsEverything(t *testing.T) {
	root := t.TempDir()
	empty := filepath.Join(root, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Discover(context.Background(), newHost(t, empty), Options{noKnownDirs: true})
	if !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	for _, want := range []string{"qemu-system-x86_64", "qemu-img", empty, "path", "not found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q:\n%v", want, err)
		}
	}
}

func TestDiscoverRejectsNonExecutableAndDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executability is decided by the extension on Windows")
	}
	root := t.TempDir()
	dir := install(t, filepath.Join(root, "x"), ArchX86_64, true)
	if err := os.Chmod(filepath.Join(dir, SystemBinary(runtime.GOOS, ArchX86_64)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Discover(context.Background(), newHost(t, dir), Options{noKnownDirs: true})
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "not executable") {
		t.Errorf("%v", err)
	}
	// A directory with the program's name is not the program.
	dir2 := filepath.Join(root, "y")
	if err := os.MkdirAll(filepath.Join(dir2, SystemBinary(runtime.GOOS, ArchX86_64)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, ImgBinary(runtime.GOOS)), []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // an executable stand-in
		t.Fatal(err)
	}
	_, err = Discover(context.Background(), newHost(t, dir2), Options{noKnownDirs: true})
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("%v", err)
	}
}

func TestDiscoverVersionAndFeatureFailures(t *testing.T) {
	dir := install(t, filepath.Join(t.TempDir(), "q"), ArchX86_64, true)
	tooOld := newHost(t, dir)
	tooOld.q.version = "QEMU emulator version 7.2.9 (Debian)\n"
	inst, err := Discover(context.Background(), tooOld, Options{})
	var ve *VersionError
	if !errors.As(err, &ve) || ve.Have != (Version{7, 2, 9}) || !strings.Contains(err.Error(), "8.0 or newer") {
		t.Fatalf("%v", err)
	}
	if inst.Dir != dir || inst.Version.Major != 7 {
		t.Errorf("the installation found is still reported: %+v", inst)
	}
	if tooOld.callCount() != 1 {
		t.Errorf("an old QEMU is not asked what it can do: %v", tooOld.calls)
	}

	lacking := newHost(t, dir)
	lacking.q.device = strings.ReplaceAll(strings.ReplaceAll(realDevice, `name "virtio-vga", bus PCI`, ""), `name "usb-tablet", bus usb-bus`, "")
	lacking.q.machine = "Supported machines are:\nmicrovm  microvm\n"
	_, err = Discover(context.Background(), lacking, Options{})
	var fe *FeatureError
	if !errors.As(err, &fe) || !slices.Equal(fe.Missing, []string{"machine q35", "device virtio-vga", "device usb-tablet"}) {
		t.Fatalf("%v", err)
	}

	for name, mut := range map[string]func(*fakeQEMU){
		"not qemu":          func(q *fakeQEMU) { q.version = "GNU bash, version 5.2\n" },
		"version fails":     func(q *fakeQEMU) { q.versionExit = 1 },
		"program won't run": func(q *fakeQEMU) { q.runErr = errors.New("exec format error") },
	} {
		h := newHost(t, dir)
		mut(&h.q)
		if _, err := Discover(context.Background(), h, Options{}); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A canceled context ends discovery with the context's error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Discover(ctx, newHost(t, dir), Options{}); !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
}

func TestDiscoverUnsupportedArch(t *testing.T) {
	h := newHost(t)
	h.goarch = "riscv64"
	if _, err := Discover(context.Background(), h, Options{}); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("%v", err)
	}
	if _, err := Discover(context.Background(), newHost(t), Options{Arch: "mips"}); err == nil {
		t.Error("an unknown guest architecture is an error")
	}
}

func TestDiscoverSandboxProbe(t *testing.T) {
	i := &Install{System: "/x/qemu"}
	run := func(h *fakeHost) func(string, ...string) (RunResult, error) {
		return func(stdin string, args ...string) (RunResult, error) {
			return h.Run(context.Background(), i.System, stdin, args...)
		}
	}
	linux := &fakeHost{goos: "linux", q: okQEMU()}
	if !i.probeSandbox(linux, run(linux)) {
		t.Error("a QEMU that exits 0 under the sandbox supports it")
	}
	if got := linux.calls[0]; !slices.Contains(got, SandboxArg) || !slices.Contains(got, "-S") || !slices.Contains(got, "-nodefaults") {
		t.Errorf("sandbox probe argv %v", got)
	}
	linux.q.sandboxExit = 1
	if i.probeSandbox(linux, run(linux)) {
		t.Error("a QEMU that refuses the sandbox does not support it")
	}
	linux.q.runErr = errors.New("boom")
	if i.probeSandbox(linux, run(linux)) {
		t.Error("an error is not support")
	}
	for _, goos := range []string{"windows", "darwin"} {
		h := &fakeHost{goos: goos, q: okQEMU()}
		if i.probeSandbox(h, run(h)) || h.callCount() != 0 {
			t.Errorf("%s: -sandbox is Linux only and must not even be tried", goos)
		}
	}
}

// --- firmware --------------------------------------------------------------------------------------------------

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fd"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverAarch64Firmware(t *testing.T) {
	root := t.TempDir()
	bin := install(t, filepath.Join(root, "prefix", "bin"), ArchAarch64, true)
	share := filepath.Join(root, "prefix", "share", "qemu")

	h := newHost(t, bin)
	h.goarch = "arm64"
	h.q.machine = "Supported machines are:\nvirt  QEMU 8.2 ARM Virtual Machine\n"
	inst, err := Discover(context.Background(), h, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Arch != ArchAarch64 || !strings.HasSuffix(inst.System, SystemBinary(runtime.GOOS, ArchAarch64)) {
		t.Errorf("%+v", inst)
	}
	if inst.Firmware != nil {
		t.Fatalf("no firmware on disk yet: %+v", inst.Firmware)
	}
	if _, err := inst.RequireFirmware(); err == nil || !strings.Contains(err.Error(), "qemu-efi-aarch64") {
		t.Errorf("the error must say what to install: %v", err)
	}

	touch(t, filepath.Join(share, "edk2-aarch64-code.fd"))
	touch(t, filepath.Join(share, "edk2-arm-vars.fd"))
	inst, err = Discover(context.Background(), h, Options{})
	if err != nil || inst.Firmware == nil {
		t.Fatalf("%+v %v", inst.Firmware, err)
	}
	fw, err := inst.RequireFirmware()
	if err != nil || fw.Code != filepath.Join(share, "edk2-aarch64-code.fd") || fw.Vars != filepath.Join(share, "edk2-arm-vars.fd") {
		t.Errorf("%+v %v", fw, err)
	}
}

func TestFirmwareNeedsBothFiles(t *testing.T) {
	root := t.TempDir()
	bin := install(t, filepath.Join(root, "p", "bin"), ArchAarch64, true)
	touch(t, filepath.Join(root, "p", "share", "qemu", "edk2-aarch64-code.fd")) // vars missing
	inst := Install{Arch: ArchAarch64, Dir: bin, System: filepath.Join(bin, SystemBinary(runtime.GOOS, ArchAarch64))}
	if fw := findFirmware("linux", inst); fw != nil && strings.HasPrefix(fw.Code, root) {
		t.Errorf("code without a vars template is unusable: %+v", fw)
	}
	// A different naming scheme next to the install is recognized as well.
	touch(t, filepath.Join(root, "p", "share", "AAVMF_CODE.fd"))
	touch(t, filepath.Join(root, "p", "share", "AAVMF_VARS.fd"))
	fw := findFirmware("linux", inst)
	if fw == nil || filepath.Base(fw.Code) != "AAVMF_CODE.fd" || filepath.Base(fw.Vars) != "AAVMF_VARS.fd" {
		t.Errorf("%+v", fw)
	}
	if findFirmware("linux", Install{Arch: ArchX86_64, Dir: bin}) != nil {
		t.Error("x86_64 has no firmware files")
	}
}

func TestFirmwareFollowsSymlinkedBinaries(t *testing.T) {
	root := t.TempDir()
	cellar := install(t, filepath.Join(root, "Cellar", "qemu", "9.0", "bin"), ArchAarch64, true)
	touch(t, filepath.Join(root, "Cellar", "qemu", "9.0", "share", "qemu", "edk2-aarch64-code.fd"))
	touch(t, filepath.Join(root, "Cellar", "qemu", "9.0", "share", "qemu", "edk2-arm-vars.fd"))
	link := filepath.Join(root, "homebrew", "bin")
	if err := os.MkdirAll(link, 0o755); err != nil {
		t.Fatal(err)
	}
	name := SystemBinary(runtime.GOOS, ArchAarch64)
	if err := os.Symlink(filepath.Join(cellar, name), filepath.Join(link, name)); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	inst := Install{Arch: ArchAarch64, Dir: link, System: filepath.Join(link, name)}
	if fw := findFirmware("darwin", inst); fw == nil || !strings.Contains(fw.Code, "Cellar") {
		t.Errorf("%+v", fw)
	}
}

// --- the real Host ----------------------------------------------------------------------------------------------

// TestDiscoverRealQEMU runs discovery against whatever QEMU this machine has, and is skipped where there is none.
// It is the check that the parsers match what QEMU actually prints.
func TestDiscoverRealQEMU(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	inst, err := Discover(ctx, OSHost{}, Options{})
	if errors.Is(err, ErrNotFound) {
		t.Skip("no QEMU installed here")
	}
	if err != nil {
		t.Fatalf("a QEMU is installed but unusable: %v\n%+v", err, inst)
	}
	t.Logf("%s at %s (%s): accels %v, sandbox %v, %d machines, %d devices", inst.Banner, inst.Dir, inst.Source,
		inst.Accels, inst.Sandbox, len(inst.Machines), len(inst.Devices))
	if !inst.HasAccel("tcg") {
		t.Errorf("every QEMU has tcg: %v", inst.Accels)
	}
	if inst.Version.Major < MinMajor {
		t.Errorf("%v", inst.Version)
	}
	if runtime.GOOS != "linux" && inst.Sandbox {
		t.Error("-sandbox is only probed on Linux")
	}
}

// TestHelperProcess is not a test: OSHost.Run starts the test binary as a stand-in for QEMU, and this is what the
// stand-in does. It does nothing in a normal test run.
func TestHelperProcess(t *testing.T) {
	i := slices.Index(os.Args, "--")
	if i < 0 {
		return
	}
	switch os.Args[i+1] {
	case "echo":
		buf := make([]byte, 64)
		n, _ := os.Stdin.Read(buf)
		os.Stdout.Write(buf[:n])
		os.Stderr.WriteString("to-stderr")
		os.Exit(3)
	case "flood":
		chunk := []byte(strings.Repeat("x", 64<<10))
		for range 32 { // 2 MiB
			os.Stdout.Write(chunk)
		}
		os.Exit(0)
	case "env":
		os.Stdout.WriteString(strings.Join(os.Environ(), "\n"))
		os.Exit(0)
	case "hang":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(9)
}

func TestOSHostRun(t *testing.T) {
	var h OSHost
	if h.GOOS() != runtime.GOOS || h.GOARCH() != runtime.GOARCH {
		t.Error("OSHost reports the running platform")
	}
	t.Setenv("CS_PROBE_SECRET", "must-not-leak")
	self := os.Args[0]
	helper := func(mode string) []string { return []string{"-test.run=^TestHelperProcess$", "--", mode} }

	res, err := h.Run(context.Background(), self, "hello", helper("echo")...)
	if err != nil || res.Stdout != "hello" || res.Stderr != "to-stderr" || res.ExitCode != 3 || res.Output() != "helloto-stderr" {
		t.Errorf("%+v %v", res, err)
	}

	res, err = h.Run(context.Background(), self, "", helper("flood")...)
	if err != nil || len(res.Stdout) != maxToolOutput {
		t.Errorf("output is capped at %d bytes, got %d (%v)", maxToolOutput, len(res.Stdout), err)
	}

	res, err = h.Run(context.Background(), self, "", helper("env")...)
	if err != nil || strings.Contains(res.Stdout, "CS_PROBE_SECRET") || !strings.Contains(res.Stdout, "LC_ALL=C") {
		t.Errorf("probes run with a minimal environment: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := h.Run(ctx, self, "", helper("hang")...); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a hung program ends with the context's error: %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Error("the hung program was not killed")
	}

	if _, err := h.Run(context.Background(), filepath.Join(t.TempDir(), "missing"), ""); err == nil {
		t.Error("a program that cannot start is an error")
	}
}
