// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/PacifAIst/controlsnack/internal/safety/killswitch"
	"github.com/PacifAIst/controlsnack/internal/viewer"
)

// The host's clock in these tests is the rig's pretend one, 2026-10-17 12:00:00 UTC, which is a day away from the browser's:
// a countdown that is right proves the page corrects for the difference.
func confirmReq(id, tool string) viewer.ConfirmRequest {
	return viewer.ConfirmRequest{
		ID: id, ActionID: "a-" + id, Tool: tool, Args: json.RawMessage(`{"command":"rm -r ~/work/old"}`),
		Rules:  []viewer.Rule{{ID: "X-RM", Explanation: "removes files and folders"}, {ID: "X-HOME", Explanation: "touches the home directory"}},
		Reason: "clean up the work directory", ExpiresAt: "2026-10-17T12:00:42.000Z",
	}
}

const (
	hasDialog = `document.querySelector('[role=alertdialog]') !== null`
	noDialog  = `document.querySelector('[role=alertdialog]') === null`
	activeID  = `document.activeElement ? document.activeElement.id : ''`
)

func (h *hubRig) confirms() []viewer.ConfirmIn {
	_, c, _ := h.in.snapshot()
	return c
}

func TestTheApprovalDialogShowsWhatTheRulesMatchedAndAsksForTheDecision(t *testing.T) {
	h, p := startPage(t)
	h.hub.Send(viewer.State{State: viewer.StateAwaitingConfirm, Reason: "waiting for you"})
	h.hub.Send(confirmReq("q1", "exec"))
	p.open(h.rig)
	p.wait("the dialog", hasDialog)

	if got := p.str(`document.getElementById('confirm-title').textContent`); got != "The model wants to run: exec" {
		t.Errorf("title %q", got)
	}
	if got := p.str(`document.getElementById('confirm-why').textContent`); got != "clean up the work directory" {
		t.Errorf("reason %q", got)
	}
	if got := p.strs(`Array.from(document.querySelectorAll('.rules li')).map(li => li.textContent.trim())`); strings.Join(got, "|") != "X-RM removes files and folders|X-HOME touches the home directory" {
		t.Errorf("rules %q", got)
	}
	if got := p.str(`document.querySelector('.modal-args').textContent`); !strings.Contains(got, `"command": "rm -r ~/work/old"`) {
		t.Errorf("the action %q", got)
	}
	if got := p.str(`document.querySelector('.modal-count').textContent`); !strings.HasPrefix(got, "Rejected automatically in 0:") {
		t.Errorf("countdown %q", got) // right only if the page corrected for the host's clock
	}
	if got := p.str(`document.querySelector('.modal-pos').textContent`); got != "Needs your decision" {
		t.Errorf("position %q", got)
	}

	// Reject has the focus; the rest of the page is out of reach, except the Kill button.
	if got := p.str(activeID); got != "confirm-reject" {
		t.Errorf("focus is on %q", got)
	}
	if got := p.str(`String(document.getElementById('main').inert)`); got != "true" {
		t.Error("the page behind the dialog can be used")
	}
	if got := p.str(`String(document.getElementById('kill').disabled)`); got != "false" {
		t.Error("the Kill button is disabled while the dialog is open")
	}
	if got := p.str(`document.querySelector('[role=alertdialog]').getAttribute('aria-modal')`); got != "true" {
		t.Errorf("aria-modal %q", got)
	}
	p.shot("viewer-dialog")

	// Esc rejects. The dialog stays until the host has taken the answer.
	p.do(chromedp.KeyEvent(kb.Escape))
	until(t, "the answer", func() bool { return len(h.confirms()) == 1 })
	if c := h.confirms()[0]; c.ID != "q1" || c.Approve {
		t.Errorf("answer %+v", c)
	}
	p.wait("the status", `document.querySelector('.modal-status').textContent.startsWith('Sent.') && document.getElementById('confirm-reject').disabled`)
	h.hub.Send(viewer.ConfirmResolved{ID: "q1", Outcome: viewer.OutcomeRejected})
	p.wait("the dialog to close", noDialog)
	if got := p.str(`String(document.getElementById('main').inert)`); got != "false" {
		t.Error("the page is still out of reach")
	}
	if n := p.str(`String(document.querySelectorAll('.notice').length)`); n != "0" {
		t.Error("the person is told what they just did")
	}
	p.clean()
}

