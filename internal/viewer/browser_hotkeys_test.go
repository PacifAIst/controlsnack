// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/PacifAIst/controlsnack-local/internal/safety/killswitch"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
)

// async evaluates an expression that gives a promise, and returns what it resolves to.
func (p *page) async(expr string) string {
	p.t.Helper()
	s, err := chromedp.Run(p.ctx, chromedp.Evaluate[string](expr, chromedp.EvalAwaitPromise))
	if err != nil {
		p.t.Fatalf("browser: %v\n(%s)", err, expr)
	}
	return s
}

// The detector is given key events with the times they are said to have happened at, so that the timing is exact.
const hotkeyScenarios = `(async () => {
  const { KillHotkeys } = await import('/hotkeys.js');
  const out = {};
  const ev = (t, extra = {}) => ({ key: 'Escape', timeStamp: t, isTrusted: true, defaultPrevented: false, isComposing: false, repeat: false, ctrlKey: false, shiftKey: false, altKey: false, metaKey: false, preventDefault() { this.defaultPrevented = true; }, ...extra });
  const run = (name, events) => {
    const kills = [], hints = [];
    const h = new KillHotkeys({ kill: (src) => kills.push(src), hint: (t) => hints.push(t) });
    const results = events.map((e) => h.onKeyDown(e));
    out[name] = { kills: kills.join(','), results: results.join(','), hints: hints.filter((t) => t !== '').join('|') };
  };
  run('three quick', [ev(0), ev(500), ev(1000)]);
  run('three at the limit', [ev(0), ev(600), ev(1200)]);
  run('three just too slow', [ev(0), ev(600), ev(1201)]);
  run('slide to the next', [ev(0), ev(600), ev(1300), ev(1400)]);
  run('two only', [ev(0), ev(100)]);
  run('four quick', [ev(0), ev(100), ev(200), ev(300), ev(400)]);
  run('a held key', [ev(0), ev(30, { repeat: true }), ev(60, { repeat: true }), ev(90, { repeat: true })]);
  run('taken by the dialog', [ev(0), ev(100, { defaultPrevented: true }), ev(200)]);
  run('during an IME composition', [ev(0, { isComposing: true }), ev(100, { isComposing: true }), ev(200, { isComposing: true })]);
  run('not a person', [ev(0, { isTrusted: false }), ev(100, { isTrusted: false }), ev(200, { isTrusted: false })]);
  run('with shift', [ev(0, { shiftKey: true }), ev(100, { shiftKey: true }), ev(200, { shiftKey: true })]);
  run('with alt', [ev(0, { altKey: true }), ev(100, { altKey: true }), ev(200, { altKey: true })]);
  run('with meta', [ev(0, { metaKey: true }), ev(100, { metaKey: true }), ev(200, { metaKey: true })]);
  run('another key', [ev(0, { key: 'Enter' }), ev(100, { key: 'Enter' }), ev(200, { key: 'Enter' })]);
  run('ctrl shift esc', [ev(0, { ctrlKey: true, shiftKey: true })]);
  run('ctrl esc', [ev(0, { ctrlKey: true }), ev(100, { ctrlKey: true }), ev(200, { ctrlKey: true })]);
  run('ctrl shift alt esc', [ev(0, { ctrlKey: true, shiftKey: true, altKey: true })]);
  run('ctrl shift esc, taken', [ev(0, { ctrlKey: true, shiftKey: true, defaultPrevented: true })]);
  run('a kill by the combination clears the count', [ev(0), ev(100), ev(200, { ctrlKey: true, shiftKey: true }), ev(300), ev(400)]);
  return JSON.stringify(out);
})()`

func TestTheKillHotkeysCountTheRightKeys(t *testing.T) {
	h, p := startPage(t)
	p.open(h.rig)
	p.wait("the page", `document.getElementById('session').textContent === 's_1234abcd'`)
	var got map[string]struct{ Kills, Results, Hints string }
	if err := json.Unmarshal([]byte(p.async(hotkeyScenarios)), &got); err != nil {
		t.Fatal(err)
	}
	// For each scenario: the kills it made, and for each key whether the detector said it was a kill.
	want := map[string]struct{ kills, results string }{
		"three quick":               {"hotkey", "false,false,true"},
		"three at the limit":        {"hotkey", "false,false,true"},
		"three just too slow":       {"", "false,false,false"},
		"slide to the next":         {"hotkey", "false,false,false,true"},
		"two only":                  {"", "false,false"},
		"four quick":                {"hotkey", "false,false,true,false,false"}, // after a kill the count starts over
		"a held key":                {"", "false,false,false,false"},
		"taken by the dialog":       {"", "false,false,false"},
		"during an IME composition": {"", "false,false,false"},
		"not a person":              {"", "false,false,false"},
		"with shift":                {"", "false,false,false"},
		"with alt":                  {"", "false,false,false"},
		"with meta":                 {"", "false,false,false"},
		"another key":               {"", "false,false,false"},
		"ctrl shift esc":            {"hotkey", "true"},
		"ctrl esc":                  {"", "false,false,false"},
		"ctrl shift alt esc":        {"", "false"},
		"ctrl shift esc, taken":     {"", "false"},
		"a kill by the combination clears the count": {"hotkey", "false,false,true,false,false"},
	}
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("%s: not run", name)
			continue
		}
		if g.Kills != w.kills || g.Results != w.results {
			t.Errorf("%s: kills %q results %q, want kills %q results %q", name, g.Kills, g.Results, w.kills, w.results)
		}
	}
	if len(got) != len(want) {
		t.Errorf("%d scenarios ran, %d are expected", len(got), len(want))
	}
	// The person is told how far they are, and a held key does not make progress.
	if g := got["three quick"].Hints; g != "Press Esc 2 more times to kill the session.|Press Esc 1 more time to kill the session." {
		t.Errorf("hints %q", g)
	}
	if g := got["a held key"].Hints; g != "Press Esc 2 more times to kill the session." {
		t.Errorf("a held key gave hints %q", g)
	}
	p.clean()
}

