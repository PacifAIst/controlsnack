// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package detect

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// cachedSetup is a fake Ollama server, a detector with a cache and a clock the test moves, and the target.
type cachedSetup struct {
	srv *fakeServer
	d   *Detector
	t   Target
	dir string
	now time.Time
}

func newCachedSetup(t *testing.T, m fakeModel) *cachedSetup {
	t.Helper()
	s := &cachedSetup{srv: newFake(t, "ollama", m), dir: t.TempDir(), now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	s.d = New(Options{CacheDir: s.dir, Now: func() time.Time { return s.now }, Getenv: func(string) string { return "" }})
	tg, err := s.d.ResolveBackend(context.Background(), cfgOf("ollama", s.srv.URL, m.Name))
	if err != nil {
		t.Fatal(err)
	}
	s.t = tg
	return s
}

func (s *cachedSetup) probe(t *testing.T, force bool) Capabilities {
	t.Helper()
	c, err := s.d.Probe(context.Background(), s.t, force)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (s *cachedSetup) files(t *testing.T) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(s.dir, cacheSubdir, "*"))
	return m
}

func TestAProbeIsCachedAndUsedAgain(t *testing.T) {
	s := newCachedSetup(t, strongModel("m"))
	first := s.probe(t, false)
	asked := s.srv.chatCount()
	if first.Cached || asked == 0 {
		t.Fatalf("first = %+v after %d questions", first, asked)
	}
	s.now = s.now.Add(48 * time.Hour)
	second := s.probe(t, false)
	if !second.Cached || s.srv.chatCount() != asked {
		t.Fatalf("second = %+v after %d questions (was %d)", second, s.srv.chatCount(), asked)
	}
	second.Cached = false
	if second.Vision != first.Vision || second.Model != first.Model || !second.ProbedAt.Equal(first.ProbedAt) || len(second.Checks) != len(first.Checks) {
		t.Fatalf("cached %+v differs from %+v", second, first)
	}
	if len(s.files(t)) != 1 {
		t.Fatalf("files = %v", s.files(t))
	}
}

func TestForceProbesAgain(t *testing.T) {
	s := newCachedSetup(t, strongModel("m"))
	s.probe(t, false)
	asked := s.srv.chatCount()
	if c := s.probe(t, true); c.Cached || s.srv.chatCount() != 2*asked {
		t.Fatalf("cached = %v questions = %d", c.Cached, s.srv.chatCount())
	}
}

func TestTheCacheExpires(t *testing.T) {
	s := newCachedSetup(t, strongModel("m"))
	s.probe(t, false)
	asked := s.srv.chatCount()
	s.now = s.now.Add(cacheTTL - time.Hour)
	if !s.probe(t, false).Cached {
		t.Fatal("a probe just inside the limit is still good")
	}
	s.now = s.now.Add(2 * time.Hour)
	if c := s.probe(t, false); c.Cached || s.srv.chatCount() == asked {
		t.Fatalf("an old probe was used: %+v", c)
	}
}

func TestANewBuildOfTheModelIsProbedAgain(t *testing.T) {
	m := strongModel("m")
	s := newCachedSetup(t, m)
	s.probe(t, false)
	asked := s.srv.chatCount()
	m.Digest = "sha256:bbbb"
	s.srv.setModels(m)
	c := s.probe(t, false)
	if c.Cached || c.Digest != "sha256:bbbb" || s.srv.chatCount() == asked {
		t.Fatalf("caps = %+v", c)
	}
	if len(s.files(t)) != 2 {
		t.Fatalf("each build has its own file: %v", s.files(t))
	}
}

func TestOtherModelsAndServersDoNotShareResults(t *testing.T) {
	s := newCachedSetup(t, strongModel("m"))
	a := s.d.cachePath(s.t, "d")
	other := s.t
	other.Model = "n"
	b := s.d.cachePath(other, "d")
	other = s.t
	other.BaseURL = "http://127.0.0.1:1"
	c := s.d.cachePath(other, "d")
	other = s.t
	other.Backend = "lmstudio"
	e := s.d.cachePath(other, "d")
	if a == b || a == c || a == e || b == c {
		t.Fatalf("paths are not distinct: %s %s %s %s", a, b, c, e)
	}
	// Moving a boundary between neighboring parts does not make two keys one.
	x, y := s.t, s.t
	x.Backend, y.Backend = "ab", "a"
	x.BaseURL, y.BaseURL = "c", "bc"
	if s.d.cachePath(x, "") == s.d.cachePath(y, "") {
		t.Fatal("backend and address run together")
	}
	x, y = s.t, s.t
	x.BaseURL, y.BaseURL = "u/m", "u"
	x.Model, y.Model = "n", "/mn"
	if s.d.cachePath(x, "") == s.d.cachePath(y, "") {
		t.Fatal("address and model run together")
	}
	x, y = s.t, s.t
	x.Model, y.Model = "m", "md"
	if s.d.cachePath(x, "d") == s.d.cachePath(y, "") {
		t.Fatal("model and digest run together")
	}
}

