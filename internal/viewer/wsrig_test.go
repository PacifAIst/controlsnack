// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/PacifAIst/controlsnack-local/internal/viewer"
	"github.com/PacifAIst/controlsnack-local/schema"
)

// inbox records what the hub hands to the session.
type inbox struct {
	mu       sync.Mutex
	chats    []string
	confirms []viewer.ConfirmIn
	controls []string
}

func (i *inbox) Chat(text string) {
	i.mu.Lock()
	i.chats = append(i.chats, text)
	i.mu.Unlock()
}

func (i *inbox) Confirm(id string, approve bool, note string) {
	i.mu.Lock()
	i.confirms = append(i.confirms, viewer.ConfirmIn{ID: id, Approve: approve, Note: note})
	i.mu.Unlock()
}

func (i *inbox) Control(op string) {
	i.mu.Lock()
	i.controls = append(i.controls, op)
	i.mu.Unlock()
}

func (i *inbox) snapshot() (chats []string, confirms []viewer.ConfirmIn, controls []string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.chats...), append([]viewer.ConfirmIn(nil), i.confirms...), append([]string(nil), i.controls...)
}

var sampleHello = viewer.Hello{
	Session: "s_1234abcd", Backend: "lmstudio", Model: "qwen3-vl-4b-instruct", Mode: "vision", Net: false, Unsafe: false, RemoteLLM: false,
	Accel:      viewer.Accel{Requested: "auto", Used: "tcg", FallbackReason: "WHPX is not available"},
	Limits:     viewer.Limits{MaxTurns: 100, MaxActions: 60, TimeoutSec: 3600, ConfirmTimeoutSec: 60, IdleTimeoutSec: 600},
	Resolution: viewer.Resolution{W: 1280, H: 800},
	StartedAt:  "2026-10-18T10:00:00.000Z",
}

// hubRig is a viewer server whose socket is a real hub.
type hubRig struct {
	*rig
	hub     *viewer.Hub
	in      *inbox
	connect chan struct{}
	gone    chan struct{}
}

func startHub(t *testing.T, mod ...func(*viewer.HubOptions)) *hubRig {
	t.Helper()
	return startHubWith(t, nil, mod...)
}

// startHubWith is startHub with a change to the server's options too (the front-end it serves, for instance).
func startHubWith(t *testing.T, serverMod func(*viewer.Options), mod ...func(*viewer.HubOptions)) *hubRig {
	t.Helper()
	h := &hubRig{in: &inbox{}, connect: make(chan struct{}, 16), gone: make(chan struct{}, 16)}
	signal := func(c chan struct{}) {
		select {
		case c <- struct{}{}:
		default:
		}
	}
	var r *rig
	r = start(t, func(o *viewer.Options) {
		ho := viewer.HubOptions{
			CSRF: func() string { return r.s.CSRF() }, Hello: func() viewer.Hello { return sampleHello }, Inbound: h.in, Killer: o.Killer,
			OnConnect:    func() { signal(h.connect) },
			OnDisconnect: func() { signal(h.gone) },
			Now:          o.Now,
		}
		for _, m := range mod {
			m(&ho)
		}
		hub, err := viewer.NewHub(ho)
		if err != nil {
			t.Fatal(err)
		}
		h.hub = hub
		o.WebSocket = hub
		if serverMod != nil {
			serverMod(o)
		}
	})
	h.rig = r
	return h
}

var noProxy = &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}

// dial opens the socket the way the page does, with the cookie and the origin.
func (r *rig) dial(t *testing.T, cookie *http.Cookie) *websocket.Conn {
	t.Helper()
	c, err := r.tryDial(cookie)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

func (r *rig) tryDial(cookie *http.Cookie) (*websocket.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h := http.Header{"Origin": {r.s.Origin()}, "Cookie": {cookie.String()}}
	c, resp, err := websocket.Dial(ctx, "ws://"+r.s.Addr()+"/ws", &websocket.DialOptions{HTTPHeader: h, HTTPClient: noProxy, CompressionMode: websocket.CompressionDisabled})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(8 << 20)
	return c, nil
}

// frame is what a read returned.
type frame struct {
	typ  websocket.MessageType
	data []byte
}

// text reads the next message, which must be text, as a JSON object.
func readJSON(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	f := read(t, c)
	if f.typ != websocket.MessageText {
		t.Fatalf("expected a text message, got binary of %d bytes", len(f.data))
	}
	var m map[string]any
	if err := json.Unmarshal(f.data, &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, f.data)
	}
	return m
}

func read(t *testing.T, c *websocket.Conn) frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("reading from the socket: %v", err)
	}
	return frame{typ, data}
}

// readType reads until a text message of the given type and returns it.
func readType(t *testing.T, c *websocket.Conn, typ string) map[string]any {
	t.Helper()
	for {
		f := read(t, c)
		if f.typ != websocket.MessageText {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(f.data, &m); err != nil {
			t.Fatalf("not JSON: %v", err)
		}
		if m["type"] == typ {
			return m
		}
	}
}

// send writes a text message to the host.
func send(t *testing.T, c *websocket.Conn, text string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte(text)); err != nil {
		t.Fatalf("writing to the socket: %v", err)
	}
}

// closed waits for the host to end the connection and returns the status it gave.
func closed(t *testing.T, c *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for {
		_, _, err := c.Read(ctx)
		if err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

// until polls a condition for up to five seconds.
func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(min(5*time.Second, pollLimit()))
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// wsSchema compiles one definition of schema/viewer-ws.schema.json.
func wsSchema(t testing.TB, def string) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema.ViewerWS))
	if err != nil {
		t.Fatal(err)
	}
	const id = "https://controlsnack.com/schema/v1/viewer-ws.json"
	c := jsonschema.NewCompiler()
	c.AssertFormat() // date-time is checked, not only noted
	if err := c.AddResource(id, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(id + "#/$defs/" + def)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func valid(s *jsonschema.Schema, text []byte) error {
	var v any
	if err := json.Unmarshal(text, &v); err != nil {
		return err
	}
	return s.Validate(v)
}
