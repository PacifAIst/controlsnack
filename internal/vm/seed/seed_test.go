// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package seed

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func sample() Files {
	return Files{
		UserData:      []byte("#cloud-config\nhostname: controlsnack\nwrite_files:\n  - path: /etc/motd\n    content: \"héllo ✓\"\n"),
		MetaData:      []byte("instance-id: controlsnack-abc123\nlocal-hostname: controlsnack\n"),
		NetworkConfig: []byte("version: 2\nethernets:\n  nics:\n    match:\n      name: \"e*\"\n    dhcp4: true\n"),
	}
}

func TestBuildAndReadBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed.iso")
	f := sample()
	if err := Build(path, f); err != nil {
		t.Fatal(err)
	}
	label, got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(label) != Label {
		t.Errorf("label %q", label)
	}
	if !bytes.Equal(got.UserData, f.UserData) || !bytes.Equal(got.MetaData, f.MetaData) || !bytes.Equal(got.NetworkConfig, f.NetworkConfig) {
		t.Errorf("contents differ:\n%q\n%q\n%q", got.UserData, got.MetaData, got.NetworkConfig)
	}
	if fi, _ := os.Stat(path); fi.Size()%blockSize != 0 || fi.Size() < 64<<10 {
		t.Errorf("size %d", fi.Size())
	}
}

func TestBuildWithoutNetworkConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed.iso")
	f := sample()
	f.NetworkConfig = nil
	if err := Build(path, f); err != nil {
		t.Fatal(err)
	}
	_, got, err := Read(path)
	if err != nil || len(got.NetworkConfig) != 0 || !bytes.Equal(got.UserData, f.UserData) {
		t.Errorf("%v %q", err, got.NetworkConfig)
	}
}

func TestBuildLargeUserData(t *testing.T) {
	// A recipe with many base64 files is a few hundred KiB.
	f := sample()
	f.UserData = append([]byte("#cloud-config\n# "), bytes.Repeat([]byte("0123456789abcdef"), 40_000)...) // 640 KB
	path := filepath.Join(t.TempDir(), "seed.iso")
	if err := Build(path, f); err != nil {
		t.Fatal(err)
	}
	_, got, err := Read(path)
	if err != nil || !bytes.Equal(got.UserData, f.UserData) {
		t.Errorf("a large user-data does not survive: %v", err)
	}
}

func TestBuildRefusesBadInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seed.iso")
	for name, mut := range map[string]func(*Files){
		"empty user-data": func(f *Files) { f.UserData = nil },
		"empty meta-data": func(f *Files) { f.MetaData = nil },
		"NUL byte":        func(f *Files) { f.UserData = []byte("#cloud-config\n\x00") },
		"invalid UTF-8":   func(f *Files) { f.MetaData = []byte{0xff, 0xfe} },
		"oversize":        func(f *Files) { f.UserData = bytes.Repeat([]byte("a"), maxFile+1) },
		"bad network":     func(f *Files) { f.NetworkConfig = []byte("a\x00b") },
	} {
		f := sample()
		mut(&f)
		if err := Build(path, f); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a refused build leaves nothing behind: %v", entries)
	}
	if err := Build(filepath.Join(dir, "missing", "seed.iso"), sample()); err == nil {
		t.Error("a directory that does not exist")
	}
}

func TestBuildReplacesAndLeavesNoTemporaries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seed.iso")
	if err := Build(path, sample()); err != nil {
		t.Fatal(err)
	}
	second := sample()
	second.MetaData = []byte("instance-id: other\n")
	if err := Build(path, second); err != nil {
		t.Fatal(err)
	}
	_, got, _ := Read(path)
	if string(got.MetaData) != "instance-id: other\n" {
		t.Errorf("%q", got.MetaData)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("only the ISO is left: %v", entries)
	}
}

func TestReadRefusesWhatIsNotAnISO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.iso")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 100_000), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Read(path); err == nil {
		t.Error("not an ISO")
	}
	if _, _, err := Read(filepath.Join(t.TempDir(), "gone.iso")); err == nil {
		t.Error("missing")
	}
}

// --- an independent reader ---------------------------------------------------------------------------------------

// rawISO is a small reader written from ECMA-119 and the Joliet specification, with no help from the library that
// wrote the file: it finds the volume labels of the primary and Joliet descriptors and walks the Joliet root
// directory, where names are UCS-2 and keep their case and hyphens.
type rawISO struct{ data []byte }

func (r rawISO) sector(n int) []byte { return r.data[n*2048 : (n+1)*2048] }

// descriptors returns the volume descriptors by type: 1 primary, 2 supplementary (Joliet).
func (r rawISO) descriptors(t *testing.T) map[byte][]byte {
	t.Helper()
	out := map[byte][]byte{}
	for n := 16; n < 32; n++ {
		s := r.sector(n)
		if string(s[1:6]) != "CD001" {
			t.Fatalf("sector %d has no CD001 signature", n)
		}
		if s[0] == 255 { // terminator
			return out
		}
		out[s[0]] = s
	}
	t.Fatal("no volume descriptor set terminator")
	return nil
}