func TestApprovingNeedsARealClickAndTakesTheNote(t *testing.T) {
	h, p := startPage(t)
	h.hub.Send(confirmReq("q1", "exec"))
	p.open(h.rig)
	p.wait("the dialog", hasDialog)
	// A script cannot answer for the person: not on Reject, and not on Approve once the pause is over either.
	p.wait("Approve to be ready", `!document.getElementById('confirm-approve').hasAttribute('aria-disabled')`)
	p.str(`(() => { document.getElementById('confirm-approve').click(); document.getElementById('confirm-reject').click(); return ''; })()`)
	time.Sleep(200 * time.Millisecond)
	if got := h.confirms(); len(got) != 0 {
		t.Fatalf("a script answered: %+v", got)
	}
	// A real click on Approve answers, with the note.
	p.do(chromedp.Focus("#confirm-note"), chromedp.KeyEvent(" it is my own folder "), chromedp.Click("#confirm-approve"))
	until(t, "the answer", func() bool { return len(h.confirms()) == 1 })
	if c := h.confirms()[0]; c != (viewer.ConfirmIn{ID: "q1", Approve: true, Note: "it is my own folder"}) {
		t.Errorf("answer %+v", c)
	}
	h.hub.Send(viewer.ConfirmResolved{ID: "q1", Outcome: viewer.OutcomeApproved})
	p.wait("the dialog to close", noDialog)
	p.clean()
}

func TestEnterOnTheFocusedButtonRejects(t *testing.T) {
	h, p := startPage(t)
	h.hub.Send(confirmReq("q1", "exec"))
	p.open(h.rig)
	p.wait("the dialog", hasDialog)
	p.do(chromedp.KeyEvent(kb.Enter)) // Reject has the focus, so a hasty Enter is a refusal
	until(t, "the answer", func() bool { return len(h.confirms()) == 1 })
	if c := h.confirms()[0]; c.Approve {
		t.Errorf("Enter approved: %+v", c)
	}
}

func TestTheApproveButtonWaitsBeforeItAnswers(t *testing.T) {
	// A dialog that appears under the pointer must not take the click meant for what was there. The page's own dialog waits
	// 700 ms; here a copy of it with a delay that is long, and one with none, are put on the page and clicked at once.
	h, p := startPage(t)
	p.open(h.rig)
	p.wait("the page", `document.getElementById('session').textContent === 's_1234abcd'`)
	p.str(`(() => { import('/confirm.js').then((m) => { window.__m = m; }); return ''; })()`)
	p.wait("the module", `window.__m !== undefined`)
	setup := func(delay int) string {
		return fmt.Sprintf(`(() => {
  if (window.__c) window.__c.clear();
  for (const e of document.querySelectorAll('.scratch')) e.remove();
  const mk = (tag) => { const e = document.createElement(tag); e.className = 'scratch'; document.body.append(e); return e; };
  const root = mk('div'), main = mk('div'), kill = mk('button');
  window.__answers = [];
  window.__c = new window.__m.Confirmations({ root, main, killButton: kill, answer: (id, a, n) => { window.__answers.push([id, a]); return true; }, notice() {}, skew: () => 0, approveDelayMs: %d });
  window.__c.add({ id: 'x1', tool: 'exec', args: {}, rules: [], reason: 'r', expires_at: new Date(Date.now() + 60000).toISOString() });
  return '';
})()`, delay)
	}
	p.str(setup(60000))
	p.wait("the copy's dialog", hasDialog)
	if got := p.str(`document.getElementById('confirm-approve').getAttribute('aria-disabled')`); got != "true" {
		t.Errorf("aria-disabled %q", got)
	}
	p.do(chromedp.Click("#confirm-approve"))
	time.Sleep(300 * time.Millisecond)
	if got := p.str(`JSON.stringify(window.__answers)`); got != "[]" {
		t.Errorf("a click in the pause answered: %s", got)
	}
	p.do(chromedp.Click("#confirm-reject")) // Reject is never held back
	p.wait("the rejection", `JSON.stringify(window.__answers) === '[["x1",false]]'`)

	p.str(setup(0))
	p.wait("the copy's dialog", hasDialog)
	if got := p.str(`String(document.getElementById('confirm-approve').hasAttribute('aria-disabled'))`); got != "false" {
		t.Error("an Approve with no pause is held back")
	}
	p.do(chromedp.Click("#confirm-approve"))
	p.wait("the approval", `JSON.stringify(window.__answers) === '[["x1",true]]'`)
}

