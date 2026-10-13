// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package safety

import (
	"strings"
	"testing"
)

var testDirs = []string{"/run/user/1000/cs-abc", "/tmp/cs-sock-1", "/home/u/.local/state/controlsnack/vms", "/home/u/.cache/controlsnack/images", "/usr/share/qemu"}

func policy(p QEMUProfile) ArgvPolicy { return ArgvPolicy{Profile: p, AllowedDirs: testDirs} }

// runtimeArgv is the x86_64 KVM command line of PLAN.md section 7.2 with concrete paths.
func runtimeArgv() []string {
	return []string{"qemu-system-x86_64", "-name", "controlsnack-abc", "-nodefaults", "-no-user-config",
		"-machine", "q35", "-enable-kvm", "-cpu", "host", "-smp", "2", "-m", "2048",
		"-drive", "if=virtio,format=qcow2,file=/run/user/1000/cs-abc/disk.qcow2,cache=unsafe,discard=unmap",
		"-device", "virtio-vga,xres=1280,yres=800",
		"-device", "qemu-xhci", "-device", "usb-tablet", "-device", "usb-kbd",
		"-device", "virtio-rng-pci", "-device", "virtio-serial-pci",
		"-chardev", "socket,id=agent0,path=/tmp/cs-sock-1/agent.sock,server=on,wait=on",
		"-device", "virtserialport,chardev=agent0,name=org.controlsnack.agent.0",
		"-chardev", "socket,id=qmp0,path=/tmp/cs-sock-1/qmp.sock,server=on,wait=on", "-mon", "chardev=qmp0,mode=control",
		"-serial", "file:/run/user/1000/cs-abc/serial.log", "-display", "none", "-nic", "none",
		"-sandbox", "on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny"}
}

func with(base []string, extra ...string) []string {
	return append(append([]string{}, base...), extra...)
}

