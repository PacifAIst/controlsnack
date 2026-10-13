// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package safety

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// QEMUProfile selects which QEMU command lines are acceptable.
type QEMUProfile int

const (
	// QEMURuntime is every session: no network device, no host sharing, nothing but local sockets.
	QEMURuntime QEMUProfile = iota
	// QEMUProvisioning is the one-off trusted image build, the only profile that may have `-nic user`
	// (PLAN.md section 7.5, step 5). It is held to the same rules in every other respect.
	QEMUProvisioning
)

// ArgvPolicy is the context a QEMU command line is checked in.
type ArgvPolicy struct {
	Profile QEMUProfile
	// AllowedDirs are the directories that disk images, firmware, sockets and log files may live in: the session
	// directory, the socket directory, the kept-VM and image directories and QEMU's firmware directory. Paths are
	// compared as text; resolving symlinks is the caller's job before it builds the command line.
	AllowedDirs []string
}

// AssertQEMUArgv returns an error listing every way argv breaks the guarantees of PLAN.md section 6.1
// (invariants 2 and 3) and section 7.2. argv[0] is the QEMU binary.
//
// It is deliberately a closed world: an option or device model that is not on the allowlist is rejected even if
// nobody has yet thought of a reason to, so new QEMU features cannot slip a NIC or a shared folder in. The
// consequence is that supporting a new device means adding it here, with a test.
func AssertQEMUArgv(argv []string, p ArgvPolicy) error {
	if v := CheckQEMUArgv(argv, p); len(v) > 0 {
		return errors.New("QEMU command line refused: " + strings.Join(v, "; "))
	}
	return nil
}

// CheckQEMUArgv is AssertQEMUArgv returning the individual violations.
func CheckQEMUArgv(argv []string, p ArgvPolicy) []string {
	c := &argvCheck{p: p}
	c.run(argv)
	return c.violations
}

type argvCheck struct {
	p          ArgvPolicy
	violations []string

	sawNicNone, sawDisplayNone, sawNicUser bool
}

func (c *argvCheck) bad(format string, args ...any) {
	c.violations = append(c.violations, fmt.Sprintf(format, args...))
}

// Options that never take a value.
var valuelessOptions = map[string]bool{
	"nodefaults": true, "no-user-config": true, "enable-kvm": true, "S": true, "no-reboot": true,
}

// Options that are refused with a specific reason (anything unlisted is refused as "not on the allowlist").
var forbiddenOptions = map[string]string{
	"virtfs":    "shares a host directory with the guest",
	"fsdev":     "shares a host directory with the guest",
	"netdev":    "adds a network backend",
	"net":       "adds a network backend",
	"vnc":       "exposes the display on the network",
	"spice":     "exposes the display on the network",
	"gdb":       "opens a debugger port",
	"s":         "opens a debugger port",
	"incoming":  "accepts a migration stream",
	"plugin":    "loads host code into QEMU",
	"monitor":   "the human monitor is not allowed; QMP goes through -mon chardev with mode=control",
	"usbdevice": "legacy USB passthrough",
	"parallel":  "host parallel port",
	"object":    "objects can map host files or add backends",
	"global":    "global property overrides are not allowed",
	"kernel":    "direct kernel boot is not used",
	"initrd":    "direct kernel boot is not used",
	"append":    "direct kernel boot is not used",
	"cdrom":     "use -drive so the path is checked",
	"hda":       "use -drive so the path is checked",
	"blockdev":  "use -drive so the path is checked",
}

// ProvisioningNIC is the value of the one `-nic` the provisioning profile may have: user-mode networking (no host
// ports are forwarded in either direction) on a virtio NIC.
const ProvisioningNIC = "user,model=virtio-net-pci"

// Substrings that are refused anywhere in any value, case-insensitively.
var forbiddenSubstrings = []string{
	"hostfwd", "guestfwd", "virtiofs", "vhost-user", "vhost-net", "vhost-vsock", "usb-host", "vfio", "ivshmem",
	"share=on", "ssd=off", "kernel-irqchip", "smb=", "tftp=", "virtio-net",
}

// Device models that may appear after -device: the ones PLAN.md section 7.2 uses, and nothing else.
var allowedDevices = map[string]bool{
	"virtio-vga": true, "virtio-gpu-pci": true, "ramfb": true,
	"qemu-xhci": true, "usb-tablet": true, "usb-kbd": true, "usb-mouse": true,
	"virtio-keyboard-pci": true, "virtio-tablet-pci": true,
	"virtio-rng-pci": true, "virtio-serial-pci": true, "virtserialport": true,
	"virtio-blk-pci": true,
}

