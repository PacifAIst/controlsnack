// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/PacifAIst/controlsnack/internal/safety/killswitch"
	"github.com/PacifAIst/controlsnack/internal/viewer"
)

func TestWhatThePersonDoesReachesTheSession(t *testing.T) {
	h := startHub(t)
	c := h.dial(t, h.login())
	readType(t, c, "hello")
	for _, m := range []string{
		`{"type":"chat","text":"  open the file manager  "}`,
		`{"type":"confirm","id":"q1","approve":true,"note":" fine "}`,
		`{"type":"confirm","id":"q2","approve":false}`,
		`{"type":"control","op":"pause"}`,
		`{"type":"control","op":"resume"}`,
		`{"type":"control","op":"stop"}`,
		`{"type":"control","op":"step_on"}`,
		`{"type":"control","op":"step_off"}`,
	} {
		send(t, c, m)
	}
	until(t, "the messages to arrive", func() bool { ch, cf, ct := h.in.snapshot(); return len(ch) == 1 && len(cf) == 2 && len(ct) == 5 })
	chats, confirms, controls := h.in.snapshot()
	if chats[0] != "open the file manager" {
		t.Errorf("chat %q", chats[0])
	}
	if confirms[0] != (viewer.ConfirmIn{ID: "q1", Approve: true, Note: "fine"}) || confirms[1] != (viewer.ConfirmIn{ID: "q2"}) {
		t.Errorf("confirms %+v", confirms)
	}
	if strings.Join(controls, " ") != "pause resume stop step_on step_off" {
		t.Errorf("controls %v", controls)
	}
	if h.k.Killed() {
		t.Error("something fired the kill switch")
	}
}

func TestKillFromTheSocketFiresTheKillSwitchWithWhatWasPressed(t *testing.T) {
	for msg, want := range map[string]killswitch.Source{
		`{"type":"control","op":"kill"}`:                killswitch.SourceButton,
		`{"type":"control","op":"kill","src":"button"}`: killswitch.SourceButton,
		`{"type":"control","op":"kill","src":"hotkey"}`: killswitch.SourceHotkey,
	} {
		h := startHub(t)
		c := h.dial(t, h.login())
		readType(t, c, "hello")
		send(t, c, msg)
		if res := h.waitKilled(); res.Source != want {
			t.Errorf("%s: %+v", msg, res)
		}
		if _, _, controls := h.in.snapshot(); len(controls) != 0 {
			t.Errorf("%s: the session was also told: %v", msg, controls)
		}
	}
}

func TestAFloodOfChatIsLimitedButTheKillIsNot(t *testing.T) {
	h := startHub(t, func(o *viewer.HubOptions) { o.Rate, o.Burst = 1, 5 })
	c := h.dial(t, h.login())
	readType(t, c, "hello")
	for i := range 60 {
		send(t, c, fmt.Sprintf(`{"type":"chat","text":"m%d"}`, i))
	}
	send(t, c, `{"type":"control","op":"kill"}`)
	h.waitKilled()
	st := h.hub.Stats()
	chats, _, _ := h.in.snapshot()
	if len(chats) < 5 || len(chats) > 9 || st.Limited < 50 {
		t.Errorf("%d chats got through, %d were limited", len(chats), st.Limited)
	}
	if chats[0] != "m0" {
		t.Errorf("the first message was %q", chats[0])
	}
	// The page was told once.
	notices := 0
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		_, data, err := c.Read(ctx)
		cancel()
		if err != nil {
			break
		}
		if strings.Contains(string(data), `"type":"notice"`) {
			notices++
		}
	}
	if notices != 1 {
		t.Errorf("%d notices", notices)
	}
}

