// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qemu

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

var update = flag.Bool("update", false, "rewrite the golden command lines in testdata/argv")

// --- fixtures --------------------------------------------------------------------------------------------------

var unixDirs = []string{"/run/user/1000/cs-abc", "/tmp/cs-s1", "/home/u/.local/state/controlsnack/vms", "/home/u/.cache/controlsnack/images"}

func unixInstall(arch string, sandbox bool) Install {
	inst := Install{Arch: arch, System: "/usr/bin/qemu-system-" + arch, Sandbox: sandbox}
	if arch == ArchAarch64 {
		inst.Firmware = &Firmware{Code: "/usr/share/AAVMF/AAVMF_CODE.fd", Vars: "/usr/share/AAVMF/AAVMF_VARS.fd"}
	}
	return inst
}

func unixRuntime(arch, accel string, sandbox bool) RuntimeSpec {
	return RuntimeSpec{
		Install: unixInstall(arch, sandbox), Accel: accel, Name: "abc", CPUs: 2, MemoryMiB: 2048, Width: 1280, Height: 800,
		Disk: "/run/user/1000/cs-abc/disk.qcow2", Vars: "/run/user/1000/cs-abc/vars.fd", Serial: "/run/user/1000/cs-abc/serial.log",
		Agent: Endpoint{Path: "/tmp/cs-s1/agent.sock"}, QMP: Endpoint{Path: "/tmp/cs-s1/qmp.sock"},
		AllowedDirs: unixDirs,
	}
}

func windowsRuntime() RuntimeSpec {
	return RuntimeSpec{
		Install: Install{Arch: ArchX86_64, System: `C:\Program Files\qemu\qemu-system-x86_64.exe`}, Accel: AccelWHPX, Name: "abc",
		CPUs: 2, MemoryMiB: 2048, Width: 1280, Height: 800,
		Disk: `C:\Users\u\AppData\Local\Temp\cs-abc\disk.qcow2`, Serial: `C:\Users\u\AppData\Local\Temp\cs-abc\serial.log`,
		Agent: Endpoint{Port: 45123}, QMP: Endpoint{Port: 45124},
		AllowedDirs: []string{`C:\Users\u\AppData\Local\Temp\cs-abc`, `C:\Users\u\AppData\Local\controlsnack\images`},
	}
}

func unixProvision(arch, accel string) ProvisionSpec {
	return ProvisionSpec{
		Install: unixInstall(arch, true), Accel: accel, Name: "provision", CPUs: 2, MemoryMiB: 2048,
		Disk: "/run/user/1000/cs-abc/work.qcow2", Seed: "/run/user/1000/cs-abc/seed.iso", Vars: "/run/user/1000/cs-abc/vars.fd",
		Serial: "/run/user/1000/cs-abc/serial.log", QMP: Endpoint{Path: "/tmp/cs-s1/qmp.sock"}, AllowedDirs: unixDirs,
	}
}

func windowsProvision() ProvisionSpec {
	w := windowsRuntime()
	return ProvisionSpec{
		Install: w.Install, Accel: AccelWHPX, Name: "provision", CPUs: 2, MemoryMiB: 2048,
		Disk: `C:\Users\u\AppData\Local\Temp\cs-abc\work.qcow2`, Seed: `C:\Users\u\AppData\Local\Temp\cs-abc\seed.iso`,
		Serial: w.Serial, QMP: Endpoint{Port: 45124}, AllowedDirs: w.AllowedDirs,
	}
}

// --- golden command lines (arch x accelerator) ---------------------------------------------------------------

