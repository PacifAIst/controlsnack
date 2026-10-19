// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package sitecheck

import (
	"encoding/json"
	"fmt"
	"html"
	"image"
	_ "image/png" // the screenshots are PNG files
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/cli"
	"github.com/PacifAIst/controlsnack/internal/config"
	"github.com/PacifAIst/controlsnack/internal/exitcode"
	"github.com/PacifAIst/controlsnack/internal/llm/actions"
	"github.com/PacifAIst/controlsnack/internal/safety/policy"
)

// The documentation page is written by hand, so these tests hold it to the program: whatever the program has (a command, a flag, a
// configuration key, a built-in rule, a tool, an exit code) the page has to describe, and the examples in it have to work.

var reSection = regexp.MustCompile(`(?s)<section id="(cmd-[a-z-]+)">(.*?)</section>`)

// commandSections maps the id of each command's section to its text.
func commandSections(t *testing.T, docs string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range reSection.FindAllStringSubmatch(docs, -1) {
		out[m[1]] = m[2]
	}
	if len(out) < 20 {
		t.Fatalf("only %d command sections found in docs.html", len(out))
	}
	return out
}

func sectionID(path string) string {
	if path == "" {
		return "cmd-global"
	}
	return "cmd-" + strings.ReplaceAll(path, " ", "-")
}

func TestEveryCommandAndFlagOfTheProgramIsDescribedInItsSection(t *testing.T) {
	docs := read(t, "docs.html")
	sections := commandSections(t, docs)
	seen := map[string]bool{}
	for _, ref := range cli.Reference() {
		id := sectionID(ref.Path)
		seen[id] = true
		text, ok := sections[id]
		if !ok {
			t.Errorf("the command %q has no section %q in docs.html", ref.Path, id)
			continue
		}
		if ref.Path != "" && !strings.Contains(text, "controlsnack "+ref.Path) {
			t.Errorf("the section of %q never names the command as it is typed", ref.Path)
		}
		for _, f := range ref.Flags {
			if !strings.Contains(text, "--"+f) {
				t.Errorf("the flag --%s of %q is not in its section", f, ref.Path)
			}
		}
	}
	for id := range sections {
		if !seen[id] {
			t.Errorf("docs.html has a section %q for a command the program does not have", id)
		}
	}
}

// flatten turns nested settings into dotted keys.
func flatten(prefix string, v any, out map[string]bool) {
	if m, ok := v.(map[string]any); ok {
		for k, x := range m {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flatten(key, x, out)
		}
		return
	}
	out[prefix] = true
}

