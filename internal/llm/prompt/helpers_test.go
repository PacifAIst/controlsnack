// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package prompt

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/safety/untrusted"
	"github.com/PacifAIst/controlsnack-local/schema"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/llm/prompt")

const testNonce = "3f9a1c"

func testFencer() *untrusted.Fencer { return untrusted.WithNonce(testNonce) }

func textRenderer() Renderer { return Renderer{Fencer: testFencer(), Format: FormatText} }
func jsonRenderer() Renderer { return Renderer{Fencer: testFencer(), Format: FormatJSON} }

func mustSet(t testing.TB, profile string) *actions.Set {
	t.Helper()
	set, err := actions.NewSet(profile)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// golden compares text with testdata/llm/prompt/<name>; with -update it writes it.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("..", "..", "..", "testdata", "llm", "prompt", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if w := strings.ReplaceAll(string(want), "\r\n", "\n"); w != got {
		t.Errorf("%s differs from its golden file (run `go test ./internal/llm/prompt -update` if the change is meant):\n%s", name, got)
	}
}

// planObservation is the example of PLAN.md section 5.2.
func planObservation() Observation {
	return Observation{
		Turn: 7, Screen: Size{1280, 800}, Cursor: &Point{640, 400}, Network: "disabled", Mode: "vision", Tools: "full",
		Task:   "Open a terminal and print the kernel version",
		Active: &Window{Class: "xfce4-terminal", Title: "Terminal - cs@guest: ~", Bounds: Rect{0, 0, 1280, 770}},
		UI: &UITree{Generation: "g12", Scope: "active_window", Nodes: []Node{
			{ID: "a1", Role: "frame", Name: "Terminal - cs@guest: ~", Bounds: Rect{0, 0, 1280, 770}},
			{ID: "a9", Role: "terminal", Name: "Terminal", Bounds: Rect{0, 60, 1280, 700}, States: []string{"focused", "editable"}, Actions: []string{"focus"}},
			{ID: "a17", Role: "push button", Name: "OK", Bounds: Rect{600, 500, 80, 30}, Actions: []string{"click"}},
		}},
		Results: []Result{{Tool: "exec", OK: true, Detail: "exit=0 31ms", Parts: []Part{{Source: "exec.stdout", Text: "6.12.x-amd64\n"}}}},
		Budget:  Budget{ActionsLeft: 41, TurnsLeft: 88, TimeLeft: 47 * time.Minute},
		Image:   &ImageNote{W: 1280, H: 800, Marks: "a1..a23"},
	}
}

// observationSchema is the compiled schema/observation.schema.json.
func observationSchema(t testing.TB) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema.Observation))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	const id = "https://controlsnack.com/schema/v1/observation.json"
	if err := c.AddResource(id, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// validObservation checks a rendered JSON observation against the schema.
func validObservation(t testing.TB, s *jsonschema.Schema, text string) {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, text)
	}
	if err := s.Validate(v); err != nil {
		t.Fatalf("does not follow the schema: %v\n%s", err, text)
	}
}
