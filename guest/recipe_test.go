// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package guest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/vm/seed"
)

func TestEveryEmbeddedFileIsUsedAndEveryUseExists(t *testing.T) {
	entries, err := embedded.ReadDir("files")
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{"provision.sh": true}
	for _, l := range layout {
		used[l.source] = true
		if len(read(l.source)) == 0 {
			t.Errorf("%s is empty", l.source)
		}
	}
	for _, e := range entries {
		if !used[e.Name()] {
			t.Errorf("files/%s is embedded but never installed", e.Name())
		}
		delete(used, e.Name())
	}
	for name := range used {
		t.Errorf("%s is installed but not embedded", name)
	}
	seen := map[string]bool{}
	for _, f := range Files() {
		if seen[f.Path] || !path.IsAbs(f.Path) || strings.Contains(f.Path, "..") {
			t.Errorf("bad or duplicate destination %q", f.Path)
		}
		seen[f.Path] = true
		if f.Mode != "0644" && f.Mode != "0755" {
			t.Errorf("%s: mode %s", f.Path, f.Mode)
		}
		if (f.Mode == "0755") != (strings.HasSuffix(f.Path, ".sh")) {
			t.Errorf("only scripts are executable: %s %s", f.Path, f.Mode)
		}
	}
}

func TestPackages(t *testing.T) {
	re := regexp.MustCompile(`^[a-z0-9][a-z0-9+.\-]+$`)
	seen := map[string]bool{}
	for _, p := range Packages {
		if !re.MatchString(p) || seen[p] {
			t.Errorf("bad or duplicate package %q", p)
		}
		seen[p] = true
	}
	// What provision.sh checks for, and what the agent will need, has to come from somewhere.
	for _, want := range []string{"lightdm", "xfce4", "xfce4-terminal", "chromium", "tesseract-ocr", "xdotool", "x11-xserver-utils", "nftables",
		"at-spi2-core", "libatk-adaptor", "libpam-systemd", "dbus-user-session"} {
		if !seen[want] {
			t.Errorf("package %s is missing", want)
		}
	}
	// What must never be installed.
	for _, bad := range []string{"openssh-server", "sudo", "unattended-upgrades", "xfce4-screensaver", "light-locker", "network-manager"} {
		if seen[bad] {
			t.Errorf("%s must not be in the image", bad)
		}
	}
}

func TestProvisionScript(t *testing.T) {
	s := string(ProvisionScript())
	if strings.Contains(s, "@PACKAGES@") {
		t.Fatal("the package placeholder is still there")
	}
	if !strings.Contains(s, "PACKAGES=("+strings.Join(Packages, " ")+")") {
		t.Error("the package list is substituted as a bash array")
	}
	for _, want := range []string{
		"apt-get install -y --no-install-recommends",
		"CS-PROVISION: ", `say "done"`, "systemctl poweroff", "cloud-init.disabled", "systemctl set-default graphical.target",
		"openssh-server", "unattended-upgrades", "light-locker", "GRUB_TIMEOUT=0", "fstrim", "systemctl enable nftables", "set -Eeuo pipefail",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("provision.sh lacks %q", want)
		}
	}
	// Failure must be visible to the host: a marker, then power off, never a hang.
	if !strings.Contains(s, "trap 'say \"FAILED") {
		t.Error("a failing step is reported and powers the VM off")
	}
}

func TestScriptsHaveValidSyntax(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash here")
	}
	dir := t.TempDir()
	for name, data := range map[string][]byte{"bootstrap.sh": read("bootstrap.sh"), "provision.sh": ProvisionScript(),
		"xsession-env.sh": read("xsession-env.sh"), "chromium-flags.sh": read("chromium-flags.sh")} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.CommandContext(context.Background(), bash, "-n", p).CombinedOutput(); err != nil { //nolint:gosec // a tool found on this machine
			t.Errorf("%s: %v: %s", name, err, out)
		}
	}
}