func TestSeveralRequestsAreAskedOneAfterAnother(t *testing.T) {
	h, p := startPage(t)
	for _, id := range []string{"q1", "q2", "q3"} {
		h.hub.Send(confirmReq(id, "exec"))
	}
	p.open(h.rig)
	p.wait("the dialog", hasDialog)
	if got := p.str(`document.querySelector('.modal-pos').textContent`); got != "Request 1 of 3 waiting" {
		t.Errorf("position %q", got)
	}
	// The host closes the first (the person answered somewhere else, or it ran out of time): the next one comes up.
	h.hub.Send(viewer.ConfirmResolved{ID: "q1", Outcome: viewer.OutcomeExpired})
	p.wait("the next request", `document.querySelector('.modal-pos').textContent === 'Request 1 of 2 waiting'`)
	if got := p.str(activeID); got != "confirm-reject" {
		t.Errorf("focus is on %q", got)
	}
	if got := p.str(`document.querySelector('.notice-warn .notice-text').textContent`); got != "The request to run exec expired before it was answered." {
		t.Errorf("notice %q", got)
	}
	p.do(chromedp.KeyEvent(kb.Escape))
	until(t, "the answer", func() bool { return len(h.confirms()) == 1 })
	if h.confirms()[0].ID != "q2" {
		t.Errorf("the answer was for %q", h.confirms()[0].ID)
	}
	h.hub.Send(viewer.ConfirmResolved{ID: "q2", Outcome: viewer.OutcomeRejected})
	p.wait("the last one", `document.querySelector('.modal-pos').textContent === 'Needs your decision'`)
	h.hub.Send(viewer.ConfirmResolved{ID: "q3", Outcome: viewer.OutcomeCanceled})
	p.wait("the dialog to close", noDialog)
}

func TestAnOpenRequestSurvivesAReloadAndIsShownOnce(t *testing.T) {
	h, p := startPage(t)
	h.hub.Send(confirmReq("q1", "exec"))
	p.open(h.rig)
	p.wait("the dialog", hasDialog)
	p.do(chromedp.Reload())
	p.wait("the dialog again", hasDialog)
	time.Sleep(300 * time.Millisecond)
	if n := p.str(`String(document.querySelectorAll('[role=alertdialog]').length)`); n != "1" {
		t.Errorf("%s dialogs", n)
	}
	if got := p.str(`document.querySelector('.modal-pos').textContent`); got != "Needs your decision" {
		t.Errorf("position %q", got)
	}
}

func TestTheKillButtonWorksWithTheDialogOpen(t *testing.T) {
	h, p := startPage(t)
	h.hub.Send(confirmReq("q1", "exec"))
	p.open(h.rig)
	p.wait("the dialog", hasDialog)
	p.do(chromedp.Click("#kill"))
	if res := h.waitKilled(); res.Source != killswitch.SourceButton && res.Source != killswitch.SourceAPI {
		t.Errorf("the kill: %+v", res)
	}
	h.hub.Send(viewer.State{State: viewer.StateKilled, Reason: "you pressed the button"})
	p.wait("the dialog to go", noDialog) // there is nothing left to decide
}

func TestTabStaysInsideTheDialogAndTheKillButton(t *testing.T) {
	h, p := startPage(t)
	h.hub.Send(confirmReq("q1", "exec"))
	p.open(h.rig)
	p.wait("the dialog", hasDialog)
	var order []string
	for range 5 {
		order = append(order, p.str(activeID))
		p.do(chromedp.KeyEvent(kb.Tab))
	}
	// Reject, then Approve, then the Kill button, then the note, and round to Reject again.
	if got := strings.Join(order, ","); got != "confirm-reject,confirm-approve,kill,confirm-note,confirm-reject" {
		t.Errorf("focus went through %q", got)
	}
	// And backwards from where it is now (on Approve): Reject, the note, then round to the Kill button.
	var back []string
	for range 3 {
		p.do(chromedp.KeyEvent(kb.Tab, chromedp.KeyModifiers(chromedp.ModifierShift)))
		back = append(back, p.str(activeID))
	}
	if got := strings.Join(back, ","); got != "confirm-reject,confirm-note,kill" {
		t.Errorf("Shift+Tab went through %q", got)
	}
}

func TestADialogIsNotLeftBehindWhenTheSessionEnds(t *testing.T) {
	h, p := startPage(t)
	h.hub.Send(confirmReq("q1", "exec"))
	p.open(h.rig)
	p.wait("the dialog", hasDialog)
	h.hub.Send(viewer.Ended{Reason: "done", Summary: "over"})
	h.hub.Send(viewer.State{State: viewer.StateEnded, Reason: "done"})
	p.wait("the dialog to go", noDialog)
	if got := p.str(`String(document.getElementById('main').inert)`); got != "false" {
		t.Error("the page is still out of reach")
	}
}

