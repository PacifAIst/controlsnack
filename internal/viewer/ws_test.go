// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/PacifAIst/controlsnack-local/internal/viewer"
)

const ts = "2026-10-18T10:00:00.000Z"

// messages is one of each kind the host sends, with the optional fields filled in and left out.
func messages() []viewer.Message {
	x, y := 640, 400
	return []viewer.Message{
		viewer.State{State: viewer.StateRunning, Reason: "task started"},
		viewer.Cursor{X: 10, Y: 20},
		viewer.Plan{ActionID: "a1", Tool: "mouse", X: &x, Y: &y},
		viewer.Plan{ActionID: "a2", Tool: "click_element", Bounds: &viewer.Bounds{X: 1, Y: 2, W: 30, H: 40}},
		viewer.Plan{ActionID: "a3", Tool: "key"},
		viewer.Trace{ActionID: "a1", Turn: 3, Tool: "mouse", Args: json.RawMessage(`{"x":640,"y":400}`), Reason: "click Open", Verdict: "allow", Rules: []string{"R1"}, Status: "done", DurationMS: 42, Summary: "clicked"},
		viewer.Trace{ActionID: "a4", Turn: 4, Tool: "exec", Verdict: "deny", Status: "denied", Summary: "refused"}, // no rules, no args
		viewer.Chat{ID: "m1", Role: "assistant", Text: "I see a dialog.", TS: ts},
		viewer.ConfirmRequest{ID: "q1", ActionID: "a5", Tool: "exec", Args: json.RawMessage(`{"command":"rm -r ~/work"}`), Rules: []viewer.Rule{{ID: "X-RM", Explanation: "removes files"}}, Reason: "clean up", ExpiresAt: ts},
		viewer.ConfirmRequest{ID: "q2", ActionID: "a6", Tool: "key", ExpiresAt: ts},
		viewer.ConfirmResolved{ID: "q1", Outcome: viewer.OutcomeRejected},
		viewer.Audit{Line: `{"seq":1,"kind":"lifecycle"}`},
		viewer.Stats{KBps: 12.5, FPS: 2, FrameBytes: 31000, LLMMS: 2100, Actions: 7},
		viewer.Notice{Level: viewer.LevelWarn, Text: "Running without hardware acceleration."},
		viewer.Ended{Reason: "done", Summary: "The task is finished."},
		viewer.Pong{T: 12.5},
	}
}

func TestEveryMessageFollowsTheSchemaAndHasItsType(t *testing.T) {
	server, serverOnly := wsSchema(t, "server_message"), wsSchema(t, "client_message")
	h := startHub(t)
	c := h.dial(t, h.login())
	hello := readJSON(t, c)
	if hello["type"] != "hello" {
		t.Fatalf("first message: %v", hello)
	}
	for _, m := range messages() {
		b, err := viewer.Encode(m)
		if err != nil {
			t.Fatalf("%T: %v", m, err)
		}
		if !strings.HasPrefix(string(b), `{"type":"`+m.MessageType()+`"`) {
			t.Errorf("%T: %s", m, b)
		}
		if err := valid(server, b); err != nil {
			t.Errorf("%T does not follow the schema: %v\n%s", m, err, b)
		}
		if err := valid(serverOnly, b); err == nil && m.MessageType() != "chat" {
			t.Errorf("%T is also a message of the page: %s", m, b)
		}
		h.hub.Send(m)
		got := readJSON(t, c)
		if got["type"] != m.MessageType() {
			t.Errorf("sent %T, got %v", m, got)
		}
	}

	// The hello message too, as the hub makes it.
	hb, _ := json.Marshal(hello)
	if err := valid(server, hb); err != nil {
		t.Errorf("hello: %v\n%s", err, hb)
	}
	if hello["proto"].(float64) != viewer.ProtoVersion || hello["csrf"] != h.s.CSRF() || hello["session"] != sampleHello.Session || hello["mode"] != "vision" {
		t.Errorf("hello: %v", hello)
	}
	if acc := hello["accel"].(map[string]any); acc["used"] != "tcg" || acc["fallback_reason"] != "WHPX is not available" {
		t.Errorf("accel: %v", acc)
	}
}

