// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
)

func TestTheKeeperWritesWhatTheModelWasAskedWithoutTheSystemPrompt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kept")
	k, err := NewKeeper(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	k.Request(2, backend.Request{Messages: []backend.Message{
		{Role: backend.RoleSystem, Text: "THE LONG SYSTEM PROMPT"},
		{Role: backend.RoleUser, Text: "TURN 1\nthe screen", Images: []backend.Image{{MIME: backend.MIMEPNG, Data: []byte("x")}}},
		{Role: backend.RoleAssistant, Text: "I will click", Calls: []parse.ToolCall{tc("c1", "click_element", `{"id":"a7"}`)}},
		{Role: backend.RoleTool, Tool: "click_element", Text: "failed: no such element"},
		{Role: backend.RoleUser, Text: "TURN 2\nthe new screen"},
	}})
	data, err := os.ReadFile(filepath.Join(dir, requestsName, "turn-0002.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"## user [1 picture(s)]\nTURN 1\nthe screen\n", "## assistant\ncall click_element {\"id\":\"a7\"}\nI will click\n", "## tool (click_element)\nfailed: no such element\n", "## user\nTURN 2\nthe new screen\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("the saved request lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "SYSTEM PROMPT") {
		t.Error("the system prompt is saved with every turn")
	}
	if strings.Index(got, "TURN 1") > strings.Index(got, "TURN 2") {
		t.Error("the messages are not in order")
	}
}

func TestTheLoopGivesTheKeeperWhatTheModelIsAskedOnTheFirstAttemptOfEachTurn(t *testing.T) {
	g := newLrig(t)
	var turns []int
	var sawObservation bool
	g.loop.KeptRequest = func(turn int, req backend.Request) {
		turns = append(turns, turn)
		last := req.Messages[len(req.Messages)-1]
		sawObservation = sawObservation || strings.Contains(last.Text, "find the kernel")
	}
	// the first answer cannot be used, so the turn asks again; the second turn is the ordinary one
	g.model.replies = []reply{{text: "words only"}, calls(keyA("c1")), calls(doneOK)}
	if out := g.run("find the kernel"); out.End != EndDone {
		t.Fatalf("outcome %+v", out)
	}
	if len(turns) != 2 || turns[0] != 1 || turns[1] != 2 {
		t.Errorf("kept for turns %v: once for each turn, not for the repeated question of a repair", turns)
	}
	if !sawObservation {
		t.Error("the request kept is not the one with the observation last")
	}
}

func TestKeepWithAModelSavesWhatTheModelWasAskedEachTurn(t *testing.T) {
	f := newFullRig(t)
	f.model.replies = []reply{calls(tc("c1", "exec", `{"command":"uname -r","timeout_s":30}`)), calls(tc("c2", "done", `{"success":true,"summary":"ok"}`))}
	cfg := headlessConfig("Print the kernel version")
	cfg.Session.Keep = true
	if err := f.run(context.Background(), cfg); err != nil {
		t.Fatalf("%v\n%s", err, f.out.String())
	}
	kept, _ := filepath.Glob(filepath.Join(f.dirs.State, "kept", "s_*"))
	if len(kept) != 1 {
		t.Fatalf("kept: %v", kept)
	}
	second, err := os.ReadFile(filepath.Join(kept[0], requestsName, "turn-0002.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(second), "Print the kernel version") || !strings.Contains(string(second), "## tool") {
		t.Errorf("the second turn's request:\n%s", second)
	}
}
