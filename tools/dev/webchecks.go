// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The viewer shows text that came from a machine that may be hostile and from a model that may be misled (PLAN.md
// section 10.1). Its content security policy allows scripts and styles from its own address only, and these checks keep
// the front-end from ever needing more: nothing in it builds markup from text, runs code from a string, reaches another
// address, or keeps anything in the browser. They are lines of text, deliberately: a rule that needs a parser to be
// understood is a rule that gets argued with.

// webRule is one thing the front-end may not do.
type webRule struct {
	name string
	re   *regexp.Regexp
	why  string
	// exts are the file extensions the rule applies to.
	exts []string
}

var (
	jsOnly   = []string{".js", ".mjs"}
	jsHTML   = []string{".js", ".mjs", ".html"}
	htmlOnly = []string{".html"}
	cssOnly  = []string{".css"}
	allWeb   = []string{".js", ".mjs", ".html", ".css"}
)

var webRules = []webRule{
	{"innerHTML", regexp.MustCompile(`\binnerHTML\b`), "builds markup from a string: use textContent or createElement", jsOnly},
	{"outerHTML", regexp.MustCompile(`\bouterHTML\b`), "builds markup from a string: use textContent or createElement", jsOnly},
	{"insertAdjacentHTML", regexp.MustCompile(`\binsertAdjacentHTML\b`), "builds markup from a string: use textContent or createElement", jsOnly},
	{"document.write", regexp.MustCompile(`\bdocument\s*\.\s*write(?:ln)?\b`), "writes markup into the page", jsOnly},
	{"srcdoc", regexp.MustCompile(`\bsrcdoc\b`), "puts a page inside the page", jsHTML},
	{"createContextualFragment", regexp.MustCompile(`\bcreateContextualFragment\b`), "builds markup from a string", jsOnly},
	{"DOMParser", regexp.MustCompile(`\bDOMParser\b`), "builds markup from a string", jsOnly},
	{"eval", regexp.MustCompile(`\beval\s*\(`), "runs code from a string", jsOnly},
	{"new Function", regexp.MustCompile(`\bnew\s+Function\b|\bFunction\s*\(`), "runs code from a string", jsOnly},
	{"timer with a string", regexp.MustCompile("\\bset(?:Timeout|Interval)\\s*\\(\\s*[\"'`]"), "runs code from a string", jsOnly},
	{"importScripts", regexp.MustCompile(`\bimportScripts\b`), "loads code from elsewhere", jsOnly},
	{"javascript: address", regexp.MustCompile(`(?i)javascript\s*:`), "runs code from an address", allWeb},
	{"style from a string", regexp.MustCompile(`\.cssText\b|setAttribute\s*\(\s*["']style["']`), "the policy refuses inline styles: use a class", jsOnly},
	{"handler attribute set by a script", regexp.MustCompile(`setAttribute\s*\(\s*["']on`), "an inline handler, which the policy refuses: use addEventListener", jsOnly},
	{"another address", regexp.MustCompile(`https?://`), "reaches (or names) an address other than the viewer's own", allWeb},
	{"another socket address", regexp.MustCompile(`\bwss?://`), "opens a socket to an address other than the viewer's own (ws://${location.host}/ws)", jsOnly},
	{"cookie", regexp.MustCompile(`\bdocument\s*\.\s*cookie\b`), "the session cookie is not for scripts", jsOnly},
	{"browser storage", regexp.MustCompile(`\b(?:localStorage|sessionStorage|indexedDB)\b`), "the page keeps nothing in the browser", jsOnly},
	{"window.open", regexp.MustCompile(`\bwindow\s*\.\s*open\s*\(`), "opens another page", jsOnly},
	{"frame, object or embed", regexp.MustCompile(`(?i)<\s*(?:iframe|object|embed|applet)\b`), "puts another page or plug-in inside the page", htmlOnly},
	{"base element", regexp.MustCompile(`(?i)<\s*base\b`), "changes where every address leads", htmlOnly},
	{"style element", regexp.MustCompile(`(?i)<\s*style\b`), "an inline style, which the policy refuses: use app.css", htmlOnly},
	{"inline handler", regexp.MustCompile(`(?i)\son[a-z]+\s*=`), "an inline handler, which the policy refuses: use addEventListener", htmlOnly},
	{"style attribute", regexp.MustCompile(`(?i)\sstyle\s*=`), "an inline style, which the policy refuses: use a class", htmlOnly},
	{"css import", regexp.MustCompile(`(?i)@import\b`), "loads a style sheet from elsewhere", cssOnly},
	{"css expression", regexp.MustCompile(`(?i)\bexpression\s*\(|-moz-binding|\bbehavior\s*:`), "runs code from a style", cssOnly},
	{"css address", regexp.MustCompile(`(?i)url\s*\(\s*["']?\s*(?://|[a-z][a-z0-9+.-]*:)`), "loads from an address (only data: is allowed, and not in the style sheet)", cssOnly},
}

// inlineScript matches a script element, to see whether it has a body or no source.
var (
	inlineScript = regexp.MustCompile(`(?is)<\s*script\b([^>]*)>(.*?)<\s*/\s*script\s*>`)
	scriptSrc    = regexp.MustCompile(`(?i)\ssrc\s*=`)
)

// webExts are the kinds of file the viewer's server serves (internal/viewer/handlers.go contentTypes): anything else in
// web/static would never reach the page, so it does not belong there.
var webExts = map[string]bool{".html": true, ".js": true, ".css": true, ".svg": true, ".png": true, ".ico": true, ".json": true, ".woff2": true}

// checkWebFile applies the rendering-safety rules to one file of the front-end. path uses forward slashes.
func checkWebFile(path string, content []byte) []string {
	ext := strings.ToLower(filepath.Ext(path))
	var problems []string
	if !webExts[ext] {
		return []string{fmt.Sprintf("%s: a file of this kind is not served by the viewer", path)}
	}
	// The page's own socket address is the one address a script may name; taking it out leaves any other to be found.
	text := strings.ReplaceAll(string(content), "ws://${location.host}/ws", "")
	line := func(offset int) int { return 1 + strings.Count(text[:offset], "\n") }
	for _, r := range webRules {
		if !contains(r.exts, ext) {
			continue
		}
		for _, m := range r.re.FindAllStringIndex(text, -1) {
			problems = append(problems, fmt.Sprintf("%s:%d: %s: %s", path, line(m[0]), r.name, r.why))
		}
	}
	if ext == ".html" {
		for _, m := range inlineScript.FindAllStringSubmatchIndex(text, -1) {
			attrs, body := text[m[2]:m[3]], text[m[4]:m[5]]
			if !scriptSrc.MatchString(attrs) || strings.TrimSpace(body) != "" {
				problems = append(problems, fmt.Sprintf("%s:%d: inline script: the policy refuses it: put the code in a file", path, line(m[0])))
			}
		}
	}
	return problems
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// walkWeb calls fn for every file of the front-end.
func walkWeb(fn func(path string, content []byte) error) error {
	const root = "web/static"
	if _, err := os.Stat(root); err != nil {
		return nil // a tree without a front-end has nothing to check
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := os.ReadFile(path) //nolint:gosec // walking our own working tree
		if err != nil {
			return err
		}
		return fn(filepath.ToSlash(path), content)
	})
}