func TestTheSchemaRefusesWhatItShould(t *testing.T) {
	server, client := wsSchema(t, "server_message"), wsSchema(t, "client_message")
	for name, text := range map[string]string{
		"no type":                   `{"state":"running","reason":""}`,
		"unknown type":              `{"type":"explode"}`,
		"a state that is none":      `{"type":"state","state":"napping","reason":""}`,
		"an extra field":            `{"type":"cursor","x":1,"y":2,"z":3}`,
		"a missing field":           `{"type":"cursor","x":1}`,
		"a string for a coordinate": `{"type":"cursor","x":"1","y":2}`,
		"a bad verdict":             `{"type":"trace","action_id":"a","turn":1,"tool":"t","args":{},"reason":"","verdict":"maybe","rules":[],"status":"x","duration_ms":1,"summary":""}`,
		"a bad id":                  `{"type":"confirm_resolved","id":"a b","outcome":"approved"}`,
		"a bad outcome":             `{"type":"confirm_resolved","id":"q","outcome":"shrugged"}`,
		"a negative count":          `{"type":"stats","kbps":1,"fps":1,"frame_bytes":-1,"llm_ms":1,"actions":1}`,
		"a bad level":               `{"type":"notice","level":"fatal","text":"x"}`,
		"a bad role":                `{"type":"chat","id":"m","role":"god","text":"x","ts":"2026-10-18T10:00:00Z"}`,
		"a bad time":                `{"type":"chat","id":"m","role":"user","text":"x","ts":"yesterday"}`,
	} {
		if valid(server, []byte(text)) == nil {
			t.Errorf("server: %s was accepted: %s", name, text)
		}
	}
	for name, text := range map[string]string{
		"no text":      `{"type":"chat"}`,
		"empty text":   `{"type":"chat","text":""}`,
		"no approve":   `{"type":"confirm","id":"q"}`,
		"a bad op":     `{"type":"control","op":"reboot"}`,
		"a bad src":    `{"type":"control","op":"kill","src":"voice"}`,
		"a text ping":  `{"type":"ping","t":"now"}`,
		"extra":        `{"type":"ping","t":1,"u":2}`,
		"a server one": `{"type":"state","state":"running","reason":""}`,
	} {
		if valid(client, []byte(text)) == nil {
			t.Errorf("client: %s was accepted: %s", name, text)
		}
	}
	for _, text := range []string{
		`{"type":"chat","text":"hello"}`, `{"type":"confirm","id":"q1","approve":true}`, `{"type":"confirm","id":"q1","approve":false,"note":"no"}`,
		`{"type":"control","op":"pause"}`, `{"type":"control","op":"kill","src":"hotkey"}`, `{"type":"ping","t":1.5}`,
	} {
		if err := valid(client, []byte(text)); err != nil {
			t.Errorf("client: %s: %v", text, err)
		}
	}
}

func TestAPageThatConnectsLaterSeesWhatItMissed(t *testing.T) {
	h := startHub(t)
	hub := h.hub
	hub.Send(viewer.State{State: viewer.StateRunning, Reason: "one"})
	hub.Send(viewer.State{State: viewer.StatePaused, Reason: "two"}) // only the last state is kept
	hub.Send(viewer.Audit{Line: "a1"})
	hub.Send(viewer.Audit{Line: "a2"})
	hub.Send(viewer.Chat{ID: "c1", Role: "user", Text: "hi", TS: ts})
	hub.Send(viewer.Trace{ActionID: "t1", Tool: "mouse", Verdict: "allow", Status: "done"})
	hub.Send(viewer.Notice{Level: viewer.LevelInfo, Text: "n"})
	hub.Send(viewer.Plan{ActionID: "p1", Tool: "mouse"}) // a plan is gone by the time a page comes
	hub.Send(viewer.Stats{KBps: 1})                      // and stats are stale
	hub.Send(viewer.ConfirmRequest{ID: "q1", ActionID: "a", Tool: "exec", ExpiresAt: ts})
	hub.Send(viewer.ConfirmRequest{ID: "q2", ActionID: "b", Tool: "key", ExpiresAt: ts})
	hub.Send(viewer.ConfirmResolved{ID: "q1", Outcome: viewer.OutcomeApproved})
	hub.Send(viewer.Cursor{X: 1, Y: 2})
	hub.Send(viewer.Cursor{X: 7, Y: 8})
	for seq := uint32(1); seq <= 2; seq++ {
		if err := hub.SendFrame(seq, 1280, 800, []byte("RIFFwebp"+fmt.Sprint(seq))); err != nil {
			t.Fatal(err)
		}
	}
	if hub.Connected() {
		t.Fatal("connected before anybody connected")
	}

	c := h.dial(t, h.login())
	var types []string
	var lines []string
	var pending []string
	for len(types) < 9 {
		m := readJSON(t, c)
		types = append(types, m["type"].(string))
		switch m["type"] {
		case "audit":
			lines = append(lines, m["line"].(string))
		case "confirm_request":
			pending = append(pending, m["id"].(string))
		case "state":
			if m["reason"] != "two" {
				t.Errorf("state: %v", m)
			}
		case "cursor":
			if m["x"].(float64) != 7 || m["y"].(float64) != 8 {
				t.Errorf("cursor: %v", m)
			}
		}
	}
	want := "hello state audit audit chat trace notice confirm_request cursor"
	f := read(t, c)
	seq, w, ht, payload, err := viewer.DecodeFrame(f.data)
	if f.typ != websocket.MessageBinary || err != nil || seq != 2 || w != 1280 || ht != 800 || string(payload) != "RIFFwebp2" {
		t.Errorf("frame: %v seq %d %dx%d %q (%v)", f.typ, seq, w, ht, payload, err)
	}
	if got := strings.Join(types, " "); got != want {
		t.Fatalf("order:\n got %s\nwant %s", got, want)
	}
	if strings.Join(lines, ",") != "a1,a2" || strings.Join(pending, ",") != "q2" {
		t.Errorf("audit %v, open confirmations %v", lines, pending)
	}
}
