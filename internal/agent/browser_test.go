// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack/internal/agent/browser"
	"github.com/PacifAIst/controlsnack/internal/protocol"
)

// stubBrowser is a backend that opens a page or fails; the other methods are not used by these tests.
type stubBrowser struct {
	browser.Backend
	openErr error
	url     string
}

func (s *stubBrowser) Open(_ context.Context, url string) error { s.url = url; return s.openErr }
func (s *stubBrowser) Summary(context.Context) (string, string, error) {
	return "Title", s.url, nil
}

func browserCall(t *testing.T, h Browser, body any) (protocol.BrowserRes, error) {
	t.Helper()
	res, _, err := h.Handle(context.Background(), frame(t, protocol.TypeBrowser, body))
	if err != nil {
		return protocol.BrowserRes{}, err
	}
	return res.(protocol.BrowserRes), nil
}

func TestBrowserHandler(t *testing.T) {
	h := NewBrowser(&stubBrowser{})
	h.Service.Settle = 0
	res, err := browserCall(t, h, protocol.BrowserReq{Op: "open", URL: "file:///home/cs/work/page.html"})
	if err != nil || res.URL != "file:///home/cs/work/page.html" || res.Title != "Title" {
		t.Errorf("%+v %v", res, err)
	}
}

func TestBrowserErrorsCarryTheProtocolCodes(t *testing.T) {
	for name, tt := range map[string]struct {
		body any
		err  error
		code string
		text string
	}{
		"bad body":     {map[string]any{"op": "open", "color": "red"}, nil, protocol.CodeInvalidArgs, ""},
		"bad op":       {protocol.BrowserReq{Op: "close"}, nil, protocol.CodeInvalidArgs, "close"},
		"bad scheme":   {protocol.BrowserReq{Op: "open", URL: "javascript:alert(1)"}, nil, protocol.CodeInvalidArgs, "not allowed"},
		"missing":      {protocol.BrowserReq{Op: "open", URL: "https://example.com/"}, browser.ErrUnavailable, protocol.CodeUnsupported, "cannot be started"},
		"not open":     {protocol.BrowserReq{Op: "open", URL: "https://example.com/"}, browser.ErrNoBrowser, protocol.CodeNotFound, "not open"},
		"stale":        {protocol.BrowserReq{Op: "open", URL: "https://example.com/"}, browser.ErrNodeGone, protocol.CodeStaleElement, "gone"},
		"timeout":      {protocol.BrowserReq{Op: "open", URL: "https://example.com/"}, context.DeadlineExceeded, protocol.CodeTimeout, "took too long"},
		"page failure": {protocol.BrowserReq{Op: "open", URL: "https://example.com/"}, errors.New("net::ERR_NAME_NOT_RESOLVED"), protocol.CodeInternal, "ERR_NAME_NOT_RESOLVED"},
	} {
		h := NewBrowser(&stubBrowser{openErr: tt.err})
		_, err := browserCall(t, h, tt.body)
		if code(err) != tt.code || !strings.Contains(err.Error(), tt.text) {
			t.Errorf("%s: %v", name, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := NewBrowser(&stubBrowser{openErr: context.Canceled})
	if _, _, err := h.Handle(ctx, frame(t, protocol.TypeBrowser, protocol.BrowserReq{Op: "open", URL: "https://example.com/"})); !errors.Is(err, context.Canceled) {
		t.Errorf("a canceled request stays a cancellation: %v", err)
	}
}
