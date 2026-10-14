// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzParse feeds Parse whatever a model might say. It must never panic, and what it accepts must be a fixed point:
// parsing the validated arguments again gives the same arguments; what it refuses has a code and one clean line for
// each problem.
func FuzzParse(f *testing.F) {
	for _, tool := range append(Names(), "nope", "") {
		f.Add(tool, `{}`)
	}
	for _, s := range []string{
		`{"command":"ls","timeout_s":30.0,"reason":"why"}`, `{"id":"a17","button":"right"}`, `{"op":"drag","x":1,"y":2,"to_x":3,"to_y":4}`,
		`{"region":[1.0,2,3,4]}`, `{"keys":"ctrl+shift+t","repeat":2}`, `{"condition":"text_visible","value":"x","timeout_s":0.1}`,
		`[]`, `null`, ``, `{"a":`, `{"command":"` + strings.Repeat("é", 100) + `"}`, `{"x":1e400}`, `{"op":"focus","window_id":"0xFF"}`,
	} {
		f.Add(ToolExec, s)
		f.Add(ToolMouse, s)
		f.Add(ToolReadScreenText, s)
		f.Add(ToolKey, s)
		f.Add(ToolWaitFor, s)
	}
	f.Fuzz(func(t *testing.T, tool, args string) {
		a, err := Parse(tool, json.RawMessage(args))
		if err != nil {
			var e *Error
			if !errors.As(err, &e) || (e.Code != CodeInvalidArgs && e.Code != CodeUnknownTool) || len(e.Problems) == 0 {
				t.Fatalf("%q %q: %v", tool, args, err)
			}
			for _, p := range e.Problems {
				if p.Path == "" || p.Message == "" || !utf8.ValidString(p.Message) || strings.ContainsAny(p.Message+p.Path, "\n\r\x00") {
					t.Fatalf("%q %q: unclean problem %+v", tool, args, p)
				}
			}
			return
		}
		if a.Args == nil || a.Args.Tool() != tool || utf8.RuneCountInString(a.Reason) > MaxReasonRunes || !json.Valid(a.Raw) {
			t.Fatalf("%q %q: %+v", tool, args, a)
		}
		again, err := Parse(tool, a.Raw)
		if err != nil || !reflect.DeepEqual(again.Args, a.Args) {
			t.Fatalf("%q %q: the validated arguments %s parse again as %+v (%v), not %+v", tool, args, a.Raw, again.Args, err, a.Args)
		}
	})
}
