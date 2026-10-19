// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseProgress(t *testing.T) {
	for line, want := range map[string]Progress{
		"CS-PROVISION: installing packages (31 requested)": {Text: "installing packages (31 requested)"},

		"CS-PROVISION: done": {Text: "done", Done: true},

		"CS-PROVISION: done\r": {Text: "done", Done: true},

		"CS-PROVISION: FAILED at line 42: apt-get update": {Text: "FAILED at line 42: apt-get update", Failed: true},

		"[   42.1] cloud-init[512]: CS-PROVISION: configuring the desktop": {Text: "configuring the desktop"},

		"\x1b[0;32mCS-PROVISION: cleaning up\x1b[0m": {Text: "cleaning up"},

		"CS-PROVISION:    start (x86_64, Debian GNU/Linux 13 (trixie))  ": {Text: "start (x86_64, Debian GNU/Linux 13 (trixie))"},

		"CS-PROVISION: doneness is not done": {Text: "doneness is not done"},

		"CS-PROVISION: cloud-init finished": {Text: "cloud-init finished"},
	} {
		got, ok := ParseProgress(line)
		if !ok || got != want {
			t.Errorf("%q: %+v %v, want %+v", line, got, ok, want)
		}
	}
	for _, line := range []string{"", "random kernel noise", "CS-PROVISION:", "CS-PROVISION: ", "cs-provision: done", "CS-PROVISIONdone"} {
		if p, ok := ParseProgress(line); ok {
			t.Errorf("%q parsed as %+v", line, p)
		}
	}
}

func TestLineTail(t *testing.T) {
	tail := NewLineTail(3)
	for _, l := range []string{"a", "b"} {
		tail.Add(l)
	}
	if !slices.Equal(tail.Lines(), []string{"a", "b"}) {
		t.Errorf("%v", tail.Lines())
	}
	for _, l := range []string{"c", "d", "e"} {
		tail.Add(l)
	}
	if !slices.Equal(tail.Lines(), []string{"c", "d", "e"}) {
		t.Errorf("%v", tail.Lines())
	}
	got := tail.Lines()
	got[0] = "mutated"
	if tail.Lines()[0] != "c" {
		t.Error("Lines returns a copy")
	}
}

type collector struct {
	mu    sync.Mutex
	lines []string
}

func (c *collector) add(s string) { c.mu.Lock(); c.lines = append(c.lines, s); c.mu.Unlock() }
func (c *collector) get() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestFollowSeesLinesAsTheyAreWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serial.log")
	var c collector
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Follow(ctx, path, 5*time.Millisecond, c.add) }()

	time.Sleep(30 * time.Millisecond) // the file does not exist yet: that is fine
	appendTo(t, path, "first\r\nsecond\nthi")
	waitFor(t, "two complete lines", func() bool { return len(c.get()) == 2 })
	if got := c.get(); got[0] != "first" || got[1] != "second" {
		t.Errorf("%q", got)
	}
	appendTo(t, path, "rd\n\nfourth\n") // completes the partial line, then an empty line
	waitFor(t, "five lines", func() bool { return len(c.get()) == 5 })
	if got := c.get(); !slices.Equal(got, []string{"first", "second", "third", "", "fourth"}) {
		t.Errorf("%q", got)
	}
	appendTo(t, path, "last line without a newline")
	time.Sleep(30 * time.Millisecond)
	if len(c.get()) != 5 {
		t.Error("an unterminated line is held back")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
}

func TestFollowKeepsEverythingWrittenBeforeTheEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serial.log")
	appendTo(t, path, "one\ntwo\n")
	var c collector
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	go func() {
		time.Sleep(10 * time.Millisecond)
		appendTo(t, path, "three\nfour\n") // arrives while Follow waits
	}()
	_ = Follow(ctx, path, 500*time.Millisecond, c.add) // the poll is longer than the run: only the final look sees the end
	if got := c.get(); !slices.Equal(got, []string{"one", "two", "three", "four"}) {
		t.Errorf("%q", got)
	}
}

func TestFollowBoundsLongAndBinaryLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serial.log")
	appendTo(t, path, strings.Repeat("x", 3*maxLine)+"\nafter\n\xff\xfebinary\n")
	var c collector
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = Follow(ctx, path, 5*time.Millisecond, c.add)
	got := c.get()
	if len(got) < 3 {
		t.Fatalf("%d lines", len(got))
	}
	for _, l := range got {
		if len(l) > maxLine {
			t.Errorf("a line of %d bytes", len(l))
		}
		if strings.ToValidUTF8(l, "?") != l {
			t.Errorf("invalid UTF-8 in %q", l)
		}
	}
	if got[len(got)-1] != "??binary" && !strings.HasSuffix(got[len(got)-1], "binary") {
		t.Errorf("%q", got[len(got)-1])
	}
}
