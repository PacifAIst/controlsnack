// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package parse

import (
	"reflect"
	"testing"
)

func TestStripReasoning(t *testing.T) {
	for name, c := range map[string]struct{ in, want string }{
		"nothing":                {"just an answer", "just an answer"},
		"a block":                {"<think>let me see</think>The answer.", "The answer."},
		"a block over lines":     {"<think>\nfirst\nsecond\n</think>\n\nThe answer.\n", "The answer."},
		"upper case":             {"<THINK>x</THINK>Answer", "Answer"},
		"thinking":               {"<thinking>x</thinking>Answer", "Answer"},
		"reasoning":              {"<reasoning>x</reasoning>Answer", "Answer"},
		"two blocks":             {"<think>a</think>One <think>b</think>two", "One two"},
		"an empty block":         {"<think></think>Answer", "Answer"},
		"never closed":           {"Answer so far <think>and then I got stuck on", "Answer so far"},
		"only an unclosed block": {"<think>thinking and thinking", ""},
		"a closing tag alone":    {"I reasoned about it a lot.</think>The answer.", "The answer."},
		"mismatched tags":        {"<think>a</thinking>Answer", "Answer"},
		"json inside a block":    {`<think>{"actions":[]}</think>{"actions":[1]}`, `{"actions":[1]}`},
		"an angle bracket":       {"1 < 2 and 3 > 2", "1 < 2 and 3 > 2"},
	} {
		if got := StripReasoning(c.in); got != c.want {
			t.Errorf("%s: StripReasoning(%q) = %q, want %q", name, c.in, got, c.want)
		}
	}
}

func TestDropTrailingCommas(t *testing.T) {
	for in, want := range map[string]string{
		`{"a":1,}`:                   `{"a":1}`,
		`[1,2,]`:                     `[1,2]`,
		`{"a":[1,2,],"b":{"c":3,},}`: `{"a":[1,2],"b":{"c":3}}`,
		"{\"a\":1,\n}":               "{\"a\":1\n}",
		`{"a":"x,}"}`:                `{"a":"x,}"}`,
		`{"a":"quote \" ,}"}`:        `{"a":"quote \" ,}"}`,
		`{"a":1,"b":2}`:              `{"a":1,"b":2}`,
		`[1,,2]`:                     `[1,,2]`,
		``:                           ``,
	} {
		if got := dropTrailingCommas(in); got != want {
			t.Errorf("dropTrailingCommas(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeForgivesOnlyTrailingCommas(t *testing.T) {
	if v := decode(`{"a":[1,2,],}`); v == nil {
		t.Error("trailing commas are forgiven")
	}
	for _, bad := range []string{`{"a":1`, `{a:1}`, `{'a':1}`, `{"a":1} {"b":2}`, `{"a":1}x`, `not json`, `{"a":1,,}`, `{"a":`, ``} {
		if v := decode(bad); v != nil {
			t.Errorf("decode(%q) = %v: nothing else is put right", bad, v)
		}
	}
}

func TestMatchBrace(t *testing.T) {
	for _, c := range []struct {
		text  string
		start int
		want  int
	}{
		{`{}`, 0, 1},
		{`{"a":{"b":1}} tail`, 0, 12},
		{`{"a":"}"}`, 0, 8},
		{`{"a":"\"}"}`, 0, 10},
		{`{"a":[1,{"b":2}]}`, 0, 16},
		{`{"a":1`, 0, -1},
		{`{"a":1]`, 0, -1},
		{`x{"a":1}`, 1, 7},
	} {
		if got := matchBrace(c.text, c.start); got != c.want {
			t.Errorf("matchBrace(%q, %d) = %d, want %d", c.text, c.start, got, c.want)
		}
	}
}

func TestCandidatesInOrder(t *testing.T) {
	text := "intro {note} more\n```json\n{\"a\":1}\n```\nand {\"b\":2} then ```\n[3]\n``` end"
	got := candidates(text)
	// The whole text is not JSON (it does not start with a brace), so: the fenced blocks, then the objects.
	want := []string{`{"a":1}`, `[3]`, `{note}`, `{"a":1}`, `{"b":2}`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("candidates = %q, want %q", got, want)
	}
	if got := candidates(`{"a":1}`); len(got) != 2 || got[0] != `{"a":1}` {
		t.Errorf("an object is the whole text and, once more, the first balanced object: %q", got)
	}
	if got := candidates("nothing here"); len(got) != 0 {
		t.Errorf("%q", got)
	}
}
