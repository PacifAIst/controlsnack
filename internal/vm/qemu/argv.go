// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qemu

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/safety"
)

// The command-line builders are pure functions of their spec: no file system, no clock, no environment. Whatever
// they return has been through safety.AssertQEMUArgv, so a spec that would produce a NIC, a shared folder or a
// path outside the session's directories is an error here and never a command line (PLAN.md sections 6.1 and 7.2).

// maxSocketPath keeps a unix socket path inside sun_path (108 bytes on Linux, 104 on macOS, with the terminator).
const maxSocketPath = 100

var reName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// Endpoint is where QEMU listens for the controller: a unix socket (Linux, macOS) or a TCP port on 127.0.0.1
// (Windows, D-28). Exactly one of the two is set. QEMU creates it and waits for the controller to connect, so the
// controller is the first and only client.
type Endpoint struct {
	Path string
	Port int
}

func (e Endpoint) chardev(id string) (string, error) {
	switch {
	case e.Path != "" && e.Port != 0:
		return "", fmt.Errorf("endpoint %s has both a socket path and a port", id)
	case e.Path != "":
		if err := checkPath("socket "+id, e.Path); err != nil {
			return "", err
		}
		if len(e.Path) > maxSocketPath {
			return "", fmt.Errorf("socket path for %s is %d bytes long; unix sockets allow about %d: use a shorter directory", id, len(e.Path), maxSocketPath)
		}
		return fmt.Sprintf("socket,id=%s,path=%s,server=on,wait=on", id, e.Path), nil
	case e.Port >= 1 && e.Port <= 65535:
		return fmt.Sprintf("socket,id=%s,host=127.0.0.1,port=%d,server=on,wait=on", id, e.Port), nil
	}
	return "", fmt.Errorf("endpoint %s has neither a socket path nor a valid port", id)
}

// checkPath requires an absolute path that survives being written into a comma-separated QEMU option.
func checkPath(what, p string) error {
	switch {
	case p == "":
		return fmt.Errorf("%s: no path", what)
	case strings.ContainsAny(p, ",\n\r\x00"):
		return fmt.Errorf("%s %q contains a comma or control character, which QEMU option strings cannot carry", what, p)
	case !isAbs(p):
		return fmt.Errorf("%s %q is not an absolute path", what, p)
	}
	return nil
}

