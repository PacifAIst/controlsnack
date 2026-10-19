// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qmp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/platform"
)

const greeting = `{"QMP": {"version": {"qemu": {"micro": 2, "minor": 2, "major": 8}, "package": "Debian 1:8.2.2"}, "capabilities": ["oob"]}}`

// fake plays QEMU on one end of a pipe.
type fake struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
	wmu  sync.Mutex

	mu       sync.Mutex
	requests []map[string]any

	handler func(f *fake, req map[string]any)
}

func (f *fake) send(line string) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	_, _ = io.WriteString(f.conn, line+"\r\n")
}

func (f *fake) reply(req map[string]any, ret string) {
	f.send(fmt.Sprintf(`{"return": %s, "id": %v}`, ret, jsonID(req["id"])))
}

func (f *fake) fail(req map[string]any, class, desc string) {
	f.send(fmt.Sprintf(`{"error": {"class": %q, "desc": %q}, "id": %v}`, class, desc, jsonID(req["id"])))
}

func (f *fake) event(name, data string) {
	if data == "" {
		f.send(fmt.Sprintf(`{"event": %q, "timestamp": {"seconds": 1700000000, "microseconds": 5}}`, name))
		return
	}
	f.send(fmt.Sprintf(`{"event": %q, "data": %s, "timestamp": {"seconds": 1700000000, "microseconds": 5}}`, name, data))
}

func jsonID(v any) string { b, _ := json.Marshal(v); return string(b) }

func (f *fake) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		out = append(out, r["execute"].(string))
	}
	return out
}

func (f *fake) serve() {
	f.send(greeting)
	for {
		line, err := f.r.ReadString('\n')
		if err != nil {
			return
		}
		var req map[string]any
		if json.Unmarshal([]byte(line), &req) != nil {
			f.t.Errorf("the client sent something that is not JSON: %q", line)
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, req)
		f.mu.Unlock()
		switch {
		case req["execute"] == "qmp_capabilities":
			f.reply(req, `{}`)
		case f.handler != nil:
			f.handler(f, req)
		default:
			f.reply(req, `{}`)
		}
	}
}

// connect returns a negotiated client talking to a fake QEMU.
func connect(t *testing.T, handler func(f *fake, req map[string]any)) (*Client, *fake) {
	t.Helper()
	a, b := net.Pipe()
	f := &fake{t: t, conn: b, r: bufio.NewReader(b), handler: handler}
	go f.serve()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := NewClient(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(); _ = b.Close() })
	return c, f
}

