// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/PacifAIst/controlsnack/internal/config"
	"github.com/PacifAIst/controlsnack/internal/exitcode"
	"github.com/PacifAIst/controlsnack/internal/llm/backend"
	"github.com/PacifAIst/controlsnack/internal/llm/detect"
	"github.com/PacifAIst/controlsnack/internal/llm/parse"
	"github.com/PacifAIst/controlsnack/internal/platform"
	"github.com/PacifAIst/controlsnack/internal/safety/audit"
	"github.com/PacifAIst/controlsnack/internal/viewer"
	"github.com/PacifAIst/controlsnack/internal/vm/qemu"
)

// fullRig is a runner whose VM, model and viewer window are fakes: the guest is a small desktop, the model follows a script.
type fullRig struct {
	*rig
	finder  *fakeFinder
	model   *scriptedModel
	agent   *guestAgent
	monitor *fakeMonitor
	mu      sync.Mutex
	launch  []viewer.LaunchOptions
}

func newFullRig(t *testing.T) *fullRig {
	t.Helper()
	f := &fullRig{rig: newRig(t), model: &scriptedModel{}, agent: newGuestAgent(1280, 800), monitor: &fakeMonitor{w: 1280, h: 800, shade: 3}}
	f.finder = localFinder()
	f.finder.backend = f.model
	f.mach.agent, f.mach.monitor = f.agent, f.monitor
	f.r.deps.models = func(config.Config, platform.Dirs) ModelFinder { return f.finder }
	f.r.deps.launch = func(o viewer.LaunchOptions) (*viewer.Window, error) {
		f.mu.Lock()
		f.launch = append(f.launch, o)
		f.mu.Unlock()
		return &viewer.Window{Notice: "The viewer is open in a window of a fake browser."}, nil
	}
	return f
}

func headlessConfig(task string) config.Config {
	cfg := config.Defaults()
	cfg.Viewer.Mode = viewer.ModeNone
	cfg.Session.Task = task
	return cfg
}

// auditRecords reads the session's log back, after checking its chain.
func (f *fullRig) auditRecords() []audit.Record {
	f.t.Helper()
	rep, lines := f.auditLog()
	if rep == nil || !rep.OK() {
		f.t.Fatalf("the audit log does not verify: %+v", rep)
	}
	var out []audit.Record
	for _, ln := range lines {
		var r audit.Record
		if err := json.Unmarshal([]byte(ln), &r); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func recordsOf(recs []audit.Record, kind, event string) []audit.Record {
	var out []audit.Record
	for _, r := range recs {
		if r.Kind == kind && (event == "" || r.Event == event) {
			out = append(out, r)
		}
	}
	return out
}

func TestAHeadlessTaskRunsFromTheBootToTheEndAndTheLogVerifies(t *testing.T) {
	f := newFullRig(t)
	f.model.replies = []reply{
		calls(tc("c1", "exec", `{"command":"uname -r","timeout_s":30}`)),
		calls(tc("c2", "done", `{"success":true,"summary":"the kernel is 6.1.0-cs"}`)),
	}
	if err := f.run(context.Background(), headlessConfig("Print the kernel version")); err != nil {
		t.Fatalf("the session: %v\n%s", err, f.out.String())
	}
	out := f.out.String()
	for _, want := range []string{"Model: qwen (VISION)", "Running the task: Print the kernel version", "Task finished in 2 turns: the kernel is 6.1.0-cs",
		"ended (task done)", "2 turns, 2 actions (2 allowed)", "Accelerator: tcg", "Audit log:", "  turn 1, exec ", "  turn 2, done "} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	if !f.mach.closed.Load() {
		t.Error("the VM was not stopped in order")
	}
	recs := f.auditRecords()
	start := recordsOf(recs, "lifecycle", "start")
	if len(start) != 1 || !strings.Contains(string(start[0].Data), `"mode":"run"`) || !strings.Contains(string(start[0].Data), `"remote_llm":false`) ||
		!strings.Contains(string(start[0].Data), `"prompt_template_sha256"`) || !strings.Contains(string(start[0].Data), `"model":"qwen"`) {
		t.Errorf("start %+v", start)
	}
	if len(recordsOf(recs, "config", "effective")) != 1 || len(recordsOf(recs, "llm", "prompt")) != 1 || len(recordsOf(recs, "llm", "response")) != 2 {
		t.Errorf("config, prompt or answers missing")
	}
	if len(recordsOf(recs, "lifecycle", "kill")) != 0 {
		t.Error("an orderly end is not a kill")
	}
	end := recordsOf(recs, "lifecycle", "end")
	if len(end) != 1 || !strings.Contains(string(end[0].Data), "task done") || recs[len(recs)-1].Event != "end" {
		t.Errorf("end %+v", end)
	}
	// The command ran in the guest, once, after its decision was on record.
	if n := len(recordsOf(recs, "action", "decision")); n != 1 {
		t.Errorf("%d decisions", n)
	}
	if got := f.agent.types(); strings.Count(got, "exec") != 1 {
		t.Errorf("the agent was called: %s", got)
	}
}

func TestAHeadlessTaskTheModelGivesUpOnOrCannotDoIsAFailure(t *testing.T) {
	f := newFullRig(t)
	f.model.replies = []reply{calls(tc("c1", "done", `{"success":false,"summary":"there is no such file"}`))}
	err := f.run(context.Background(), headlessConfig("Find it"))
	if exitcode.Of(err) != exitcode.Failure || !strings.Contains(err.Error(), "the model gave up: there is no such file") {
		t.Errorf("error %v", err)
	}
	g := newFullRig(t)
	g.model.replies = []reply{{err: &backend.Error{Kind: backend.KindAuth, Backend: "openai", Message: "bad key"}}}
	err = g.run(context.Background(), headlessConfig("Find it"))
	if exitcode.Of(err) != exitcode.Failure || !strings.Contains(err.Error(), "bad key") {
		t.Errorf("error %v", err)
	}
	if !g.mach.closed.Load() {
		t.Error("the VM was left running")
	}
}

func TestACtrlCDuringATaskIsAKillAndLeavesNoVm(t *testing.T) {
	f := newFullRig(t)
	started := make(chan struct{})
	f.model.replies = []reply{{hook: func(ctx context.Context) {
		close(started)
		<-ctx.Done()
	}, err: context.Canceled}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.run(ctx, headlessConfig("Wait")) }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the model was never asked")
	}
	cancel()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end")
	}
	if exitcode.Of(err) != exitcode.Killed {
		t.Errorf("error %v (%d)", err, exitcode.Of(err))
	}
	if !f.mach.proc.killed.Load() {
		t.Error("QEMU is still running")
	}
	recs := f.auditRecords()
	if k := recordsOf(recs, "lifecycle", "kill"); len(k) != 1 || !strings.Contains(string(k[0].Data), "signal") {
		t.Errorf("kill %+v", k)
	}
	if !strings.Contains(f.out.String(), "ended (killed: signal)") {
		t.Errorf("the output:\n%s", f.out.String())
	}
}