var (
	reAccel  = regexp.MustCompile(`^(kvm|whpx|hvf|tcg)(,[a-z0-9-]+=[A-Za-z0-9._:-]+)*$`)
	reQMP    = regexp.MustCompile(`^(unix:[^,]+|tcp:127\.0\.0\.1:[0-9]{1,5})(,(server(=on)?|wait=(on|off)|nowait))*$`)
	reNicUse = regexp.MustCompile(`^user(,model=[a-z0-9-]+)?$`)
)

func (c *argvCheck) run(argv []string) {
	if len(argv) == 0 {
		c.bad("empty command line")
		return
	}
	for i := 1; i < len(argv); i++ {
		tok := argv[i]
		if !strings.HasPrefix(tok, "-") || tok == "-" {
			c.bad("unexpected argument %q", tok)
			continue
		}
		name := strings.TrimLeft(tok, "-")
		val, hasVal := "", false
		if !valuelessOptions[name] && i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
			val, hasVal = argv[i+1], true
			i++
		}
		c.option(name, val, hasVal)
	}
	if !c.sawDisplayNone {
		c.bad("-display none is required")
	}
	switch c.p.Profile {
	case QEMUProvisioning:
		if !c.sawNicUser {
			c.bad("the provisioning profile must declare its network with -nic user")
		}
	default:
		if !c.sawNicNone {
			c.bad("-nic none is required: the runtime VM has no network device")
		}
	}
}

func (c *argvCheck) option(name, val string, hasVal bool) {
	if why, bad := forbiddenOptions[name]; bad {
		c.bad("-%s is not allowed (%s)", name, why)
		return
	}
	lower := strings.ToLower(val)
	for _, sub := range forbiddenSubstrings {
		// The one NIC the provisioning boot has is a virtio one: Debian's cloud kernel has no driver for the
		// emulated Intel NIC QEMU would pick on its own. Only this exact option, only in that profile.
		if sub == "virtio-net" && name == "nic" && c.p.Profile == QEMUProvisioning && val == ProvisioningNIC {
			continue
		}
		if strings.Contains(lower, sub) {
			c.bad("-%s %q contains %q, which is not allowed", name, val, sub)
		}
	}

	switch name {
	case "name", "m", "smp", "cpu", "rtc", "boot", "machine", "M", "sandbox":
		if !hasVal {
			c.bad("-%s needs a value", name)
		}
	case "nodefaults", "no-user-config", "S", "no-reboot":
		// harmless flags
	case "enable-kvm":
		// -enable-kvm is the KVM spelling of -accel kvm (PLAN.md section 7.2)
	case "accel":
		if !reAccel.MatchString(val) {
			c.bad("-accel %q is not one of kvm, whpx, hvf, tcg", val)
		}
	case "display":
		if val != "none" {
			c.bad("-display %q is not allowed; only -display none (the viewer shows the guest through the agent)", val)
		} else {
			c.sawDisplayNone = true
		}
	case "nic":
		c.nic(val)
	case "device":
		c.device(val)
	case "drive":
		c.drive(val)
	case "chardev":
		c.chardev(val)
	case "mon":
		c.mon(val)
	case "serial":
		c.serial(val)
	case "qmp":
		if !reQMP.MatchString(val) {
			c.bad("-qmp %q is not a local unix socket or a 127.0.0.1 TCP endpoint", val)
		} else if strings.HasPrefix(val, "unix:") {
			c.checkPath("-qmp unix socket", firstField(strings.TrimPrefix(val, "unix:")))
		}
	default:
		c.bad("-%s is not on the allowlist of QEMU options", name)
	}
}

func (c *argvCheck) nic(val string) {
	switch {
	case val == "none":
		c.sawNicNone = true
	case c.p.Profile == QEMUProvisioning && reNicUse.MatchString(val):
		c.sawNicUser = true
	default:
		c.bad("-nic %q is not allowed; the runtime VM has no network device", val)
	}
}

func (c *argvCheck) device(val string) {
	model := strings.ToLower(firstField(val))
	if !allowedDevices[model] {
		c.bad("-device %q is not on the allowlist of device models", model)
	}
}

func (c *argvCheck) drive(val string) {
	for _, kv := range splitKV(val) {
		switch {
		case kv.key == "file":
			c.checkPath("-drive file", kv.val)
		case strings.HasPrefix(kv.key, "file."):
			c.bad("-drive %s=%s is not allowed (nested block drivers could reach the network)", kv.key, kv.val)
		case kv.key == "driver" && kv.val != "qcow2" && kv.val != "raw" && kv.val != "file":
			c.bad("-drive driver=%s is not allowed", kv.val)
		}
	}
}

