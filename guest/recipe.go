// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package guest

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed files
var embedded embed.FS

// RecipeVersion changes when the way the recipe is turned into an image changes without any of the files changing
// (the cloud-config layout, say). It is part of the recipe hash.
const RecipeVersion = "1"

// User is the only account in the image: no password, no sudo, a normal user ID.
const User = "cs"

// Packages are installed with apt-get install --no-install-recommends (PLAN.md section 7.5). Adjust after testing;
// every change changes the recipe hash, which makes `run` suggest a rebuild.
var Packages = []string{
	// display and login
	"xserver-xorg-core", "xserver-xorg-input-libinput", "x11-xserver-utils", "xauth", "libgl1-mesa-dri",
	"lightdm", "lightdm-gtk-greeter", "dbus-x11", "dbus-user-session", "libpam-systemd",
	// the desktop
	"xfce4", "xfce4-terminal", "xfce4-power-manager", "mousepad", "thunar",
	// accessibility, which is how the agent sees applications
	"at-spi2-core", "libatk-adaptor", "gsettings-desktop-schemas",
	// the browser, text recognition, typing fallback, fonts
	"chromium", "tesseract-ocr", "tesseract-ocr-eng", "libwebp7", "xdotool", "fonts-dejavu-core", "fonts-noto-core",
	// network lock-down and the basics of a shell session
	"nftables", "ca-certificates", "nano", "less", "file", "unzip", "zip",
}

// File is a file the recipe puts in the guest.
type File struct {
	Path    string // absolute path in the guest
	Mode    string // octal, as cloud-init wants it: "0644"
	Content []byte
	source  string // name under files/
}

// layout maps embedded files to their place in the guest.
var layout = []struct{ source, path, mode string }{
	{"apt.conf", "/etc/apt/apt.conf.d/99controlsnack", "0644"},
	{"lightdm-controlsnack.conf", "/etc/lightdm/lightdm.conf.d/50-controlsnack.conf", "0644"},
	{"xsession-env.sh", "/etc/X11/Xsession.d/90controlsnack-env", "0644"},
	{"xfce4-power-manager.xml", "/etc/xdg/xfce4/xfconf/xfce-perchannel-xml/xfce4-power-manager.xml", "0644"},
	{"bootstrap.desktop", "/etc/xdg/autostart/controlsnack-bootstrap.desktop", "0644"},
	{"bootstrap.sh", "/usr/local/lib/controlsnack/bootstrap.sh", "0755"},
	{"udev.rules", "/etc/udev/rules.d/99-controlsnack.rules", "0644"},
	{"chromium-policy.json", "/etc/chromium/policies/managed/controlsnack.json", "0644"},
	{"chromium-flags.sh", "/etc/chromium.d/controlsnack", "0644"},
	{"nftables.conf", "/etc/nftables.conf", "0644"},
	{"controlsnack.gschema.override", "/usr/share/glib-2.0/schemas/90_controlsnack.gschema.override", "0644"},
}

// ProvisionPath is where the provisioning script is written; cloud-init runs it.
const ProvisionPath = "/usr/local/lib/controlsnack/provision.sh"

func read(name string) []byte {
	data, err := embedded.ReadFile("files/" + name)
	if err != nil {
		panic("guest: embedded file missing: " + name) // a build error, caught by the tests
	}
	return data
}

// Files returns the configuration files of the image, in a fixed order.
func Files() []File {
	out := make([]File, 0, len(layout)+1)
	for _, l := range layout {
		out = append(out, File{Path: l.path, Mode: l.mode, Content: read(l.source), source: l.source})
	}
	out = append(out, File{Path: ProvisionPath, Mode: "0755", Content: ProvisionScript(), source: "provision.sh"})
	return out
}

// ProvisionScript is the provisioning script with the package list filled in.
func ProvisionScript() []byte {
	return bytes.ReplaceAll(read("provision.sh"), []byte("@PACKAGES@"), []byte(strings.Join(Packages, " ")))
}

// RecipeHash identifies everything that determines what the image contains: the files, the provisioning script,
// the package list, the user and the recipe version. `run` compares it with the one in the image's manifest.
func RecipeHash() string { return hashRecipe(Packages, Files()) }

func hashRecipe(packages []string, files []File) string {
	h := sha256.New()
	field := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:]) // length-prefixed, so the fields cannot run into each other
		h.Write([]byte(s))
	}
	field("controlsnack-recipe")
	field(RecipeVersion)
	field(User)
	pk := append([]string(nil), packages...)
	sort.Strings(pk)
	field(strings.Join(pk, "\n"))
	for _, f := range files {
		field(f.Path)
		field(f.Mode)
		field(string(f.Content))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// cloudConfig is the shape of the user-data; yaml.Marshal makes sure it is valid YAML whatever the file contents.
type cloudConfig struct {
	Hostname       string     `yaml:"hostname"`
	ManageEtcHosts bool       `yaml:"manage_etc_hosts"`
	Users          []ccUser   `yaml:"users"`
	SSHPwauth      bool       `yaml:"ssh_pwauth"`
	DisableRoot    bool       `yaml:"disable_root"`
	WriteFiles     []ccFile   `yaml:"write_files"`
	Runcmd         [][]string `yaml:"runcmd"`
	FinalMessage   string     `yaml:"final_message"`
}

type ccUser struct {
	Name       string `yaml:"name"`
	UID        int    `yaml:"uid"`
	Gecos      string `yaml:"gecos"`
	Shell      string `yaml:"shell"`
	LockPasswd bool   `yaml:"lock_passwd"`
	Sudo       bool   `yaml:"sudo"`
	CreateHome bool   `yaml:"create_home"`
}

type ccFile struct {
	Path        string `yaml:"path"`
	Owner       string `yaml:"owner"`
	Permissions string `yaml:"permissions"`
	Encoding    string `yaml:"encoding"`
	Content     string `yaml:"content"`
}

// UserData returns the cloud-config for the provisioning boot. Files travel base64-encoded so that no content can
// break the YAML. The only account is the unprivileged desktop user; there is no default user and no sudo.
func UserData() ([]byte, error) {
	cfg := cloudConfig{
		Hostname:       "controlsnack",
		ManageEtcHosts: true,
		Users: []ccUser{{Name: User, UID: 1000, Gecos: "ControlSnack", Shell: "/bin/bash",
			LockPasswd: true, Sudo: false, CreateHome: true}},
		SSHPwauth:    false,
		DisableRoot:  true,
		Runcmd:       [][]string{{ProvisionPath}},
		FinalMessage: "CS-PROVISION: cloud-init finished",
	}
	for _, f := range Files() {
		cfg.WriteFiles = append(cfg.WriteFiles, ccFile{Path: f.Path, Owner: "root:root", Permissions: f.Mode, Encoding: "b64",
			Content: base64.StdEncoding.EncodeToString(f.Content)})
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("encoding the cloud-config: %w", err)
	}
	return append([]byte("#cloud-config\n"), body...), nil
}

// MetaData returns the NoCloud meta-data. The instance id changes with the recipe, so cloud-init treats every
// recipe as a new instance.
func MetaData() []byte {
	return []byte("instance-id: controlsnack-" + RecipeHash()[:12] + "\nlocal-hostname: controlsnack\n")
}

// NetworkConfig returns the NoCloud network configuration (version 2): DHCP on every Ethernet device, which is
// the single virtual NIC of the provisioning boot.
func NetworkConfig() []byte {
	return []byte("version: 2\nethernets:\n  nics:\n    match:\n      name: \"e*\"\n    dhcp4: true\n    dhcp6: false\n")
}