func TestConfigFilesAreWellFormed(t *testing.T) {
	var policy map[string]any
	if err := json.Unmarshal(read("chromium-policy.json"), &policy); err != nil {
		t.Fatalf("chromium policy: %v", err)
	}
	if policy["ProxyServer"] != "127.0.0.1:3128" || policy["ProxyMode"] != "fixed_servers" || policy["PasswordManagerEnabled"] != false ||
		policy["SyncDisabled"] != true || policy["DownloadDirectory"] != "/home/cs/Downloads" {
		t.Errorf("policy %v", policy)
	}
	dec := xml.NewDecoder(bytes.NewReader(read("xfce4-power-manager.xml")))
	for {
		if _, err := dec.Token(); err != nil {
			if err.Error() != "EOF" {
				t.Errorf("power manager xml: %v", err)
			}
			break
		}
	}
	// The proxy the browser and the shell use is the one port the agent serves on.
	if !strings.Contains(string(read("xsession-env.sh")), "http://127.0.0.1:3128") {
		t.Error("the session proxy")
	}
	nft := string(read("nftables.conf"))
	if strings.Count(nft, "policy drop") != 3 || !strings.Contains(nft, `oifname "lo" accept`) {
		t.Errorf("nftables: all hooks drop by default and loopback is allowed:\n%s", nft)
	}
	if !strings.Contains(string(read("lightdm-controlsnack.conf")), "autologin-user="+User) {
		t.Error("autologin user")
	}
	for _, want := range []string{"NO_AT_BRIDGE=0", "QT_ACCESSIBILITY=1", "GNOME_ACCESSIBILITY=1", "QT_LINUX_ACCESSIBILITY_ALWAYS_ON=1", "GTK_MODULES", "xset s off"} {
		if !strings.Contains(string(read("xsession-env.sh")), want) {
			t.Errorf("session environment lacks %s", want)
		}
	}
	if !strings.Contains(string(read("chromium-flags.sh")), "--force-renderer-accessibility") {
		t.Error("renderer accessibility")
	}
	if !strings.Contains(string(read("bootstrap.desktop")), "Exec=/usr/local/lib/controlsnack/bootstrap.sh") {
		t.Error("autostart entry")
	}
}

// TestGuestAndHostAgreeOnThePorts ties the guest files to the host's port names.
func TestGuestAndHostAgreeOnThePorts(t *testing.T) {
	rule := string(read("udev.rules"))
	m := regexp.MustCompile(`ATTR\{name\}=="([^"]+)"`).FindStringSubmatch(rule)
	if m == nil {
		t.Fatal(rule)
	}
	for _, port := range []string{protocol.PortAgent, protocol.PortNet} {
		if ok, _ := path.Match(m[1], port); !ok {
			t.Errorf("the udev rule %q does not match the port %q", m[1], port)
		}
	}
	if ok, _ := path.Match(m[1], "org.freedesktop.other"); ok {
		t.Error("the rule must not match other ports")
	}
	if !strings.Contains(rule, `OWNER="cs"`) || !strings.Contains(rule, `MODE="0600"`) {
		t.Error("only the desktop user may open the port")
	}
	if !strings.Contains(string(read("bootstrap.sh")), "/dev/virtio-ports/"+protocol.PortAgent) {
		t.Error("the stub opens the agent port")
	}
}