func TestEveryConfigurationKeyIsInTheTableWithItsDefaultAndFlag(t *testing.T) {
	docs := read(t, "docs.html")
	raw, err := json.Marshal(config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	flatten("", tree, keys)
	delete(keys, "safety.unsafe") // not a key, on purpose
	if len(keys) < 40 {
		t.Fatalf("only %d keys found", len(keys))
	}
	table := docs[strings.Index(docs, `id="config-keys"`):]
	table = table[:strings.Index(table, `</section>`)]
	for key := range keys {
		row := regexp.MustCompile(`(?s)<tr><td><code>` + regexp.QuoteMeta(key) + `</code></td>(.*?)</tr>`).FindStringSubmatch(table)
		if row == nil {
			t.Errorf("the configuration key %s has no row in the table", key)
			continue
		}
		// The flag column names the flag that sets the key, if there is one.
		for flag, k := range config.FlagKeys {
			if k == key && !strings.Contains(row[1], "--"+flag) {
				t.Errorf("the row of %s does not name its flag --%s", key, flag)
			}
		}
	}
	if strings.Contains(table, "<code>safety.unsafe</code>") {
		t.Error("safety.unsafe is not a configuration key and must not be in the table")
	}
	// The environment variable rule is stated, with an example that is right.
	if got := config.EnvName("vm.memory_mib"); !strings.Contains(docs, "<code>"+got+"</code>") {
		t.Errorf("the page does not show %s as the variable of vm.memory_mib", got)
	}
}

func TestTheDefaultsAreTheOnesTheFlagsOfRunShow(t *testing.T) {
	docs := read(t, "docs.html")
	d := config.Defaults()
	for name, want := range map[string]string{
		"cpus": strconv.Itoa(d.VM.CPUs), "memory": strconv.Itoa(d.VM.MemoryMiB), "resolution": d.VM.Resolution,
		"max-turns": strconv.Itoa(d.Safety.MaxTurns), "actions-per-task": strconv.Itoa(d.Safety.ActionsPerTask),
		"actions-per-response": strconv.Itoa(d.Safety.ActionsPerResponse), "actions-per-sec": strconv.FormatFloat(d.Safety.ActionsPerSec, 'f', -1, 64),
	} {
		block := regexp.MustCompile(`(?s)<div class="flag" id="flag-` + name + `">(.*?)</div>`).FindStringSubmatch(docs)
		if block == nil {
			t.Errorf("no block for --%s", name)
			continue
		}
		if !strings.Contains(block[1], "<b>Default:</b> <code>"+want+"</code>") {
			t.Errorf("--%s is documented with a default other than %s: %s", name, want, regexp.MustCompile(`<b>Default:</b>[^<]*<code>[^<]*</code>`).FindString(block[1]))
		}
	}
	if !strings.Contains(docs, "<code>"+d.Safety.Timeout.D().String()) && !strings.Contains(docs, "<code>1h</code>") {
		t.Error("the default session timeout is not shown")
	}
}

func TestEveryBuiltInRuleIsInThePolicyTableWithItsClassAndWhetherItIsFixed(t *testing.T) {
	docs := read(t, "docs.html")
	rs, err := policy.DefaultRuleSet()
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Rules) < 20 {
		t.Fatalf("%d rules", len(rs.Rules))
	}
	for _, r := range rs.Rules {
		row := regexp.MustCompile(`(?s)<tr><td><code>` + regexp.QuoteMeta(r.ID) + `</code></td><td>([^<]*)</td>`).FindStringSubmatch(docs)
		if row == nil {
			t.Errorf("the rule %s has no row in the policy table", r.ID)
			continue
		}
		class := strings.ToLower(fmt.Sprint(r.Class))
		if !strings.HasPrefix(row[1], class) {
			t.Errorf("%s is documented as %q; the program says %s", r.ID, row[1], class)
		}
		if fixed := strings.Contains(row[1], "fixed"); fixed != r.Invariant {
			t.Errorf("%s: documented as fixed=%v, the program says invariant=%v", r.ID, fixed, r.Invariant)
		}
	}
}

func TestEveryToolIsInTheToolTableAndTheCompactColumnIsRight(t *testing.T) {
	docs := read(t, "docs.html")
	for _, tool := range actions.Tools() {
		row := regexp.MustCompile(`(?s)<tr><td><code>` + regexp.QuoteMeta(tool.Name) + `</code></td><td>.*?</td><td>(yes|no)</td></tr>`).FindStringSubmatch(docs)
		if row == nil {
			t.Errorf("the tool %s has no row in the table of tools", tool.Name)
			continue
		}
		if (row[1] == "yes") != tool.Compact {
			t.Errorf("%s: the page says compact=%s, the program says %v", tool.Name, row[1], tool.Compact)
		}
	}
	// The counts the page states in words.
	full, compact := len(actions.Tools()), len(actions.CompactTools())
	if !strings.Contains(docs, "offers all fifteen") || full != 15 {
		t.Errorf("the page says the full profile has fifteen tools; the program has %d", full)
	}
	if !strings.Contains(docs, "offers nine") || compact != 9 {
		t.Errorf("the page says the compact profile has nine tools; the program has %d", compact)
	}
}

