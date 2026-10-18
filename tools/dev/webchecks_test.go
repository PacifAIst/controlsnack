// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bad is what each rule must find: the file to check, and a line that breaks the rule.
var bad = []struct{ rule, path, content string }{
	{"innerHTML", "web/static/a.js", "box.innerHTML = text;"},
	{"innerHTML", "web/static/a.js", "const s = el['inner' + 'HTML']; box.innerHTML += s;"},
	{"outerHTML", "web/static/a.js", "el.outerHTML = text;"},
	{"insertAdjacentHTML", "web/static/a.js", "el.insertAdjacentHTML('beforeend', text);"},
	{"document.write", "web/static/a.js", "document.write(text);"},
	{"document.write", "web/static/a.js", "document . writeln(text);"},
	{"srcdoc", "web/static/a.js", "frame.srcdoc = text;"},
	{"srcdoc", "web/static/index.html", `<p srcdoc="x">`},
	{"createContextualFragment", "web/static/a.js", "range.createContextualFragment(text);"},
	{"DOMParser", "web/static/a.js", "new DOMParser().parseFromString(text, 'text/html');"},
	{"eval", "web/static/a.js", "eval(code);"},
	{"eval", "web/static/a.js", "window.eval (code);"},
	{"new Function", "web/static/a.js", "const f = new Function('return 1');"},
	{"new Function", "web/static/a.js", "const f = Function('return 1');"},
	{"timer with a string", "web/static/a.js", "setTimeout('go()', 10);"},
	{"timer with a string", "web/static/a.js", "setInterval(`go()`, 10);"},
	{"importScripts", "web/static/a.js", "importScripts('x.js');"},
	{"javascript: address", "web/static/a.js", "a.href = 'javascript:go()';"},
	{"javascript: address", "web/static/index.html", `<a href=" JavaScript :go()">`},
	{"style from a string", "web/static/a.js", "el.style.cssText = 'color: red';"},
	{"style from a string", "web/static/a.js", "el.setAttribute('style', 'color: red');"},
	{"handler attribute set by a script", "web/static/a.js", "el.setAttribute('onclick', 'go()');"},
	{"another address", "web/static/a.js", "fetch('https://example.com/x');"},
	{"another address", "web/static/index.html", `<script src="http://cdn.example/x.js"></script>`},
	{"another address", "web/static/app.css", "body { background: url(https://example.com/x.png); }"},
	{"another socket address", "web/static/a.js", "new WebSocket('ws://example.com/ws');"},
	{"another socket address", "web/static/a.js", "new WebSocket(`wss://${host}/ws`);"},
	{"cookie", "web/static/a.js", "const c = document.cookie;"},
	{"browser storage", "web/static/a.js", "localStorage.setItem('csrf', value);"},
	{"browser storage", "web/static/a.js", "const db = indexedDB.open('x');"},
	{"window.open", "web/static/a.js", "window.open('/other');"},
	{"frame, object or embed", "web/static/index.html", `<iframe src="/x"></iframe>`},
	{"frame, object or embed", "web/static/index.html", `<EMBED src="/x">`},
	{"base element", "web/static/index.html", `<base href="/x/">`},
	{"style element", "web/static/index.html", `<style>p { color: red }</style>`},
	{"inline handler", "web/static/index.html", `<button onclick="go()">x</button>`},
	{"inline handler", "web/static/index.html", `<img src=x onerror=go()>`},
	{"style attribute", "web/static/index.html", `<p style="color: red">`},
	{"inline script", "web/static/index.html", `<script>go()</script>`},
	{"inline script", "web/static/index.html", `<script src="/a.js">go()</script>`},
	{"inline script", "web/static/index.html", "<script type=module>\ngo()\n</script >"},
	{"inline script", "web/static/index.html", `<script type="module">import "/a.js"</script>`},
	{"css import", "web/static/app.css", `@import "/other.css";`},
	{"css expression", "web/static/app.css", "p { width: expression(document.body.clientWidth); }"},
	{"css expression", "web/static/app.css", "p { -moz-binding: url(x); }"},
	{"css address", "web/static/app.css", "p { background: url('//example.com/x.png'); }"},
	{"css address", "web/static/app.css", "p { background: url(data:image/png;base64,AAAA); }"},
	{"not served", "web/static/app.js.map", "{}"},
	{"not served", "web/static/notes.txt", "x"},
}