func TestEscThreeTimesKillsTheSession(t *testing.T) {
	h, p := startPage(t)
	p.open(h.rig)
	p.wait("the page", `document.getElementById('session').textContent === 's_1234abcd'`)
	p.str(`(() => { window.fetch = () => Promise.reject(new Error('blocked by the test')); return ''; })()`) // the socket alone, so that the source is the key's
	p.do(chromedp.KeyEvent(kb.Escape))
	p.wait("the hint", `!document.getElementById('hotkey-hint').hidden && document.getElementById('hotkey-hint').textContent.includes('2 more times')`)
	if h.k.Killed() {
		t.Fatal("one Esc killed the session")
	}
	p.do(chromedp.KeyEvent(kb.Escape), chromedp.KeyEvent(kb.Escape))
	if res := h.waitKilled(); res.Source != killswitch.SourceHotkey {
		t.Errorf("the kill: %+v", res)
	}
	p.wait("the hint to go", `document.getElementById('hotkey-hint').hidden`)
}

func TestCtrlShiftEscKillsTheSession(t *testing.T) {
	h, p := startPage(t)
	p.open(h.rig)
	p.wait("the page", `document.getElementById('session').textContent === 's_1234abcd'`)
	p.str(`(() => { window.fetch = () => Promise.reject(new Error('blocked by the test')); return ''; })()`)
	p.do(chromedp.KeyEvent(kb.Escape, chromedp.KeyModifiers(chromedp.ModifierCtrl|chromedp.ModifierShift)))
	if res := h.waitKilled(); res.Source != killswitch.SourceHotkey {
		t.Errorf("the kill: %+v", res)
	}
}

func TestEscAnswersTheDialogsAndDoesNotKill(t *testing.T) {
	// Three Esc presses in a row, with three approval requests waiting: each one rejects a request, and none of them counts
	// towards the kill.
	h, p := startPage(t)
	for _, id := range []string{"q1", "q2", "q3"} {
		h.hub.Send(confirmReq(id, "exec"))
	}
	p.open(h.rig)
	p.wait("the dialog", hasDialog)
	// An Esc that the dialog takes never reaches a listener on the window: that is the first of the two things that keep it from counting.
	p.str(`(() => { window.__esc = 0; window.__prevented = 'unseen'; window.addEventListener('keydown', (e) => { if (e.key === 'Escape') window.__esc++; }); document.addEventListener('keydown', (e) => { if (e.key === 'Escape') window.__prevented = String(e.defaultPrevented); }, true); return ''; })()`) // the second listener is on the document, after the dialog's: it sees what the dialog did to the event
	next := []string{"Request 1 of 2 waiting", "Needs your decision", ""}                                                                                                                                                                                                                                       // what the next dialog says about itself
	for i, id := range []string{"q1", "q2", "q3"} {
		p.do(chromedp.KeyEvent(kb.Escape))
		until(t, "the answer", func() bool { return len(h.confirms()) == i+1 })
		if h.confirms()[i].ID != id || h.confirms()[i].Approve {
			t.Errorf("answer %d: %+v", i, h.confirms()[i])
		}
		h.hub.Send(viewer.ConfirmResolved{ID: id, Outcome: viewer.OutcomeRejected})
		if i < 2 {
			p.wait("the next request", `document.querySelector(".modal-pos") && document.querySelector(".modal-pos").textContent === "`+next[i]+`"`)
		}
	}
	p.wait("the dialog to close", noDialog)
	time.Sleep(300 * time.Millisecond)
	if h.k.Killed() {
		t.Error("the Esc presses that answered the dialogs killed the session")
	}
	if got := p.str(`String(window.__esc)`); got != "0" {
		t.Errorf("%s Esc events reached the window past the dialog", got)
	}
	if got := p.str(`window.__prevented`); got != "true" {
		t.Errorf("the dialog left the Esc event's default alone (%s)", got)
	}
	if got := p.str(`String(document.getElementById('hotkey-hint').hidden)`); got != "true" {
		t.Error("the hint shows for presses that were answers")
	}
}

func TestTheKillButtonSaysWhichKeysKill(t *testing.T) {
	h, p := startPage(t)
	p.open(h.rig)
	p.wait("the page", `document.getElementById('kill') !== null`)
	title := p.str(`document.getElementById('kill').title`)
	for _, want := range []string{"Ctrl+Shift+Esc", "Esc three times"} {
		if !strings.Contains(title, want) {
			t.Errorf("the tooltip %q does not name %q", title, want)
		}
	}
}

func TestTheHintGoesAwayByItself(t *testing.T) {
	h, p := startPage(t)
	p.open(h.rig)
	p.wait("the page", `document.getElementById('session').textContent === 's_1234abcd'`)
	p.do(chromedp.KeyEvent(kb.Escape))
	p.wait("the hint", `!document.getElementById('hotkey-hint').hidden`)
	p.wait("the hint to go", `document.getElementById('hotkey-hint').hidden`) // after the 1.2 s in which a second press would have counted
	if h.k.Killed() {
		t.Error("one Esc killed the session")
	}
}