func ctx5(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// --- handshake -------------------------------------------------------------------------------------------------

func TestHandshake(t *testing.T) {
	c, f := connect(t, nil)
	g := c.Greeting()
	if g.QMP.Version.QEMU.Major != 8 || g.QMP.Version.QEMU.Minor != 2 || g.QMP.Version.Package != "Debian 1:8.2.2" {
		t.Errorf("%+v", g)
	}
	if got := f.commands(); len(got) != 1 || got[0] != "qmp_capabilities" {
		t.Errorf("the first command must be qmp_capabilities, got %v", got)
	}
	if c.Err() != nil {
		t.Errorf("%v", c.Err())
	}
}

func TestHandshakeFailures(t *testing.T) {
	try := func(serve func(net.Conn), ctx context.Context) error {
		a, b := net.Pipe()
		go func() { serve(b); _ = b.Close() }()
		_, err := NewClient(ctx, a)
		return err
	}
	if err := try(func(net.Conn) {}, ctx5(t)); err == nil {
		t.Error("EOF instead of a greeting")
	}
	if err := try(func(c net.Conn) { _, _ = io.WriteString(c, "SSH-2.0-OpenSSH\r\n") }, ctx5(t)); err == nil || !strings.Contains(err.Error(), "did not greet like QEMU") {
		t.Errorf("%v", err)
	}
	if err := try(func(c net.Conn) { _, _ = io.WriteString(c, "{not json}\r\n") }, ctx5(t)); err == nil {
		t.Error("garbage greeting")
	}
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := try(func(c net.Conn) { time.Sleep(2 * time.Second) }, short); err == nil || time.Since(start) > time.Second {
		t.Errorf("a silent peer is given up on at the deadline: %v after %v", err, time.Since(start))
	}
	// QEMU greets but refuses the capabilities command.
	err := try(func(c net.Conn) {
		_, _ = io.WriteString(c, greeting+"\r\n")
		line, _ := bufio.NewReader(c).ReadString('\n')
		var req map[string]any
		_ = json.Unmarshal([]byte(line), &req)
		_, _ = io.WriteString(c, fmt.Sprintf(`{"error":{"class":"CommandNotFound","desc":"no"},"id":%v}`+"\r\n", jsonID(req["id"])))
	}, ctx5(t))
	var qe *Error
	if !errors.As(err, &qe) || qe.Class != "CommandNotFound" {
		t.Errorf("%v", err)
	}
}

// --- commands --------------------------------------------------------------------------------------------------

func TestExecuteAndErrors(t *testing.T) {
	c, f := connect(t, func(f *fake, req map[string]any) {
		switch req["execute"] {
		case "query-status":
			f.reply(req, `{"status":"running","singlestep":false,"running":true}`)
		case "stop":
			f.fail(req, "GenericError", "the VM is not in a state to stop")
		default:
			f.reply(req, `{}`)
		}
	})
	st, err := c.QueryStatus(ctx5(t))
	if err != nil || st.Status != "running" || !st.Running {
		t.Errorf("%+v %v", st, err)
	}
	err = c.Stop(ctx5(t))
	var qe *Error
	if !errors.As(err, &qe) || qe.Class != "GenericError" || !strings.Contains(err.Error(), "not in a state") {
		t.Errorf("%v", err)
	}
	if c.Err() != nil {
		t.Error("a QMP error reply does not end the connection")
	}
	if err := c.Cont(ctx5(t)); err != nil {
		t.Error(err)
	}
	if err := c.SystemPowerdown(ctx5(t)); err != nil {
		t.Error(err)
	}
	if got := f.commands(); strings.Join(got, ",") != "qmp_capabilities,query-status,stop,cont,system_powerdown" {
		t.Errorf("%v", got)
	}
}

func TestOnlyAllowedCommandsAreSent(t *testing.T) {
	c, f := connect(t, nil)
	for _, cmd := range []string{"human-monitor-command", "object-add", "netdev_add", "chardev-add", "blockdev-add", "migrate", "getfd",
		"device_add", "qom-set", "add-fd", "", "Quit", "quit ", "query-status\n"} {
		err := c.Execute(ctx5(t), cmd, map[string]string{"command-line": "info status"}, nil)
		if !errors.Is(err, ErrCommandNotAllowed) {
			t.Errorf("%q: %v", cmd, err)
		}
	}
	if got := f.commands(); len(got) != 1 {
		t.Errorf("nothing but the handshake reached QEMU: %v", got)
	}
}

func TestRepliesAreMatchedByID(t *testing.T) {
	var mu sync.Mutex
	var held []map[string]any
	c, _ := connect(t, func(f *fake, req map[string]any) {
		// Hold the first two requests and answer them in reverse order, after a third request has been answered.
		mu.Lock()
		defer mu.Unlock()
		held = append(held, req)
		if len(held) == 3 {
			for i := len(held) - 1; i >= 0; i-- {
				f.reply(held[i], fmt.Sprintf(`{"status":"s%d","running":false,"singlestep":false}`, i))
			}
		}
	})
	var wg sync.WaitGroup
	results := make([]string, 3)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := c.QueryStatus(ctx5(t))
			if err != nil {
				t.Error(err)
			}
			results[i] = st.Status
		}()
		time.Sleep(20 * time.Millisecond) // fix the order in which the requests reach the fake
	}
	wg.Wait()
	for i, got := range results {
		if got != fmt.Sprintf("s%d", i) {
			t.Errorf("caller %d got %q: replies must be matched by id", i, got)
		}
	}
}

