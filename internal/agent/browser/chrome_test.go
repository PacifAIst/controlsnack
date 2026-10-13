// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package browser

import (
	"errors"
	"testing"
)

func TestIsGone(t *testing.T) {
	for msg, want := range map[string]bool{
		"No node with given id found (-32000)":               true,
		"Could not find node with given id (-32000)":         true,
		"Frame is detached. (-32000)":                        true,
		"Node is detached from document (-32000)":            true,
		"Cannot find context with specified id (-32000)":     true,
		"Node with given id does not belong to the document": true,
		"net::ERR_CONNECTION_REFUSED":                        false,
		"Could not compute box model. (-32000)":              false,
		"context deadline exceeded":                          false,
	} {
		if got := isGone(errors.New(msg)); got != want {
			t.Errorf("%q: %v, want %v", msg, got, want)
		}
	}
	if isGone(nil) {
		t.Error("nil is not gone")
	}
}

func TestQuadBounds(t *testing.T) {
	x0, y0, x1, y1, ok := quadBounds([]float64{10, 20, 110, 20, 110, 50, 10, 50})
	if !ok || x0 != 10 || y0 != 20 || x1 != 110 || y1 != 50 {
		t.Errorf("%v %v %v %v %v", x0, y0, x1, y1, ok)
	}
	// A turned box is bounded by its corners.
	x0, y0, x1, y1, ok = quadBounds([]float64{50, 0, 100, 50, 50, 100, 0, 50})
	if !ok || x0 != 0 || y0 != 0 || x1 != 100 || y1 != 100 {
		t.Errorf("%v %v %v %v %v", x0, y0, x1, y1, ok)
	}
	for _, q := range [][]float64{nil, {1, 2}, {1, 2, 3, 4, 5, 6, 7}} {
		if _, _, _, _, ok := quadBounds(q); ok {
			t.Errorf("%v is not a quad", q)
		}
	}
}

func TestChromiumWithoutABrowserProgram(t *testing.T) {
	c := NewChromium("/nonexistent/chromium", t.TempDir(), "")
	if tabs, err := c.Tabs(t.Context()); err != nil || len(tabs) != 0 {
		t.Errorf("no browser, no tabs: %v %v", tabs, err)
	}
	if err := c.Navigate(t.Context(), "https://example.com/"); !errors.Is(err, ErrNoBrowser) {
		t.Errorf("navigating with no browser: %v", err)
	}
	if err := c.Open(t.Context(), "https://example.com/"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("a browser program that is not there: %v", err)
	}
	if _, _, err := c.Summary(t.Context()); !errors.Is(err, ErrNoBrowser) {
		t.Errorf("%v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("closing a browser that never ran: %v", err)
	}
}

func TestRemoveString(t *testing.T) {
	got := removeString([]string{"a", "b", "c", "b"}, "b")
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Errorf("%v", got)
	}
}
