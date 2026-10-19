// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package parse

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/PacifAIst/controlsnack/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack/internal/llm/actions"
)

// SourceCall: the text holds tool calls written as function calls, exec(command='ls'), which is what a model does when the
// server does not recognize the format its template uses for tool calls and hands the call back as words. Two forms are
// read: Python's, exec(command='ls'), and name{key:value,...}, with Gemma's own string quotes or ordinary ones.
const SourceCall Source = "call"

// gemmaQuote delimits a string in the tool-call format of Gemma models, name{key:<|"|>text<|"|>,...}, which leaks into the
// words of an answer when the server does not recognize it.
const gemmaQuote = `<|"|>`

// callsFromText reads function-call syntax out of a model's words: a call of a tool that exists, written at the start of a
// line (after a bullet, a back tick or a code fence, if there is one), with its arguments as name=value. It is a fallback
// for the answer that has no native call and no JSON, and a narrow one: a tool named in the middle of a sentence is prose,
// not a call, and an argument written without its name is a mistake the model is told about. The words that are not a
// call are returned too; they are the model's thought.
func callsFromText(text string) (calls []pending, rest string) {
	names := toolNames()
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var kept []string
	for i := 0; i < len(lines); i++ {
		name, from, ok := callStart(lines[i], names)
		if !ok {
			kept = append(kept, lines[i])
			continue
		}
		// The call may run over several lines: take lines until the parenthesis closes.
		src := lines[i][from:]
		body, end, closed := closeCall(src)
		for !closed && i+1 < len(lines) {
			i++
			src += "\n" + lines[i]
			body, end, closed = closeCall(src)
		}
		if !closed {
			kept = append(kept, lines[i])
			continue
		}
		p := pending{index: len(calls) + 1, tool: name}
		if args, problem := parseKeywords(body); problem != "" {
			p.fault = &actions.Problem{Path: "args", Message: problem}
		} else if raw, err := json.Marshal(args); err != nil {
			p.fault = &actions.Problem{Path: "args", Message: "are not valid JSON"}
		} else {
			p.args = raw
		}
		calls = append(calls, p)
		if tail := strings.Trim(strings.TrimSpace(src[end:]), "`"); tail != "" && tail != ";" {
			kept = append(kept, tail)
		}
	}
	return calls, strings.TrimSpace(strings.Join(kept, "\n"))
}

func toolNames() map[string]bool {
	names := map[string]bool{}
	for _, t := range actions.Tools() {
		names[t.Name] = true
	}
	return names
}

// callStart finds a tool's name followed by an opening parenthesis at the start of a line, ignoring a bullet, a back tick
// or a "call:" label before it. It returns the name and where the parenthesis is in the line.
func callStart(line string, names map[string]bool) (name string, paren int, ok bool) {
	s := strings.TrimLeftFunc(line, func(r rune) bool { return unicode.IsSpace(r) || r == '-' || r == '*' || r == '`' || r == '•' })
	if i := strings.IndexFunc(s, func(r rune) bool { return !unicode.IsDigit(r) }); i > 0 && strings.HasPrefix(s[i:], ". ") {
		s = strings.TrimSpace(s[i+2:]) // "1. exec(...)"
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "call:"), "tool:")
	s = strings.TrimLeft(s, " `")
	end := 0
	for end < len(s) && (s[end] == '_' || s[end] >= 'a' && s[end] <= 'z' || s[end] >= '0' && s[end] <= '9') {
		end++
	}
	if end == 0 || end >= len(s) || (s[end] != '(' && s[end] != '{') || !names[s[:end]] {
		return "", 0, false
	}
	offset := len(line) - len(s)
	return s[:end], offset + end, true
}

// closeCall takes src, which starts at the opening parenthesis, and returns what is between it and the matching closing
// one and the index after it. Quotes and brackets inside are skipped over.
func closeCall(src string) (body string, end int, closed bool) {
	want := byte(')')
	if src != "" && src[0] == '{' {
		want = '}'
	}
	depth := 0
	for i := 0; i < len(src); {
		if strings.HasPrefix(src[i:], gemmaQuote) {
			j := strings.Index(src[i+len(gemmaQuote):], gemmaQuote)
			if j < 0 {
				return "", 0, false
			}
			i += len(gemmaQuote) + j + len(gemmaQuote)
			continue
		}
		switch c := src[i]; c {
		case '"', '\'':
			j := endOfString(src, i)
			if j < 0 {
				return "", 0, false
			}
			i = j
			continue
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				if c != want {
					return "", 0, false
				}
				return src[1:i], i + 1, true
			}
		}
		i++
	}
	return "", 0, false
}

// endOfString returns the index after the string literal that starts at src[i] (single, double or triple quoted), or -1
// when it is not closed.
func endOfString(src string, i int) int {
	q := src[i]
	if strings.HasPrefix(src[i:], strings.Repeat(string(q), 3)) {
		j := strings.Index(src[i+3:], strings.Repeat(string(q), 3))
		if j < 0 {
			return -1
		}
		return i + 3 + j + 3
	}
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case q:
			return j + 1
		case '\n':
			if q == '\'' || q == '"' {
				// A line break inside a quoted string is allowed: models write commands over several lines.
				continue
			}
		}
	}
	return -1
}