func TestContextEndsAWaitButNotTheConnection(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	c, _ := connect(t, func(f *fake, req map[string]any) {
		if req["execute"] == "stop" {
			once.Do(func() { <-release })
		}
		f.reply(req, `{"status":"running","running":true,"singlestep":false}`)
	})
	short, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := c.Stop(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("%v", err)
	}
	close(release) // the late reply arrives and is discarded
	if st, err := c.QueryStatus(ctx5(t)); err != nil || !st.Running {
		t.Errorf("the connection survives a command that timed out: %+v %v", st, err)
	}
}

func TestQuit(t *testing.T) {
	// QEMU answers and then goes away.
	c, f := connect(t, func(f *fake, req map[string]any) {
		f.reply(req, `{}`)
		_ = f.conn.Close()
	})
	if err := c.Quit(ctx5(t)); err != nil {
		t.Errorf("%v", err)
	}
	_ = f
	// QEMU goes away without answering.
	c2, _ := connect(t, func(f *fake, req map[string]any) { _ = f.conn.Close() })
	if err := c2.Quit(ctx5(t)); err != nil {
		t.Errorf("a connection that ends because of quit is success, got %v", err)
	}
	// After the client was closed, quit cannot be sent.
	c3, _ := connect(t, nil)
	_ = c3.Close()
	if err := c3.Quit(ctx5(t)); !errors.Is(err, ErrClosed) {
		t.Errorf("%v", err)
	}
	// QEMU refuses.
	c4, _ := connect(t, func(f *fake, req map[string]any) { f.fail(req, "GenericError", "no") })
	var qe *Error
	if err := c4.Quit(ctx5(t)); !errors.As(err, &qe) {
		t.Errorf("%v", err)
	}
}

func TestConnectionLoss(t *testing.T) {
	c, f := connect(t, func(f *fake, req map[string]any) {
		if req["execute"] == "stop" {
			_ = f.conn.Close() // QEMU dies with a request in flight
		}
	})
	if err := c.Stop(ctx5(t)); err == nil || !IsConnectionEnd(err) {
		t.Errorf("a request in flight when QEMU exits fails with the connection error: %v", err)
	}
	select {
	case <-c.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done must close")
	}
	if !errors.Is(c.Err(), io.EOF) {
		t.Errorf("%v", c.Err())
	}
	start := time.Now()
	if _, err := c.QueryStatus(ctx5(t)); err == nil || time.Since(start) > time.Second {
		t.Errorf("commands on a dead connection fail at once: %v", err)
	}
	_ = f
}

func TestBadPeers(t *testing.T) {
	// An enormous message.
	c, f := connect(t, func(f *fake, req map[string]any) {
		f.send(`{"return": "` + strings.Repeat("x", maxMessage+10) + `", "id": 99}`)
	})
	_ = f
	if _, err := c.QueryStatus(ctx5(t)); err == nil {
		t.Error("an oversize message must end the connection")
	}
	if c.Err() == nil {
		t.Error("connection must be marked failed")
	}
	// Something that is not JSON.
	c2, _ := connect(t, func(f *fake, req map[string]any) { f.send("this is not json") })
	if _, err := c2.QueryStatus(ctx5(t)); err == nil || c2.Err() == nil {
		t.Errorf("%v", err)
	}
	// Replies for ids nobody asked about are ignored.
	c3, _ := connect(t, func(f *fake, req map[string]any) {
		f.send(`{"return": {}, "id": 424242}`)
		f.send(`{"return": {}, "id": "not-a-number"}`)
		f.send(`{"return": {}}`)
		f.reply(req, `{"status":"running","running":true,"singlestep":false}`)
	})
	if st, err := c3.QueryStatus(ctx5(t)); err != nil || !st.Running {
		t.Errorf("%+v %v", st, err)
	}
}