func TestACacheThatCannotBeTrustedIsIgnored(t *testing.T) {
	// Each case is a real result with one thing wrong, so that only that thing can be what rejects it.
	for name, spoil := range map[string]func(c Capabilities, now time.Time) string{
		"garbage":       func(Capabilities, time.Time) string { return "this is not json" },
		"empty":         func(Capabilities, time.Time) string { return "" },
		"other version": func(c Capabilities, _ time.Time) string { c.Version = ProbeVersion + 1; return marshal(t, c) },
		"old version":   func(c Capabilities, _ time.Time) string { c.Version = 0; return marshal(t, c) },
		"another model": func(c Capabilities, _ time.Time) string { c.Model = "zzz"; return marshal(t, c) },
		"another build": func(c Capabilities, _ time.Time) string { c.Digest = "sha256:other"; return marshal(t, c) },
		"another kind":  func(c Capabilities, _ time.Time) string { c.Backend = "lmstudio"; return marshal(t, c) },
		"another server": func(c Capabilities, _ time.Time) string {
			c.BaseURL = "http://127.0.0.1:1"
			return marshal(t, c)
		},
		"from the future": func(c Capabilities, now time.Time) string {
			c.ProbedAt = now.Add(30 * 24 * time.Hour)
			return marshal(t, c)
		},
		"too big": func(c Capabilities, _ time.Time) string {
			whole := marshal(t, c)
			return strings.TrimSuffix(whole, "}") + `,"Padding":"` + strings.Repeat("x", maxCacheFile) + `"}`
		},
		"too big, a good result then blanks": func(c Capabilities, _ time.Time) string {
			// Cut at the limit this is still a whole result: only the size says no.
			return marshal(t, c) + strings.Repeat(" ", maxCacheFile)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newCachedSetup(t, strongModel("m"))
			first := s.probe(t, false)
			asked := s.srv.chatCount()
			path := s.files(t)[0]
			// The result as it was is believed: the case is only what is spoiled.
			if good := s.probe(t, false); !good.Cached {
				t.Fatal("the unspoiled result is not used")
			}
			if err := os.WriteFile(path, []byte(spoil(first, s.now)), 0o600); err != nil {
				t.Fatal(err)
			}
			if c := s.probe(t, false); c.Cached || s.srv.chatCount() == asked {
				t.Fatalf("a bad cache was believed: %+v", c)
			}
		})
	}
}

func TestAModelThatCanDoNothingIsNotCached(t *testing.T) {
	m := strongModel("m")
	m.Native, m.Structured, m.JSONPrompt = false, false, false
	s := newCachedSetup(t, m)
	if c := s.probe(t, false); c.Usable() {
		t.Fatalf("caps = %+v", c)
	}
	if len(s.files(t)) != 0 {
		t.Fatalf("files = %v", s.files(t))
	}
}

func TestTheCacheFileIsPrivateAndHoldsNoKey(t *testing.T) {
	srv := newFake(t, "openai", strongModel("m"))
	srv.apiKey = "sk-very-secret-key"
	dir := t.TempDir()
	d := New(Options{CacheDir: dir, Getenv: func(k string) string {
		if k == "OPENAI_API_KEY" {
			return "sk-very-secret-key"
		}
		return ""
	}})
	tg, err := d.ResolveBackend(context.Background(), cfgOf("openai", srv.URL, "m"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Probe(context.Background(), tg, true); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, cacheSubdir, "*"))
	if len(files) != 1 {
		t.Fatalf("files = %v", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "Bearer") {
		t.Fatalf("the cache holds the key:\n%s", data)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(files[0]); info.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %v", info.Mode().Perm())
		}
		if info, _ := os.Stat(filepath.Dir(files[0])); info.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %v", info.Mode().Perm())
		}
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, cacheSubdir, ".probe-*")); len(leftovers) != 0 {
		t.Fatalf("temporary files left: %v", leftovers)
	}
}

func TestWithoutACacheDirectoryNothingIsWritten(t *testing.T) {
	srv := newFake(t, "ollama", strongModel("m"))
	d := New(Options{})
	tg, _ := d.ResolveBackend(context.Background(), cfgOf("ollama", srv.URL, "m"))
	if d.cachePath(tg, "") != "" {
		t.Fatal("a path without a directory")
	}
	if _, err := d.Probe(context.Background(), tg, false); err != nil {
		t.Fatal(err)
	}
	asked := srv.chatCount()
	if c, _ := d.Probe(context.Background(), tg, false); c.Cached || srv.chatCount() == asked {
		t.Fatal("cached without a directory")
	}
}

func TestACacheDirectoryThatCannotBeUsedDoesNotFailTheProbe(t *testing.T) {
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := newFake(t, "ollama", strongModel("m"))
	d := New(Options{CacheDir: file}) // a file where the directory should be
	tg, _ := d.ResolveBackend(context.Background(), cfgOf("ollama", srv.URL, "m"))
	c, err := d.Probe(context.Background(), tg, false)
	if err != nil || !c.Usable() {
		t.Fatalf("caps = %+v err = %v", c, err)
	}
}

func marshal(t *testing.T, c Capabilities) string {
	t.Helper()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