// TestBootstrapStubAcceptsWhatTheHostSends checks the stub's line pattern against the host's encoder, both ways.
func TestBootstrapStubAcceptsWhatTheHostSends(t *testing.T) {
	m := regexp.MustCompile(`(?m)^re='([^']+)'$`).FindSubmatch(read("bootstrap.sh"))
	if m == nil {
		t.Fatal("no re= line in bootstrap.sh")
	}
	re := regexp.MustCompile(string(m[1])) // the pattern is plain ERE that RE2 reads the same way
	sum, nonce := strings.Repeat("ab", 32), strings.Repeat("0f", 16)
	for _, arch := range []string{protocol.ArchX86_64, protocol.ArchAarch64} {
		for _, size := range []int64{1, 1573026, 64 << 20} {
			line, err := protocol.Push{Arch: arch, Size: size, SHA256: sum, Nonce: nonce}.Line()
			if err != nil {
				t.Fatal(err)
			}
			if !re.MatchString(strings.TrimSuffix(line, "\n")) {
				t.Errorf("the stub would refuse the host's own line: %q", line)
			}
		}
	}
	for _, bad := range []string{
		"CSBOOT/1 PUSH arch=riscv64 size=1 sha256=" + sum + " nonce=" + nonce,
		"CSBOOT/1 PUSH arch=x86_64 size=-1 sha256=" + sum + " nonce=" + nonce,
		"CSBOOT/1 PUSH arch=x86_64 size=1234567890 sha256=" + sum + " nonce=" + nonce, // ten digits
		"CSBOOT/1 PUSH arch=x86_64 size=1 sha256=" + strings.ToUpper(sum) + " nonce=" + nonce,
		"CSBOOT/1 PUSH arch=x86_64 size=1 sha256=" + sum + " nonce=zz",
		"CSBOOT/1 PUSH arch=x86_64 size=1 sha256=" + sum + " nonce=" + nonce + " extra=1",
		"CSBOOT/2 PUSH arch=x86_64 size=1 sha256=" + sum + " nonce=" + nonce,
	} {
		if re.MatchString(bad) {
			t.Errorf("the stub accepts %q", bad)
		}
	}
}

// --- the cloud-config ------------------------------------------------------------------------------------------

