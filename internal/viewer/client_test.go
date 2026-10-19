// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer_test

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/PacifAIst/controlsnack/internal/viewer"
)

func TestTheMessagesOfThePageAreRead(t *testing.T) {
	client := wsSchema(t, "client_message")
	for name, tc := range map[string]struct {
		in   string
		want any
	}{
		"chat":                    {`{"type":"chat","text":"hello"}`, viewer.ChatIn{Text: "hello"}},
		"chat trimmed":            {"{\"type\":\"chat\",\"text\":\"  \\n hello\\tthere \\n\"}", viewer.ChatIn{Text: "hello\tthere"}},
		"chat with line breaks":   {`{"type":"chat","text":"a\nb\r\nc"}`, viewer.ChatIn{Text: "a\nb\r\nc"}},
		"chat in other scripts":   {`{"type":"chat","text":"ábc 日本語 😀"}`, viewer.ChatIn{Text: "ábc 日本語 😀"}},
		"chat at the limit":       {`{"type":"chat","text":"` + strings.Repeat("é", viewer.MaxChatRunes) + `"}`, viewer.ChatIn{Text: strings.Repeat("é", viewer.MaxChatRunes)}},
		"approve":                 {`{"type":"confirm","id":"q1","approve":true}`, viewer.ConfirmIn{ID: "q1", Approve: true}},
		"reject with a note":      {`{"type":"confirm","id":"c_1.a:b-2","approve":false,"note":" not now "}`, viewer.ConfirmIn{ID: "c_1.a:b-2", Note: "not now"}},
		"pause":                   {`{"type":"control","op":"pause"}`, viewer.ControlIn{Op: "pause"}},
		"resume":                  {`{"type":"control","op":"resume"}`, viewer.ControlIn{Op: "resume"}},
		"stop":                    {`{"type":"control","op":"stop"}`, viewer.ControlIn{Op: "stop"}},
		"step on":                 {`{"type":"control","op":"step_on"}`, viewer.ControlIn{Op: "step_on"}},
		"step off":                {`{"type":"control","op":"step_off"}`, viewer.ControlIn{Op: "step_off"}},
		"kill":                    {`{"type":"control","op":"kill"}`, viewer.ControlIn{Op: "kill", Src: "button"}},
		"kill by the button":      {`{"type":"control","op":"kill","src":"button"}`, viewer.ControlIn{Op: "kill", Src: "button"}},
		"kill by the hotkey":      {`{"type":"control","op":"kill","src":"hotkey"}`, viewer.ControlIn{Op: "kill", Src: "hotkey"}},
		"ping":                    {`{"type":"ping","t":1234.5}`, viewer.PingIn{T: 1234.5}},
		"ping at zero":            {`{"type":"ping","t":0}`, viewer.PingIn{T: 0}},
		"ping, whitespace around": {" \n{\"type\" : \"ping\", \"t\" : -1}\n ", viewer.PingIn{T: -1}},
	} {
		got, err := viewer.ParseClient([]byte(tc.in))
		if err != nil || got != tc.want {
			t.Errorf("%s: %#v, %v; want %#v", name, got, err, tc.want)
			continue
		}
		if err := valid(client, []byte(tc.in)); err != nil {
			t.Errorf("%s: read, but the schema refuses it: %v", name, err)
		}
	}
}