func golden(t *testing.T, name string, argv []string) {
	t.Helper()
	path := filepath.Join("testdata", "argv", name+".golden")
	got := strings.Join(argv, "\n") + "\n"
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil { //nolint:gosec // a test fixture
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test ./internal/vm/qemu -update` to create it)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from its golden file\n--- want\n%s--- got\n%s", name, want, got)
	}
}

type goldenCase struct {
	name string
	spec RuntimeSpec
}

func runtimeCases() []goldenCase {
	kept := unixRuntime(ArchX86_64, AccelKVM, true)
	kept.Cache = "writeback"
	net := unixRuntime(ArchX86_64, AccelKVM, true)
	net.Net = &Endpoint{Path: "/tmp/cs-s1/net.sock"}
	armTCP := unixRuntime(ArchAarch64, AccelHVF, false)
	armTCP.Agent, armTCP.QMP = Endpoint{Port: 45123}, Endpoint{Port: 45124}
	return []goldenCase{
		{"runtime-x86_64-kvm", unixRuntime(ArchX86_64, AccelKVM, true)},
		{"runtime-x86_64-whpx", windowsRuntime()},
		{"runtime-x86_64-hvf", unixRuntime(ArchX86_64, AccelHVF, false)},
		{"runtime-x86_64-tcg", unixRuntime(ArchX86_64, AccelTCG, true)}, // also the argv of the TCG retry (PLAN.md section 7.9)
		{"runtime-aarch64-kvm", unixRuntime(ArchAarch64, AccelKVM, true)},
		{"runtime-aarch64-hvf", armTCP},
		{"runtime-aarch64-tcg", unixRuntime(ArchAarch64, AccelTCG, true)},
		{"runtime-x86_64-kvm-allow-net", net},
		{"runtime-x86_64-kvm-kept-vm", kept},
	}
}

func TestRuntimeGolden(t *testing.T) {
	for _, c := range runtimeCases() {
		t.Run(c.name, func(t *testing.T) {
			argv, err := RuntimeArgv(c.spec)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, c.name, argv)
		})
	}
}

func TestProvisionGolden(t *testing.T) {
	for name, spec := range map[string]ProvisionSpec{
		"provision-x86_64-kvm":  unixProvision(ArchX86_64, AccelKVM),
		"provision-x86_64-whpx": windowsProvision(),
		"provision-x86_64-tcg":  unixProvision(ArchX86_64, AccelTCG),
		"provision-aarch64-hvf": unixProvision(ArchAarch64, AccelHVF),
		"provision-aarch64-tcg": unixProvision(ArchAarch64, AccelTCG),
	} {
		t.Run(name, func(t *testing.T) {
			argv, err := ProvisionArgv(spec)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, name, argv)
		})
	}
}

// --- the shape that does not depend on a golden file ---------------------------------------------------------

func opt(argv []string, name string) (string, bool) {
	i := slices.Index(argv, name)
	if i < 0 || i+1 >= len(argv) {
		return "", false
	}
	return argv[i+1], true
}

func TestAcceleratorLines(t *testing.T) {
	for _, tt := range []struct {
		accel   string
		flags   []string
		cpu     string
		hasCPU  bool
		machine string
	}{
		{AccelKVM, []string{"-enable-kvm"}, "host", true, "q35"},
		{AccelHVF, []string{"-accel", "hvf"}, "host", true, "q35"},
		{AccelTCG, []string{"-accel", "tcg"}, "max", true, "q35"},
	} {
		argv, err := RuntimeArgv(unixRuntime(ArchX86_64, tt.accel, false))
		if err != nil {
			t.Fatal(err)
		}
		if i := slices.Index(argv, tt.flags[0]); i < 0 || !slices.Equal(argv[i:i+len(tt.flags)], tt.flags) {
			t.Errorf("%s: %v", tt.accel, argv)
		}
		if cpu, ok := opt(argv, "-cpu"); ok != tt.hasCPU || cpu != tt.cpu {
			t.Errorf("%s: -cpu %q (%v), want %q", tt.accel, cpu, ok, tt.cpu)
		}
		if m, _ := opt(argv, "-machine"); m != tt.machine {
			t.Errorf("%s: machine %q", tt.accel, m)
		}
	}
	// WHPX keeps QEMU's default CPU model: `max` is reported to fail under it (QEMU issue #1043).
	argv, err := RuntimeArgv(windowsRuntime())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := opt(argv, "-cpu"); ok || slices.Contains(argv, "max") {
		t.Errorf("whpx must not pass -cpu: %v", argv)
	}
	if i := slices.Index(argv, "-accel"); i < 0 || argv[i+1] != "whpx" {
		t.Errorf("%v", argv)
	}
	// aarch64 uses the virt machine and virtio-gpu.
	argv, err = RuntimeArgv(unixRuntime(ArchAarch64, AccelKVM, false))
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := opt(argv, "-machine"); m != "virt" || !slices.Contains(argv, "virtio-gpu-pci,xres=1280,yres=800") || slices.Contains(argv, "virtio-vga,xres=1280,yres=800") {
		t.Errorf("%v", argv)
	}
}

func TestAarch64Firmware(t *testing.T) {
	argv, err := RuntimeArgv(unixRuntime(ArchAarch64, AccelTCG, false))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"-drive if=pflash,format=raw,readonly=on,file=/usr/share/AAVMF/AAVMF_CODE.fd",
		"-drive if=pflash,format=raw,file=/run/user/1000/cs-abc/vars.fd",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, argv)
		}
	}
	// The code image is read-only and the variable store is the session's own copy, never the system template.
	if strings.Contains(joined, "AAVMF_VARS.fd") {
		t.Error("the template of the variable store must not be attached")
	}
	// x86_64 has no pflash units: it boots from the BIOS inside QEMU.
	x86, _ := RuntimeArgv(unixRuntime(ArchX86_64, AccelTCG, false))
	if strings.Contains(strings.Join(x86, " "), "pflash") {
		t.Error("x86_64 uses the built-in BIOS")
	}
}

func TestPortsSandboxAndCache(t *testing.T) {
	argv, _ := RuntimeArgv(unixRuntime(ArchX86_64, AccelKVM, true))
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "virtserialport,chardev=agent0,name="+protocol.PortAgent) || !strings.Contains(joined, "cache=unsafe") {
		t.Errorf("%v", argv)
	}
	if strings.Contains(joined, protocol.PortNet) || strings.Contains(joined, "net0") {
		t.Error("without --allow-net the guest has no way to ask for network access: there is no second port")
	}
	if sb, ok := opt(argv, "-sandbox"); !ok || sb != SandboxArg {
		t.Errorf("-sandbox %q", sb)
	}
	if argv[len(argv)-2] != "-sandbox" {
		t.Error("the sandbox option closes the command line")
	}
	noSB, _ := RuntimeArgv(unixRuntime(ArchX86_64, AccelKVM, false))
	if slices.Contains(noSB, "-sandbox") {
		t.Error("-sandbox only when the probe found it works")
	}
	if !slices.Contains(argv, "-no-reboot") {
		t.Error("a rebooting guest must end QEMU (D-45)")
	}

	kept := unixRuntime(ArchX86_64, AccelKVM, false)
	kept.Cache = "writeback"
	k, _ := RuntimeArgv(kept)
	if !strings.Contains(strings.Join(k, " "), "cache=writeback") || strings.Contains(strings.Join(k, " "), "cache=unsafe") {
		t.Errorf("kept VMs use cache=writeback: %v", k)
	}

	netSpec := unixRuntime(ArchX86_64, AccelKVM, false)
	netSpec.Net = &Endpoint{Port: 45125}
	n, err := RuntimeArgv(netSpec)
	if err != nil {
		t.Fatal(err)
	}
	nj := strings.Join(n, " ")
	if !strings.Contains(nj, "socket,id=net0,host=127.0.0.1,port=45125,server=on,wait=on") || !strings.Contains(nj, "name="+protocol.PortNet) {
		t.Errorf("--allow-net adds the second port: %v", n)
	}
	// The agent port always comes first: QEMU waits for the connections in the order they are declared.
	if strings.Index(nj, "agent0") > strings.Index(nj, "qmp0") || strings.Index(nj, "qmp0") > strings.Index(nj, "net0") {
		t.Errorf("chardev order: %v", n)
	}
}

func TestProvisioningProfile(t *testing.T) {
	argv, err := ProvisionArgv(unixProvision(ArchX86_64, AccelKVM))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"-nic user", "media=cdrom,readonly=on,file=/run/user/1000/cs-abc/seed.iso", "-display none", "-serial file:"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, argv)
		}
	}
	for _, unwanted := range []string{"virtserialport", "virtio-serial-pci", "virtio-vga", "-sandbox", "agent0", "-nic none"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("a provisioning boot has no %q: %v", unwanted, argv)
		}
	}
	// The runtime builder cannot be talked into a network device, whatever the spec says.
	for _, c := range runtimeCases() {
		a, _ := RuntimeArgv(c.spec)
		if !slices.Contains(a, "none") || strings.Contains(strings.Join(a, " "), "-nic user") {
			t.Errorf("%s: %v", c.name, a)
		}
	}
}

// --- the forbidden-flag invariant (PLAN.md section 7.2), independent of the safety package -----------------------

var forbiddenWords = []string{"-virtfs", "-fsdev", "virtiofs", "vhost-user", "-netdev", "hostfwd", "guestfwd", "-vnc", "-spice",
	"usb-host", "vfio", "ivshmem", "-gdb", "-incoming", "-plugin", "share=on", "ssd=off", "kernel-irqchip", "-monitor", "virtio-net"}

func assertInvariant(t *testing.T, name string, argv []string, dirs []string, allowUserNIC bool) {
	t.Helper()
	for i, a := range argv {
		for _, w := range forbiddenWords {
			if strings.Contains(strings.ToLower(a), w) {
				t.Errorf("%s: %q contains forbidden %q", name, a, w)
			}
		}
		next := ""
		if i+1 < len(argv) {
			next = argv[i+1]
		}
		switch a {
		case "-s":
			t.Errorf("%s: -s opens a debugger port", name)
		case "-nic":
			if next != "none" && (!allowUserNIC || next != "user") {
				t.Errorf("%s: -nic %s", name, next)
			}
		case "-display":
			if next != "none" {
				t.Errorf("%s: -display %s", name, next)
			}
		case "-chardev", "-drive", "-serial":
			for _, kv := range strings.Split(strings.TrimPrefix(next, "file:"), ",") {
				k, v, _ := strings.Cut(kv, "=")
				switch {
				case k == "host" && v != "127.0.0.1":
					t.Errorf("%s: chardev host=%s", name, v)
				case k == "path" || k == "file" || (a == "-serial" && !strings.Contains(next, "=")):
					p := v
					if a == "-serial" {
						p = strings.TrimPrefix(next, "file:")
					}
					if !under(p, dirs) {
						t.Errorf("%s: %s %q is outside the session, VM and image directories", name, a, p)
					}
				}
			}
		}
	}
}

func under(p string, dirs []string) bool {
	p = strings.ToLower(strings.ReplaceAll(p, `\`, "/"))
	for _, d := range dirs {
		d = strings.ToLower(strings.ReplaceAll(d, `\`, "/"))
		if strings.HasPrefix(p, strings.TrimSuffix(d, "/")+"/") {
			return true
		}
	}
	return false
}

func TestForbiddenFlagInvariant(t *testing.T) {
	for _, c := range runtimeCases() {
		argv, err := RuntimeArgv(c.spec)
		if err != nil {
			t.Fatal(err)
		}
		dirs := append([]string{"/usr/share/AAVMF"}, c.spec.AllowedDirs...)
		assertInvariant(t, c.name, argv, dirs, false)
	}
	for name, spec := range map[string]ProvisionSpec{
		"provision-x86_64": unixProvision(ArchX86_64, AccelKVM), "provision-aarch64": unixProvision(ArchAarch64, AccelTCG),
	} {
		argv, err := ProvisionArgv(spec)
		if err != nil {
			t.Fatal(err)
		}
		assertInvariant(t, name, argv, append([]string{"/usr/share/AAVMF"}, spec.AllowedDirs...), true)
	}
}

// --- specs that must be refused --------------------------------------------------------------------------------

func TestRuntimeArgvRefusesBadSpecs(t *testing.T) {
	long := "/tmp/" + strings.Repeat("d", 120) + "/agent.sock"
	tests := []struct {
		name string
		mut  func(*RuntimeSpec)
		want string
	}{
		{"unknown accelerator", func(s *RuntimeSpec) { s.Accel = "xen" }, "unknown accelerator"},
		{"auto is not an accelerator", func(s *RuntimeSpec) { s.Accel = "auto" }, "unknown accelerator"},
		{"empty accelerator", func(s *RuntimeSpec) { s.Accel = "" }, "unknown accelerator"},
		{"whpx on aarch64", func(s *RuntimeSpec) { s.Install = unixInstall(ArchAarch64, false); s.Accel = AccelWHPX }, "x86_64 guests only"},
		{"no QEMU", func(s *RuntimeSpec) { s.Install.System = "" }, "no QEMU installation"},
		{"unknown arch", func(s *RuntimeSpec) { s.Install.Arch = "riscv64" }, "unsupported guest architecture"},
		{"bad name", func(s *RuntimeSpec) { s.Name = "Abc;rm" }, "VM name"},
		{"name with a comma", func(s *RuntimeSpec) { s.Name = "a,b" }, "VM name"},
		{"empty name", func(s *RuntimeSpec) { s.Name = "" }, "VM name"},
		{"no CPUs", func(s *RuntimeSpec) { s.CPUs = 0 }, "CPUs"},
		{"too many CPUs", func(s *RuntimeSpec) { s.CPUs = 65 }, "CPUs"},
		{"tiny memory", func(s *RuntimeSpec) { s.MemoryMiB = 64 }, "memory"},
		{"tiny screen", func(s *RuntimeSpec) { s.Width = 100 }, "resolution"},
		{"huge screen", func(s *RuntimeSpec) { s.Height = 9000 }, "resolution"},
		{"unknown cache mode", func(s *RuntimeSpec) { s.Cache = "none" }, "cache mode"},
		{"relative disk", func(s *RuntimeSpec) { s.Disk = "disk.qcow2" }, "not an absolute path"},
		{"comma in disk path", func(s *RuntimeSpec) { s.Disk = "/run/user/1000/cs-abc/a,b.qcow2" }, "comma"},
		{"disk option injection", func(s *RuntimeSpec) { s.Disk = "/run/user/1000/cs-abc/d.qcow2,file=/etc/passwd" }, "comma"},
		{"newline in serial path", func(s *RuntimeSpec) { s.Serial = "/run/user/1000/cs-abc/s\n.log" }, "control character"},
		{"empty serial", func(s *RuntimeSpec) { s.Serial = "" }, "no path"},
		{"disk outside the session", func(s *RuntimeSpec) { s.Disk = "/etc/disk.qcow2" }, "outside the directories"},
		{"disk traversal", func(s *RuntimeSpec) { s.Disk = "/run/user/1000/cs-abc/../../../../etc/shadow" }, "outside the directories"},
		{"no allowed directories", func(s *RuntimeSpec) { s.AllowedDirs = nil }, "no directories"},
		{"agent endpoint both", func(s *RuntimeSpec) { s.Agent = Endpoint{Path: "/tmp/cs-s1/a.sock", Port: 1234} }, "both"},
		{"agent endpoint neither", func(s *RuntimeSpec) { s.Agent = Endpoint{} }, "neither"},
		{"agent port 0 is not a choice", func(s *RuntimeSpec) { s.Agent = Endpoint{Port: 0} }, "neither"},
		{"agent port too big", func(s *RuntimeSpec) { s.Agent = Endpoint{Port: 70000} }, "neither"},
		{"qmp relative socket", func(s *RuntimeSpec) { s.QMP = Endpoint{Path: "qmp.sock"} }, "not an absolute path"},
		{"socket path too long", func(s *RuntimeSpec) { s.Agent = Endpoint{Path: long} }, "shorter directory"},
		{"socket outside the socket dir", func(s *RuntimeSpec) { s.QMP = Endpoint{Path: "/var/tmp/qmp.sock"} }, "outside the directories"},
		{"net endpoint broken", func(s *RuntimeSpec) { s.Net = &Endpoint{} }, "net0"},
		{"aarch64 without a variable store", func(s *RuntimeSpec) { s.Install = unixInstall(ArchAarch64, false); s.Vars = "" }, "variable store"},
		{"aarch64 without firmware", func(s *RuntimeSpec) { s.Install = unixInstall(ArchAarch64, false); s.Install.Firmware = nil }, "qemu-efi-aarch64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := unixRuntime(ArchX86_64, AccelKVM, false)
			tt.mut(&s)
			argv, err := RuntimeArgv(s)
			if err == nil {
				t.Fatalf("accepted: %v", argv)
			}
			if argv != nil {
				t.Errorf("an error comes with no command line, got %v", argv)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
			if !strings.HasPrefix(err.Error(), "runtime command line:") {
				t.Errorf("%v", err)
			}
		})
	}
}

func TestProvisionArgvRefusesBadSpecs(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*ProvisionSpec)
		want string
	}{
		{"no seed", func(s *ProvisionSpec) { s.Seed = "" }, "seed image"},
		{"seed outside the session", func(s *ProvisionSpec) { s.Seed = "/etc/passwd" }, "outside the directories"},
		{"seed with an injected option", func(s *ProvisionSpec) { s.Seed = "/run/user/1000/cs-abc/seed.iso,file=/etc/passwd" }, "comma"},
		{"no qmp endpoint", func(s *ProvisionSpec) { s.QMP = Endpoint{} }, "qmp0"},
		{"bad accelerator", func(s *ProvisionSpec) { s.Accel = "auto" }, "unknown accelerator"},
		{"whpx on aarch64", func(s *ProvisionSpec) { s.Install = unixInstall(ArchAarch64, false); s.Accel = AccelWHPX }, "x86_64 guests only"},
		{"aarch64 without firmware", func(s *ProvisionSpec) { s.Install = unixInstall(ArchAarch64, false); s.Install.Firmware = nil }, "qemu-efi-aarch64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := unixProvision(ArchX86_64, AccelKVM)
			tt.mut(&s)
			if argv, err := ProvisionArgv(s); err == nil || !strings.Contains(err.Error(), tt.want) || argv != nil {
				t.Errorf("%v %v", argv, err)
			}
		})
	}
}

func TestIsAbs(t *testing.T) {
	for p, want := range map[string]bool{"/a": true, "/": true, `C:\a`: true, "c:/a": true, `D:\`: true, "a": false, "": false, "./a": false,
		`C:a`: false, `\\host\share`: false, "1:/a": false} {
		if isAbs(p) != want {
			t.Errorf("isAbs(%q) = %v, want %v", p, !want, want)
		}
	}
}