func volumeID(desc []byte, ucs2 bool) string {
	id := desc[40:72]
	if ucs2 {
		u := make([]uint16, 0, 16)
		for i := 0; i+1 < len(id); i += 2 {
			u = append(u, binary.BigEndian.Uint16(id[i:]))
		}
		return strings.TrimRight(string(utf16.Decode(u)), " \x00")
	}
	return strings.TrimRight(string(id), " ")
}

// rootFiles lists the files in the root directory of the tree whose descriptor is desc: name -> content.
func (r rawISO) rootFiles(t *testing.T, desc []byte, ucs2 bool) map[string][]byte {
	t.Helper()
	root := desc[156 : 156+34]
	extent, length := int(binary.LittleEndian.Uint32(root[2:])), int(binary.LittleEndian.Uint32(root[10:]))
	dir := r.data[extent*2048 : extent*2048+length]
	files := map[string][]byte{}
	for off := 0; off < len(dir); {
		recLen := int(dir[off])
		if recLen == 0 { // padding to the end of the sector
			off = (off/2048 + 1) * 2048
			continue
		}
		rec := dir[off : off+recLen]
		off += recLen
		flags, nameLen := rec[25], int(rec[32])
		raw := rec[33 : 33+nameLen]
		if flags&2 != 0 || (nameLen == 1 && (raw[0] == 0 || raw[0] == 1)) { // a directory, ".", ".."
			continue
		}
		name := string(raw)
		if ucs2 {
			u := make([]uint16, 0, nameLen/2)
			for i := 0; i+1 < nameLen; i += 2 {
				u = append(u, binary.BigEndian.Uint16(raw[i:]))
			}
			name = string(utf16.Decode(u))
		}
		if i := strings.IndexByte(name, ';'); i >= 0 { // the ISO 9660 version suffix
			name = name[:i]
		}
		fe, fl := int(binary.LittleEndian.Uint32(rec[2:])), int(binary.LittleEndian.Uint32(rec[10:]))
		files[name] = append([]byte(nil), r.data[fe*2048:fe*2048+fl]...)
	}
	return files
}

func TestISOStructureWithAnIndependentReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed.iso")
	f := sample()
	if err := Build(path, f); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r := rawISO{data}
	d := r.descriptors(t)
	if d[1] == nil || d[2] == nil {
		t.Fatalf("a primary and a Joliet descriptor are needed (Joliet keeps the long lower-case names): %v", len(d))
	}
	if volumeID(d[1], false) != "cidata" || volumeID(d[2], true) != "cidata" {
		t.Errorf("volume ids %q %q", volumeID(d[1], false), volumeID(d[2], true))
	}
	if !bytes.Contains(d[2][88:91], []byte("%/")) {
		t.Errorf("the Joliet escape sequence: %q", d[2][88:91])
	}
	joliet := r.rootFiles(t, d[2], true)
	for name, want := range map[string][]byte{"user-data": f.UserData, "meta-data": f.MetaData, "network-config": f.NetworkConfig} {
		if !bytes.Equal(joliet[name], want) {
			t.Errorf("Joliet tree: %s = %q, want %q", name, joliet[name], want)
		}
	}
	if len(joliet) != 3 {
		t.Errorf("nothing else is on the disk: %v", len(joliet))
	}
	// The primary tree has the mangled 8.3 names, and Rock Ridge records carry the real ones.
	primary := r.rootFiles(t, d[1], false)
	if len(primary) != 3 {
		t.Errorf("primary tree: %d files", len(primary))
	}
	if !bytes.Contains(data, []byte("user-data")) || !bytes.Contains(data, []byte("network-config")) {
		t.Error("Rock Ridge NM records with the real names are missing")
	}
}

// TestBlkidSeesTheLabel asks the tool the guest uses to find the seed which label it sees. It runs where blkid
// exists (Linux).
func TestBlkidSeesTheLabel(t *testing.T) {
	blkid, err := exec.LookPath("blkid")
	if err != nil {
		for _, p := range []string{"/usr/sbin/blkid", "/sbin/blkid"} {
			if _, serr := os.Stat(p); serr == nil {
				blkid, err = p, nil
				break
			}
		}
	}
	if err != nil {
		t.Skip("no blkid here")
	}
	path := filepath.Join(t.TempDir(), "seed.iso")
	if err := Build(path, sample()); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(context.Background(), blkid, "-p", path).CombinedOutput() //nolint:gosec // a tool found on this machine
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	t.Logf("%s", out)
	if !strings.Contains(string(out), `LABEL="cidata"`) || !strings.Contains(string(out), `TYPE="iso9660"`) {
		t.Errorf("%s", out)
	}
}