func TestUserData(t *testing.T) {
	data, err := UserData()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("#cloud-config\n")) {
		t.Fatalf("cloud-init needs the header: %.40q", data)
	}
	var cfg struct {
		Hostname    string `yaml:"hostname"`
		SSHPwauth   bool   `yaml:"ssh_pwauth"`
		DisableRoot bool   `yaml:"disable_root"`
		Users       []struct {
			Name       string `yaml:"name"`
			UID        int    `yaml:"uid"`
			Shell      string `yaml:"shell"`
			LockPasswd bool   `yaml:"lock_passwd"`
			Sudo       bool   `yaml:"sudo"`
		} `yaml:"users"`
		WriteFiles []struct {
			Path, Owner, Permissions, Encoding, Content string
		} `yaml:"write_files"`
		Runcmd [][]string `yaml:"runcmd"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("the user-data is not valid YAML: %v", err)
	}

	// One account, no sudo, no password, and no default user next to it.
	if len(cfg.Users) != 1 || cfg.Users[0].Name != "cs" || cfg.Users[0].UID != 1000 || !cfg.Users[0].LockPasswd || cfg.Users[0].Sudo || cfg.Users[0].Shell != "/bin/bash" {
		t.Errorf("users %+v", cfg.Users)
	}
	for _, forbidden := range []string{"default", "debian", "ssh_authorized_keys", "ssh-rsa", "ssh-ed25519", "NOPASSWD"} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("user-data mentions %q", forbidden)
		}
	}
	// No password of any kind (the key lock_passwd is the opposite of one).
	if re := regexp.MustCompile(`(?m)^\s*(- )?(passwd|plain_text_passwd|hashed_passwd|chpasswd):`); re.Match(data) {
		t.Errorf("user-data sets a password: %s", re.Find(data))
	}
	if cfg.SSHPwauth || !cfg.DisableRoot || cfg.Hostname != "controlsnack" {
		t.Errorf("%+v", cfg)
	}

	// Every file arrives intact, owned by root, with the right mode.
	files := map[string]File{}
	for _, f := range Files() {
		files[f.Path] = f
	}
	if len(cfg.WriteFiles) != len(files) {
		t.Fatalf("%d files written, %d expected", len(cfg.WriteFiles), len(files))
	}
	for _, w := range cfg.WriteFiles {
		want, ok := files[w.Path]
		if !ok {
			t.Errorf("unexpected file %s", w.Path)
			continue
		}
		got, err := base64.StdEncoding.DecodeString(w.Content)
		if err != nil || !bytes.Equal(got, want.Content) || w.Encoding != "b64" || w.Owner != "root:root" || w.Permissions != want.Mode {
			t.Errorf("%s did not survive the cloud-config: %v %q %q %q", w.Path, err, w.Encoding, w.Owner, w.Permissions)
		}
	}
	// Provisioning is one script, run once.
	if len(cfg.Runcmd) != 1 || len(cfg.Runcmd[0]) != 1 || cfg.Runcmd[0][0] != ProvisionPath {
		t.Errorf("runcmd %v", cfg.Runcmd)
	}
	if _, ok := files[ProvisionPath]; !ok {
		t.Error("the script that runcmd runs is not written")
	}
}

func TestRecipeHash(t *testing.T) {
	a, b := RecipeHash(), RecipeHash()
	if a != b || len(a) != 64 {
		t.Fatalf("%q %q", a, b)
	}
	files, pk := Files(), append([]string(nil), Packages...)
	if hashRecipe(pk, files) != a {
		t.Error("the hash is a function of the packages and the files")
	}
	// Everything that goes into the image changes the hash.
	changed := append([]File(nil), files...)
	changed[0].Content = append(append([]byte(nil), changed[0].Content...), '#')
	if hashRecipe(pk, changed) == a {
		t.Error("file content")
	}
	changed = append([]File(nil), files...)
	changed[1].Mode = "0600"
	if hashRecipe(pk, changed) == a {
		t.Error("file mode")
	}
	changed = append([]File(nil), files...)
	changed[2].Path += "x"
	if hashRecipe(pk, changed) == a {
		t.Error("file path")
	}
	if hashRecipe(append(pk, "gimp"), files) == a {
		t.Error("package list")
	}
	// The order of the package list does not matter, the set does.
	rev := append([]string(nil), pk...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	if hashRecipe(rev, files) != a {
		t.Error("the same packages in another order are the same recipe")
	}
	// Fields cannot run into each other: moving a byte from one to the next must change the hash.
	x := []File{{Path: "/a", Mode: "0644", Content: []byte("bc")}}
	y := []File{{Path: "/ab", Mode: "0644", Content: []byte("c")}}
	if hashRecipe(nil, x) == hashRecipe(nil, y) {
		t.Error("ambiguous encoding")
	}
	if !strings.Contains(string(MetaData()), "instance-id: controlsnack-"+a[:12]) {
		t.Errorf("%s", MetaData())
	}
}

func TestNetworkConfigIsValidYAML(t *testing.T) {
	var cfg map[string]any
	if err := yaml.Unmarshal(NetworkConfig(), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["version"] != 2 || cfg["ethernets"] == nil {
		t.Errorf("%v", cfg)
	}
}

// TestTheRecipeFitsInASeedISO builds the real seed from the real recipe and reads it back.
func TestTheRecipeFitsInASeedISO(t *testing.T) {
	user, err := UserData()
	if err != nil {
		t.Fatal(err)
	}
	f := seed.Files{UserData: user, MetaData: MetaData(), NetworkConfig: NetworkConfig()}
	iso := filepath.Join(t.TempDir(), "seed.iso")
	if err := seed.Build(iso, f); err != nil {
		t.Fatal(err)
	}
	label, got, err := seed.Read(iso)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(label) != "cidata" || !bytes.Equal(got.UserData, user) || !bytes.Equal(got.MetaData, f.MetaData) || !bytes.Equal(got.NetworkConfig, f.NetworkConfig) {
		t.Error("the seed does not carry the recipe")
	}
	fi, _ := os.Stat(iso)
	t.Logf("user-data %d bytes, seed ISO %d bytes", len(user), fi.Size())
}
