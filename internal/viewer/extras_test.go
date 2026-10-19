// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack/internal/viewer"
)

func TestThePageCanAskForTheCSRFValueBeforeTheSocketIsUp(t *testing.T) {
	r := start(t)
	c := r.login()
	resp, body := r.do(req{path: "/api/csrf", cookie: c, header: map[string]string{"Origin": r.s.Origin(), "Sec-Fetch-Site": "same-origin"}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"csrf":"`+r.s.CSRF()+`"`) || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("%s %q", resp.Status, body)
	}
	// A page that has no cookie, or is another site, or asks the wrong way, gets nothing.
	for name, q := range map[string]req{
		"no cookie":    {path: "/api/csrf"},
		"wrong cookie": {path: "/api/csrf", cookie: &http.Cookie{Name: viewer.CookieName, Value: "x"}},
		"another site": {path: "/api/csrf", cookie: c, header: map[string]string{"Origin": "http://evil.example"}},
		"cross-site":   {path: "/api/csrf", cookie: c, header: map[string]string{"Sec-Fetch-Site": "cross-site"}},
		"POST":         {method: http.MethodPost, path: "/api/csrf", cookie: c},
	} {
		resp, body := r.do(q)
		if resp.StatusCode == http.StatusOK || strings.Contains(body, r.s.CSRF()) {
			t.Errorf("%s: %s %q", name, resp.Status, body)
		}
	}
	// And what it gives is what makes the kill button work.
	resp, _ = r.do(req{method: http.MethodPost, path: "/api/kill", cookie: c, header: map[string]string{viewer.HeaderCSRF: r.s.CSRF()}})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("the kill with that value: %s", resp.Status)
	}
	r.waitKilled()
}

func TestAPageThatConnectsLaterIsToldWhetherTheAgentIsThere(t *testing.T) {
	h := startHub(t)
	h.hub.Send(viewer.Agent{Online: true})
	h.hub.Send(viewer.State{State: viewer.StatePaused, Reason: "waiting"})
	h.hub.Send(viewer.Agent{Online: false, Detail: "the agent stopped answering"}) // only the last one is kept
	c := h.dial(t, h.login())
	hello := readJSON(t, c)
	if hello["now"] != "2026-10-17T12:00:00.000Z" { // the host's clock, here the pretend one
		t.Errorf("now %v", hello["now"])
	}
	if hello["started_at"] != sampleHello.StartedAt {
		t.Errorf("started_at %v", hello["started_at"])
	}
	state := readJSON(t, c)
	agent := readJSON(t, c)
	if state["type"] != "state" || agent["type"] != "agent" || agent["online"] != false || agent["detail"] != "the agent stopped answering" {
		t.Errorf("%v %v", state, agent)
	}
}
