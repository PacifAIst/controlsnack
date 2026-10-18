// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
)

func readTranscript(t *testing.T, dir string) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, transcriptName))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("a line of the transcript is not JSON: %q", sc.Text())
		}
		out = append(out, line)
	}
	return out
}

func TestTheKeeperWritesWhatThePersonSawAndLeavesOutWhatIsStillRunning(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kept", "s_1")
	k, err := NewKeeper(dir)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 18, 13, 0, 0, 0, time.FixedZone("CET", 3600)) // the transcript is in UTC whatever the clock says
	k.now = func() time.Time { at = at.Add(time.Second); return at }
	k.Send(viewer.Chat{Role: "user", Text: "open an editor"})
	k.Send(viewer.Chat{Role: "assistant", Text: "I will"})
	k.Send(viewer.Trace{Turn: 1, Tool: "exec", ActionID: "t1a1", Status: "running", Summary: "starting"})
	k.Send(viewer.Trace{Turn: 1, Tool: "exec", ActionID: "t1a1", Status: "done", Summary: "exit=0"})
	k.Send(viewer.Notice{Level: viewer.LevelWarn, Text: "careful"})
	k.Send(viewer.Ended{Reason: "x"}) // not part of the conversation
	if err := k.Close(); err != nil {
		t.Fatal(err)
	}
	got := readTranscript(t, dir)
	if len(got) != 4 {
		t.Fatalf("%d lines: %v", len(got), got)
	}
	want := []map[string]any{
		{"kind": "chat", "role": "user", "text": "open an editor", "time": "2026-10-18T12:00:01Z"},
		{"kind": "chat", "role": "assistant", "text": "I will", "time": "2026-10-18T12:00:02Z"},
		{"kind": "action", "turn": float64(1), "tool": "exec", "id": "t1a1", "status": "done", "summary": "exit=0", "time": "2026-10-18T12:00:03Z"},
		{"kind": "notice", "level": viewer.LevelWarn, "text": "careful", "time": "2026-10-18T12:00:04Z"},
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Errorf("line %d: %v, want %v", i, got[i], want[i])
			continue
		}
		for k, v := range want[i] {
			if got[i][k] != v {
				t.Errorf("line %d, %s: %v, want %v", i, k, got[i][k], v)
			}
		}
	}
}

func TestTheKeeperSavesEachPictureUnderItsTurnWithTheRightExtension(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kept")
	k, err := NewKeeper(dir)
	if err != nil {
		t.Fatal(err)
	}
	k.Shot(1, backend.Image{MIME: backend.MIMEPNG, Data: []byte("one")})
	k.Shot(12, backend.Image{MIME: "image/jpeg", Data: []byte("twelve")})
	k.Shot(3, backend.Image{MIME: "image/webp", Data: []byte("three")})
	k.Shot(4, backend.Image{MIME: "image/x-strange", Data: []byte("four")})
	if err := k.Close(); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"turn-0001.png": "one", "turn-0012.jpg": "twelve", "turn-0003.webp": "three", "turn-0004.img": "four"} {
		data, err := os.ReadFile(filepath.Join(dir, shotsName, name))
		if err != nil || string(data) != content {
			t.Errorf("%s: %q %v", name, data, err)
		}
	}
	if k.Shots() != 4 || k.Dir() != dir {
		t.Errorf("%d shots in %s", k.Shots(), k.Dir())
	}
}

func TestWhatIsKeptIsForTheOwnerAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no permission bits to read")
	}
	dir := filepath.Join(t.TempDir(), "kept")
	k, err := NewKeeper(dir)
	if err != nil {
		t.Fatal(err)
	}
	k.Shot(1, backend.Image{MIME: backend.MIMEPNG, Data: []byte("x")})
	k.Send(viewer.Chat{Role: "user", Text: "x"})
	_ = k.Close()
	for _, p := range []string{dir, filepath.Join(dir, shotsName), filepath.Join(dir, transcriptName), filepath.Join(dir, shotsName, "turn-0001.png")} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s: %v %v", p, info, err)
		}
	}
}

func TestAKeeperThatCannotWriteSaysSoAtTheEndAndNeverStopsTheSession(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kept")
	k, err := NewKeeper(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, shotsName)); err != nil {
		t.Fatal(err)
	}
	k.Shot(1, backend.Image{MIME: backend.MIMEPNG, Data: []byte("x")}) // the directory is gone
	k.Send(viewer.Chat{Role: "user", Text: "still works"})
	if err := k.Close(); err == nil {
		t.Error("Close does not report that a picture could not be saved")
	}
	if got := readTranscript(t, dir); len(got) != 1 {
		t.Errorf("the transcript goes on: %v", got)
	}
	k.Send(viewer.Chat{Role: "user", Text: "after the end"}) // a late message is dropped, not a crash
	if err := k.Close(); err == nil {
		t.Error("Close again forgets the error")
	}
}

