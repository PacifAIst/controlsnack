// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package prompt

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
)

func TestEstimateTokens(t *testing.T) {
	for name, tc := range map[string]struct {
		in   string
		want int
	}{
		"empty":        {"", 0},
		"one letter":   {"a", 1},
		"four letters": {"abcd", 1},
		"five letters": {"abcde", 2},
		"a sentence":   {"The quick brown fox jumps over the lazy dog", 11},
		"japanese":     {"日本語", 3},
		"mixed":        {"abcd日本", 3},
		"accents":      {"ñandú", 3},
	} {
		if got := EstimateTokens(tc.in); got != tc.want {
			t.Errorf("%s: EstimateTokens(%q) = %d, want %d", name, tc.in, got, tc.want)
		}
	}
	// Never below a quarter of the bytes, so that the budget does not run low on a guess.
	long := strings.Repeat("abc日本語 def\n", 400)
	if got := EstimateTokens(long); got < len(long)/4 {
		t.Errorf("EstimateTokens = %d for %d bytes", got, len(long))
	}
}

func TestImageTokens(t *testing.T) {
	for name, tc := range map[string]struct {
		backend string
		w, h    int
		want    int
	}{
		"anthropic 1280x800":    {"anthropic", 1280, 800, 1366},
		"anthropic 1920x1080":   {"anthropic", 1920, 1080, 1600},
		"anthropic tiny":        {"anthropic", 10, 10, 1},
		"openai 1024x640":       {"openai", 1024, 640, 85 + 170*4},
		"openai 1280x800":       {"openai", 1280, 800, 85 + 170*6},
		"openai 1920x1080":      {"openai", 1920, 1080, 85 + 170*6},
		"openai 4096x4096":      {"openai", 4096, 4096, 85 + 170*4},
		"openai small":          {"openai", 100, 100, 85 + 170},
		"ollama 1024x640":       {"ollama", 1024, 640, 37 * 23},
		"ollama small":          {"ollama", 100, 100, 600},
		"ollama huge":           {"ollama", 4096, 4096, 2400},
		"other kinds are local": {"lmstudio", 1024, 640, 37 * 23},
		"no width":              {"openai", 0, 100, 0},
		"no height":             {"anthropic", 100, -1, 0},
	} {
		if got := ImageTokens(tc.backend, tc.w, tc.h); got != tc.want {
			t.Errorf("%s: ImageTokens = %d, want %d", name, got, tc.want)
		}
	}
}

func TestCeil(t *testing.T) {
	for in, want := range map[float64]float64{0: 0, 0.1: 1, 1: 1, 1.5: 2, 2.0000001: 3, 7.99: 8} {
		if got := ceilF(in); got != want {
			t.Errorf("ceilF(%v) = %v, want %v", in, got, want)
		}
	}
	if ceilDiv(10, 3) != 4 || ceilDiv(9, 3) != 3 || ceilDiv(1, 28) != 1 {
		t.Error("ceilDiv")
	}
}

func TestAConversationOfJSONObservationsAnswersCallsInJSON(t *testing.T) {
	set := mustSet(t, "compact")
	c, err := NewConversation("system", Options{Backend: "openai", Mode: backend.ModeNative, Set: set, Renderer: jsonRenderer()})
	if err != nil {
		t.Fatal(err)
	}
	c.Begin(obs(1), testImage)
	if err := c.Assistant("", calls("exec")); err != nil {
		t.Fatal(err)
	}
	if err := c.Results([]Result{{CallID: "call_1", Tool: "exec", OK: true, Detail: "exit=0", Parts: []Part{{Source: "exec.stdout", Text: "hi"}}}}, Limits{}); err != nil {
		t.Fatal(err)
	}
	c.Begin(obs(2), testImage)
	req, _, err := c.Request(3)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(req.Messages[3].Text), &result); err != nil || result["status"] != "ok" || result["tool"] != "exec" {
		t.Fatalf("the result of a call is JSON too: %q %v", req.Messages[3].Text, err)
	}
	var cur map[string]any
	if err := json.Unmarshal([]byte(req.Messages[4].Text), &cur); err != nil || cur["turn"] != float64(2) {
		t.Fatalf("the observation is JSON: %q %v", req.Messages[4].Text, err)
	}
	validObservation(t, observationSchema(t), req.Messages[4].Text)
}