func TestEveryExitCodeIsInTheTable(t *testing.T) {
	docs := read(t, "docs.html")
	for name, code := range map[string]int{"ok": exitcode.OK, "error": exitcode.Failure, "usage": exitcode.Usage, "environment": exitcode.Environment,
		"image missing": exitcode.ImageMissing, "killed or interrupted": exitcode.Killed, "timeout": exitcode.Timeout, "not implemented": exitcode.NotImplemented} {
		want := fmt.Sprintf("<tr><td><code>%d</code></td><td>%s</td>", code, name)
		if !strings.Contains(docs, want) {
			t.Errorf("the exit code %d (%s) is not in the table", code, name)
		}
	}
}

func TestTheLimitsOfUnsafeModeAreTheOnesTheProgramEnforces(t *testing.T) {
	docs := read(t, "docs.html")
	u, s := config.UnsafeBounds, config.SafeBounds
	table := docs[strings.Index(docs, `id="run-unsafe"`):]
	table = table[:strings.Index(table, `</table>`)]
	for label, want := range map[string]string{
		"Actions per second":   fmt.Sprintf("up to %g</td><td>up to %g", s.ActionsPerSec, u.ActionsPerSec),
		"Actions per response": fmt.Sprintf("up to %d</td><td>up to %d", s.ActionsPerResponse, u.ActionsPerResponse),
		"Actions per task":     fmt.Sprintf("up to %d</td><td>up to %d", s.ActionsPerTask, u.ActionsPerTask),
		"Turns per task":       fmt.Sprintf("up to %d</td><td>up to %d", s.MaxTurns, u.MaxTurns),
		"Session time":         fmt.Sprintf("up to %d hour</td><td>up to %d hours", int(s.SessionTimeout.Hours()), int(u.SessionTimeout.Hours())),
	} {
		if !strings.Contains(table, "<td>"+label+"</td><td>"+want) {
			t.Errorf("%s: the table does not say %q", label, want)
		}
	}
}

