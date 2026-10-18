// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReplaceOnceWantsExactlyOneMatchAndARealChange(t *testing.T) {
	for name, tc := range map[string]struct {
		content, old, repl string
		want               string
		err                string
	}{
		"one match":       {"a + b", "+", "-", "a - b", ""},
		"a longer match":  {"if (x) { go(); }", "go();", "stop();", "if (x) { stop(); }", ""},
		"no match":        {"a + b", "*", "/", "", "not in the file"},
		"two matches":     {"a + b + c", "+", "-", "", "2 times"},
		"nothing to find": {"a + b", "", "x", "", "empty"},
		"the same text":   {"a + b", "+", "+", "", "same as the text"},
		"a line break":    {"a\nb", "a\nb", "a\nc", "a\nc", ""},
	} {
		got, err := replaceOnce(tc.content, tc.old, tc.repl)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: error %v, want one with %q", name, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: %q, %v; want %q", name, got, err, tc.want)
		}
	}
}

// module is a tiny Go module in a temporary directory, which becomes the working directory.
func module(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module tiny\n\ngo 1.22\n")
	write("a.go", "package tiny\n\n// Add adds.\nfunc Add(a, b int) int { return a + b }\n\n// Unused is not tested.\nfunc Unused() int { return 1 }\n")
	write("a_test.go", "package tiny\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"2 + 3 is not 5\")\n\t}\n}\n")
	t.Chdir(dir)
	return dir
}

func TestAMutationIsKilledSurvivesOrIsInvalidAndTheFileIsAlwaysPutBack(t *testing.T) {
	dir := module(t)
	orig, err := os.ReadFile(filepath.Join(dir, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		old, repl string
		want      mutation
		detail    string
	}{
		"a change a test sees":            {"return a + b", "return a - b", mutKilled, "TestAdd"},
		"a change nothing sees":           {"return 1", "return 2", mutSurvived, ""},
		"a change that does not compile":  {"return a + b", "return a + ", mutInvalid, ""},
		"a change with an undefined name": {"return a + b", "return a + nothing", mutInvalid, ""},
	} {
		res, detail, err := tryMutation("a.go", tc.old, tc.repl, "TestAdd", []string{"."}, time.Minute)
		if err != nil || res != tc.want || !strings.Contains(detail, tc.detail) {
			t.Errorf("%s: result %d, detail %q, error %v; want %d with %q", name, res, detail, err, tc.want, tc.detail)
		}
		now, err := os.ReadFile(filepath.Join(dir, "a.go"))
		if err != nil || string(now) != string(orig) {
			t.Fatalf("%s: the file was not put back:\n%s", name, now)
		}
	}
	// A change that cannot be made leaves the file alone and says why.
	if _, _, err := tryMutation("a.go", "no such text", "x", "TestAdd", []string{"."}, time.Minute); err == nil {
		t.Error("a change to nothing was accepted")
	}
	if now, _ := os.ReadFile(filepath.Join(dir, "a.go")); string(now) != string(orig) {
		t.Error("the file changed")
	}
	// A test that is not there is not a pass: with nothing selected, the run says so and the change counts as unseen.
	if res, _, err := tryMutation("a.go", "return a + b", "return a - b", "TestNoSuchTest", []string{"."}, time.Minute); err != nil || res != mutSurvived {
		t.Errorf("no test selected: %d, %v", res, err)
	}
}

func TestTheMutateCommandSaysWhatHappened(t *testing.T) {
	module(t)
	if err := cmdMutate([]string{"-run", "TestAdd", "-file", "a.go", "-old", "return a + b", "-new", "return a - b", "-name", "add"}); err != nil {
		t.Errorf("a killed mutation is not an error: %v", err)
	}
	if err := cmdMutate([]string{"-run", "TestAdd", "-file", "a.go", "-old", "return 1", "-new", "return 2"}); err == nil || !strings.Contains(err.Error(), "survived") {
		t.Errorf("a survivor must fail the command: %v", err)
	}
	if err := cmdMutate([]string{"-run", "TestAdd", "-file", "a.go", "-old", "return a + b", "-new", "return a +"}); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Errorf("an invalid mutation must fail the command: %v", err)
	}
	if err := cmdMutate([]string{"-file", "a.go", "-old", "x"}); err == nil {
		t.Error("a mutation with no tests named was accepted")
	}
	// -esc reads \n and \t, for text over several lines.
	if err := cmdMutate([]string{"-esc", "-run", "TestAdd", "-file", "a.go", "-old", "// Add adds.\\nfunc Add(a, b int) int { return a + b }", "-new", "func Add(a, b int) int { return 0 }"}); err != nil {
		t.Errorf("a killed mutation over two lines: %v", err)
	}
}
