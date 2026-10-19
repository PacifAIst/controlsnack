// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/config"
	"github.com/PacifAIst/controlsnack/internal/exitcode"
)

func managerIn(dir string) *FileManager {
	return &FileManager{Dir: func() (string, error) { return dir, nil }, Arch: ArchX86_64}
}

func TestManagerListShowsImagesAndProblems(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "images")
	m := managerIn(dir)
	var out bytes.Buffer
	if err := m.List(context.Background(), &out); err != nil || !strings.Contains(out.String(), "No base images. Build one with: controlsnack image build") {
		t.Fatalf("%v %q", err, out.String())
	}

	s := Store{Dir: dir}
	a := commit(t, s, ArchX86_64, 1, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	b := commit(t, s, ArchAarch64, 2, time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC))
	if err := os.WriteFile(filepath.Join(dir, "base-aaaaaaaaaaaa.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := m.List(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"ID", "ARCH", "CREATED", "SIZE", "UPSTREAM", "RECIPE", a.Manifest.ID, b.Manifest.ID, "x86_64", "aarch64",
		"4.0 KiB", "debian-13-generic-amd64.qcow2 (sha512 abababab…)", "cccccccccccc", "problem: base-aaaaaaaaaaaa.json"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if strings.Index(text, b.Manifest.ID) > strings.Index(text, a.Manifest.ID) {
		t.Error("newest first")
	}
}

func TestManagerRemoveVerifyPath(t *testing.T) {
	dir := t.TempDir()
	m := managerIn(dir)
	s := Store{Dir: dir}
	ctx := context.Background()
	var out bytes.Buffer

	// With no image at all the commands say so, with exit code 4.
	for name, err := range map[string]error{
		"path":   m.Path(ctx, &out, ""),
		"verify": m.Verify(ctx, &out, ""),
		"rm":     m.Remove(ctx, &out, "abcdef123456"),
	} {
		if exitcode.Of(err) != exitcode.ImageMissing {
			t.Errorf("%s: %v (%d)", name, err, exitcode.Of(err))
		}
	}

	good := commit(t, s, ArchX86_64, 1, time.Now())
	out.Reset()
	if err := m.Path(ctx, &out, ""); err != nil || strings.TrimSpace(out.String()) != good.Path || strings.Count(out.String(), "\n") != 1 {
		t.Errorf("path prints the path and nothing else: %q %v", out.String(), err)
	}
	out.Reset()
	if err := m.Path(ctx, &out, good.Manifest.ID[:6]); err != nil || strings.TrimSpace(out.String()) != good.Path {
		t.Errorf("%q %v", out.String(), err)
	}
	out.Reset()
	if err := m.Verify(ctx, &out, ""); err != nil || !strings.Contains(out.String(), "ok      "+good.Manifest.ID) {
		t.Errorf("%q %v", out.String(), err)
	}

	// A damaged image fails verification and the message says what to do.
	bad := commit(t, s, ArchX86_64, 2, time.Now().Add(time.Hour))
	_ = os.Chmod(bad.Path, 0o600)
	data, _ := os.ReadFile(bad.Path)
	data[1000] ^= 1
	if err := os.WriteFile(bad.Path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	err := m.Verify(ctx, &out, "")
	if err == nil || !strings.Contains(err.Error(), "1 of 2 images") || !strings.Contains(out.String(), "FAILED  "+bad.Manifest.ID) || !strings.Contains(out.String(), "ok      "+good.Manifest.ID) {
		t.Errorf("%v\n%s", err, out.String())
	}
	out.Reset()
	if err := m.Verify(ctx, &out, good.Manifest.ID); err != nil {
		t.Errorf("verifying one image only: %v", err)
	}

	out.Reset()
	if err := m.Remove(ctx, &out, bad.Manifest.ID); err != nil || !strings.Contains(out.String(), "removed image "+bad.Manifest.ID) {
		t.Errorf("%v %q", err, out.String())
	}
	if err := m.Path(ctx, &out, bad.Manifest.ID); exitcode.Of(err) != exitcode.ImageMissing {
		t.Errorf("%v", err)
	}
	// "Newest" follows the host architecture.
	arm := &FileManager{Dir: m.Dir, Arch: ArchAarch64}
	if err := arm.Path(ctx, &out, ""); exitcode.Of(err) != exitcode.ImageMissing {
		t.Errorf("no aarch64 image: %v", err)
	}
}

func TestManagerImport(t *testing.T) {
	src := t.TempDir()
	disk := filepath.Join(src, "d.qcow2")
	sum := fakeQCOW2(t, disk, 4, 5000)
	mf := validManifest()
	mf.SHA256, mf.ID, mf.Size = sum, IDOf(sum), 5000
	data, _ := mf.Encode()
	manifest := filepath.Join(src, "d.json")
	if err := os.WriteFile(manifest, data, 0o644); err != nil {
		t.Fatal(err)
	}
	m := managerIn(filepath.Join(t.TempDir(), "images"))
	var out bytes.Buffer
	if err := m.Import(context.Background(), &out, disk, manifest); err != nil || !strings.Contains(out.String(), "imported image "+mf.ID) {
		t.Fatalf("%v %q", err, out.String())
	}
	if err := m.Import(context.Background(), &out, disk, manifest); err == nil {
		t.Error("importing twice")
	}
	other := filepath.Join(src, "o.qcow2")
	fakeQCOW2(t, other, 9, 5000)
	m2 := managerIn(filepath.Join(t.TempDir(), "images"))
	err := m2.Import(context.Background(), &out, other, manifest)
	if exitcode.Of(err) != exitcode.Failure || !errors.Is(err, ErrBadImage) {
		t.Errorf("a mismatch between file and manifest: %v", err)
	}
}

func TestBuildNeedsItsWiring(t *testing.T) {
	// The manager alone cannot resolve the accelerator or boot-check an image: the vm package supplies both.
	err := managerIn(t.TempDir()).Build(context.Background(), &bytes.Buffer{}, config.Defaults(), BuildOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("%v", err)
	}
}

func TestHumanSizeAndArch(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 341508096: "325.7 MiB", 3 << 30: "3.0 GiB"} {
		if got := HumanSize(n); got != want {
			t.Errorf("HumanSize(%d) = %q, want %q", n, got, want)
		}
	}
	if ArchFor("amd64") != ArchX86_64 || ArchFor("arm64") != ArchAarch64 || ArchFor("riscv64") != "riscv64" || hostArch() == "" {
		t.Error("arch names")
	}
	if d, err := DefaultDir(); err != nil || !strings.HasSuffix(filepath.ToSlash(d), "controlsnack/images") {
		t.Errorf("%q %v", d, err)
	}
}