func TestTheExamplesInThePageAreValid(t *testing.T) {
	docs := read(t, "docs.html")
	example := func(id string) string {
		m := regexp.MustCompile(`(?s)<pre class="out" id="` + id + `"><code>(.*?)</code></pre>`).FindStringSubmatch(docs)
		if m == nil {
			t.Fatalf("no example %q in docs.html", id)
		}
		return html.UnescapeString(m[1])
	}

	// The example policy loads, and its rules are there.
	rs, err := policy.LoadRuleSet([]byte(example("policy-example")))
	if err != nil {
		t.Fatalf("the example policy does not load: %v", err)
	}
	for _, id := range []string{"USER-NO-CURL", "CONFIRM-REGEX-TMP-RM"} {
		if _, ok := rs.Rule(id); !ok {
			t.Errorf("the example policy lacks its rule %s", id)
		}
	}
	if r, ok := rs.Rule("CONFIRM-PERMS"); !ok || !r.Disabled {
		t.Errorf("the example policy does not switch CONFIRM-PERMS off: %+v", r)
	}

	// The example configuration file loads and validates.
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(example("config-example")), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(config.LoadOptions{ConfigFile: path, Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatalf("the example configuration does not load: %v", err)
	}
	if err := loaded.Validate(config.ValidateOptions{HostOS: "windows"}); err != nil {
		t.Errorf("the example configuration is not valid: %v", err)
	}
	if loaded.Config.VM.MemoryMiB != 4096 || loaded.Config.LLM.Backend != "lmstudio" || loaded.Config.Safety.IdleTimeout.D().Minutes() != 10 {
		t.Errorf("the example configuration says other things than the page: %+v", loaded.Config)
	}
}

var (
	reID   = regexp.MustCompile(`\sid="([^"]+)"`)
	reHref = regexp.MustCompile(`href="(?:docs\.html)?#([A-Za-z0-9_-]+)"`)
	reDocs = regexp.MustCompile(`href="docs\.html#([A-Za-z0-9_-]+)"`)
	reImg  = regexp.MustCompile(`<img\s[^>]*>`)
	reAttr = regexp.MustCompile(`(\w+)="([^"]*)"`)
)

func TestEveryLinkInTheDocumentationGoesToAnId(t *testing.T) {
	docs := read(t, "docs.html")
	ids := map[string]int{}
	for _, m := range reID.FindAllStringSubmatch(docs, -1) {
		ids[m[1]]++
	}
	for id, n := range ids {
		if n > 1 {
			t.Errorf("the id %q is used %d times", id, n)
		}
	}
	for _, m := range reHref.FindAllStringSubmatch(docs, -1) {
		if m[1] != "top" && ids[m[1]] == 0 {
			t.Errorf("docs.html links to #%s and nothing has that id", m[1])
		}
	}
	// The links of the other pages into this one.
	for _, page := range []string{"index.html", "404.html"} {
		for _, m := range reDocs.FindAllStringSubmatch(read(t, page), -1) {
			if ids[m[1]] == 0 {
				t.Errorf("%s links to docs.html#%s and nothing has that id", page, m[1])
			}
		}
	}
	// The sections the contents list names are there, and the other way round for the groups of commands.
	for _, id := range []string{"newbies", "new-choose", "new-before", "new-local", "new-api", "new-viewer", "new-stuck", "overview", "commands", "configuration", "config-keys",
		"api-keys", "exit-codes", "policy", "tools", "stopping", "guarantees", "files", "recipes"} {
		if ids[id] == 0 {
			t.Errorf("docs.html has no #%s", id)
		}
		if !strings.Contains(docs, `<li><a href="#`+id+`">`) {
			t.Errorf("the contents list has no entry for #%s", id)
		}
	}
}

func TestEveryScreenshotExistsHasItsTrueSizeAndAnAccurateDescription(t *testing.T) {
	docs := read(t, "docs.html")
	imgs := reImg.FindAllString(docs, -1)
	if len(imgs) < 8 {
		t.Fatalf("only %d images in docs.html", len(imgs))
	}
	used := map[string]bool{}
	for _, tag := range imgs {
		attrs := map[string]string{}
		for _, m := range reAttr.FindAllStringSubmatch(tag, -1) {
			attrs[m[1]] = m[2]
		}
		src := attrs["src"]
		used[filepath.Base(src)] = true
		f, err := os.Open(filepath.Join(siteDir, filepath.FromSlash(src)))
		if err != nil {
			t.Errorf("the image %s does not exist: %v", src, err)
			continue
		}
		cfg, _, err := image.DecodeConfig(f)
		f.Close()
		if err != nil {
			t.Errorf("%s is not an image: %v", src, err)
			continue
		}
		if attrs["width"] != strconv.Itoa(cfg.Width) || attrs["height"] != strconv.Itoa(cfg.Height) {
			t.Errorf("%s is %dx%d and the page says %sx%s: the layout would jump while it loads", src, cfg.Width, cfg.Height, attrs["width"], attrs["height"])
		}
		if len(attrs["alt"]) < 60 {
			t.Errorf("%s has too short a description for someone who cannot see it: %q", src, attrs["alt"])
		}
		if attrs["loading"] != "lazy" {
			t.Errorf("%s is not loaded lazily", src)
		}
	}
	// No file in img/ goes unused (a screenshot that nothing shows is clutter in the repository).
	entries, err := os.ReadDir(filepath.Join(siteDir, "img"))
	if err != nil {
		t.Fatal(err)
	}
	var unused []string
	for _, e := range entries {
		if !used[e.Name()] {
			unused = append(unused, e.Name())
		}
	}
	sort.Strings(unused)
	if len(unused) > 0 {
		t.Errorf("images that no page uses: %v", unused)
	}
}

func TestNoPasswordOrKeyAppearsInThePageOrItsScreenshotsText(t *testing.T) {
	docs := read(t, "docs.html")
	for _, bad := range []string{"sk-or-v1-", "sk-ant-", "sk-proj-", "ghp_", "Bearer "} {
		if strings.Contains(docs, bad) {
			t.Errorf("docs.html contains %q, which looks like a key", bad)
		}
	}
}

func TestTheDocumentationPageWorksInABrowserOnADesktop(t *testing.T) {
	url := serve(t)
	b := newBrowser(t, 1440, 900)
	b.goTo(url + "docs.html")

	// Every screenshot loads (they are lazy, so ask for them all), and none is wider than its box.
	b.run(`document.querySelectorAll('img').forEach(i => { i.loading = 'eager'; }); ''`)
	time.Sleep(1500 * time.Millisecond)
	if got := b.run(`Array.from(document.images).filter(i => !i.complete || i.naturalWidth === 0).map(i => i.getAttribute('src')).join()`); got != "" {
		t.Errorf("images that did not load: %s", got)
	}
	if got := b.run(`[document.documentElement.scrollWidth, window.innerWidth].join()`); strings.Split(got, ",")[0] != strings.Split(got, ",")[1] {
		t.Errorf("the page scrolls sideways on a desktop: %s", got)
	}

	// The contents sit beside the text and stay in view, open.
	if got := b.run(`[getComputedStyle(document.querySelector('.toc')).position, String(document.getElementById('toc-box').open)].join()`); got != "sticky,true" {
		t.Errorf("the contents box on a desktop: %s", got)
	}
	// A link in it goes to its section.
	b.click(`.toc a[href="#cmd-run"]`)
	// The page scrolls smoothly, which takes longer on a slow machine: wait until it has arrived.
	const arrived = `location.hash + ' ' + Math.round(document.getElementById('cmd-run').getBoundingClientRect().top < 200 && document.getElementById('cmd-run').getBoundingClientRect().top > -50)`
	got := b.run(arrived)
	for i := 0; got != "#cmd-run 1" && i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		got = b.run(arrived)
	}
	if got != "#cmd-run 1" {
		t.Errorf("the contents link to the run section: %s", got)
	}
	// Every command in the examples can be copied.
	if got := b.run(`document.querySelectorAll('pre[data-copy] .copy').length + '/' + document.querySelectorAll('pre[data-copy]').length`); strings.Split(got, "/")[0] != strings.Split(got, "/")[1] || got == "0/0" {
		t.Errorf("copy buttons on commands: %s", got)
	}
	b.shot("docs-desktop")
	b.view("docs-newbies", "#newbies")
	b.view("docs-run", "#cmd-run")
	b.view("docs-config", "#config-keys")
	b.clean()
}