func TestKnownGoodCommandLinesPass(t *testing.T) {
	arm := []string{"qemu-system-aarch64", "-name", "controlsnack-abc", "-nodefaults", "-no-user-config",
		"-machine", "virt", "-accel", "hvf", "-cpu", "host", "-smp", "2", "-m", "2048",
		"-drive", "if=pflash,format=raw,readonly=on,file=/usr/share/qemu/edk2-aarch64-code.fd",
		"-drive", "if=pflash,format=raw,file=/run/user/1000/cs-abc/vars.fd",
		"-drive", "if=virtio,format=qcow2,file=/run/user/1000/cs-abc/disk.qcow2",
		"-device", "virtio-gpu-pci,xres=1280,yres=800", "-device", "qemu-xhci", "-device", "usb-tablet", "-device", "usb-kbd",
		"-device", "virtio-serial-pci",
		"-chardev", "socket,id=agent0,host=127.0.0.1,port=45123,server=on,wait=on",
		"-device", "virtserialport,chardev=agent0,name=org.controlsnack.agent.0",
		"-display", "none", "-nic", "none"}
	provisioning := []string{"qemu-system-x86_64", "-nodefaults", "-machine", "q35", "-accel", "tcg", "-cpu", "max", "-m", "2048",
		"-drive", "if=virtio,format=qcow2,file=/run/user/1000/cs-abc/work.qcow2",
		"-drive", "file=/run/user/1000/cs-abc/seed.iso,media=cdrom,readonly=on",
		"-display", "none", "-nic", "user", "-serial", "stdio"}

	cases := []struct {
		name string
		argv []string
		prof QEMUProfile
	}{
		{"x86_64 kvm", runtimeArgv(), QEMURuntime},
		{"x86_64 whpx", replace(runtimeArgv(), "-enable-kvm", "-accel", "whpx"), QEMURuntime},
		{"x86_64 tcg retry", append(removeOpt(runtimeArgv(), "-enable-kvm", false), "-accel", "tcg"), QEMURuntime},
		{"aarch64 hvf with tcp chardev and pflash", arm, QEMURuntime},
		{"provisioning", provisioning, QEMUProvisioning},
	}
	for _, c := range cases {
		if err := AssertQEMUArgv(c.argv, policy(c.prof)); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func replace(argv []string, old string, repl ...string) []string {
	var out []string
	for _, a := range argv {
		if a == old {
			out = append(out, repl...)
		} else {
			out = append(out, a)
		}
	}
	return out
}

func removeOpt(argv []string, opt string, hasValue bool) []string {
	var out []string
	for i := 0; i < len(argv); i++ {
		if argv[i] == opt {
			if hasValue {
				i++
			}
			continue
		}
		out = append(out, argv[i])
	}
	return out
}

func TestForbiddenFlags(t *testing.T) {
	// PLAN.md section 7.2, "Forbidden-flag invariant", plus the NIC and sharing variants that matter.
	tests := []struct {
		name  string
		extra []string
		want  string
	}{
		{"virtfs", []string{"-virtfs", "local,path=/,mount_tag=host,security_model=none"}, "-virtfs"},
		{"fsdev", []string{"-fsdev", "local,id=f,path=/,security_model=none"}, "-fsdev"},
		{"virtiofs device", []string{"-device", "vhost-user-fs-pci,chardev=c,tag=t"}, "vhost-user"},
		{"vhost-user substring in any value", []string{"-chardev", "socket,id=v,path=/tmp/cs-sock-1/vhost-user.sock"}, "vhost-user"},
		{"virtiofsd", []string{"-device", "virtiofs,x=1"}, "virtiofs"},
		{"netdev", []string{"-netdev", "user,id=n0"}, "-netdev"},
		{"legacy -net", []string{"-net", "nic"}, "-net"},
		{"extra -nic user", []string{"-nic", "user"}, "-nic"},
		{"extra -nic tap", []string{"-nic", "tap,ifname=tap0"}, "-nic"},
		{"nic device virtio-net", []string{"-device", "virtio-net-pci,netdev=n0"}, "virtio-net"},
		{"nic device e1000", []string{"-device", "e1000,netdev=n0"}, "e1000"},
		{"nic device rtl8139", []string{"-device", "rtl8139"}, "rtl8139"},
		{"nic device vmxnet3", []string{"-device", "vmxnet3"}, "vmxnet3"},
		{"usb nic", []string{"-device", "usb-net,netdev=n"}, "usb-net"},
		{"hostfwd", []string{"-machine", "q35,hostfwd=tcp::2222-:22"}, "hostfwd"},
		{"guestfwd", []string{"-machine", "q35,guestfwd=tcp:10.0.2.1:80-cmd:x"}, "guestfwd"},
		{"vnc", []string{"-vnc", ":1"}, "-vnc"},
		{"spice", []string{"-spice", "port=5930,disable-ticketing=on"}, "-spice"},
		{"display gtk", []string{"-display", "gtk"}, "-display"},
		{"display sdl", []string{"-display", "sdl"}, "-display"},
		{"usb-host", []string{"-device", "usb-host,vendorid=0x1234"}, "usb-host"},
		{"vfio", []string{"-device", "vfio-pci,host=00:02.0"}, "vfio"},
		{"ivshmem", []string{"-device", "ivshmem-plain,memdev=hostmem"}, "ivshmem"},
		{"gdb", []string{"-gdb", "tcp::1234"}, "-gdb"},
		{"gdb shorthand", []string{"-s"}, "-s"},
		{"incoming", []string{"-incoming", "tcp:0:4444"}, "-incoming"},
		{"plugin", []string{"-plugin", "libx.so"}, "-plugin"},
		{"share=on", []string{"-machine", "q35,memory-backend=m,share=on"}, "share=on"},
		{"ssd=off", []string{"-accel", "whpx,ssd=off"}, "ssd=off"},
		{"kernel-irqchip", []string{"-accel", "whpx,kernel-irqchip=off"}, "kernel-irqchip"},
		{"human monitor", []string{"-monitor", "stdio"}, "-monitor"},
		{"monitor in readline mode", []string{"-mon", "chardev=qmp0,mode=readline"}, "mode=control"},
		{"memory backend file", []string{"-object", "memory-backend-file,id=m,size=1G,mem-path=/dev/shm/x,share=on"}, "-object"},
		{"usbdevice", []string{"-usbdevice", "host:1:2"}, "-usbdevice"},
		{"parallel", []string{"-parallel", "stdio"}, "-parallel"},
		{"direct kernel", []string{"-kernel", "/boot/vmlinuz"}, "-kernel"},
		{"9p device", []string{"-device", "virtio-9p-pci,fsdev=f"}, "allowlist"},
		{"unknown option", []string{"-frobnicate", "yes"}, "allowlist"},
		{"unknown device", []string{"-device", "some-new-nic-model"}, "allowlist"},
		{"stray positional", []string{"/etc/passwd"}, "unexpected argument"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AssertQEMUArgv(with(runtimeArgv(), tt.extra...), policy(QEMURuntime))
			if err == nil {
				t.Fatalf("%v must be refused", tt.extra)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error should mention %q: %v", tt.want, err)
			}
		})
	}
}

func TestChardevRules(t *testing.T) {
	tests := []struct {
		name string
		dev  string
		want string // empty = accepted
	}{
		{"unix socket in an allowed dir", "socket,id=a,path=/tmp/cs-sock-1/a.sock,server=on,wait=on", ""},
		{"loopback tcp", "socket,id=a,host=127.0.0.1,port=4000,server=on,wait=on", ""},
		{"escaped comma in a path", "socket,id=a,path=/tmp/cs-sock-1/a,,b.sock", ""},
		{"wildcard host", "socket,id=a,host=0.0.0.0,port=4000,server=on", "chardevs bind 127.0.0.1 only"},
		{"lan host", "socket,id=a,host=192.168.1.5,port=4000,server=on", "chardevs bind 127.0.0.1 only"},
		{"port without host listens everywhere", "socket,id=a,port=4000,server=on", "every interface"},
		{"unix socket outside the dirs", "socket,id=a,path=/var/run/docker.sock", "outside the directories"},
		{"relative socket path", "socket,id=a,path=agent.sock", "outside the directories"},
		{"path traversal", "socket,id=a,path=/tmp/cs-sock-1/../../etc/x", "outside the directories"},
		{"abstract socket", "socket,id=a,path=/tmp/cs-sock-1/a,abstract=on", "abstract"},
		{"fd passing", "socket,id=a,fd=5", "fd=5"},
		{"socket without an address", "socket,id=a,server=on", "needs a local path"},
		{"pty", "pty,id=a", "not allowed"},
		{"udp", "udp,id=a,host=127.0.0.1,port=1", "not allowed"},
		{"stdio at runtime", "stdio,id=a", "provisioning"},
		{"file in an allowed dir", "file,id=a,path=/run/user/1000/cs-abc/x.log", ""},
		{"file outside", "file,id=a,path=/etc/cron.d/x", "outside the directories"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AssertQEMUArgv(with(runtimeArgv(), "-chardev", tt.dev), policy(QEMURuntime))
			if tt.want == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want an error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestDrivePathsMustStayInsideTheAllowedDirs(t *testing.T) {
	tests := []struct {
		file string
		ok   bool
	}{
		{"/run/user/1000/cs-abc/disk2.qcow2", true},
		{"/home/u/.local/state/controlsnack/vms/lab/disk.qcow2", true},
		{"/home/u/.cache/controlsnack/images/base-1.qcow2", true},
		{"/etc/passwd", false},
		{"/dev/sda", false},
		{"/run/user/1000/cs-abc/../../../etc/shadow", false},
		{"/run/user/1000/cs-abc", false},    // the directory itself is not a file inside it
		{"/run/user/1000/cs-abcd/x", false}, // a sibling that merely shares the prefix
		{"relative.qcow2", false},
		{"nbd:127.0.0.1:10809", false},
		{"http://example.com/x.qcow2", false},
		{`C:\Users\u\x.qcow2`, false},
	}
	for _, tt := range tests {
		err := AssertQEMUArgv(with(runtimeArgv(), "-drive", "if=none,file="+tt.file), policy(QEMURuntime))
		if (err == nil) != tt.ok {
			t.Errorf("file=%q accepted=%v, want %v (%v)", tt.file, err == nil, tt.ok, err)
		}
	}
	if err := AssertQEMUArgv(with(runtimeArgv(), "-drive", "if=none,file.driver=nbd,file.host=evil"), policy(QEMURuntime)); err == nil {
		t.Error("nested block driver options could reach the network and must be refused")
	}
}

func TestWindowsPathsAreComparedPortably(t *testing.T) {
	pol := ArgvPolicy{AllowedDirs: []string{`C:\Users\pc\AppData\Local\Temp\cs-abc`}}
	argv := []string{"qemu-system-x86_64.exe", "-nodefaults", "-display", "none", "-nic", "none",
		"-drive", `if=virtio,format=qcow2,file=C:\Users\pc\AppData\Local\Temp\cs-abc\disk.qcow2`,
		"-chardev", "socket,id=a,host=127.0.0.1,port=5000,server=on,wait=on"}
	if err := AssertQEMUArgv(argv, pol); err != nil {
		t.Fatalf("a Windows path inside the allowed directory should pass: %v", err)
	}
	argv[7] = `if=virtio,file=c:/users/pc/appdata/local/temp/cs-abc/disk.qcow2`
	if err := AssertQEMUArgv(argv, pol); err != nil {
		t.Errorf("drive letters and slashes must not matter: %v", err)
	}
	argv[7] = `if=virtio,file=C:\Windows\System32\config\SAM`
	if err := AssertQEMUArgv(argv, pol); err == nil {
		t.Error("a Windows path outside the allowed directory must be refused")
	}
	argv[7] = `if=virtio,file=C:\Users\pc\AppData\Local\Temp\cs-abc\..\..\..\secret.txt`
	if err := AssertQEMUArgv(argv, pol); err == nil {
		t.Error("a traversal out of the directory must be refused")
	}
}

func TestRequiredOptions(t *testing.T) {
	noNic := removeOpt(runtimeArgv(), "-nic", true)
	if err := AssertQEMUArgv(noNic, policy(QEMURuntime)); err == nil || !strings.Contains(err.Error(), "-nic none is required") {
		t.Errorf("a runtime command line without -nic none must be refused (QEMU adds a default NIC): %v", err)
	}
	noDisplay := removeOpt(runtimeArgv(), "-display", true)
	if err := AssertQEMUArgv(noDisplay, policy(QEMURuntime)); err == nil || !strings.Contains(err.Error(), "-display none is required") {
		t.Errorf("a command line without -display none must be refused: %v", err)
	}
	if err := AssertQEMUArgv(nil, policy(QEMURuntime)); err == nil {
		t.Error("an empty command line must be refused")
	}
}

func TestNicUserOnlyWhileProvisioning(t *testing.T) {
	// The provisioning profile is the only place `-nic user` is legal, and it is held to everything else.
	prov := []string{"qemu-system-x86_64", "-nodefaults", "-accel", "tcg", "-display", "none", "-nic", "user", "-serial", "stdio"}
	if err := AssertQEMUArgv(prov, policy(QEMUProvisioning)); err != nil {
		t.Fatalf("provisioning: %v", err)
	}
	if err := AssertQEMUArgv(prov, policy(QEMURuntime)); err == nil {
		t.Fatal("the runtime profile must refuse -nic user and -serial stdio")
	}
	for _, nic := range []string{"user,hostfwd=tcp::2222-:22", "user,guestfwd=tcp:10.0.2.1:80-cmd:x", "user,smb=/home", "tap", "user;rm"} {
		argv := append(removeOpt(prov, "-nic", true), "-nic", nic)
		if err := AssertQEMUArgv(argv, policy(QEMUProvisioning)); err == nil {
			t.Errorf("provisioning must still refuse -nic %q", nic)
		}
	}
	if err := AssertQEMUArgv(removeOpt(prov, "-nic", true), policy(QEMUProvisioning)); err == nil {
		t.Error("provisioning without a declared network is a builder bug and must be refused")
	}
}

// TestProvisioningNIC pins down the one NIC a provisioning boot has: Debian's cloud kernel has no driver for the
// emulated Intel NIC, so it must be a virtio one. Exactly this option, in that profile, and nothing around it.
func TestProvisioningNIC(t *testing.T) {
	prov := func(nic string) []string {
		return append(removeOpt([]string{"qemu-system-x86_64", "-nodefaults", "-accel", "tcg", "-display", "none", "-nic", "user", "-serial", "stdio"}, "-nic", true), "-nic", nic)
	}
	if err := AssertQEMUArgv(prov(ProvisioningNIC), policy(QEMUProvisioning)); err != nil {
		t.Fatalf("the provisioning NIC: %v", err)
	}
	if err := AssertQEMUArgv(prov(ProvisioningNIC), policy(QEMURuntime)); err == nil {
		t.Error("a runtime VM never has a NIC, not even this one")
	}
	for _, nic := range []string{
		ProvisioningNIC + ",hostfwd=tcp::2222-:22", ProvisioningNIC + ",guestfwd=tcp:10.0.2.1:80-cmd:x", ProvisioningNIC + ",smb=/home",
		"user,model=virtio-net-device", "user,model=virtio-net-pci,netdev=n0", "user,model=VIRTIO-NET-PCI", "user, model=virtio-net-pci", "tap,model=virtio-net-pci",
		"user,model=virtio-net-pci;x",
	} {
		if err := AssertQEMUArgv(prov(nic), policy(QEMUProvisioning)); err == nil {
			t.Errorf("provisioning must refuse -nic %q", nic)
		}
	}
	// The exemption is for -nic only: a virtio network device on its own is still refused everywhere.
	for _, p := range []QEMUProfile{QEMURuntime, QEMUProvisioning} {
		argv := append(prov(ProvisioningNIC), "-device", "virtio-net-pci,netdev=n0")
		if err := AssertQEMUArgv(argv, policy(p)); err == nil {
			t.Errorf("profile %d: -device virtio-net-pci must be refused", p)
		}
	}
}

func TestSerialAndQMP(t *testing.T) {
	base := removeOpt(runtimeArgv(), "-serial", true)
	for _, ok := range []string{"file:/run/user/1000/cs-abc/serial.log", "none", "null"} {
		if err := AssertQEMUArgv(with(base, "-serial", ok), policy(QEMURuntime)); err != nil {
			t.Errorf("-serial %s: %v", ok, err)
		}
	}
	for _, bad := range []string{"tcp:0.0.0.0:4555,server", "telnet:127.0.0.1:1,server", "udp::1", "pty", "file:/etc/x", "stdio"} {
		if err := AssertQEMUArgv(with(base, "-serial", bad), policy(QEMURuntime)); err == nil {
			t.Errorf("-serial %s must be refused", bad)
		}
	}
	for _, ok := range []string{"unix:/tmp/cs-sock-1/qmp.sock,server,nowait", "tcp:127.0.0.1:4444,server=on,wait=off"} {
		if err := AssertQEMUArgv(with(runtimeArgv(), "-qmp", ok), policy(QEMURuntime)); err != nil {
			t.Errorf("-qmp %s: %v", ok, err)
		}
	}
	for _, bad := range []string{"tcp:0.0.0.0:4444,server", "tcp::4444,server", "tcp:192.168.0.2:1", "unix:/tmp/other/q.sock", "telnet:127.0.0.1:1"} {
		if err := AssertQEMUArgv(with(runtimeArgv(), "-qmp", bad), policy(QEMURuntime)); err == nil {
			t.Errorf("-qmp %s must be refused", bad)
		}
	}
}

func TestAccelValues(t *testing.T) {
	base := removeOpt(removeOpt(runtimeArgv(), "-enable-kvm", false), "-cpu", true)
	for _, ok := range []string{"kvm", "whpx", "hvf", "tcg", "tcg,thread=multi"} {
		if err := AssertQEMUArgv(with(base, "-accel", ok), policy(QEMURuntime)); err != nil {
			t.Errorf("-accel %s: %v", ok, err)
		}
	}
	for _, bad := range []string{"xen", "kvm;x", "tcg,x", ""} {
		if err := AssertQEMUArgv(with(base, "-accel", bad), policy(QEMURuntime)); err == nil {
			t.Errorf("-accel %q must be refused", bad)
		}
	}
}

func TestSplitKV(t *testing.T) {
	got := splitKV("socket,id=a,path=/x/a,,b.sock,server=on")
	want := []kv{{"socket", ""}, {"id", "a"}, {"path", "/x/a,b.sock"}, {"server", "on"}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("part %d = %v, want %v", i, got[i], want[i])
		}
	}
	if got := splitKV(""); len(got) != 1 {
		t.Errorf("empty input yields one empty part, got %v", got)
	}
}
