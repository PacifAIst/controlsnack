// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var sha512x = strings.Repeat("ab", 64)

// fakeQCOW2 writes a file that starts like a qcow2 image and returns its SHA-256. seed makes different files.
func fakeQCOW2(t *testing.T, path string, seed byte, size int) string {
	t.Helper()
	data := make([]byte, size)
	copy(data, qcow2Magic)
	for i := len(qcow2Magic); i < size; i++ {
		data[i] = byte(i*7) + seed
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// baseManifest is a valid manifest for a built image; Commit fills in the id, size and checksum.
func baseManifest(arch string, created time.Time) Manifest {
	return Manifest{
		Created: created, Version: "v0.1.0", RecipeSHA256: strings.Repeat("c", 64),
		Upstream: Upstream{URL: "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2", SHA512: sha512x,
			SignatureNote: "Debian publishes no signature for SHA512SUMS"},
		Arch: arch, Packages: []string{"xfce4", "chromium", "libwebp7", "at-spi2-core"},
	}
}

// commit builds and seals a fake image into s.
func commit(t *testing.T, s Store, arch string, seed byte, created time.Time) Image {
	t.Helper()
	built := filepath.Join(t.TempDir(), "built.qcow2")
	fakeQCOW2(t, built, seed, 4096)
	im, err := s.Commit(context.Background(), built, baseManifest(arch, created))
	if err != nil {
		t.Fatal(err)
	}
	return im
}

// --- manifest --------------------------------------------------------------------------------------------------

func validManifest() Manifest {
	m := baseManifest(ArchX86_64, time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC))
	m.SHA256 = strings.Repeat("1f", 32)
	m.ID, m.Size = IDOf(m.SHA256), 12345
	return m
}

func TestManifestValidation(t *testing.T) {
	if err := validManifest().Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*Manifest){
		"short sha256":      func(m *Manifest) { m.SHA256 = "abc" },
		"upper-case sha256": func(m *Manifest) { m.SHA256 = strings.ToUpper(m.SHA256) },
		"id mismatch":       func(m *Manifest) { m.ID = "000000000000" },
		"id with a path":    func(m *Manifest) { m.ID = "../../etc/pa" },
		"unknown arch":      func(m *Manifest) { m.Arch = "riscv64" },
		"zero size":         func(m *Manifest) { m.Size = 0 },
		"huge size":         func(m *Manifest) { m.Size = 1 << 50 },
		"no date":           func(m *Manifest) { m.Created = time.Time{} },
		"short recipe":      func(m *Manifest) { m.RecipeSHA256 = "x" },
		"short sha512":      func(m *Manifest) { m.Upstream.SHA512 = "ab" },
		"http upstream":     func(m *Manifest) { m.Upstream.URL = "http://cloud.debian.org/x.qcow2" },
		"file upstream":     func(m *Manifest) { m.Upstream.URL = "file:///etc/passwd" },
		"no upstream host":  func(m *Manifest) { m.Upstream.URL = "https:///x" },
		"no version":        func(m *Manifest) { m.Version = "" },
		"bad package":       func(m *Manifest) { m.Packages = []string{"xfce4", "x; rm -rf /"} },
		"long note":         func(m *Manifest) { m.Upstream.SignatureNote = strings.Repeat("x", 600) },
		"too many packages": func(m *Manifest) { m.Packages = make([]string, 1001) },
	}
	for name, mut := range bad {
		m := validManifest()
		mut(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Several problems are all reported.
	m := validManifest()
	m.Arch, m.Size = "x", 0
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "arch") || !strings.Contains(err.Error(), "size") {
		t.Errorf("%v", err)
	}
}

func TestManifestRoundTripAndStrictParsing(t *testing.T) {
	m := validManifest()
	data, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseManifest(data)
	if err != nil || back.ID != m.ID || !back.Created.Equal(m.Created) || back.Upstream != m.Upstream || len(back.Packages) != 4 {
		t.Fatalf("%+v %v", back, err)
	}
	if !strings.Contains(string(data), `"signature_verified": false`) || !strings.Contains(string(data), `"controlsnack_version"`) {
		t.Errorf("the field names of PLAN.md section 7.5:\n%s", data)
	}
	for name, doc := range map[string]string{
		"unknown field": strings.Replace(string(data), `"arch"`, `"extra": 1, "arch"`, 1),
		"trailing data": string(data) + `{"id":"x"}`,
		"not json":      "nope",
		"array":         "[]",
		"empty":         "",
	} {
		if _, err := ParseManifest([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseManifest(bytes.Repeat([]byte(" "), maxManifestBytes+1)); err == nil {
		t.Error("an enormous manifest")
	}
	bad := m
	bad.Arch = "mips"
	if _, err := bad.Encode(); err == nil {
		t.Error("an invalid manifest is not written")
	}
}

// --- the store -------------------------------------------------------------------------------------------------

func TestCommitListFindNewest(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "images")}
	if images, problems, err := s.List(); err != nil || len(images) != 0 || len(problems) != 0 {
		t.Fatalf("a store that does not exist yet is empty: %v %v %v", images, problems, err)
	}
	if _, err := s.Newest(ArchX86_64); !errors.Is(err, ErrNoImage) || !strings.Contains(err.Error(), "controlsnack image build") {
		t.Errorf("%v", err)
	}

	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	older := commit(t, s, ArchX86_64, 1, t0)
	newer := commit(t, s, ArchX86_64, 2, t0.Add(48*time.Hour))
	arm := commit(t, s, ArchAarch64, 3, t0.Add(72*time.Hour))

	if older.Manifest.ID == newer.Manifest.ID || len(older.Manifest.ID) != 12 || older.Manifest.Size != 4096 {
		t.Errorf("ids come from the contents: %+v %+v", older.Manifest, newer.Manifest)
	}
	if fi, err := os.Stat(older.Path); err != nil || fi.Mode().Perm()&0o222 != 0 {
		t.Errorf("a sealed image is read-only: %v %v", fi.Mode(), err)
	}
	if filepath.Base(older.Path) != "base-"+older.Manifest.ID+".qcow2" || filepath.Base(older.ManifestPath) != "base-"+older.Manifest.ID+".json" {
		t.Errorf("%s %s", older.Path, older.ManifestPath)
	}

	images, problems, err := s.List()
	if err != nil || len(problems) != 0 || len(images) != 3 || images[0].Manifest.ID != arm.Manifest.ID || images[2].Manifest.ID != older.Manifest.ID {
		t.Fatalf("newest first: %v %v %v", ids(images), problems, err)
	}
	if im, err := s.Newest(ArchX86_64); err != nil || im.Manifest.ID != newer.Manifest.ID {
		t.Errorf("the newest x86_64 image, not the newest image: %v %v", im.Manifest.ID, err)
	}
	if im, err := s.Newest(ArchAarch64); err != nil || im.Manifest.ID != arm.Manifest.ID {
		t.Errorf("%v %v", im.Manifest.ID, err)
	}

	if im, err := s.Find(older.Manifest.ID); err != nil || im.Path != older.Path {
		t.Errorf("%v", err)
	}
	if im, err := s.Find(strings.ToUpper(older.Manifest.ID[:6])); err != nil || im.Manifest.ID != older.Manifest.ID {
		t.Errorf("prefixes work, in any case: %v", err)
	}
	for _, bad := range []string{"", "abc", "xyzxyz", "../etc", strings.Repeat("a", 13), "base-" + older.Manifest.ID} {
		if _, err := s.Find(bad); err == nil || errors.Is(err, ErrNoImage) {
			t.Errorf("%q must be refused as an id: %v", bad, err)
		}
	}
	if _, err := s.Find("ffffffffffff"); !errors.Is(err, ErrNoImage) {
		t.Errorf("%v", err)
	}
}

func ids(images []Image) []string {
	var out []string
	for _, im := range images {
		out = append(out, im.Manifest.ID)
	}
	return out
}

func TestFindAmbiguousPrefix(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	a := commit(t, s, ArchX86_64, 1, time.Now())
	// Forge a second image whose id shares the first four digits.
	b := validManifest()
	b.SHA256 = a.Manifest.SHA256[:4] + strings.Repeat("0", 60)
	b.ID = IDOf(b.SHA256)
	data, _ := b.Encode()
	if err := os.WriteFile(filepath.Join(s.Dir, ManifestName(b.ID)), data, 0o644); err != nil {
		t.Fatal(err)
	}
	fakeQCOW2(t, filepath.Join(s.Dir, DiskName(b.ID)), 9, 100)
	if _, err := s.Find(a.Manifest.ID[:4]); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("%v", err)
	}
	if _, err := s.Find(a.Manifest.ID); err != nil {
		t.Errorf("a full id is never ambiguous: %v", err)
	}
}

func TestListReportsBrokenEntries(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	good := commit(t, s, ArchX86_64, 1, time.Now())
	if err := os.WriteFile(filepath.Join(s.Dir, "base-aaaaaaaaaaaa.json"), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	gone := commit(t, s, ArchX86_64, 2, time.Now().Add(time.Hour))
	_ = os.Chmod(gone.Path, 0o600)
	if err := os.Remove(gone.Path); err != nil {
		t.Fatal(err)
	}
	wrong := validManifest()
	data, _ := wrong.Encode()
	if err := os.WriteFile(filepath.Join(s.Dir, "base-bbbbbbbbbbbb.json"), data, 0o644); err != nil { // the file name says another id
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	images, problems, err := s.List()
	if err != nil || len(images) != 1 || images[0].Manifest.ID != good.Manifest.ID {
		t.Fatalf("%v %v", ids(images), err)
	}
	joined := strings.Join(problems, "\n")
	for _, want := range []string{"base-aaaaaaaaaaaa.json", "the image file is missing", "the manifest is for image"} {
		if !strings.Contains(joined, want) {
			t.Errorf("problems lack %q:\n%s", want, joined)
		}
	}
}

func TestResolve(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "images")}
	im := commit(t, s, ArchX86_64, 1, time.Now())

	for _, spec := range []string{"", im.Manifest.ID, im.Manifest.ID[:5]} {
		got, err := s.Resolve(spec, ArchX86_64)
		if err != nil || got.Manifest.ID != im.Manifest.ID || got.Unmanaged {
			t.Errorf("%q: %v %v", spec, got.Manifest.ID, err)
		}
	}
	if _, err := s.Resolve(im.Manifest.ID, ArchAarch64); err == nil || !strings.Contains(err.Error(), "runs aarch64 guests") {
		t.Errorf("an image of the other architecture is refused: %v", err)
	}
	if _, err := s.Resolve("", ArchAarch64); !errors.Is(err, ErrNoImage) {
		t.Errorf("%v", err)
	}

	// An image given by path is used as it is.
	loose := filepath.Join(t.TempDir(), "mine.qcow2")
	fakeQCOW2(t, loose, 5, 2048)
	got, err := s.Resolve(loose, ArchX86_64)
	if err != nil || !got.Unmanaged || got.Path != loose {
		t.Fatalf("%+v %v", got, err)
	}
	if err := s.QuickCheck(got); err != nil {
		t.Errorf("%v", err)
	}
	if _, err := s.Verify(context.Background(), got); err == nil {
		t.Error("an unmanaged image has nothing to verify against")
	}
	// ...and with a sidecar manifest it is a managed one.
	side := validManifest()
	side.SHA256 = fakeQCOW2(t, loose, 5, 2048)
	side.ID, side.Size = IDOf(side.SHA256), 2048
	data, _ := side.Encode()
	if err := os.WriteFile(strings.TrimSuffix(loose, ".qcow2")+".json", data, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Resolve(loose, ArchX86_64); err != nil || got.Unmanaged || got.Manifest.ID != side.ID {
		t.Errorf("%+v %v", got, err)
	}
	// Not a qcow2 file, not a file, not there.
	notQ := filepath.Join(t.TempDir(), "x.qcow2")
	if err := os.WriteFile(notQ, []byte("this is not a disk image"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{notQ, t.TempDir() + string(filepath.Separator) + "gone.qcow2", t.TempDir() + string(filepath.Separator) + "."} {
		if _, err := s.Resolve(bad, ArchX86_64); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestQuickCheckAndVerify(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	im := commit(t, s, ArchX86_64, 1, time.Now())
	if err := s.QuickCheck(im); err != nil {
		t.Fatal(err)
	}
	res, err := s.Verify(context.Background(), im)
	if err != nil || !res.OK || len(res.Problems) != 0 {
		t.Fatalf("%+v %v", res, err)
	}

	// Flip one byte without changing the size: the quick check cannot see it, verify can.
	_ = os.Chmod(im.Path, 0o600)
	data, _ := os.ReadFile(im.Path)
	data[2000] ^= 0xff
	if err := os.WriteFile(im.Path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.QuickCheck(im); err != nil {
		t.Errorf("the quick check looks at the size only: %v", err)
	}
	res, err = s.Verify(context.Background(), im)
	if err != nil || res.OK || len(res.Problems) != 1 || !strings.Contains(res.Problems[0], "sha256 is") {
		t.Errorf("%+v %v", res, err)
	}

	// A different size is caught by both.
	if err := os.WriteFile(im.Path, data[:3000], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.QuickCheck(im); !errors.Is(err, ErrBadImage) {
		t.Errorf("%v", err)
	}
	if res, _ := s.Verify(context.Background(), im); res.OK || !strings.Contains(strings.Join(res.Problems, ";"), "size is 3000") {
		t.Errorf("%+v", res)
	}
	// Missing.
	_ = os.Remove(im.Path)
	if err := s.QuickCheck(im); !errors.Is(err, ErrNoImage) {
		t.Errorf("%v", err)
	}
	if res, err := s.Verify(context.Background(), im); err != nil || res.OK {
		t.Errorf("%+v %v", res, err)
	}
	// Verification stops when asked to.
	im2 := commit(t, s, ArchX86_64, 2, time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Verify(ctx, im2); !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
}

func TestRemove(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	im := commit(t, s, ArchX86_64, 1, time.Now())
	other := commit(t, s, ArchX86_64, 2, time.Now().Add(time.Hour))
	got, err := s.Remove(im.Manifest.ID[:6])
	if err != nil || got.Manifest.ID != im.Manifest.ID {
		t.Fatal(err)
	}
	for _, p := range []string{im.Path, im.ManifestPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s must be gone (%v)", p, err)
		}
	}
	if _, err := os.Stat(other.Path); err != nil {
		t.Errorf("the other image stays: %v", err)
	}
	if _, err := s.Remove(im.Manifest.ID); !errors.Is(err, ErrNoImage) {
		t.Errorf("%v", err)
	}
}

func TestImport(t *testing.T) {
	src := t.TempDir()
	disk := filepath.Join(src, "shared.qcow2")
	sum := fakeQCOW2(t, disk, 7, 8192)
	m := validManifest()
	m.SHA256, m.ID, m.Size = sum, IDOf(sum), 8192
	data, _ := m.Encode()
	manifest := filepath.Join(src, "shared.json")
	if err := os.WriteFile(manifest, data, 0o644); err != nil {
		t.Fatal(err)
	}

	s := Store{Dir: filepath.Join(t.TempDir(), "images")}
	im, err := s.Import(context.Background(), disk, manifest)
	if err != nil || im.Manifest.ID != m.ID {
		t.Fatalf("%v", err)
	}
	if fi, _ := os.Stat(im.Path); fi.Mode().Perm()&0o222 != 0 || fi.Size() != 8192 {
		t.Errorf("imported images are sealed too: %v", fi.Mode())
	}
	if res, err := s.Verify(context.Background(), im); err != nil || !res.OK {
		t.Errorf("%+v %v", res, err)
	}
	if entries, _ := os.ReadDir(s.Dir); len(entries) != 2 {
		t.Errorf("no temporary files are left behind: %v", entries)
	}
	if _, err := s.Import(context.Background(), disk, manifest); err == nil || !strings.Contains(err.Error(), "already in the store") {
		t.Errorf("%v", err)
	}

	// Things that must be refused, each into a fresh store.
	other := func() Store { return Store{Dir: filepath.Join(t.TempDir(), "images")} }
	flip := filepath.Join(src, "flipped.qcow2")
	fakeQCOW2(t, flip, 8, 8192) // same size, other contents: the checksum differs
	if _, err := other().Import(context.Background(), flip, manifest); !errors.Is(err, ErrBadImage) {
		t.Errorf("wrong checksum: %v", err)
	}
	short := filepath.Join(src, "short.qcow2")
	fakeQCOW2(t, short, 7, 4096)
	if _, err := other().Import(context.Background(), short, manifest); !errors.Is(err, ErrBadImage) {
		t.Errorf("wrong size: %v", err)
	}
	notQ := filepath.Join(src, "text.qcow2")
	if err := os.WriteFile(notQ, bytes.Repeat([]byte("x"), 8192), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := other().Import(context.Background(), notQ, manifest); err == nil {
		t.Error("not a qcow2 image")
	}
	badManifest := filepath.Join(src, "bad.json")
	if err := os.WriteFile(badManifest, []byte(`{"id":"../../x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := other().Import(context.Background(), disk, badManifest); err == nil {
		t.Error("invalid manifest")
	}
	if _, err := other().Import(context.Background(), filepath.Join(src, "gone.qcow2"), manifest); err == nil {
		t.Error("missing file")
	}
	if _, err := other().Import(context.Background(), disk, filepath.Join(src, "gone.json")); err == nil {
		t.Error("missing manifest")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st := other()
	if _, err := st.Import(ctx, disk, manifest); !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
	if entries, _ := os.ReadDir(st.Dir); len(entries) != 0 {
		t.Errorf("a canceled import leaves nothing: %v", entries)
	}
}

func TestCommitRefusesWhatIsNotAnImage(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	junk := filepath.Join(t.TempDir(), "junk.qcow2")
	if err := os.WriteFile(junk, []byte("not a disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(context.Background(), junk, baseManifest(ArchX86_64, time.Now())); err == nil {
		t.Error("junk")
	}
	good := filepath.Join(t.TempDir(), "good.qcow2")
	fakeQCOW2(t, good, 1, 4096)
	m := baseManifest("sparc", time.Now())
	if _, err := s.Commit(context.Background(), good, m); err == nil {
		t.Error("invalid manifest")
	}
	if entries, _ := os.ReadDir(s.Dir); len(entries) != 0 {
		t.Errorf("%v", entries)
	}
}