func TestTheApprovalDialogIsReadableInBothColorSchemes(t *testing.T) {
	for _, scheme := range []string{"dark", "light"} {
		t.Run(scheme, func(t *testing.T) {
			h, p := startPage(t)
			p.scheme(scheme)
			h.frame(t, 1, desktop(1280, 800, 2))
			h.hub.Send(confirmReq("q1", "exec"))
			h.hub.Send(confirmReq("q2", "key"))
			p.open(h.rig)
			p.wait("the dialog", hasDialog)
			p.str(`(() => { document.getElementById('confirm-reject').click(); return ''; })()`) // a script's click does nothing: it only shows the status text below
			p.str(`(() => { document.querySelector('.modal-status').textContent = 'The host has not answered yet. You can try again.'; return ''; })()`)
			const js = `(() => {
  const parse = (s) => { const p = s.match(/rgba?\(([^)]+)\)/)[1].split(',').map(parseFloat); return { r: p[0], g: p[1], b: p[2], a: p.length > 3 ? p[3] : 1 }; };
  const lum = (c) => { const f = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }; return 0.2126 * f(c.r) + 0.7152 * f(c.g) + 0.0722 * f(c.b); };
  const bg = (el) => { for (let e = el; e; e = e.parentElement) { const c = parse(getComputedStyle(e).backgroundColor); if (c.a > 0.95) return c; } return { r: 255, g: 255, b: 255, a: 1 }; };
  const sels = ['.modal-pos', '.modal-title', '.modal-why', '.modal-section h3', '.rule-id', '.rule-text', '.modal-args', '.modal-note span', '.modal-note input', '.modal-count', '.modal-status', '.btn-reject', '.btn-approve'];
  return sels.map((s) => { const el = document.querySelector(s); if (!el) return s + ': missing'; const l1 = lum(parse(getComputedStyle(el).color)), l2 = lum(bg(el)); return s + ': ' + ((Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05)).toFixed(2); });
})()`
			lowest, n := 100.0, 0
			for _, line := range p.strs(js) {
				name, ratio, ok := strings.Cut(line, ": ")
				var r float64
				if _, err := fmt.Sscan(ratio, &r); !ok || err != nil || r < 4.5 {
					t.Errorf("%s: %s (the minimum for text is 4.5)", name, ratio)
					continue
				}
				n++
				lowest = min(lowest, r)
			}
			t.Logf("%s: %d kinds of text in the dialog, the lowest contrast is %.2f", scheme, n, lowest)
			if n != 13 {
				t.Errorf("%d kinds of text measured", n)
			}
			p.shot("viewer-dialog-" + scheme)
			p.clean()
		})
	}
}

func TestAnAnswerTheHostNeverTakesCanBeGivenAgain(t *testing.T) {
	h, p := startPage(t)
	h.hub.Send(confirmReq("q1", "exec"))
	p.open(h.rig)
	p.wait("the dialog", hasDialog)
	p.do(chromedp.KeyEvent(kb.Escape))
	until(t, "the answer", func() bool { return len(h.confirms()) == 1 })
	p.wait("the buttons to be held", `document.getElementById('confirm-reject').disabled && document.getElementById('confirm-approve').disabled`)
	// The host says nothing: after a few seconds the person is told, and can answer again.
	p.wait("the buttons to come back", `!document.getElementById('confirm-reject').disabled && document.querySelector('.modal-status').textContent.includes('has not answered yet')`)
	p.do(chromedp.Click("#confirm-reject"))
	until(t, "the second answer", func() bool { return len(h.confirms()) == 2 })
}

func TestTheFocusGoesBackToWhereItWas(t *testing.T) {
	h, p := startPage(t)
	p.open(h.rig)
	p.wait("the page", `document.getElementById('session').textContent === 's_1234abcd'`)
	p.do(chromedp.Focus("#chat-input"))
	h.hub.Send(confirmReq("q1", "exec"))
	p.wait("the dialog", hasDialog)
	if got := p.str(activeID); got != "confirm-reject" {
		t.Fatalf("focus is on %q", got)
	}
	h.hub.Send(viewer.ConfirmResolved{ID: "q1", Outcome: viewer.OutcomeExpired})
	p.wait("the dialog to close", noDialog)
	if got := p.str(activeID); got != "chat-input" {
		t.Errorf("focus went to %q, not back to the chat box", got)
	}
}
