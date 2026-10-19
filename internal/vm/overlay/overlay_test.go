// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package overlay

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/exitcode"
	"github.com/PacifAIst/controlsnack/internal/vm/image"
)

// writeQCOW2 writes the start of a qcow2 file whose header names a backing file (none when backing is empty).
func writeQCOW2(t *testing.T, path, backing string) {
	t.Helper()
	data := make([]byte, 512)
	copy(data, "QFI\xfb")
	binary.BigEndian.PutUint32(data[4:], 3)
	if backing != "" {
		binary.BigEndian.PutUint64(data[8:], 256)
		binary.BigEndian.PutUint32(data[16:], uint32(len(backing)))
		copy(data[256:], backing)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fakeOverlay(ctx context.Context, img, backing, dest, size string) error {
	if !filepath.IsAbs(backing) {
		return errors.New("backing path must be absolute")
	}
	data := make([]byte, 512)
	copy(data, "QFI\xfb")
	binary.BigEndian.PutUint64(data[8:], 256)
	binary.BigEndian.PutUint32(data[16:], uint32(len(backing)))
	copy(data[256:], backing)
	return os.WriteFile(dest, data, 0o600)
}

// baseImage is an image of the store: it has an id and a file.
func baseImage(t *testing.T, id string) image.Image {
	t.Helper()
	p := filepath.Join(t.TempDir(), "base-"+id+".qcow2")
	writeQCOW2(t, p, "")
	return image.Image{Manifest: image.Manifest{ID: id, Arch: "x86_64"}, Path: p}
}

func lib(t *testing.T) Library {
	t.Helper()
	return Library{Dir: filepath.Join(t.TempDir(), "vms"), Overlay: fakeOverlay}
}

// --- the qcow2 header ----------------------------------------------------------------------------------------------

func TestBackingFile(t *testing.T) {
	dir := t.TempDir()
	with := filepath.Join(dir, "with.qcow2")
	writeQCOW2(t, with, "/home/u/.cache/controlsnack/images/base-abc.qcow2")
	if got, err := BackingFile(with); err != nil || got != "/home/u/.cache/controlsnack/images/base-abc.qcow2" {
		t.Errorf("%q %v", got, err)
	}
	without := filepath.Join(dir, "without.qcow2")
	writeQCOW2(t, without, "")
	if got, err := BackingFile(without); err != nil || got != "" {
		t.Errorf("%q %v", got, err)
	}
	bad := map[string][]byte{
		"not qcow2": append([]byte("QCOW"), make([]byte, 100)...),
		"too short": []byte("QFI\xfb"),
		"empty":     nil,
		"name too long": func() []byte {
			b := make([]byte, 600)
			copy(b, "QFI\xfb")
			binary.BigEndian.PutUint64(b[8:], 100)
			binary.BigEndian.PutUint32(b[16:], 5000)
			return b
		}(),
		"name past end": func() []byte {
			b := make([]byte, 64)
			copy(b, "QFI\xfb")
			binary.BigEndian.PutUint64(b[8:], 60)
			binary.BigEndian.PutUint32(b[16:], 200)
			return b
		}(),
		"offset in 2^63": func() []byte {
			b := make([]byte, 64)
			copy(b, "QFI\xfb")
			binary.BigEndian.PutUint64(b[8:], 1<<63)
			binary.BigEndian.PutUint32(b[16:], 10)
			return b
		}(),
	}
	for name, data := range bad {
		p := filepath.Join(dir, strings.ReplaceAll(name, " ", "_"))
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := BackingFile(p); err == nil {
			t.Errorf("%s: accepted, backing %q", name, got)
		}
	}
	if _, err := BackingFile(filepath.Join(dir, "gone")); err == nil {
		t.Error("missing file")
	}
}

func TestCheckBacking(t *testing.T) {
	dir := t.TempDir()
	disk := filepath.Join(dir, "disk.qcow2")
	base := filepath.Join(dir, "images", "base-a.qcow2")
	writeQCOW2(t, disk, base)
	if err := CheckBacking(disk, base); err != nil {
		t.Error(err)
	}
	if err := CheckBacking(disk, filepath.Join(dir, "images", "..", "images", "base-a.qcow2")); err != nil {
		t.Errorf("paths are compared cleaned: %v", err)
	}
	if err := CheckBacking(disk, filepath.Join(dir, "images", "base-b.qcow2")); err == nil || !strings.Contains(err.Error(), "not by the base image") {
		t.Errorf("another base: %v", err)
	}
	writeQCOW2(t, disk, "")
	if err := CheckBacking(disk, base); err == nil || !strings.Contains(err.Error(), "no backing image") {
		t.Errorf("no backing: %v", err)
	}
	writeQCOW2(t, disk, "/etc/passwd") // an overlay turned toward another file
	if err := CheckBacking(disk, base); err == nil {
		t.Error("a tampered backing file must be caught")
	}
	if !sameFile(`C:\Users\u\Images\base.qcow2`, `c:/users/u/images/base.qcow2`) && filepath.Separator == '\\' {
		t.Error("drive paths compare case-insensitively on Windows")
	}
}

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"a", "lab", "my-vm-2", "0day", strings.Repeat("x", 40)} {
		if err := ValidateName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-lab", "Lab", "my_vm", "my vm", "a/b", `a\b`, "..", ".", "lab.", strings.Repeat("x", 41), "é", "con", "nul", "com1", "lpt9", "a:b"} {
		if err := ValidateName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// --- the library ---------------------------------------------------------------------------------------------------

func TestCreateOpenList(t *testing.T) {
	l := lib(t)
	base := baseImage(t, "aaaaaaaaaaaa")
	l.Now = func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }
	vm, err := l.Create(context.Background(), "/usr/bin/qemu-img", "lab", base, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if vm.Name != "lab" || vm.BaseID != "aaaaaaaaaaaa" || vm.Arch != "x86_64" || vm.Version != "v1" || !vm.Created.Equal(l.Now()) ||
		filepath.Base(vm.Disk) != "disk.qcow2" || filepath.Dir(vm.Disk) != vm.Dir || filepath.Base(vm.Vars) != "vars.fd" {
		t.Errorf("%+v", vm)
	}
	if got, _ := BackingFile(vm.Disk); !sameFile(got, base.Path) {
		t.Errorf("the overlay is backed by %q", got)
	}
	if fi, err := os.Stat(filepath.Join(vm.Dir, "vm.json")); err != nil || fi.Mode().Perm()&0o077 != 0 && filepath.Separator == '/' {
		t.Errorf("vm.json is private: %v %v", fi, err)
	}

	opened, err := l.Open("lab", base)
	if err != nil || opened.Name != "lab" || opened.Disk != vm.Disk {
		t.Fatalf("%+v %v", opened, err)
	}

	l.Now = func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }
	if _, err := l.Create(context.Background(), "x", "scratch", base, "v1"); err != nil {
		t.Fatal(err)
	}
	vms, problems, err := l.List()
	if err != nil || len(problems) != 0 || len(vms) != 2 || vms[0].Name != "scratch" || vms[1].Name != "lab" {
		t.Fatalf("most recent first: %v %v %v", vms, problems, err)
	}
	if err := l.Touch("lab"); err != nil {
		t.Fatal(err)
	}
	vms, _, _ = l.List()
	if vms[0].Name != "lab" || !vms[0].LastUsed.Equal(l.Now()) {
		t.Errorf("Touch makes it the most recent: %v", vms[0])
	}
}

func TestCreateRefusals(t *testing.T) {
	l := lib(t)
	base := baseImage(t, "aaaaaaaaaaaa")
	if _, err := l.Create(context.Background(), "x", "Bad Name", base, "v"); err == nil {
		t.Error("invalid name")
	}
	if _, err := l.Create(context.Background(), "x", "lab", image.Image{Path: base.Path, Unmanaged: true}, "v"); err == nil {
		t.Error("a base outside the store has no id to remember")
	}
	if _, err := l.Create(context.Background(), "x", "lab", base, "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Create(context.Background(), "x", "lab", base, "v"); !errors.Is(err, ErrExists) {
		t.Errorf("%v", err)
	}
	// A failed overlay leaves nothing behind.
	l.Overlay = func(context.Context, string, string, string, string) error {
		return errors.New("qemu-img: no space left")
	}
	if _, err := l.Create(context.Background(), "x", "other", base, "v"); err == nil || !strings.Contains(err.Error(), "no space left") {
		t.Errorf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(l.Dir, "other")); !os.IsNotExist(err) {
		t.Error("the directory of a failed VM is removed")
	}
}

func TestOpenChecksTheBase(t *testing.T) {
	l := lib(t)
	base := baseImage(t, "aaaaaaaaaaaa")
	if _, err := l.Create(context.Background(), "x", "lab", base, "v"); err != nil {
		t.Fatal(err)
	}
	// A different image: the message names both and says what to do.
	other := baseImage(t, "bbbbbbbbbbbb")
	_, err := l.Open("lab", other)
	var mismatch *BaseMismatchError
	if !errors.As(err, &mismatch) || mismatch.Have != "aaaaaaaaaaaa" || mismatch.Want != "bbbbbbbbbbbb" ||
		!strings.Contains(err.Error(), "--image aaaaaaaaaaaa") || !strings.Contains(err.Error(), "controlsnack vm rm lab") {
		t.Errorf("%v", err)
	}
	if _, err := l.Open("lab", image.Image{Path: base.Path, Unmanaged: true}); !errors.As(err, &mismatch) {
		t.Errorf("%v", err)
	}
	if _, err := l.Open("nope", base); !errors.Is(err, ErrNoVM) {
		t.Errorf("%v", err)
	}
	if _, err := l.Open("../etc", base); err == nil {
		t.Error("a name with a path is refused before anything is read")
	}
	// The same id but a disk that was pointed at another file.
	writeQCOW2(t, filepath.Join(l.Dir, "lab", "disk.qcow2"), "/etc/shadow")
	if _, err := l.Open("lab", base); err == nil || !strings.Contains(err.Error(), "not by the base image") {
		t.Errorf("%v", err)
	}
	// The same id and a base that moved: the image path no longer matches the overlay's.
	moved := base
	moved.Path = filepath.Join(t.TempDir(), "elsewhere.qcow2")
	writeQCOW2(t, filepath.Join(l.Dir, "lab", "disk.qcow2"), base.Path)
	if _, err := l.Open("lab", moved); err == nil {
		t.Error("the overlay still points at the old location")
	}
}

func TestAcquire(t *testing.T) {
	l := lib(t)
	base := baseImage(t, "aaaaaaaaaaaa")
	if _, err := l.Create(context.Background(), "x", "lab", base, "v"); err != nil {
		t.Fatal(err)
	}
	release, err := l.Acquire("lab")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire("lab"); err == nil || !strings.Contains(err.Error(), "in use by another controlsnack session") {
		t.Errorf("%v", err)
	}
	if _, err := l.Remove("lab"); err == nil {
		t.Error("a VM in use is not removed")
	}
	if _, err := os.Stat(filepath.Join(l.Dir, "lab", "disk.qcow2")); err != nil {
		t.Error("and its disk is untouched")
	}
	release()
	if rel, err := l.Acquire("lab"); err != nil {
		t.Errorf("after release: %v", err)
	} else {
		rel()
	}
	if _, err := l.Acquire("nope"); !errors.Is(err, ErrNoVM) {
		t.Errorf("%v", err)
	}
	if _, err := l.Acquire("Bad Name"); err == nil {
		t.Error("invalid name")
	}
}

func TestListReportsDamagedEntries(t *testing.T) {
	l := lib(t)
	base := baseImage(t, "aaaaaaaaaaaa")
	if _, err := l.Create(context.Background(), "x", "good", base, "v"); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"broken": "{nope", "liar": `{"name":"someone-else","base_image_id":"x"}`} {
		if err := os.MkdirAll(filepath.Join(l.Dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(l.Dir, name, "vm.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(l.Dir, "empty"), 0o700); err != nil { // a directory without vm.json
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.Dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	vms, problems, err := l.List()
	if err != nil || len(vms) != 1 || vms[0].Name != "good" || len(problems) != 3 {
		t.Fatalf("%v\n%v\n%v", vms, problems, err)
	}
	if empty, _, err := (Library{Dir: filepath.Join(t.TempDir(), "none")}).List(); err != nil || len(empty) != 0 {
		t.Errorf("a library that does not exist yet is empty: %v %v", empty, err)
	}
}

func TestRemove(t *testing.T) {
	l := lib(t)
	base := baseImage(t, "aaaaaaaaaaaa")
	if _, err := l.Create(context.Background(), "x", "lab", base, "v"); err != nil {
		t.Fatal(err)
	}
	vm, err := l.Remove("lab")
	if err != nil || vm.Name != "lab" {
		t.Fatal(err)
	}
	if _, err := os.Stat(vm.Dir); !os.IsNotExist(err) {
		t.Error("the directory is gone")
	}
	if entries, _ := os.ReadDir(l.Dir); len(entries) != 0 {
		t.Errorf("no lock file is left behind: %v", entries)
	}
	if _, err := l.Remove("lab"); !errors.Is(err, ErrNoVM) {
		t.Errorf("%v", err)
	}
	if _, err := l.Remove("../x"); err == nil {
		t.Error("a name with a path")
	}
}

func TestRemoveDoesNotFollowLinks(t *testing.T) {
	l := lib(t)
	base := baseImage(t, "aaaaaaaaaaaa")
	vm, err := l.Create(context.Background(), "x", "lab", base, "v")
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	precious := filepath.Join(outside, "precious.txt")
	if err := os.WriteFile(precious, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(vm.Dir, "link")); err != nil {
		t.Skipf("cannot create symbolic links here: %v", err)
	}
	if _, err := l.Remove("lab"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(precious); err != nil || string(data) != "keep" {
		t.Errorf("removing a VM must never reach through a link: %v", err)
	}
}

// --- the store behind `vm ls` and `vm rm` ---------------------------------------------------------------------------

func TestFileStore(t *testing.T) {
	imagesDir := t.TempDir()
	// A real image of the store so the VM's base exists.
	st := image.Store{Dir: imagesDir}
	built := filepath.Join(t.TempDir(), "built.qcow2")
	writeQCOW2(t, built, "")
	im, err := st.Commit(context.Background(), built, image.Manifest{
		Created: time.Now(), Version: "v", RecipeSHA256: strings.Repeat("c", 64),
		Upstream: image.Upstream{URL: "https://example.org/x.qcow2", SHA512: strings.Repeat("d", 128)}, Arch: "x86_64"})
	if err != nil {
		t.Fatal(err)
	}

	vmsDir := filepath.Join(t.TempDir(), "vms")
	store := &FileStore{Dir: func() (string, error) { return vmsDir, nil }, Images: func() (image.Store, error) { return st, nil }}
	var out bytes.Buffer
	if err := store.List(context.Background(), &out); err != nil || !strings.Contains(out.String(), "No kept VMs. Start one with: controlsnack run --keep-vm=NAME") {
		t.Fatalf("%v %q", err, out.String())
	}

	l := Library{Dir: vmsDir, Overlay: fakeOverlay}
	if _, err := l.Create(context.Background(), "x", "lab", im, "v"); err != nil {
		t.Fatal(err)
	}
	orphan := baseImage(t, "ffffffffffff") // its base image is not in the store
	if _, err := l.Create(context.Background(), "x", "orphan", orphan, "v"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := store.List(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"NAME", "IMAGE", "LAST USED", "lab", im.Manifest.ID, "orphan", "ffffffffffff", "base image ffffffffffff is gone: this VM cannot start", "512 B"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if !strings.Contains(text, "ok") {
		t.Errorf("the healthy VM says ok:\n%s", text)
	}

	out.Reset()
	if err := store.Remove(context.Background(), &out, "orphan"); err != nil || !strings.Contains(out.String(), "removed kept VM orphan") {
		t.Errorf("%v %q", err, out.String())
	}
	err = store.Remove(context.Background(), &out, "orphan")
	if exitcode.Of(err) != exitcode.Failure || !errors.Is(err, ErrNoVM) {
		t.Errorf("%v (%d)", err, exitcode.Of(err))
	}
	if d, err := DefaultDir(); err != nil || !strings.HasSuffix(filepath.ToSlash(d), "/vms") {
		t.Errorf("%q %v", d, err)
	}
}

// --- against the real qemu-img ---------------------------------------------------------------------------------------

// TestBackingFileOfARealOverlay checks the header parser against what qemu-img writes.
func TestBackingFileOfARealOverlay(t *testing.T) {
	img, err := exec.LookPath("qemu-img")
	if err != nil {
		for _, p := range []string{`C:\Program Files\qemu\qemu-img.exe`, "/usr/bin/qemu-img", "/usr/local/bin/qemu-img", "/opt/homebrew/bin/qemu-img"} {
			if _, serr := os.Stat(p); serr == nil {
				img, err = p, nil
				break
			}
		}
	}
	if err != nil {
		t.Skip("no qemu-img here")
	}
	dir := t.TempDir()
	base := filepath.Join(dir, "base.qcow2")
	if out, err := exec.CommandContext(context.Background(), img, "create", "-q", "-f", "qcow2", base, "1M").CombinedOutput(); err != nil { //nolint:gosec // a tool found on this machine
		t.Fatalf("%v: %s", err, out)
	}
	disk := filepath.Join(dir, "disk.qcow2")
	if err := image.CreateOverlay(context.Background(), img, base, disk, ""); err != nil {
		t.Fatal(err)
	}
	got, err := BackingFile(disk)
	if err != nil || !sameFile(got, base) {
		t.Errorf("qemu-img wrote backing file %q, want %q (%v)", got, base, err)
	}
	if err := CheckBacking(disk, base); err != nil {
		t.Error(err)
	}
	if err := CheckBacking(disk, filepath.Join(dir, "other.qcow2")); err == nil {
		t.Error("another base")
	}
	if got, err := BackingFile(base); err != nil || got != "" {
		t.Errorf("a plain image has no backing file: %q %v", got, err)
	}
}