// --- events ----------------------------------------------------------------------------------------------------

func TestEventsAndPorts(t *testing.T) {
	c, f := connect(t, nil)
	if c.PortOpen("agent0") {
		t.Error("closed until the guest says otherwise")
	}
	f.event("RESUME", "")
	f.event("VSERPORT_CHANGE", `{"id":"agent0","open":true}`)
	// The event may already be in the history when the waiter starts.
	if err := c.WaitPortOpen(ctx5(t), "agent0"); err != nil {
		t.Fatal(err)
	}
	if !c.PortOpen("agent0") || c.PortOpen("net0") {
		t.Error("state per port")
	}
	ev, err := c.WaitEvent(ctx5(t), func(e Event) bool { return e.Name == EventResume })
	if err != nil || ev.Time.Unix() != 1700000000 {
		t.Errorf("an event from before the wait is still found: %+v %v", ev, err)
	}

	// A future event wakes a waiter.
	got := make(chan Event, 1)
	go func() {
		e, err := c.WaitEvent(ctx5(t), func(e Event) bool { return e.Name == EventShutdown })
		if err != nil {
			t.Error(err)
		}
		got <- e
	}()
	time.Sleep(50 * time.Millisecond)
	f.event("SHUTDOWN", `{"guest":false,"reason":"host-qmp-quit"}`)
	select {
	case e := <-got:
		var sd Shutdown
		if err := e.Decode(&sd); err != nil || sd.Guest || sd.Reason != "host-qmp-quit" {
			t.Errorf("%+v %v", sd, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the waiter never woke")
	}

	// The port closes (the stub restarts), and reopens.
	f.event("VSERPORT_CHANGE", `{"id":"agent0","open":false}`)
	deadline := time.Now().Add(2 * time.Second)
	for c.PortOpen("agent0") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c.PortOpen("agent0") {
		t.Fatal("close not seen")
	}
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := c.WaitPortOpen(short, "agent0"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a closed port is waited for: %v", err)
	}
	f.event("VSERPORT_CHANGE", `{"id":"agent0","open":true}`)
	if err := c.WaitPortOpen(ctx5(t), "agent0"); err != nil {
		t.Error(err)
	}
	if n := len(c.Events()); n != 5 {
		t.Errorf("%d events remembered", n)
	}
}

func TestEventsInterleavedWithReplies(t *testing.T) {
	c, _ := connect(t, func(f *fake, req map[string]any) {
		f.event("STOP", "")
		f.reply(req, `{"status":"paused","running":false,"singlestep":false}`)
		f.event("RESUME", "")
	})
	st, err := c.QueryStatus(ctx5(t))
	if err != nil || st.Status != "paused" {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := c.WaitEvent(ctx5(t), func(e Event) bool { return e.Name == "RESUME" }); err != nil {
		t.Error(err)
	}
}

func TestEventHistoryIsBounded(t *testing.T) {
	c, f := connect(t, nil)
	for i := 0; i < historySize+44; i++ {
		f.event("DEVICE_DELETED", fmt.Sprintf(`{"device":"d%d"}`, i))
	}
	f.event("SENTINEL", "")
	if _, err := c.WaitEvent(ctx5(t), func(e Event) bool { return e.Name == "SENTINEL" }); err != nil {
		t.Fatal(err)
	}
	evs := c.Events()
	if len(evs) != historySize {
		t.Fatalf("%d events kept, want %d", len(evs), historySize)
	}
	if !strings.Contains(string(evs[0].Data), `"d45"`) {
		t.Errorf("the oldest events are the ones dropped: %s", evs[0].Data)
	}
	// An event that has been dropped is not found, and the wait ends with the context, not with a hang.
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.WaitEvent(short, func(e Event) bool { return strings.Contains(string(e.Data), `"d0"`) }); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("%v", err)
	}
}

func TestWaitersEndWithTheConnection(t *testing.T) {
	c, f := connect(t, nil)
	errc := make(chan error, 2)
	go func() { _, err := c.WaitEvent(ctx5(t), func(Event) bool { return false }); errc <- err }()
	go func() { errc <- c.WaitPortOpen(ctx5(t), "agent0") }()
	time.Sleep(50 * time.Millisecond)
	_ = f.conn.Close()
	for range 2 {
		select {
		case err := <-errc:
			if err == nil || !IsConnectionEnd(err) {
				t.Errorf("%v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("waiters must be released when the connection ends")
		}
	}
}

// --- screendump and input ----------------------------------------------------------------------------------------

func TestScreendumpAndInputArguments(t *testing.T) {
	var got []map[string]any
	var mu sync.Mutex
	c, _ := connect(t, func(f *fake, req map[string]any) {
		mu.Lock()
		got = append(got, req)
		mu.Unlock()
		f.reply(req, `{}`)
	})
	if err := c.Screendump(ctx5(t), "/run/user/1000/cs-abc/shot.png", "png"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][2]string{{"relative.png", "png"}, {"", "png"}, {"/x/s.png", "jpeg"}, {"/x/s.png", ""}, {"/x/s\n.png", "png"}, {"/x/s\x00.png", "png"}} {
		if err := c.Screendump(ctx5(t), bad[0], bad[1]); err == nil {
			t.Errorf("screendump(%q, %q) accepted", bad[0], bad[1])
		}
	}
	if err := c.Screendump(ctx5(t), `C:\Users\u\AppData\Local\Temp\cs\shot.ppm`, "ppm"); err != nil {
		t.Errorf("a Windows path: %v", err)
	}

	move, err := MoveTo(640, 400, 1280, 800)
	if err != nil {
		t.Fatal(err)
	}
	down, _ := Button("left", true)
	up, _ := Button("left", false)
	key, _ := Key("ctrl", true)
	if err := c.InputSendEvent(ctx5(t), append(append(move, down, up), key)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if args := got[0]["arguments"].(map[string]any); args["filename"] != "/run/user/1000/cs-abc/shot.png" || args["format"] != "png" {
		t.Errorf("%v", args)
	}
	events := got[len(got)-1]["arguments"].(map[string]any)["events"].([]any)
	raw, _ := json.Marshal(events)
	for _, want := range []string{`{"data":{"axis":"x","value":`, `"axis":"y"`, `{"data":{"button":"left","down":true},"type":"btn"}`,
		`{"data":{"down":true,"key":{"data":"ctrl","type":"qcode"}},"type":"key"}`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("events lack %s:\n%s", want, raw)
		}
	}
	if err := c.InputSendEvent(ctx5(t), nil); err == nil {
		t.Error("no events")
	}
	if err := c.InputSendEvent(ctx5(t), make([]InputEvent, 257)); err == nil {
		t.Error("too many events")
	}
}

func TestInputBuilders(t *testing.T) {
	value := func(e InputEvent) int { return e.Data.(map[string]any)["value"].(int) }
	for _, tt := range []struct {
		x, y, w, h   int
		wantX, wantY int
	}{
		{0, 0, 1280, 800, 0, 0},
		{1279, 799, 1280, 800, 0x7fff, 0x7fff},
		{-50, 900, 1280, 800, 0, 0x7fff}, // clamped to the screen
		{5000, -1, 1280, 800, 0x7fff, 0},
		{640, 400, 1280, 800, 16396, 16404}, // round(x/(w-1) * 0x7fff)
	} {
		ev, err := MoveTo(tt.x, tt.y, tt.w, tt.h)
		if err != nil || len(ev) != 2 || ev[0].Type != "abs" || value(ev[0]) != tt.wantX || value(ev[1]) != tt.wantY {
			t.Errorf("MoveTo(%d,%d,%d,%d) = %v %v, want %d,%d", tt.x, tt.y, tt.w, tt.h, ev, err, tt.wantX, tt.wantY)
		}
	}
	if _, err := MoveTo(1, 1, 1, 800); err == nil {
		t.Error("a screen one pixel wide")
	}
	for _, name := range []string{"left", "right", "middle", "wheel-up", "wheel-down"} {
		if _, err := Button(name, true); err != nil {
			t.Error(name, err)
		}
	}
	for _, bad := range []string{"", "Left", "left,right", "back"} {
		if _, err := Button(bad, true); err == nil {
			t.Errorf("button %q accepted", bad)
		}
	}
	for _, good := range []string{"a", "ret", "ctrl", "f5", "kp_0", "shift_r", "esc"} {
		if _, err := Key(good, false); err != nil {
			t.Error(good, err)
		}
	}
	for _, bad := range []string{"", "CTRL", "ctrl+l", "a b", strings.Repeat("a", 33), "é"} {
		if _, err := Key(bad, true); err == nil {
			t.Errorf("key %q accepted", bad)
		}
	}
}

// --- dialing ---------------------------------------------------------------------------------------------------

func TestDialRejectsNonLoopbackEndpoints(t *testing.T) {
	for _, tt := range [][2]string{{"tcp", "0.0.0.0:4444"}, {"tcp", "10.0.0.5:4444"}, {"tcp", "localhost:4444"}, {"tcp", "[::1]:4444"},
		{"tcp", "4444"}, {"udp", "127.0.0.1:4444"}, {"unix", ""}, {"", "x"}} {
		if _, err := Dial(ctx5(t), tt[0], tt[1], DialOptions{}); err == nil {
			t.Errorf("%v accepted", tt)
		}
	}
}

func TestDialRetriesUntilQEMUServes(t *testing.T) {
	ln, err := platform.ListenLoopback(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	// The first connections are accepted and dropped, the way QEMU's socket exists before anything serves it.
	go func() {
		for i := 0; ; i++ {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			if i < 2 {
				_ = conn.Close()
				continue
			}
			f := &fake{t: t, conn: conn, r: bufio.NewReader(conn)}
			go f.serve()
		}
	}()
	c, err := Dial(ctx5(t), "tcp", ln.Addr().String(), DialOptions{Interval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if st, err := c.QueryStatus(ctx5(t)); err != nil || st.Status != "" {
		t.Errorf("%+v %v", st, err)
	}
}

func TestDialGivesUpWhenTheContextEnds(t *testing.T) {
	ln, err := platform.ListenLoopback(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listens there now
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Dial(ctx, "tcp", addr, DialOptions{Interval: 10 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Errorf("%v after %v", err, time.Since(start))
	}
}

// QEMU often answers a command and exits at once. A reply that arrived before the connection was seen to end is a
// reply: select picks at random between "a reply is waiting" and "the connection is gone", which this loop makes
// likely enough to catch.
func TestAReplyThatArrivesBeforeTheConnectionClosesIsDelivered(t *testing.T) {
	for i := range 300 {
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			br := bufio.NewReader(b)
			_, _ = io.WriteString(b, greeting+"\r\n")
			for range 2 { // qmp_capabilities, then query-status
				line, err := br.ReadString('\n')
				if err != nil {
					return
				}
				var req map[string]any
				_ = json.Unmarshal([]byte(line), &req)
				ret := `{}`
				if req["execute"] == "query-status" {
					ret = `{"status":"shutdown","running":false}`
				}
				_, _ = io.WriteString(b, fmt.Sprintf(`{"return":%s,"id":%s}`+"\r\n", ret, jsonID(req["id"])))
			}
		}() // returns, and closes the pipe, right after the last reply
		c, err := NewClient(ctx5(t), a)
		if err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		st, err := c.QueryStatus(ctx5(t))
		if err != nil || st.Status != "shutdown" {
			t.Fatalf("round %d: the reply was lost to the closing connection: %+v %v", i, st, err)
		}
		_ = c.Close()
	}
}