// isAbs accepts unix paths and Windows drive paths on every OS, so the builders are testable anywhere.
func isAbs(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	return len(p) >= 3 && (p[0]|0x20 >= 'a' && p[0]|0x20 <= 'z') && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

// common is what both profiles share.
type common struct {
	Install     Install
	Accel       string
	Name        string
	CPUs        int
	MemoryMiB   int
	Disk        string
	Vars        string // aarch64: the session's own copy of the UEFI variable store
	Serial      string
	AllowedDirs []string
}

func (c common) validate() error {
	if c.Install.System == "" {
		return fmt.Errorf("no QEMU installation")
	}
	switch c.Install.Arch {
	case ArchX86_64, ArchAarch64:
	default:
		return fmt.Errorf("unsupported guest architecture %q", c.Install.Arch)
	}
	switch c.Accel {
	case AccelKVM, AccelHVF, AccelTCG:
	case AccelWHPX:
		if c.Install.Arch != ArchX86_64 {
			return fmt.Errorf("whpx accelerates x86_64 guests only, not %s", c.Install.Arch)
		}
	default:
		return fmt.Errorf("unknown accelerator %q", c.Accel)
	}
	switch {
	case !reName.MatchString(c.Name):
		return fmt.Errorf("VM name %q is not 1-40 characters of a-z, 0-9 and -", c.Name)
	case c.CPUs < 1 || c.CPUs > 64:
		return fmt.Errorf("%d CPUs is outside 1-64", c.CPUs)
	case c.MemoryMiB < 256 || c.MemoryMiB > 1<<20:
		return fmt.Errorf("%d MiB of memory is outside 256-%d", c.MemoryMiB, 1<<20)
	case len(c.AllowedDirs) == 0:
		return fmt.Errorf("no directories QEMU may touch were given")
	}
	if err := checkPath("disk", c.Disk); err != nil {
		return err
	}
	if err := checkPath("serial log", c.Serial); err != nil {
		return err
	}
	if c.Install.Arch == ArchAarch64 {
		if err := checkPath("UEFI variable store", c.Vars); err != nil {
			return err
		}
	}
	return nil
}

// head is the start of every command line up to and including memory.
func (c common) head() []string {
	argv := []string{c.Install.System, "-name", "controlsnack-" + c.Name, "-nodefaults", "-no-user-config",
		"-machine", requiredMachine[c.Install.Arch]}
	argv = append(argv, AccelArgs(c.Accel)...)
	if cpu := CPUModel(c.Accel); cpu != "" {
		argv = append(argv, "-cpu", cpu)
	}
	return append(argv, "-smp", strconv.Itoa(c.CPUs), "-m", strconv.Itoa(c.MemoryMiB))
}

// firmware adds the UEFI pflash units of an aarch64 guest and returns the directory the code image lives in.
func (c common) firmware() (args []string, dir string, err error) {
	if c.Install.Arch != ArchAarch64 {
		return nil, "", nil
	}
	fw, err := c.Install.RequireFirmware()
	if err != nil {
		return nil, "", err
	}
	if err := checkPath("UEFI code image", fw.Code); err != nil {
		return nil, "", err
	}
	return []string{
		"-drive", "if=pflash,format=raw,readonly=on,file=" + fw.Code,
		"-drive", "if=pflash,format=raw,file=" + c.Vars,
	}, filepath.Dir(fw.Code), nil
}

// finish runs the safety check over the finished command line.
func (c common) finish(argv []string, fwDir string, profile safety.QEMUProfile) ([]string, error) {
	dirs := append([]string(nil), c.AllowedDirs...)
	if fwDir != "" {
		dirs = append(dirs, fwDir) // QEMU's firmware directory (safety.ArgvPolicy)
	}
	if err := safety.AssertQEMUArgv(argv, safety.ArgvPolicy{Profile: profile, AllowedDirs: dirs}); err != nil {
		return nil, err
	}
	return argv, nil
}

// RuntimeSpec describes the command line of a session VM (PLAN.md section 7.2).
type RuntimeSpec struct {
	Install   Install
	Accel     string // the accelerator that was chosen: kvm, whpx, hvf or tcg (also for the TCG retry)
	Name      string // session id; the VM is named controlsnack-<Name>
	CPUs      int
	MemoryMiB int
	Width     int // guest resolution
	Height    int
	Disk      string // the session's qcow2 overlay
	// Cache is the disk cache mode: "unsafe" for throwaway sessions (the default), "writeback" for kept VMs.
	Cache  string
	Vars   string   // aarch64: the session's copy of the UEFI variable store
	Serial string   // serial console log
	Agent  Endpoint // the agent's virtio-serial port
	QMP    Endpoint // the control channel
	// Net is the second virtio-serial port, set only with --allow-net; nil means the guest has no way to ask the host
	// for network access at all.
	Net *Endpoint
	// AllowedDirs are the directories QEMU may touch: the session directory, the socket directory, the kept-VM
	// directory and the image directory. QEMU's firmware directory is added for aarch64.
	AllowedDirs []string
}

// RuntimeArgv builds the command line of a session VM. The result has passed safety.AssertQEMUArgv.
func RuntimeArgv(s RuntimeSpec) ([]string, error) {
	c := common{Install: s.Install, Accel: s.Accel, Name: s.Name, CPUs: s.CPUs, MemoryMiB: s.MemoryMiB, Disk: s.Disk,
		Vars: s.Vars, Serial: s.Serial, AllowedDirs: s.AllowedDirs}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("runtime command line: %w", err)
	}
	cache := s.Cache
	switch cache {
	case "":
		cache = "unsafe"
	case "unsafe", "writeback":
	default:
		return nil, fmt.Errorf("runtime command line: unknown disk cache mode %q", s.Cache)
	}
	if s.Width < 320 || s.Width > 7680 || s.Height < 200 || s.Height > 4320 {
		return nil, fmt.Errorf("runtime command line: resolution %dx%d is outside 320x200-7680x4320", s.Width, s.Height)
	}
	agent, err := s.Agent.chardev("agent0")
	if err != nil {
		return nil, fmt.Errorf("runtime command line: %w", err)
	}
	qmp, err := s.QMP.chardev("qmp0")
	if err != nil {
		return nil, fmt.Errorf("runtime command line: %w", err)
	}
	fwArgs, fwDir, err := c.firmware()
	if err != nil {
		return nil, fmt.Errorf("runtime command line: %w", err)
	}

	gpu := "virtio-vga"
	if s.Install.Arch == ArchAarch64 {
		gpu = "virtio-gpu-pci"
	}
	argv := c.head()
	argv = append(argv, fwArgs...)
	argv = append(argv,
		"-drive", fmt.Sprintf("if=virtio,format=qcow2,file=%s,cache=%s,discard=unmap", s.Disk, cache),
		"-device", fmt.Sprintf("%s,xres=%d,yres=%d", gpu, s.Width, s.Height),
		"-device", "qemu-xhci", "-device", "usb-tablet", "-device", "usb-kbd",
		"-device", "virtio-rng-pci", "-device", "virtio-serial-pci",
		"-chardev", agent, "-device", "virtserialport,chardev=agent0,name="+protocol.PortAgent,
		"-chardev", qmp, "-mon", "chardev=qmp0,mode=control",
		"-serial", "file:"+s.Serial, "-display", "none", "-nic", "none",
		// A guest that reboots would loop unseen; with -no-reboot QEMU exits and the supervisor notices (D-45).
		"-no-reboot")
	if s.Net != nil {
		net, err := s.Net.chardev("net0")
		if err != nil {
			return nil, fmt.Errorf("runtime command line: %w", err)
		}
		argv = append(argv, "-chardev", net, "-device", "virtserialport,chardev=net0,name="+protocol.PortNet)
	}
	if s.Install.Sandbox {
		argv = append(argv, "-sandbox", SandboxArg)
	}
	argv, err = c.finish(argv, fwDir, safety.QEMURuntime)
	if err != nil {
		return nil, fmt.Errorf("runtime command line: %w", err)
	}
	return argv, nil
}