func TestTheDocumentationPageFitsAPhone(t *testing.T) {
	url := serve(t)
	b := newBrowser(t, 390, 844)
	b.goTo(url + "docs.html")
	b.run(`document.querySelectorAll('img').forEach(i => { i.loading = 'eager'; }); ''`)
	time.Sleep(1500 * time.Millisecond)
	// Nothing makes the page wider than the screen: tables, commands and screenshots scroll or shrink inside their own boxes.
	if got := b.run(`[document.documentElement.scrollWidth, window.innerWidth].join()`); strings.Split(got, ",")[0] != strings.Split(got, ",")[1] {
		t.Errorf("the documentation scrolls sideways on a phone: %s", got)
	}
	// What is wider than the screen on a phone: name it, so that it can be fixed.
	if got := b.run(`Array.from(document.querySelectorAll('.doc *')).filter(e => !e.closest('.table-wrap') && !e.closest('pre') && e.getBoundingClientRect().right > window.innerWidth + 1).slice(0, 5).map(e => e.tagName + '.' + e.className + '#' + e.id).join()`); got != "" {
		t.Errorf("elements wider than the screen: %s", got)
	}
	// The contents are a closed box on a phone.
	if got := b.run(`String(document.getElementById('toc-box').open)`); got != "false" {
		t.Errorf("the contents box should be closed on a phone: open=%s", got)
	}
	if got := b.run(`Array.from(document.querySelectorAll('button, summary')).filter(e => e.offsetParent && e.getBoundingClientRect().height < 30).map(e => e.className || e.tagName).join()`); got != "" {
		t.Errorf("small tap targets: %s", got)
	}
	b.view("docs-phone-top", "#main")
	b.view("docs-phone-viewer", "#new-viewer")
	b.view("docs-phone-keys", "#config-keys")
	b.view("docs-phone-run", "#flag-keep-vm")
	b.clean()
}