func TestMessagesOfThePageThatBreakTheRules(t *testing.T) {
	tooLong := strings.Repeat("a", viewer.MaxChatRunes+1)
	for name, in := range map[string]string{
		"chat without text":               `{"type":"chat"}`,
		"chat with empty text":            `{"type":"chat","text":""}`,
		"chat with blank text":            `{"type":"chat","text":" \n\t "}`,
		"chat over the limit":             `{"type":"chat","text":"` + tooLong + `"}`,
		"chat over the limit in runes":    `{"type":"chat","text":"` + strings.Repeat("日", viewer.MaxChatRunes+1) + `"}`,
		"chat with a NUL":                 `{"type":"chat","text":"a\u0000b"}`,
		"chat with an escape":             `{"type":"chat","text":"a\u001b[31mb"}`,
		"chat with a DEL":                 `{"type":"chat","text":"a\u007fb"}`,
		"chat with a C1 control":          `{"type":"chat","text":"a\u0085b"}`,
		"chat with a number as text":      `{"type":"chat","text":5}`,
		"chat with an extra field":        `{"type":"chat","text":"a","role":"system"}`,
		"confirm without an id":           `{"type":"confirm","approve":true}`,
		"confirm with an empty id":        `{"type":"confirm","id":"","approve":true}`,
		"confirm with a space in the id":  `{"type":"confirm","id":"a b","approve":true}`,
		"confirm with a path as an id":    `{"type":"confirm","id":"../x","approve":true}`,
		"confirm with a long id":          `{"type":"confirm","id":"` + strings.Repeat("a", 65) + `","approve":true}`,
		"confirm without approve":         `{"type":"confirm","id":"q"}`,
		"confirm with approve null":       `{"type":"confirm","id":"q","approve":null}`,
		"confirm with approve as text":    `{"type":"confirm","id":"q","approve":"true"}`,
		"confirm with a long note":        `{"type":"confirm","id":"q","approve":true,"note":"` + strings.Repeat("n", viewer.MaxNoteRunes+1) + `"}`,
		"confirm with a control in note":  `{"type":"confirm","id":"q","approve":true,"note":"a\u0007"}`,
		"confirm with an extra field":     `{"type":"confirm","id":"q","approve":true,"force":true}`,
		"control without an op":           `{"type":"control"}`,
		"control with an unknown op":      `{"type":"control","op":"reboot"}`,
		"control with the op in capitals": `{"type":"control","op":"KILL"}`,
		"control with a number as op":     `{"type":"control","op":1}`,
		"control pause with a src":        `{"type":"control","op":"pause","src":"button"}`,
		"control kill with a bad src":     `{"type":"control","op":"kill","src":"voice"}`,
		"ping without t":                  `{"type":"ping"}`,
		"ping with t null":                `{"type":"ping","t":null}`,
		"ping with t as text":             `{"type":"ping","t":"now"}`,
		"ping with an extra field":        `{"type":"ping","t":1,"u":2}`,
	} {
		_, err := viewer.ParseClient([]byte(in))
		var bad *viewer.InvalidError
		if !errors.As(err, &bad) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestMessagesOfThePageThatAreNotMessages(t *testing.T) {
	for in, want := range map[string]error{
		`{}`:                          viewer.ErrUnknownType,
		`null`:                        viewer.ErrUnknownType,
		`{"type":""}`:                 viewer.ErrUnknownType,
		`{"type":"explode"}`:          viewer.ErrUnknownType,
		`{"type":"hello"}`:            viewer.ErrUnknownType,
		`{"type":"CHAT","text":"a"}`:  viewer.ErrUnknownType,
		`{"type":"chat ","text":"a"}`: viewer.ErrUnknownType,
	} {
		if _, err := viewer.ParseClient([]byte(in)); !errors.Is(err, want) {
			t.Errorf("%s: %v", in, err)
		}
	}
	for _, in := range []string{``, `not json`, `[]`, `{"type":"chat","text":"a"} {"type":"chat","text":"b"}`, `{"type":"chat","text":"a"}x`, `"chat"`, `5`, `{"type":5}`, `{"type":["chat"]}`, `{`, `{"type":"chat"`, "\xff\xfe"} {
		_, err := viewer.ParseClient([]byte(in))
		var bad *viewer.InvalidError
		if err == nil || errors.Is(err, viewer.ErrUnknownType) || errors.As(err, &bad) {
			t.Errorf("%q: %v", in, err)
		}
	}
	if _, err := viewer.ParseClient([]byte(`{"type":"chat","text":"` + strings.Repeat("a", viewer.MaxClientBytes) + `"}`)); err == nil {
		t.Error("a message over the limit was read")
	}
}

func FuzzParseClient(f *testing.F) {
	for _, s := range []string{
		`{"type":"chat","text":"hello"}`, `{"type":"confirm","id":"q1","approve":true,"note":"x"}`, `{"type":"control","op":"kill","src":"hotkey"}`,
		`{"type":"ping","t":1e308}`, `{"type":"chat","text":"a\u0000"}`, `{"type":"chat","text":"\ud800"}`, `[`, `{"type":"chat","type":"ping","t":1}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := viewer.ParseClient(data)
		if err != nil {
			if got != nil {
				t.Fatalf("an error and a message: %#v, %v", got, err)
			}
			return
		}
		switch m := got.(type) {
		case viewer.ChatIn:
			if m.Text == "" || m.Text != strings.TrimSpace(m.Text) || !utf8.ValidString(m.Text) || utf8.RuneCountInString(m.Text) > viewer.MaxChatRunes {
				t.Fatalf("chat %q", m.Text)
			}
			for _, r := range m.Text {
				if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || r == 0x7f || (r >= 0x80 && r < 0xa0) {
					t.Fatalf("chat holds U+%04X", r)
				}
			}
		case viewer.ConfirmIn:
			if m.ID == "" || len(m.ID) > 64 || strings.ContainsAny(m.ID, " /\\\"'<>&\x00\n") || utf8.RuneCountInString(m.Note) > viewer.MaxNoteRunes {
				t.Fatalf("confirm %+v", m)
			}
		case viewer.ControlIn:
			switch m.Op {
			case "pause", "resume", "stop", "step_on", "step_off":
				if m.Src != "" {
					t.Fatalf("control %+v", m)
				}
			case "kill":
				if m.Src != "button" && m.Src != "hotkey" {
					t.Fatalf("control %+v", m)
				}
			default:
				t.Fatalf("control %+v", m)
			}
		case viewer.PingIn:
		default:
			t.Fatalf("a message of type %T", got)
		}
	})
}

func TestFramesRoundTripAndRefuseWhatTheyCannotCarry(t *testing.T) {
	payload := []byte("RIFF....WEBPVP8 data")
	b, err := viewer.EncodeFrame(0xDEADBEEF, 1920, 1080, payload)
	if err != nil {
		t.Fatal(err)
	}
	if b[0] != 0x01 || len(b) != 9+len(payload) || b[1] != 0xDE || b[4] != 0xEF || b[5] != 0x07 || b[6] != 0x80 || b[7] != 0x04 || b[8] != 0x38 {
		t.Errorf("layout % x", b[:9])
	}
	seq, w, h, got, err := viewer.DecodeFrame(b)
	if err != nil || seq != 0xDEADBEEF || w != 1920 || h != 1080 || string(got) != string(payload) {
		t.Errorf("round trip: %d %dx%d %q %v", seq, w, h, got, err)
	}
	for name, args := range map[string]struct {
		w, h int
		data []byte
	}{
		"no picture":  {10, 10, nil},
		"zero width":  {0, 10, payload},
		"zero height": {10, 0, payload},
		"wide":        {70000, 10, payload},
		"tall":        {10, 65536, payload},
		"negative":    {-1, 10, payload},
		"too large":   {10, 10, make([]byte, viewer.MaxFrameBytes)},
	} {
		if _, err := viewer.EncodeFrame(1, args.w, args.h, args.data); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, in := range map[string][]byte{"empty": nil, "short": b[:9], "wrong kind": append([]byte{0x02}, b[1:]...)} {
		if _, _, _, _, err := viewer.DecodeFrame(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := viewer.EncodeFrame(1, 65535, 65535, payload); err != nil {
		t.Errorf("the largest size: %v", err)
	}
}

type emptyMessage struct{}

func (emptyMessage) MessageType() string { return "empty" }

type notAnObject []int

func (notAnObject) MessageType() string { return "list" }

func TestEncodePutsTheTypeFirst(t *testing.T) {
	b, err := viewer.Encode(emptyMessage{})
	if err != nil || string(b) != `{"type":"empty"}` {
		t.Errorf("%s %v", b, err)
	}
	b, err = viewer.Encode(viewer.Cursor{X: 1, Y: -2})
	if err != nil || string(b) != `{"type":"cursor","x":1,"y":-2}` {
		t.Errorf("%s %v", b, err)
	}
	if _, err := viewer.Encode(notAnObject{1}); err == nil {
		t.Error("a list was encoded as a message")
	}
	b, _ = viewer.Encode(viewer.Trace{ActionID: "a", Tool: "t", Verdict: "allow"})
	if !strings.Contains(string(b), `"rules":[]`) || !strings.Contains(string(b), `"args":{}`) {
		t.Errorf("a trace with nothing to show: %s", b)
	}
	b, _ = viewer.Encode(viewer.ConfirmRequest{ID: "q"})
	if !strings.Contains(string(b), `"rules":[]`) || !strings.Contains(string(b), `"args":{}`) {
		t.Errorf("a confirmation with nothing to show: %s", b)
	}
	// A text with markup is only text: it comes out escaped for JSON and nothing more.
	b, _ = viewer.Encode(viewer.Notice{Level: "info", Text: `<img src=x onerror=alert(1)> "quoted" \ back`})
	if strings.Contains(string(b), "<") || strings.Contains(string(b), ">") {
		t.Errorf("markup characters are not escaped: %s", b)
	}
}