func TestMessagesThatBreakTheRulesAreDroppedAndTooManyEndTheConnection(t *testing.T) {
	h := startHub(t)
	c := h.dial(t, h.login())
	readType(t, c, "hello")
	for _, m := range []string{
		`not json`, `[]`, `{"type":5}`, `{"type":"hello"}`, `{"type":"explode"}`, `{}`, `null`,
		`{"type":"chat","text":""}`, `{"type":"chat","text":"a","x":1}`, `{"type":"confirm","id":"a b","approve":true}`,
		`{"type":"control","op":"reboot"}`, `{"type":"ping"}`,
	} {
		send(t, c, m)
	}
	// None of that ended the connection, and a good message still works.
	send(t, c, `{"type":"ping","t":3}`)
	if pong := readType(t, c, "pong"); pong["t"].(float64) != 3 {
		t.Errorf("pong %v", pong)
	}
	st := h.hub.Stats()
	// hello, explode, {} and null are of no type the page sends; "not json", [] and {"type":5} are not messages at all.
	if st.Unknown != 4 || st.Invalid != 8 {
		t.Errorf("unknown %d, invalid %d", st.Unknown, st.Invalid)
	}
	if chats, confirms, controls := h.in.snapshot(); len(chats)+len(confirms)+len(controls) != 0 {
		t.Errorf("the session heard %v %v %v", chats, confirms, controls)
	}
	// A page that keeps sending them is cut off.
	for range 60 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"ping"}`))
		cancel()
		if err != nil {
			break
		}
	}
	if code := closed(t, c); code != websocket.StatusPolicyViolation {
		t.Errorf("closed with %d", code)
	}
}

func TestABinaryMessageFromThePageEndsTheConnection(t *testing.T) {
	h := startHub(t)
	c := h.dial(t, h.login())
	readType(t, c, "hello")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if code := closed(t, c); code != websocket.StatusUnsupportedData {
		t.Errorf("closed with %d", code)
	}
	<-h.gone
}

func TestAMessageOverTheLimitEndsTheConnection(t *testing.T) {
	h := startHub(t)
	c := h.dial(t, h.login())
	readType(t, c, "hello")
	big := `{"type":"chat","text":"` + strings.Repeat("a", viewer.MaxClientBytes) + `"}`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.Write(ctx, websocket.MessageText, []byte(big))
	if code := closed(t, c); code != websocket.StatusMessageTooBig {
		t.Errorf("closed with %d", code)
	}
	if chats, _, _ := h.in.snapshot(); len(chats) != 0 {
		t.Error("the session heard it")
	}
}

func TestPingIsAnsweredWithTheSameT(t *testing.T) {
	h := startHub(t)
	c := h.dial(t, h.login())
	readType(t, c, "hello")
	for _, tt := range []string{"12.5", "0", "-3", "1e3"} {
		send(t, c, `{"type":"ping","t":`+tt+`}`)
		pong := readType(t, c, "pong")
		var want float64
		_, _ = fmt.Sscan(tt, &want)
		if pong["t"].(float64) != want {
			t.Errorf("ping %s -> %v", tt, pong)
		}
	}
}

func TestCallbacksRunOncePerConnection(t *testing.T) {
	h := startHub(t)
	c1 := h.dial(t, h.login())
	readType(t, c1, "hello")
	<-h.connect
	if !h.hub.Connected() {
		t.Error("not connected")
	}
	_ = c1.Close(websocket.StatusNormalClosure, "bye")
	<-h.gone
	until(t, "the hub to see the page go", func() bool { return !h.hub.Connected() })
	c2 := h.dial(t, h.login())
	readType(t, c2, "hello")
	<-h.connect
	if got := h.hub.Stats().Connections; got != 2 {
		t.Errorf("%d connections", got)
	}
	select {
	case <-h.gone:
		t.Error("a second disconnect with the second page still there")
	default:
	}
}

func TestAWindowThatLogsInOverAnotherGetsItsHubConnectionClosed(t *testing.T) {
	h := startHub(t)
	old := h.login()
	c := h.dial(t, old)
	readType(t, c, "hello")
	fresh := h.login()
	if code := closed(t, c); code != websocket.StatusGoingAway {
		t.Errorf("closed with %d", code)
	}
	<-h.gone
	if _, err := h.tryDial(old); err == nil {
		t.Error("the old cookie still opens a socket")
	}
	c2 := h.dial(t, fresh)
	if hello := readJSON(t, c2); hello["type"] != "hello" {
		t.Errorf("%v", hello)
	}
}

func TestAMessageTooLargeToSendBecomesANotice(t *testing.T) {
	h := startHub(t)
	c := h.dial(t, h.login())
	readType(t, c, "hello")
	h.hub.Send(viewer.Chat{ID: "m1", Role: "assistant", Text: strings.Repeat("x", viewer.MaxMessageBytes), TS: ts})
	m := readJSON(t, c)
	if m["type"] != "notice" || m["level"] != "warn" || !strings.Contains(m["text"].(string), "chat") {
		t.Errorf("%v", m)
	}
	if _, err := viewer.Encode(viewer.Chat{Text: strings.Repeat("x", viewer.MaxMessageBytes)}); !errors.Is(err, viewer.ErrTooLarge) {
		t.Errorf("Encode: %v", err)
	}
}

func TestAPageThatDoesNotReadCannotBlockTheSession(t *testing.T) {
	h := startHub(t, func(o *viewer.HubOptions) { o.Queue, o.WriteTimeout = 8, 500*time.Millisecond })
	c := h.dial(t, h.login())
	_ = c // never read from
	<-h.connect

	big := strings.Repeat("x", viewer.MaxMessageBytes-1000)
	start := time.Now()
	for i := range 300 {
		h.hub.Send(viewer.Notice{Level: viewer.LevelInfo, Text: big})
		h.hub.Send(viewer.Cursor{X: i, Y: i})
		h.hub.Send(viewer.Audit{Line: "line"})
		if err := h.hub.SendFrame(uint32(i+1), 1280, 800, []byte(big)); err != nil {
			t.Fatal(err)
		}
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the session was held up for %v", took)
	}
	// The page that cannot keep up is let go; the session goes on.
	until(t, "the hub to cut the page off", func() bool { return !h.hub.Connected() })
	<-h.gone
	st := h.hub.Stats()
	if st.Overflows == 0 && st.FramesDropped == 0 {
		t.Errorf("nothing was dropped or cut off: %+v", st)
	}
	h.hub.Send(viewer.Notice{Level: viewer.LevelInfo, Text: "still fine"}) // and nothing breaks with no page
}

func TestAPageThatStopsAnsweringPingsIsLetGo(t *testing.T) {
	h := startHub(t, func(o *viewer.HubOptions) { o.PingEvery, o.PingTimeout = 50*time.Millisecond, 200*time.Millisecond })
	c := h.dial(t, h.login())
	_ = c // a client that does not read does not answer pings
	<-h.connect
	until(t, "the hub to let the page go", func() bool { return !h.hub.Connected() })
	<-h.gone
}

func TestHistoryIsBoundedAndOpenConfirmationsAreNotLost(t *testing.T) {
	h := startHub(t, func(o *viewer.HubOptions) { o.History = 5 })
	h.hub.Send(viewer.ConfirmRequest{ID: "q1", ActionID: "a", Tool: "exec", ExpiresAt: ts})
	for i := range 20 {
		h.hub.Send(viewer.Chat{ID: fmt.Sprintf("m%d", i), Role: "user", Text: fmt.Sprint(i), TS: ts})
		h.hub.Send(viewer.Audit{Line: fmt.Sprintf("l%d", i)})
	}
	c := h.dial(t, h.login())
	var chats, audits, confirms []string
	for len(chats)+len(audits)+len(confirms) < 11 {
		m := readJSON(t, c)
		switch m["type"] {
		case "chat":
			chats = append(chats, m["text"].(string))
		case "audit":
			audits = append(audits, m["line"].(string))
		case "confirm_request":
			confirms = append(confirms, m["id"].(string))
		}
	}
	if strings.Join(chats, ",") != "15,16,17,18,19" || strings.Join(audits, ",") != "l15,l16,l17,l18,l19" || strings.Join(confirms, ",") != "q1" {
		t.Errorf("chats %v audits %v confirmations %v", chats, audits, confirms)
	}
}
