// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack-local/web"
)

func TestTheFrontEndHasItsPagesAtTheRoot(t *testing.T) {
	for _, name := range []string{"index.html", "app.js", "app.css"} {
		b, err := fs.ReadFile(web.Static(), name)
		if err != nil || len(b) == 0 {
			t.Errorf("%s: %d bytes, %v", name, len(b), err)
		}
	}
}

var (
	reScript  = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
	reSrc     = regexp.MustCompile(`(?i)\ssrc\s*=`)
	reHandler = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	reStyleEl = regexp.MustCompile(`(?i)<style\b`)
	reStyleAt = regexp.MustCompile(`(?i)\sstyle\s*=`)
	reJSURL   = regexp.MustCompile(`(?i)javascript\s*:`)
)

// inlineCode lists what a page holds that the viewer's CSP (scripts and styles from its own address only) would not
// run: a script with a body or without a src, a style element or attribute, an event handler attribute, a javascript:
// address. (P6-T06 holds the whole front-end to the stricter rules about how it builds text.)
func inlineCode(page string) []string {
	var found []string
	for _, m := range reScript.FindAllStringSubmatch(page, -1) {
		if !reSrc.MatchString(m[1]) || strings.TrimSpace(m[2]) != "" {
			found = append(found, "an inline script")
		}
	}
	for what, re := range map[string]*regexp.Regexp{"an event handler": reHandler, "a style element": reStyleEl, "a style attribute": reStyleAt, "a javascript: address": reJSURL} {
		if re.MatchString(page) {
			found = append(found, what)
		}
	}
	return found
}

func TestThePageNeedsNoInlineCode(t *testing.T) {
	err := fs.WalkDir(web.Static(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(name, ".html") {
			return err
		}
		b, err := fs.ReadFile(web.Static(), name)
		if err != nil {
			return err
		}
		if found := inlineCode(string(b)); len(found) > 0 {
			t.Errorf("%s has %v", name, found)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTheCheckForInlineCodeSeesWhatItShould(t *testing.T) {
	for page, want := range map[string]string{
		`<script>alert(1)</script>`:                    "an inline script",
		`<script src="/a.js">x()</script>`:             "an inline script",
		`<SCRIPT type=module>go()</SCRIPT >`:           "an inline script",
		`<button onclick="go()">`:                      "an event handler",
		`<img src=x onerror=go()>`:                     "an event handler",
		`<style>p{}</style>`:                           "a style element",
		`<p style="color:red">`:                        "a style attribute",
		`<a href="javascript:go()">`:                   "a javascript: address",
		"<a href=' JavaScript :go()'>":                 "a javascript: address",
		"<script\nsrc=/a.js>\n</script>\n<p ONLOAD=x>": "an event handler",
	} {
		found := inlineCode(page)
		ok := false
		for _, f := range found {
			ok = ok || f == want
		}
		if !ok {
			t.Errorf("%q: found %v, want %q", page, found, want)
		}
	}
	for _, page := range []string{`<script src="/app.js"></script>`, "<script\n  src='/a.js' defer>\n</script>", `<link rel="stylesheet" href="/app.css"><p id=x>`} {
		if found := inlineCode(page); len(found) != 0 {
			t.Errorf("%q: found %v", page, found)
		}
	}
}