func TestWhenNoModelCanBeFoundTheVmIsNotStarted(t *testing.T) {
	f := newFullRig(t)
	f.finder.resolveErr = &detect.NoServerError{}
	err := f.run(context.Background(), headlessConfig("Anything"))
	var ns *detect.NoServerError
	if !errors.As(err, &ns) {
		t.Fatalf("error %v", err)
	}
	if f.boots() != 0 {
		t.Error("a VM was started although there is no model")
	}
	if !strings.Contains(f.out.String(), "LM Studio") && !strings.Contains(f.out.String(), "Ollama") {
		t.Errorf("the person is not told how to start a server:\n%s", f.out.String())
	}
}

func TestAModelOnAnotherMachineIsSaidAtTheStartAndInTheLog(t *testing.T) {
	f := newFullRig(t)
	f.finder.target.Local, f.finder.target.BaseURL = false, "https://api.example.org/v1"
	f.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	if err := f.run(context.Background(), headlessConfig("Anything")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), "NOTE: The model runs on api.example.org, not on this machine") {
		t.Errorf("the output:\n%s", f.out.String())
	}
	start := recordsOf(f.auditRecords(), "lifecycle", "start")
	if len(start) != 1 || !strings.Contains(string(start[0].Data), `"remote_llm":true`) {
		t.Errorf("start %+v", start)
	}
}

