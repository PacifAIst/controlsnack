// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package parse

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// Bounds on the work done on one answer: a model can write a lot, and none of it may cost more than a moment.
const (
	maxAnswerBytes = 512 << 10 // what is looked at of an answer
	maxCandidates  = 32        // how many objects of an answer are tried
	maxStarts      = 256       // how many braces of an answer are tried as the start of an object
)

// reasoning blocks: what reasoning models write before the answer. <think> is the usual one.
var (
	reBlock  = regexp.MustCompile(`(?is)<(think|thinking|reasoning|thought)>.*?</(think|thinking|reasoning|thought)>`)
	reOpen   = regexp.MustCompile(`(?i)<(think|thinking|reasoning|thought)>`)
	reClose  = regexp.MustCompile(`(?i)</(think|thinking|reasoning|thought)>`)
	reFences = regexp.MustCompile("(?s)```[A-Za-z0-9_-]*[ \\t]*\\r?\\n?(.*?)```")
)

// StripReasoning removes the reasoning blocks a model wrote before its answer: <think>...</think> and its variants. A
// block that is never closed (the answer was cut off while the model was still thinking) takes the rest of the text with
// it, and a closing tag with no opening one (some models leave the opening tag out) takes what comes before it.
func StripReasoning(text string) string {
	text = reBlock.ReplaceAllString(text, "")
	if loc := reOpen.FindStringIndex(text); loc != nil {
		text = text[:loc[0]]
	}
	if loc := reClose.FindStringIndex(text); loc != nil {
		text = text[loc[1]:]
	}
	return strings.TrimSpace(text)
}

// candidates lists, in order, the pieces of an answer that may be its JSON: the whole text, each fenced block, then each
// balanced object. Only the pieces that are an object or an array are returned.
func candidates(text string) []string {
	if len(text) > maxAnswerBytes {
		text = text[:maxAnswerBytes]
	}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && (s[0] == '{' || s[0] == '[') && len(out) < maxCandidates {
			out = append(out, s)
		}
	}
	add(text)
	for _, m := range reFences.FindAllStringSubmatch(text, maxCandidates) {
		add(m[1])
	}
	for _, s := range balancedObjects(text) {
		add(s)
	}
	return out
}

// balancedObjects finds the {...} pieces of a text, string-aware: each object that starts where the previous one did not
// end, and, for each start, the piece up to its matching brace. A brace that closes nothing costs a look at the rest of
// the text, so only the first maxStarts braces are tried.
func balancedObjects(text string) []string {
	var out []string
	for i, tries := 0, 0; i < len(text) && len(out) < maxCandidates && tries < maxStarts; i++ {
		if text[i] != '{' {
			continue
		}
		tries++
		if end := matchBrace(text, i); end > i {
			out = append(out, text[i:end+1])
			i = end // the objects inside this one are part of it
		}
	}
	return out
}

// matchBrace returns the index of the brace that closes the one at text[start], or -1.
func matchBrace(text string, start int) int {
	var open []byte // the closers that are still owed, innermost last
	inString, escaped := false, false
	for i := start; i < len(text); i++ {
		c := text[i]
		switch {
		case inString:
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
		case c == '"':
			inString = true
		case c == '{':
			open = append(open, '}')
		case c == '[':
			open = append(open, ']')
		case c == '}' || c == ']':
			if len(open) == 0 || open[len(open)-1] != c {
				return -1 // a closer that closes nothing, or the wrong kind
			}
			open = open[:len(open)-1]
			if len(open) == 0 {
				return i
			}
		}
	}
	return -1
}

// decode reads a piece of an answer as JSON: as it is, or with the commas before a closing bracket taken out (the one
// mistake that is put right). It returns nil when the piece is not JSON.
func decode(piece string) any {
	for _, s := range []string{piece, dropTrailingCommas(piece)} {
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err == nil && !dec.More() {
			return v
		}
	}
	return nil
}

// dropTrailingCommas takes out the commas that stand before a } or a ], outside strings: {"a":1,} and [1,2,].
func dropTrailingCommas(s string) string {
	var b bytes.Buffer
	inString, escaped := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inString:
			b.WriteByte(c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
		case c == '"':
			inString = true
			b.WriteByte(c)
		case c == ',':
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r') {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue // the comma is dropped, the white space after it stays
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