// good is what the front-end does instead, and must not be found.
var good = []struct{ path, content string }{
	{"web/static/a.js", "box.textContent = text;\nconst e = document.createElement('div');\ne.className = 'msg';\nbox.append(e);"},
	{"web/static/a.js", "const conn = new Connection(`ws://${location.host}/ws`, handlers);"},
	{"web/static/a.js", "setTimeout(() => go(), 10);\nsetInterval(function () { go(); }, 500);"},
	{"web/static/a.js", "el.style.width = '10px'; el.hidden = true; el.dataset.state = 'x'; el.setAttribute('role', 'dialog');"},
	{"web/static/a.js", "fetch('/api/kill', { method: 'POST', credentials: 'same-origin' });"},
	{"web/static/a.js", "import { el } from './panels.js';\nconst m = await import('/confirm.js');"},
	{"web/static/a.js", "// A message with <b>markup</b> in it is shown as the characters.\nconst t = 'x';"},
	{"web/static/a.js", "const evaluate = 1; const functional = 2; const myFunction = 3; el.onclick = go;"},
	{"web/static/index.html", `<script type="module" src="/app.js"></script>`},
	{"web/static/index.html", "<script\n  src='/a.js' defer>\n</script>"},
	{"web/static/index.html", `<link rel="stylesheet" href="/app.css"><link rel="icon" href="/favicon.svg" type="image/svg+xml">`},
	{"web/static/index.html", `<button type="button" id="kill" title="Ends the session">Kill</button>`},
	{"web/static/app.css", "p { color: var(--text); background: #fff; font: 14px system-ui; }\n.a::before { content: 'x'; }"},
	{"web/static/favicon.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect width="32" height="32"/></svg>`},
}

func TestTheWebRulesFindWhatTheyAreForAndOnlyThat(t *testing.T) {
	seen := map[string]bool{}
	for _, tc := range bad {
		problems := checkWebFile(tc.path, []byte(tc.content))
		found := false
		for _, p := range problems {
			if strings.Contains(p, tc.rule+":") || (tc.rule == "not served" && strings.Contains(p, "not served")) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s in %q: not found (%v)", tc.rule, tc.content, problems)
		}
		seen[tc.rule] = true
		if tc.rule != "not served" && len(problems) > 0 && !strings.HasPrefix(problems[0], tc.path+":") {
			t.Errorf("a problem that does not say where it is: %q", problems[0])
		}
	}
	for _, r := range webRules {
		if !seen[r.name] {
			t.Errorf("no test case makes the rule %q find something", r.name)
		}
	}
	if !seen["inline script"] {
		t.Error("no test case for the inline script check")
	}
	for _, tc := range good {
		if problems := checkWebFile(tc.path, []byte(tc.content)); len(problems) != 0 {
			t.Errorf("%q: %v", tc.content, problems)
		}
	}
}

func TestAProblemNamesItsLine(t *testing.T) {
	got := checkWebFile("web/static/a.js", []byte("const a = 1;\n\nbox.innerHTML = x;\nconst b = 2;\nel.outerHTML = y;\n"))
	if len(got) != 2 || !strings.HasPrefix(got[0], "web/static/a.js:3: innerHTML") || !strings.HasPrefix(got[1], "web/static/a.js:5: outerHTML") {
		t.Errorf("%q", got)
	}
	got = checkWebFile("web/static/index.html", []byte("<p>\n</p>\n<script>\nx\n</script>"))
	if len(got) != 1 || !strings.HasPrefix(got[0], "web/static/index.html:3: inline script") {
		t.Errorf("%q", got)
	}
}

func TestTheFrontEndOfThisTreePassesItsOwnRules(t *testing.T) {
	t.Chdir("../..")
	files := 0
	var problems []string
	if err := walkWeb(func(path string, content []byte) error {
		files++
		problems = append(problems, checkWebFile(path, content)...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if files < 10 { // index, css, nine scripts and the icon: a walk that finds nothing would pass anything
		t.Errorf("only %d files in web/static", files)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// The repo step, run on a tree with one bad file, fails and names it; on a clean one it passes.
func TestTheRepoStepRefusesABadFrontEnd(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	write := func(name, content string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("web/static/ok.js", "box.textContent = 'x';\n")
	if _, err := stepRepo(); err != nil {
		t.Fatalf("a clean front-end: %v", err)
	}
	write("web/static/bad.js", "box.innerHTML = text;\n")
	_, err := stepRepo()
	if err == nil || !strings.Contains(err.Error(), "web/static/bad.js:1: innerHTML") {
		t.Fatalf("a bad front-end: %v", err)
	}
	if strings.Contains(err.Error(), "ok.js") {
		t.Errorf("the good file is named too: %v", err)
	}
}