func TestTheGuideInTheRepositoryShowsTheSameScreenshotsAsThePage(t *testing.T) {
	guide, err := os.ReadFile("../../docs/GETTING-STARTED.md")
	if err != nil {
		t.Fatal(err)
	}
	md := regexp.MustCompile(`!\[([^\]]*)\]\(\.\./site/img/([^)]+)\)`).FindAllStringSubmatch(string(guide), -1)
	inGuide := map[string]bool{}
	for _, m := range md {
		inGuide[m[2]] = true
		if len(m[1]) < 60 {
			t.Errorf("%s has too short a description in the guide: %q", m[2], m[1])
		}
		if _, err := os.Stat(filepath.Join(siteDir, "img", m[2])); err != nil {
			t.Errorf("the guide shows %s and it does not exist", m[2])
		}
	}
	inPage := map[string]bool{}
	for _, tag := range reImg.FindAllString(read(t, "docs.html"), -1) {
		inPage[filepath.Base(regexp.MustCompile(`src="([^"]+)"`).FindStringSubmatch(tag)[1])] = true
	}
	for name := range inPage {
		if !inGuide[name] {
			t.Errorf("the page shows %s and the guide in the repository does not", name)
		}
	}
	for name := range inGuide {
		if !inPage[name] {
			t.Errorf("the guide shows %s and the page does not", name)
		}
	}
	// Every in-document link of the guide goes to a heading it has.
	text := string(guide)
	for _, m := range regexp.MustCompile(`\]\(#([a-z0-9-]+)\)`).FindAllStringSubmatch(text, -1) {
		found := false
		for _, h := range regexp.MustCompile(`(?m)^#+ (.+)$`).FindAllStringSubmatch(text, -1) {
			slug := regexp.MustCompile(`[^a-z0-9 -]`).ReplaceAllString(strings.ToLower(h[1]), "")
			if strings.ReplaceAll(slug, " ", "-") == m[1] {
				found = true
			}
		}
		if !found {
			t.Errorf("the guide links to #%s and has no such heading", m[1])
		}
	}
	for _, bad := range []string{"sk-or-v1-", "sk-ant-", `C:` + string(rune(92)) + `Users` + string(rune(92)) + `pc`} {
		if strings.Contains(text, bad) {
			t.Errorf("the guide contains %q", bad)
		}
	}
}

// The site lives at https://controlsnack.smartcityjaen.workers.dev and nowhere else (D-114): the addresses that were given up must not
// come back into what a reader is told, and the real one must be where a reader is told where the documentation is.
func TestTheReadingMatterNamesTheAddressOfTheSiteAndNotTheOnesGivenUp(t *testing.T) {
	const site = "https://controlsnack.smartcityjaen.workers.dev"
	files := []string{"../../README.md", "../../TESTING.md", "../../docs/GETTING-STARTED.md", "../../docs/DEPLOY-SITE.md", "../../.github/release-notes.md",
		"../../site/index.html", "../../site/docs.html", "../../site/404.html"}
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, given := range []string{"controlsnack.com", "controlsnack.pages.dev"} {
			if strings.Contains(string(data), given) {
				t.Errorf("%s names %s, which was given up", name, given)
			}
		}
	}
	for _, name := range []string{"../../README.md", "../../docs/GETTING-STARTED.md", "../../.github/release-notes.md", "../../site/docs.html"} {
		data, _ := os.ReadFile(name)
		if !strings.Contains(string(data), site+"/docs") {
			t.Errorf("%s does not say where the documentation is: %s/docs", name, site)
		}
	}
}
