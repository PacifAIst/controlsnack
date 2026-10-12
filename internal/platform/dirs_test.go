// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func fakeEnv(goos string, vars map[string]string) Environment {
	return Environment{
		GOOS:          goos,
		Getenv:        func(k string) string { return vars[k] },
		UserHomeDir:   func() (string, error) { return filepath.FromSlash("/home/u"), nil },
		UserConfigDir: func() (string, error) { return filepath.FromSlash("/cfg"), nil },
		UserCacheDir:  func() (string, error) { return filepath.FromSlash("/cache"), nil },
	}
}

func TestStateDirPerOS(t *testing.T) {
	tests := []struct {
		name string
		env  Environment
		want string
	}{
		{"linux default", fakeEnv("linux", nil), filepath.FromSlash("/home/u/.local/state/controlsnack")},
		{"linux with XDG_STATE_HOME", fakeEnv("linux", map[string]string{"XDG_STATE_HOME": "/var/state"}), filepath.FromSlash("/var/state/controlsnack")},
		{"linux ignores a relative XDG_STATE_HOME", fakeEnv("linux", map[string]string{"XDG_STATE_HOME": "relative/dir"}), filepath.FromSlash("/home/u/.local/state/controlsnack")},
		{"macOS", fakeEnv("darwin", nil), filepath.FromSlash("/home/u/Library/Application Support/ControlSnack")},
		{"windows with LocalAppData", fakeEnv("windows", map[string]string{"LocalAppData": `C:\Users\u\AppData\Local`}), filepath.Join(`C:\Users\u\AppData\Local`, "ControlSnack")},
		{"windows without LocalAppData", fakeEnv("windows", nil), filepath.FromSlash("/home/u/AppData/Local/ControlSnack")},
		{"unknown unix", fakeEnv("freebsd", nil), filepath.FromSlash("/home/u/.local/state/controlsnack")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := ResolveDirs(tt.env)
			if err != nil {
				t.Fatal(err)
			}
			if d.State != tt.want {
				t.Errorf("State = %q, want %q", d.State, tt.want)
			}
			if d.Config != filepath.FromSlash("/cfg/controlsnack") || d.Cache != filepath.FromSlash("/cache/controlsnack") {
				t.Errorf("config/cache: %+v", d)
			}
		})
	}
}

func TestDerivedDirs(t *testing.T) {
	d := Dirs{Config: "/c", Cache: "/k", State: "/s"}
	for got, want := range map[string]string{
		d.AuditDir(): "/s/audit", d.SessionsDir(): "/s/sessions", d.VMsDir(): "/s/vms", d.ImagesDir(): "/k/images",
	} {
		if got != filepath.FromSlash(want) {
			t.Errorf("got %q want %q", got, want)
		}
	}
}

func TestResolveDirsErrors(t *testing.T) {
	e := fakeEnv("linux", nil)
	e.UserConfigDir = func() (string, error) { return "", errors.New("no config") }
	if _, err := ResolveDirs(e); err == nil {
		t.Error("config dir failure must surface")
	}
	e = fakeEnv("linux", nil)
	e.UserCacheDir = func() (string, error) { return "", errors.New("no cache") }
	if _, err := ResolveDirs(e); err == nil {
		t.Error("cache dir failure must surface")
	}
	e = fakeEnv("linux", nil)
	e.UserHomeDir = func() (string, error) { return "", errors.New("no home") }
	if _, err := ResolveDirs(e); err == nil {
		t.Error("home dir failure must surface when it is needed")
	}
	e = fakeEnv("linux", map[string]string{"XDG_STATE_HOME": "/x"})
	e.UserHomeDir = func() (string, error) { return "", errors.New("no home") }
	if _, err := ResolveDirs(e); err != nil {
		t.Errorf("an absolute XDG_STATE_HOME needs no home: %v", err)
	}
}

func TestDefaultDirsOnThisMachine(t *testing.T) {
	d, err := DefaultDirs()
	if err != nil {
		t.Skipf("no usable home on this machine: %v", err)
	}
	if !filepath.IsAbs(d.State) || !filepath.IsAbs(d.Config) || !filepath.IsAbs(d.Cache) {
		t.Errorf("directories must be absolute: %+v", d)
	}
}

func TestEnsureDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "a", "b", "c")
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("idempotent: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatal("not created")
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(file); err == nil {
		t.Error("a file where a directory is needed must be an error")
	}
}