// ProvisionSpec describes the command line of the one-off image build (PLAN.md section 7.5, step 5): the only
// profile with a network device, a trusted step with no LLM, that `run` cannot select.
type ProvisionSpec struct {
	Install     Install
	Accel       string
	Name        string
	CPUs        int
	MemoryMiB   int
	Disk        string   // the provisioning overlay
	Seed        string   // the NoCloud ISO (volume label cidata)
	Vars        string   // aarch64: a copy of the UEFI variable store
	Serial      string   // the console log the builder follows for progress
	QMP         Endpoint // for a clean quit; the builder is the only client
	AllowedDirs []string
}

// ProvisionArgv builds the command line of the provisioning boot. The result has passed safety.AssertQEMUArgv with
// the provisioning profile; there is no agent port, no display device and no sandbox (cloud-init needs to do
// anything it likes inside the guest, and the guest only has user-mode networking).
func ProvisionArgv(s ProvisionSpec) ([]string, error) {
	c := common{Install: s.Install, Accel: s.Accel, Name: s.Name, CPUs: s.CPUs, MemoryMiB: s.MemoryMiB, Disk: s.Disk,
		Vars: s.Vars, Serial: s.Serial, AllowedDirs: s.AllowedDirs}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("provisioning command line: %w", err)
	}
	if err := checkPath("seed image", s.Seed); err != nil {
		return nil, fmt.Errorf("provisioning command line: %w", err)
	}
	qmp, err := s.QMP.chardev("qmp0")
	if err != nil {
		return nil, fmt.Errorf("provisioning command line: %w", err)
	}
	fwArgs, fwDir, err := c.firmware()
	if err != nil {
		return nil, fmt.Errorf("provisioning command line: %w", err)
	}
	argv := c.head()
	argv = append(argv, fwArgs...)
	argv = append(argv,
		"-drive", fmt.Sprintf("if=virtio,format=qcow2,file=%s,cache=unsafe,discard=unmap", s.Disk),
		"-drive", "if=virtio,format=raw,media=cdrom,readonly=on,file="+s.Seed,
		"-device", "virtio-rng-pci",
		"-chardev", qmp, "-mon", "chardev=qmp0,mode=control",
		"-serial", "file:"+s.Serial, "-display", "none",
		"-nic", "user", // the only place a guest of ours gets a network device
		"-no-reboot")
	argv, err = c.finish(argv, fwDir, safety.QEMUProvisioning)
	if err != nil {
		return nil, fmt.Errorf("provisioning command line: %w", err)
	}
	return argv, nil
}