func TestAPolicyFileThatCannotBeReadIsAMistakeOfTheUserBeforeAnythingStarts(t *testing.T) {
	f := newFullRig(t)
	cfg := headlessConfig("Anything")
	cfg.Safety.Policy = f.dirs.Config + "/nope.yaml"
	if err := f.run(context.Background(), cfg); exitcode.Of(err) != exitcode.Usage || f.boots() != 0 {
		t.Errorf("error %v, %d boots", err, f.boots())
	}
	rules := f.dirs.Config + "/policy.yaml"
	if err := os.MkdirAll(f.dirs.Config, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rules, []byte("version: 1\nrules:\n  - id: USER-NO-LS\n    class: deny\n    explain: no listing\n    match:\n      - command: ls\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Safety.Policy = rules
	f.model.replies = []reply{calls(tc("c1", "exec", `{"command":"ls","timeout_s":10}`)), calls(tc("c2", "done", `{"success":false,"summary":"refused"}`))}
	_ = f.run(context.Background(), cfg)
	if tools := toolTexts(f.model.requests()[1]); len(tools) != 1 || !strings.Contains(tools[0], "USER-NO-LS") {
		t.Errorf("the user's rule was not in force: %q", tools)
	}
}

func TestTheSessionTimeEndsATaskThatWouldNotEnd(t *testing.T) {
	f := newFullRig(t)
	f.model.replies = []reply{{hook: func(ctx context.Context) { <-ctx.Done() }, err: context.Canceled}}
	cfg := headlessConfig("Wait")
	cfg.Safety.Timeout = config.Duration(200 * time.Millisecond)
	err := f.run(context.Background(), cfg)
	if exitcode.Of(err) != exitcode.Timeout {
		t.Errorf("error %v (%d)", err, exitcode.Of(err))
	}
	if !f.mach.closed.Load() || f.mach.proc.killed.Load() {
		t.Error("a timeout is an orderly end")
	}
	if !strings.Contains(f.out.String(), "ended (session timeout)") {
		t.Errorf("the output:\n%s", f.out.String())
	}
}

func TestAGuestThatShutsDownEndsTheSession(t *testing.T) {
	f := newFullRig(t)
	f.model.replies = []reply{{hook: func(ctx context.Context) { f.mach.proc.exit(0); <-ctx.Done() }, err: context.Canceled}}
	err := f.run(context.Background(), headlessConfig("Wait"))
	if err != nil {
		t.Errorf("error %v", err)
	}
	if !strings.Contains(f.out.String(), "ended (the guest shut down)") {
		t.Errorf("the output:\n%s", f.out.String())
	}
}

func TestTheNetworkPortIsAskedForOnlyWithAllowNet(t *testing.T) {
	for _, net := range []bool{false, true} {
		f := newFullRig(t)
		f.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
		cfg := headlessConfig("Anything")
		cfg.Net.Allow = net
		if err := f.run(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		o := f.options[0]
		f.mu.Unlock()
		if o.Features.Net != net {
			t.Errorf("allow-net=%v: the agent was asked for the proxy: %v", net, o.Features.Net)
		}
	}
}

// page is the viewer's page as a test plays it: logged in, connected, reading.
type page struct {
	t    *testing.T
	conn *websocket.Conn
	mu   sync.Mutex
	msgs []map[string]any

	// what the host has sent this page, and how much of it was pictures
	bytes, frames atomic.Int64

	// how it got in, to connect again with the same cookie
	host   string
	client *http.Client
	cookie *http.Cookie
}

func openPage(t *testing.T, link string) *page {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, link, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == viewer.CookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatalf("no cookie from the login: %s", resp.Status)
	}
	return connectPage(t, u.Host, client, cookie)
}

// connectPage opens the socket of a page that has logged in.
func connectPage(t *testing.T, host string, client *http.Client, cookie *http.Cookie) *page {
	t.Helper()
	p, err := dialPage(t, host, client, cookie)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func dialPage(t *testing.T, host string, client *http.Client, cookie *http.Cookie) (*page, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, wresp, err := websocket.Dial(ctx, "ws://"+host+"/ws", &websocket.DialOptions{HTTPClient: client,
		HTTPHeader: http.Header{"Origin": {"http://" + host}, "Cookie": {cookie.String()}}, CompressionMode: websocket.CompressionDisabled})
	if wresp != nil && wresp.Body != nil {
		_ = wresp.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(8 << 20)
	p := &page{t: t, conn: c, host: host, client: client, cookie: cookie}
	go p.read()
	t.Cleanup(func() { _ = c.CloseNow() })
	return p, nil
}

// again is the same window connecting anew, as after a break in the connection: the host takes one page at a time, so this
// closes the page it is called on and waits for the host to notice.
func (p *page) again() *page {
	p.t.Helper()
	_ = p.conn.CloseNow()
	deadline := time.Now().Add(5 * time.Second)
	for {
		q, err := dialPage(p.t, p.host, p.client, p.cookie)
		if err == nil {
			return q
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("the host did not take the page back: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (p *page) read() {
	for {
		typ, data, err := p.conn.Read(context.Background())
		if err != nil {
			return
		}
		p.bytes.Add(int64(len(data)))
		if typ != websocket.MessageText {
			p.frames.Add(1)
			continue
		}
		var m map[string]any
		if json.Unmarshal(data, &m) == nil {
			p.mu.Lock()
			p.msgs = append(p.msgs, m)
			p.mu.Unlock()
		}
	}
}

func (p *page) send(v any) {
	p.t.Helper()
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.conn.Write(ctx, websocket.MessageText, b); err != nil {
		p.t.Fatal(err)
	}
}

// waitFor waits for a message that the predicate accepts, and returns it.
func (p *page) waitFor(what string, pred func(map[string]any) bool) map[string]any {
	p.t.Helper()
	return p.waitWithin(10*time.Second, what, pred)
}

// waitWithin is waitFor with the time to wait given: a real guest is slow.
func (p *page) waitWithin(d time.Duration, what string, pred func(map[string]any) bool) map[string]any {
	p.t.Helper()
	deadline := time.Now().Add(d)
	for {
		p.mu.Lock()
		for _, m := range p.msgs {
			if pred(m) {
				p.mu.Unlock()
				return m
			}
		}
		p.mu.Unlock()
		if time.Now().After(deadline) {
			p.t.Fatalf("timed out waiting for %s; got %d messages", what, len(p.msgs))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func ofType(typ string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["type"] == typ }
}

func TestThePageTypesATaskWatchesItAndKillsTheSession(t *testing.T) {
	f := newFullRig(t)
	f.model.replies = []reply{
		{text: "Looking.", calls: []parse.ToolCall{tc("c1", "exec", `{"command":"uname -r","timeout_s":30}`)}},
		calls(tc("c2", "done", `{"success":true,"summary":"all done"}`)),
	}
	cfg := config.Defaults()
	cfg.Viewer.Mode = viewer.ModeBrowser
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.run(ctx, cfg) }()
	eventually(t, "the viewer to be launched", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.launch) == 1 })
	f.mu.Lock()
	opts := f.launch[0]
	f.mu.Unlock()
	if opts.Mode != viewer.ModeBrowser || !strings.HasPrefix(opts.URL, "http://127.0.0.1:") || !strings.Contains(f.out.String(), "fake browser") {
		t.Fatalf("launch options %+v\n%s", opts, f.out.String())
	}
	pg := openPage(t, opts.URL)
	hello := pg.waitFor("the hello", ofType("hello"))
	if hello["model"] != "qwen" || hello["mode"] != "vision" || hello["backend"] != "lmstudio" || hello["remote_llm"] != false {
		t.Errorf("hello %+v", hello)
	}
	pg.waitFor("the session to be ready", func(m map[string]any) bool { return m["type"] == "state" && m["state"] == "ready" })
	// With no task on the command line the terminal says the task is to be typed in the viewer.
	if out := f.out.String(); !strings.Contains(out, "Ready. Give the model a task in the viewer.") || strings.Contains(out, "Running the task") {
		t.Errorf("the terminal does not say where the task goes:\n%s", out)
	}

	// The person types the task in the chat, and the model's work comes back as chat lines, traces and notices.
	pg.send(map[string]any{"type": "chat", "text": "Print the kernel version"})
	pg.waitFor("the echo of the person's words", func(m map[string]any) bool { return m["type"] == "chat" && m["role"] == "user" })
	pg.waitFor("a state of running", func(m map[string]any) bool { return m["type"] == "state" && m["state"] == "running" })
	// The page's tail of the audit log gets the decision about the command, as the line `audit show` prints.
	al := pg.waitFor("the audit line of the decision", func(m map[string]any) bool {
		line, _ := m["line"].(string)
		return m["type"] == "audit" && strings.Contains(line, "decision") && strings.Contains(line, "exec")
	})
	if line := al["line"].(string); !strings.Contains(line, "-> allow") {
		t.Errorf("the audit line %q does not say what was decided", line)
	}
	tr := pg.waitFor("the command's trace", func(m map[string]any) bool {
		return m["type"] == "trace" && m["tool"] == "exec" && m["status"] == "done"
	})
	if tr["verdict"] != "allow" {
		t.Errorf("trace %+v", tr)
	}
	pg.waitFor("the end of the task", func(m map[string]any) bool {
		return m["type"] == "notice" && strings.Contains(m["text"].(string), "Task finished in 2 turns: all done")
	})
	pg.waitFor("ready again", func(m map[string]any) bool { return m["type"] == "state" && m["state"] == "ready" })

	// Kill from the page ends everything.
	pg.send(map[string]any{"type": "control", "op": "kill", "src": "button"})
	select {
	case err := <-done:
		if exitcode.Of(err) != exitcode.Killed {
			t.Errorf("error %v (%d)", err, exitcode.Of(err))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end")
	}
	if !f.mach.proc.killed.Load() {
		t.Error("QEMU is still running")
	}
	recs := f.auditRecords()
	if len(recordsOf(recs, "user", "chat")) != 1 || len(recordsOf(recs, "user", "viewer_auth")) != 1 {
		t.Errorf("the person's words or the login are not in the log")
	}
	if k := recordsOf(recs, "lifecycle", "kill"); len(k) != 1 || !strings.Contains(string(k[0].Data), "button") {
		t.Errorf("kill %+v", k)
	}
}

func TestATaskGivenOnTheCommandLineIsRunWithTheViewerOpenAndTheTerminalSaysSo(t *testing.T) {
	f := newFullRig(t)
	f.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"all done"}`))}
	cfg := config.Defaults()
	cfg.Viewer.Mode = viewer.ModeBrowser
	cfg.Session.Task = "Print the kernel version"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.run(ctx, cfg) }()
	eventually(t, "the task to be run", func() bool { return strings.Contains(f.out.String(), "Task finished in 1 turn") })
	out := f.out.String()
	if !strings.Contains(out, "Running the task: Print the kernel version\nWatch it in the viewer.") || strings.Contains(out, "Give the model a task in the viewer") {
		t.Errorf("the terminal does not say a task is running:\n%s", out)
	}
	cancel()
	select {
	case err := <-done:
		if exitcode.Of(err) != exitcode.Killed {
			t.Errorf("error %v (%d)", err, exitcode.Of(err))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end")
	}
}

func TestTheDeadmanPausesTheTaskWhenThePageGoesAndEndsTheSessionAfterAMinute(t *testing.T) {
	f := newFullRig(t)
	ft := &fakeTimers{}
	f.r.deps.afterFunc = ft.after
	var sess *Session
	var smu sync.Mutex
	f.r.deps.assembled = func(s *Session) { smu.Lock(); sess = s; smu.Unlock() }
	f.model.replies = []reply{{hook: func(ctx context.Context) { <-ctx.Done() }, err: context.Canceled}}
	cfg := config.Defaults()
	cfg.Viewer.Mode = viewer.ModeBrowser
	cfg.Session.Task = "Wait"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.run(ctx, cfg) }()
	eventually(t, "the viewer to be launched", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.launch) == 1 })
	f.mu.Lock()
	opts := f.launch[0]
	f.mu.Unlock()
	pg := openPage(t, opts.URL)
	pg.waitFor("a state of running", func(m map[string]any) bool { return m["type"] == "state" && m["state"] == "running" })
	state := func() State {
		smu.Lock()
		defer smu.Unlock()
		return sess.Machine.State()
	}
	_ = pg.conn.Close(websocket.StatusNormalClosure, "bye")
	// The page has gone: after three seconds the task is paused, and it stays paused.
	eventually(t, "the dead-man switch to have started its clocks", func() bool { ft.mu.Lock(); defer ft.mu.Unlock(); return len(ft.timers) >= 4 }) // two from when the window was opened, two from the page going
	ft.fire(3 * time.Second)
	eventually(t, "the task to be paused", func() bool { return state() == Paused })
	// A minute after, the session is ended by the kill switch.
	ft.fire(60 * time.Second)
	select {
	case err := <-done:
		if exitcode.Of(err) != exitcode.Killed {
			t.Errorf("error %v (%d)", err, exitcode.Of(err))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end")
	}
	if k := recordsOf(f.auditRecords(), "lifecycle", "kill"); len(k) != 1 || !strings.Contains(string(k[0].Data), "deadman") {
		t.Errorf("kill %+v", k)
	}
}

func TestWhatThePersonTypedIsCheckedBeforeWhatTheMachineHas(t *testing.T) {
	// No QEMU, and a policy file that is not there: the second is the mistake to fix first, and it is the cheaper one.
	f := newFullRig(t)
	f.r.deps.discover = func(context.Context, string) (qemu.Install, error) {
		return qemu.Install{}, errors.New("QEMU is not installed")
	}
	cfg := headlessConfig("Anything")
	cfg.Safety.Policy = f.dirs.Config + "/nope.yaml"
	if err := f.run(context.Background(), cfg); exitcode.Of(err) != exitcode.Usage {
		t.Errorf("a missing policy file: %v", err)
	}
	cfg = headlessConfig("Anything")
	cfg.VM.Resolution = "huge"
	if err := f.run(context.Background(), cfg); exitcode.Of(err) != exitcode.Usage {
		t.Errorf("a resolution that is not one: %v", err)
	}
	if err := f.run(context.Background(), headlessConfig("Anything")); exitcode.Of(err) != exitcode.Environment {
		t.Errorf("with nothing wrong in what was typed, no QEMU is the environment's: %v", err)
	}
}