func TestTheKeeperReportsTheFirstThingThatWentWrongAndAClosedOneIsQuiet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kept")
	k, err := NewKeeper(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, shotsName)); err != nil {
		t.Fatal(err)
	}
	k.Shot(1, backend.Image{MIME: backend.MIMEPNG, Data: []byte("x")})
	k.Shot(2, backend.Image{MIME: backend.MIMEPNG, Data: []byte("y")})
	if err := k.Close(); err == nil || !strings.Contains(err.Error(), "turn-0001") || strings.Contains(err.Error(), "turn-0002") {
		t.Errorf("the first failure is the one reported: %v", err)
	}
	// A keeper that worked says nothing, however often it is closed, and drops what comes after the end.
	h, err := NewKeeper(filepath.Join(t.TempDir(), "kept"))
	if err != nil {
		t.Fatal(err)
	}
	h.Send(viewer.Chat{Role: "user", Text: "hello"})
	if err := h.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	h.Send(viewer.Chat{Role: "user", Text: "too late"})
	if err := h.Close(); err != nil {
		t.Errorf("a second Close after a late message: %v", err)
	}
	if got := readTranscript(t, h.Dir()); len(got) != 1 {
		t.Errorf("the transcript: %v", got)
	}
}

func TestNewKeeperRefusesADirectoryThatHasATranscriptAlready(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kept")
	first, err := NewKeeper(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := NewKeeper(dir); !errors.Is(err, os.ErrExist) {
		t.Errorf("a second keeper in the same place: %v", err)
	}
}

func TestTheLoopOffersTheKeeperEachNewPictureTheModelIsShown(t *testing.T) {
	g := newLrig(t)
	g.look.reused = map[int]bool{2: true}
	type shot struct {
		turn int
		data string
	}
	var kept []shot
	g.loop.Kept = func(turn int, img backend.Image) { kept = append(kept, shot{turn, string(img.Data)}) }
	g.model.replies = []reply{calls(keyA("c1")), calls(keyA("c2")), calls(doneOK)}
	if out := g.run("go"); out.End != EndDone {
		t.Fatalf("outcome %+v", out)
	}
	if len(kept) != 2 || kept[0] != (shot{1, "png-1"}) || kept[1] != (shot{3, "png-3"}) {
		t.Errorf("kept %v: the second turn showed the same picture again", kept)
	}
	// Without a picture (a model that cannot see) there is nothing to keep.
	h := newLrig(t)
	h.look.vision = false
	n := 0
	h.loop.Kept = func(int, backend.Image) { n++ }
	h.model.replies = []reply{calls(doneOK)}
	_ = h.run("go")
	if n != 0 {
		t.Errorf("%d pictures kept for a model that sees none", n)
	}
}

func TestKeepWithAModelSavesTheTranscriptAndThePicturesAndNothingElse(t *testing.T) {
	f := newFullRig(t)
	f.model.replies = []reply{
		{text: "I will look at the kernel.", calls: []parse.ToolCall{tc("c1", "exec", `{"command":"uname -r","timeout_s":30}`)}},
		calls(tc("c2", "done", `{"success":true,"summary":"the kernel is 6.1.0-cs"}`)),
	}
	cfg := headlessConfig("Print the kernel version")
	cfg.Session.Keep = true
	if err := f.run(context.Background(), cfg); err != nil {
		t.Fatalf("the session: %v\n%s", err, f.out.String())
	}
	kept, _ := filepath.Glob(filepath.Join(f.dirs.State, "kept", "s_*"))
	if len(kept) != 1 {
		t.Fatalf("kept: %v", kept)
	}
	lines := readTranscript(t, kept[0])
	var kinds []string
	for _, l := range lines {
		kinds = append(kinds, l["kind"].(string))
	}
	text := ""
	for _, l := range lines {
		text += fmt.Sprint(l["text"], l["summary"]) + "|"
	}
	if !strings.Contains(strings.Join(kinds, ","), "chat") || !strings.Contains(strings.Join(kinds, ","), "action") || !strings.Contains(text, "I will look at the kernel.") {
		t.Errorf("transcript kinds %v, texts %q", kinds, text)
	}
	shots, _ := filepath.Glob(filepath.Join(kept[0], shotsName, "turn-*"))
	if len(shots) == 0 {
		t.Errorf("no picture was kept")
	}
	if !strings.Contains(f.out.String(), "Kept in "+kept[0]) {
		t.Errorf("the person is not told where:\n%s", f.out.String())
	}
	if left, _ := filepath.Glob(filepath.Join(f.dirs.State, "run", "*")); len(left) != 0 {
		t.Errorf("the session's own files stay: %v", left)
	}
}

func TestWhenWhatWasToBeKeptCannotBeSavedThePersonIsToldAtTheEndAndTheTaskGoesOn(t *testing.T) {
	f := newFullRig(t)
	f.model.replies = []reply{
		{calls: []parse.ToolCall{tc("c1", "exec", `{"command":"uname -r","timeout_s":30}`)}, hook: func(context.Context) {
			shots, _ := filepath.Glob(filepath.Join(f.dirs.State, "kept", "s_*", shotsName))
			for _, d := range shots {
				_ = os.RemoveAll(d) // the next picture has nowhere to go
			}
		}},
		calls(tc("c2", "done", `{"success":true,"summary":"done"}`)),
	}
	cfg := headlessConfig("Print the kernel version")
	cfg.Session.Keep = true
	if err := f.run(context.Background(), cfg); err != nil {
		t.Fatalf("keeping is never a reason to fail a task: %v\n%s", err, f.out.String())
	}
	if out := f.out.String(); !strings.Contains(out, "Not everything that --keep was to keep could be saved") || !strings.Contains(out, "Kept in ") {
		t.Errorf("the person is not told:\n%s", out)
	}
}