// parseKeywords reads name=value arguments. It returns a sentence for the model when they cannot be read.
func parseKeywords(body string) (map[string]any, string) {
	args := map[string]any{}
	i := 0
	skip := func() {
		for i < len(body) && (body[i] == ' ' || body[i] == '\t' || body[i] == '\n' || body[i] == '\r') {
			i++
		}
	}
	for skip(); i < len(body); skip() {
		start := i
		for i < len(body) && (body[i] == '_' || body[i] >= 'a' && body[i] <= 'z' || body[i] >= 'A' && body[i] <= 'Z' || body[i] >= '0' && body[i] <= '9') {
			i++
		}
		key := body[start:i]
		if key == "" && i < len(body) && (body[i] == '"' || body[i] == '\'') { // {"command": "ls"}
			if end := endOfString(body, i); end > 0 {
				key, i = unescape(body[i+1:end-1]), end
			}
		}
		skip()
		// name=value is Python's, name:value is JSON's and the format some models use for their own tool calls.
		if key == "" || i >= len(body) || (body[i] != '=' && body[i] != ':') || (body[i] == '=' && i+1 < len(body) && body[i+1] == '=') {
			return nil, "must be written as name=value, one for each argument"
		}
		i++
		skip()
		v, n, problem := parseValue(body[i:])
		if problem != "" {
			return nil, fmt.Sprintf("%s: %s", key, problem)
		}
		if _, dup := args[key]; dup {
			return nil, fmt.Sprintf("%s is given twice", key)
		}
		args[key] = v
		i += n
		skip()
		if i < len(body) {
			if body[i] != ',' {
				return nil, fmt.Sprintf("%s: expected a comma after the value", key)
			}
			i++
		}
	}
	return args, ""
}

// parseValue reads one value: a quoted string, a number, True/False/None, or a list or object written as JSON.
func parseValue(s string) (v any, n int, problem string) {
	if s == "" {
		return nil, 0, "has no value"
	}
	if strings.HasPrefix(s, gemmaQuote) {
		j := strings.Index(s[len(gemmaQuote):], gemmaQuote)
		if j < 0 {
			return nil, 0, "the text is not closed"
		}
		return s[len(gemmaQuote) : len(gemmaQuote)+j], len(gemmaQuote) + j + len(gemmaQuote), ""
	}
	switch c := s[0]; c {
	case '"', '\'':
		end := endOfString(s, 0)
		if end < 0 {
			return nil, 0, "the text is not closed"
		}
		q := 1
		if strings.HasPrefix(s, strings.Repeat(string(c), 3)) {
			q = 3
		}
		return unescape(s[q : end-q]), end, ""
	case '[', '{':
		depth, i := 0, 0
		for ; i < len(s); i++ {
			if s[i] == '"' || s[i] == '\'' {
				j := endOfString(s, i)
				if j < 0 {
					return nil, 0, "the list is not closed"
				}
				i = j - 1
				continue
			}
			if s[i] == '[' || s[i] == '{' {
				depth++
			} else if s[i] == ']' || s[i] == '}' {
				depth--
				if depth == 0 {
					i++
					break
				}
			}
		}
		if depth != 0 {
			return nil, 0, "the list is not closed"
		}
		lit := pythonLiterals(s[:i])
		var out any
		if err := json.Unmarshal([]byte(lit), &out); err != nil {
			return nil, 0, "the list or object must be written as JSON"
		}
		return out, i, ""
	}
	end := 0
	for end < len(s) && s[end] != ',' && s[end] != ')' && s[end] != '\n' {
		end++
	}
	word := strings.TrimSpace(s[:end])
	switch word {
	case "True", "true":
		return true, end, ""
	case "False", "false":
		return false, end, ""
	case "None", "null":
		return nil, end, ""
	}
	if n, err := strconv.ParseInt(word, 10, 64); err == nil {
		return n, end, ""
	}
	if f, err := strconv.ParseFloat(word, 64); err == nil {
		return f, end, ""
	}
	return nil, 0, "text must be written in quotes"
}

// pythonLiterals turns the Python spellings that models use in a list into JSON: single-quoted strings, True, False, None.
func pythonLiterals(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			j := endOfString(s, i)
			if j < 0 {
				return s
			}
			b.WriteString(strconv.Quote(unescape(s[i+1 : j-1])))
			i = j - 1
		case c == '"':
			j := endOfString(s, i)
			if j < 0 {
				return s
			}
			b.WriteString(s[i:j])
			i = j - 1
		case strings.HasPrefix(s[i:], "True"):
			b.WriteString("true")
			i += 3
		case strings.HasPrefix(s[i:], "False"):
			b.WriteString("false")
			i += 4
		case strings.HasPrefix(s[i:], "None"):
			b.WriteString("null")
			i += 3
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// unescape reads the backslash escapes of a quoted string the way a model means them.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'u':
			if i+4 < len(s) {
				if r, err := strconv.ParseUint(s[i+1:i+5], 16, 16); err == nil && utf8.ValidRune(rune(r)) { //nolint:gosec // four hex digits: at most 0xFFFF
					b.WriteRune(rune(r)) //nolint:gosec // the same
					i += 4
					continue
				}
			}
			b.WriteString(`\u`)
		default:
			b.WriteByte(s[i]) // \\ \' \" \/ and anything else stands for itself
		}
	}
	return b.String()
}

// fromCalls evaluates the function calls found in the words of an answer, or reports false when there are none.
func fromCalls(set *actions.Set, text string, max int) (Result, bool) {
	calls, rest := callsFromText(text)
	if len(calls) == 0 {
		return Result{}, false
	}
	res := Result{Source: SourceCall, Thought: textclean.Block(rest, maxThoughtRunes)}
	if len(calls) > max {
		res.Dropped = len(calls) - max
		calls = calls[:max]
	}
	evaluate(set, calls, &res)
	return res, true
}