func (c *argvCheck) chardev(val string) {
	backend := firstField(val)
	kvs := splitKV(val)[1:]
	switch backend {
	case "socket":
		var host, port, sockPath string
		for _, kv := range kvs {
			switch kv.key {
			case "host":
				host = kv.val
			case "port":
				port = kv.val
			case "path":
				sockPath = kv.val
			case "abstract":
				if kv.val != "off" {
					c.bad("-chardev socket abstract=%s is not allowed: abstract sockets are reachable by every local process", kv.val)
				}
			case "fd":
				c.bad("-chardev socket fd=%s is not allowed", kv.val)
			}
		}
		if host != "" && host != "127.0.0.1" {
			c.bad("-chardev socket host=%s is not allowed; chardevs bind 127.0.0.1 only", host)
		}
		if port != "" && host != "127.0.0.1" {
			c.bad("-chardev socket port=%s without host=127.0.0.1 would listen on every interface", port)
		}
		if sockPath != "" {
			c.checkPath("-chardev socket path", sockPath)
		}
		if sockPath == "" && port == "" {
			c.bad("-chardev socket needs a local path or a 127.0.0.1 port")
		}
	case "file":
		for _, kv := range kvs {
			if kv.key == "path" {
				c.checkPath("-chardev file path", kv.val)
			}
		}
	case "null":
	case "stdio":
		if c.p.Profile != QEMUProvisioning {
			c.bad("-chardev stdio is only allowed while provisioning the image")
		}
	default:
		c.bad("-chardev backend %q is not allowed (socket, file or null only)", backend)
	}
}

func (c *argvCheck) mon(val string) {
	mode := ""
	for _, kv := range splitKV(val) {
		if kv.key == "mode" {
			mode = kv.val
		}
	}
	if mode != "control" {
		c.bad("-mon must use mode=control (QMP), got %q", mode)
	}
}

func (c *argvCheck) serial(val string) {
	switch {
	case val == "none" || val == "null":
	case strings.HasPrefix(val, "file:"):
		c.checkPath("-serial file", strings.TrimPrefix(val, "file:"))
	case val == "stdio" && c.p.Profile == QEMUProvisioning:
	default:
		c.bad("-serial %q is not allowed (file:PATH, none or null; stdio only while provisioning)", val)
	}
}

// checkPath requires p to be an absolute path inside one of the allowed directories.
func (c *argvCheck) checkPath(what, p string) {
	if !pathUnderAny(p, c.p.AllowedDirs) {
		c.bad("%s=%q is outside the directories QEMU may touch", what, p)
	}
}

// pathUnderAny reports whether p is an absolute path strictly inside one of dirs. The comparison is textual and
// platform-neutral: backslashes count as slashes, ".." segments are resolved first, and paths with a drive
// letter compare case-insensitively.
func pathUnderAny(p string, dirs []string) bool {
	np, ok := normalizeAbs(p)
	if !ok {
		return false
	}
	for _, d := range dirs {
		nd, ok := normalizeAbs(d)
		if !ok {
			continue
		}
		if np != nd && strings.HasPrefix(np, strings.TrimSuffix(nd, "/")+"/") {
			return true
		}
	}
	return false
}

func normalizeAbs(p string) (string, bool) {
	p = strings.ReplaceAll(p, `\`, "/")
	if strings.Contains(p, "\x00") {
		return "", false
	}
	windows := len(p) >= 3 && p[1] == ':' && p[2] == '/'
	if !windows && !strings.HasPrefix(p, "/") {
		return "", false
	}
	p = path.Clean(p)
	if windows {
		p = strings.ToLower(p)
	}
	return p, true
}

type kv struct{ key, val string }

// splitKV splits a QEMU "a,b=c,d=e" option value into pairs. A doubled comma is a literal comma. The first
// element has an empty val and its text in key (it is the backend or device name).
func splitKV(s string) []kv {
	var parts []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			if i+1 < len(s) && s[i+1] == ',' {
				cur.WriteByte(',')
				i++
				continue
			}
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(s[i])
	}
	parts = append(parts, cur.String())
	out := make([]kv, 0, len(parts))
	for _, part := range parts {
		k, v, _ := strings.Cut(part, "=")
		out = append(out, kv{key: k, val: v})
	}
	return out
}

func firstField(s string) string {
	f, _, _ := strings.Cut(s, ",")
	return f
}
