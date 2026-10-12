// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package policy

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

const (
	maxInvocations = 4000 // commands reviewed in one action, scripts included
	maxScripts     = 12   // script files opened in one action
	peekBytes      = 64 << 10
)

// baseCtx is the context an outer command passes to a nested analysis (bash -c STRING, a script it runs): its
// stdout redirects and pipe position still apply to everything inside.
type baseCtx struct {
	redirs     []redirect
	piped      bool
	inPipeline bool
	background bool
}

func baseFrom(c *invocation) baseCtx {
	return baseCtx{redirs: c.redirs, piped: c.piped, inPipeline: c.inPipeline, background: c.background}
}

type written struct {
	known   bool
	content string
}

// analysis is the state of evaluating one action.
type analysis struct {
	e    *Engine
	ctx  context.Context
	hits []RuleHit
	seen map[string]bool

	cwd     string
	written map[string]*written

	count       int
	scripts     int
	tooComplex  bool
	scriptStack []string
}

func newParser() *syntax.Parser { return syntax.NewParser(syntax.Variant(syntax.LangBash)) }

// emit records a hit of the rule with the given ID, if that rule is active.
func (a *analysis) emit(id, invocation string) {
	r, ok := a.e.rs.Rule(id)
	if !ok || r.Disabled {
		return
	}
	if r.NetEgressOnly && !a.e.o.NetEgress {
		return
	}
	key := id + "\x00" + invocation
	if a.seen[key] {
		return
	}
	a.seen[key] = true
	a.hits = append(a.hits, RuleHit{ID: id, Class: r.Class, Invocation: invocation, Explanation: r.Explain})
}

func (a *analysis) unparseable(why string) { a.emit("DENY-UNPARSEABLE", why) }

// analyzeText parses a shell command line and reviews every command in it.
func (a *analysis) analyzeText(text string, depth int, base baseCtx) {
	if depth > a.e.o.MaxNesting {
		a.unparseable(fmt.Sprintf("shells nested deeper than %d levels", a.e.o.MaxNesting))
		return
	}
	file, err := newParser().Parse(strings.NewReader(text), "")
	if err != nil {
		msg := err.Error()
		if len(msg) > 140 {
			msg = msg[:137] + "..."
		}
		a.unparseable("syntax error: " + msg)
		return
	}
	saved := a.cwd
	a.walk(file, depth, base)
	a.cwd = saved
}

// walk visits every node of the tree. Because syntax.Walk reaches every command wherever it hides (pipelines,
// subshells, functions, loops, command and process substitutions, here-document bodies, test expressions), no
// command can escape review by nesting. The ancestor stack gives each command its context.
func (a *analysis) walk(root syntax.Node, depth int, base baseCtx) {
	var stack []syntax.Node
	var cwds []string
	syntax.Walk(root, func(n syntax.Node) bool {
		if n == nil {
			top := stack[len(stack)-1]
			prev := cwds[len(cwds)-1]
			stack, cwds = stack[:len(stack)-1], cwds[:len(cwds)-1]
			switch top.(type) {
			case *syntax.Subshell, *syntax.CmdSubst, *syntax.ProcSubst:
				a.cwd = prev // a cd inside these does not change the parent's directory
			}
			return true
		}
		stack, cwds = append(stack, n), append(cwds, a.cwd)
		switch n := n.(type) {
		case *syntax.CallExpr:
			a.call(n, stack, depth, base)
		case *syntax.FuncDecl:
			a.checkForkBomb(n)
		}
		return true
	})
}

// pipeContext derives, from the ancestors of a node, whether it reads from a pipe, belongs to a pipeline at all,
// and runs in the background. Context stops at command and process substitutions and function definitions: what
// is outside them does not apply inside.
func pipeContext(stack []syntax.Node) (piped, inPipeline, background bool) {
	for i := len(stack) - 2; i >= 0; i-- {
		switch n := stack[i].(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst, *syntax.FuncDecl:
			return
		case *syntax.Stmt:
			if n.Background {
				background = true
			}
		case *syntax.BinaryCmd:
			if n.Op == syntax.Pipe || n.Op == syntax.PipeAll {
				inPipeline = true
				if child, ok := stack[i+1].(*syntax.Stmt); ok && child == n.Y {
					piped = true
				}
			}
		}
	}
	return
}

// call reviews one command.
func (a *analysis) call(c *syntax.CallExpr, stack []syntax.Node, depth int, base baseCtx) {
	if len(c.Args) == 0 {
		return // assignments only
	}
	argv, dyn := a.expandWords(c.Args)
	if len(argv) == 0 || argv[0] == "" && !dyn[0] {
		return
	}
	if dyn[0] {
		a.emit("DENY-DYNAMIC-CMD", dynamicText(argv, dyn))
		return
	}

	inv := &invocation{exe: argv[0], name: baseName(argv[0]), argv: argv[1:], dyn: dyn[1:], cwd: a.cwd}
	inv.piped, inv.inPipeline, inv.background = pipeContext(stack)
	inv.piped, inv.inPipeline, inv.background = inv.piped || base.piped, inv.inPipeline || base.inPipeline, inv.background || base.background
	a.applyRedirects(inv, stack, base)
	a.process(inv, depth)
}

// globInName reports whether the command word contains glob characters, which the shell would expand at run
// time (/bin/r? matches /bin/rm), so the name cannot be known. The test command `[` is the one literal exception.
func globInName(exe string) bool {
	if baseName(exe) == "[" {
		exe = strings.TrimSuffix(exe, "[")
	}
	return strings.ContainsAny(exe, "*?[")
}

func dynamicText(argv []string, dyn []bool) string {
	parts := make([]string, len(argv))
	for i, s := range argv {
		if dyn[i] {
			s = "<dynamic>"
		}
		parts[i] = s
	}
	t := strings.Join(parts, " ")
	if len(t) > 160 {
		t = t[:157] + "..."
	}
	return t
}

// process peels wrappers off a command and reviews each layer. Each layer is matched against the rules, so
// `sudo rm -rf /` hits both the privilege-escalation rule and the delete rules.
func (a *analysis) process(inv *invocation, depth int) {
	a.count++
	if a.count > maxInvocations {
		if !a.tooComplex {
			a.tooComplex = true
			a.unparseable("too many commands to review")
		}
		return
	}
	chain := []*invocation{inv}
	cur := inv
	for layers := 0; ; layers++ {
		if cur.name == dynMarker || globInName(cur.exe) {
			a.emit("DENY-DYNAMIC-CMD", cur.text())
			chain = chain[:len(chain)-1]
			break
		}
		w, ok := wrappers[cur.name]
		if !ok {
			break
		}
		if layers >= 16 {
			a.unparseable("wrappers nested too deeply")
			break
		}
		res := w.unwrap(cur)
		if res.unanalyzable {
			a.emit("DENY-DYNAMIC-CMD", cur.text())
			break
		}
		if res.hasSh {
			a.analyzeText(res.shell, depth+1, baseFrom(cur))
		}
		if res.inner == nil {
			break
		}
		chain = append(chain, res.inner)
		cur = res.inner
	}
	for _, c := range chain {
		a.evaluate(c, depth)
	}
}

// evaluate matches one effective command against the rules and follows what it runs.
func (a *analysis) evaluate(c *invocation, depth int) {
	if c.name == "find" {
		c.findRoots = findRoots(c)
		a.findExec(c, depth)
	}
	if a.e.rs.Strict() && !a.e.allow[c.name] {
		a.emit("DENY-NOT-ALLOWLISTED", c.text())
	}
	a.matchRules(c)
	a.follow(c, depth)
	a.trackCwd(c)
	a.recordWrites(c)
}

// findRoots returns the start paths of a find command: the words before the first predicate.
func findRoots(c *invocation) []string {
	var roots []string
	for i, tok := range c.argv {
		if !c.dyn[i] && (strings.HasPrefix(tok, "-") || tok == "(" || tok == "!") {
			break
		}
		if !c.dyn[i] {
			roots = append(roots, tok)
		}
	}
	if len(roots) == 0 && len(c.argv) == 0 {
		roots = []string{"."}
	}
	return roots
}

func (a *analysis) trackCwd(c *invocation) {
	switch c.name {
	case "cd", "pushd":
		ai := c.info()
		switch {
		case len(ai.pos) == 0:
			a.cwd = a.e.o.Home
		case ai.posDyn[0] || ai.pos[0] == "-":
			a.cwd = ""
		default:
			a.cwd = normalizePath(ai.pos[0], c.cwd, a.e.o.Home)
		}
	case "popd":
		a.cwd = ""
	}
}

// ---------------------------------------------------------------------------------------------------------
// rule matching

func (a *analysis) matchRules(c *invocation) {
	for i := range a.e.rs.Rules {
		r := &a.e.rs.Rules[i]
		if r.Disabled || r.Structural || len(r.Matches) == 0 {
			continue
		}
		if r.NetEgressOnly && !a.e.o.NetEgress {
			continue
		}
		for j := range r.Matches {
			if a.matchOne(&r.Matches[j], c) {
				a.emit(r.ID, c.text())
				break
			}
		}
	}
}

func (a *analysis) matchOne(m *Match, c *invocation) bool {
	if len(m.Command) > 0 && !nameMatches(m.Command, c.name) {
		return false
	}
	ai := c.info()
	if len(m.Flags) > 0 && !ai.any(m.Flags...) {
		return false
	}
	if len(m.WithoutFlags) > 0 && ai.any(m.WithoutFlags...) {
		return false
	}
	if len(m.Args) > 0 && !positionalIn(ai, m.Args) {
		return false
	}
	if len(m.argRegex) > 0 {
		hit := false
		for i, arg := range c.argv {
			if c.dyn[i] {
				continue
			}
			for _, re := range m.argRegex {
				if re.MatchString(arg) {
					hit = true
				}
			}
		}
		if !hit {
			return false
		}
	}
	if len(m.WritesTo) > 0 && !a.writesIn(c, m.WritesTo) {
		return false
	}
	if m.Detect != "" && !detectors[m.Detect](a, c) {
		return false
	}
	return true
}

func nameMatches(patterns []string, name string) bool {
	for _, p := range patterns {
		if p == name {
			return true
		}
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

func positionalIn(ai *argInfo, want []string) bool {
	for i, p := range ai.pos {
		if ai.posDyn[i] {
			continue
		}
		for _, w := range want {
			if p == w {
				return true
			}
		}
	}
	return false
}

func (a *analysis) writesIn(c *invocation, sets []string) bool {
	for _, p := range a.writeTargets(c) {
		for _, s := range sets {
			if a.e.ps.in(s, p) {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------------------------------------
// words and redirects

// expandEnv is the environment word expansion sees: only the guest account's well-known variables. Every other
// variable is unknown, which makes a word that uses it dynamic.
func (a *analysis) expandEnv() expand.Environ {
	pwd := a.cwd
	if pwd == "" {
		pwd = a.e.o.Home
	}
	return expand.ListEnviron("HOME="+a.e.o.Home, "USER="+a.e.o.User, "LOGNAME="+a.e.o.User, "PWD="+pwd)
}

var knownParams = map[string]bool{"HOME": true, "USER": true, "LOGNAME": true, "PWD": true}

// dynamicWord reports whether a word's value depends on something only known at run time.
func dynamicWord(w *syntax.Word) bool {
	for _, p := range w.Parts {
		if dynamicPart(p) {
			return true
		}
	}
	return false
}

func dynamicPart(p syntax.WordPart) bool {
	switch p := p.(type) {
	case *syntax.Lit, *syntax.SglQuoted:
		return false
	case *syntax.DblQuoted:
		for _, q := range p.Parts {
			if dynamicPart(q) {
				return true
			}
		}
		return false
	case *syntax.ParamExp:
		simple := p.Param != nil && !p.Excl && !p.Length && !p.Width && !p.IsSet && p.Index == nil &&
			p.Slice == nil && p.Repl == nil && p.Names == 0 && p.Exp == nil && p.NestedParam == nil && p.Flags == nil
		return !simple || !knownParams[p.Param.Value]
	default:
		// command substitution, process substitution, arithmetic, extended globs: not knowable in advance
		return true
	}
}

// expandWords evaluates the words of a command into fields. A word with a dynamic part yields one placeholder.
func (a *analysis) expandWords(words []*syntax.Word) (argv []string, dyn []bool) {
	cfg := &expand.Config{Env: a.expandEnv(), NoUnset: true}
	for _, w := range words {
		if dynamicWord(w) || braceFields(w) > maxBraceFields {
			argv, dyn = append(argv, dynMarker), append(dyn, true)
			continue
		}
		fields, err := expand.Fields(cfg, w)
		if err != nil {
			argv, dyn = append(argv, dynMarker), append(dyn, true)
			continue
		}
		for _, f := range fields {
			argv, dyn = append(argv, f), append(dyn, false)
		}
	}
	return argv, dyn
}

// applyRedirects fills the redirect-derived fields of inv from its enclosing statements.
func (a *analysis) applyRedirects(inv *invocation, stack []syntax.Node, base baseCtx) {
	inv.redirs = append(inv.redirs, base.redirs...)
	for i := len(stack) - 2; i >= 0; i-- {
		switch n := stack[i].(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst, *syntax.FuncDecl:
			return
		case *syntax.Stmt:
			for _, r := range n.Redirs {
				a.applyRedirect(inv, r)
			}
		}
	}
}

func (a *analysis) applyRedirect(inv *invocation, r *syntax.Redirect) {
	target := func() (string, bool) {
		if r.Word == nil {
			return "", true
		}
		argv, dyn := a.expandWords([]*syntax.Word{r.Word})
		if len(argv) != 1 || dyn[0] {
			return "", true
		}
		return argv[0], false
	}
	switch r.Op {
	case syntax.RdrOut, syntax.RdrClob, syntax.RdrAll:
		t, d := target()
		inv.redirs = append(inv.redirs, redirect{target: t, dynamic: d, truncate: true})
	case syntax.AppOut, syntax.AppAll:
		t, d := target()
		inv.redirs = append(inv.redirs, redirect{target: t, dynamic: d})
	case syntax.RdrInOut:
		t, d := target()
		inv.redirs = append(inv.redirs, redirect{target: t, dynamic: d})
	case syntax.DplOut:
		// 2>&1 and >&- duplicate or close a descriptor; `>& file` writes to the file.
		t, d := target()
		if !d && (t == "-" || isNumber(t)) {
			return
		}
		inv.redirs = append(inv.redirs, redirect{target: t, dynamic: d, truncate: true})
	case syntax.RdrIn:
		t, d := target()
		inv.stdinFile, inv.stdinFileDynamic = t, d
	case syntax.Hdoc, syntax.DashHdoc:
		if !inv.hasHeredoc {
			inv.heredoc, inv.heredocDynamic = literalText(r.Hdoc)
			inv.hasHeredoc = true
		}
	case syntax.WordHdoc:
		if !inv.hasHeredoc {
			inv.heredoc, inv.heredocDynamic = literalText(r.Word)
			inv.hasHeredoc = true
			if !inv.heredocDynamic {
				inv.heredoc += "\n"
			}
		}
	}
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// literalText concatenates a word made only of literal text (a here-document body with a quoted delimiter, a
// here-string). Anything with an expansion is dynamic: its text is not known in advance.
func literalText(w *syntax.Word) (text string, dynamic bool) {
	if w == nil {
		return "", false
	}
	var b strings.Builder
	var add func(p syntax.WordPart) bool
	add = func(p syntax.WordPart) bool {
		switch p := p.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				if !add(q) {
					return false
				}
			}
		default:
			return false
		}
		return true
	}
	for _, p := range w.Parts {
		if !add(p) {
			return "", true
		}
	}
	return b.String(), false
}

// ---------------------------------------------------------------------------------------------------------
// functions

// checkForkBomb flags a function that calls itself from a pipeline or in the background, the shape of
// `:(){ :|:& };:`.
func (a *analysis) checkForkBomb(fd *syntax.FuncDecl) {
	var names []string
	if fd.Name != nil {
		names = append(names, fd.Name.Value)
	}
	for _, n := range fd.Names {
		names = append(names, n.Value)
	}
	if fd.Body == nil || len(names) == 0 {
		return
	}
	var stack []syntax.Node
	syntax.Walk(fd.Body, func(n syntax.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		call, ok := n.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		lit := call.Args[0].Lit()
		for _, name := range names {
			if lit == name {
				if _, inPipe, bg := pipeContext(stack); inPipe || bg {
					a.emit("DENY-FORK-BOMB", name+"() calls itself in a pipeline or in the background")
				}
			}
		}
		return true
	})
}

// ---------------------------------------------------------------------------------------------------------
// shells, scripts and file writes

// follow descends into what a command runs: a shell's -c string or here-document, a script file, a sourced file.
func (a *analysis) follow(c *invocation, depth int) {
	switch {
	case shells[c.name]:
		u := shellUsage(c)
		base := baseFrom(c)
		switch u.kind {
		case useCString:
			if !u.dynamic {
				a.analyzeText(u.text, depth+1, base)
			}
		case useScript:
			if !u.dynamic {
				a.scriptFile(c, u.text, depth, true)
			}
		case useStdin:
			switch {
			case c.hasHeredoc && !c.heredocDynamic:
				a.analyzeText(c.heredoc, depth+1, base)
			case c.stdinFile != "" && !c.stdinFileDynamic:
				a.scriptFile(c, c.stdinFile, depth, true)
			}
		}
	case c.name == "source" || c.name == ".":
		ai := c.info()
		if len(ai.pos) > 0 && !ai.posDyn[0] && !isStdinPath(ai.pos[0]) {
			a.scriptFile(c, ai.pos[0], depth, true)
		}
	case c.name == "trap":
		// trap 'COMMANDS' SIGNAL...: the string is shell code that runs later.
		ai := c.info()
		if len(ai.pos) == 0 || ai.flags["l"] || ai.flags["p"] {
			return
		}
		if ai.posDyn[0] {
			a.emit("DENY-PIPE-TO-SHELL", c.text()+" (trap action built from an expansion)")
		} else if ai.pos[0] != "-" && ai.pos[0] != "" {
			a.analyzeText(ai.pos[0], depth+1, baseFrom(c))
		}
	case strings.Contains(c.exe, "/") && !systemBinDir(c.exe) && !interpreters[c.name]:
		// ./script.sh or /path/to/script: run directly, so the shebang (or bash, without one) decides.
		a.scriptFile(c, c.exe, depth, false)
	}
}

var systemBinDirs = []string{"/bin/", "/sbin/", "/usr/bin/", "/usr/sbin/", "/usr/local/bin/", "/usr/local/sbin/", "/usr/games/", "/snap/bin/"}

func systemBinDir(exe string) bool {
	for _, d := range systemBinDirs {
		if strings.HasPrefix(exe, d) {
			return true
		}
	}
	return false
}

func isStdinPath(p string) bool {
	return p == "-" || p == "/dev/stdin" || strings.HasPrefix(p, "/dev/fd/") || strings.HasPrefix(p, "/proc/self/fd/")
}

// scriptFile reviews a script before it runs. explicitShell is true when a shell was asked to read the file
// (bash x.sh, source x.sh), in which case it is analyzed as shell whatever its first line says.
//
// The content comes from, in order: a file written earlier in the same action (whose text is known only for
// `cat > f <<EOF` and `echo ... > f`), then the guest file system through the ScriptReader. A script created
// in the same action by any other means has unknown content and is refused: split the action so it can be read.
func (a *analysis) scriptFile(c *invocation, p string, depth int, explicitShell bool) {
	norm := normalizePath(p, c.cwd, a.e.o.Home)
	if norm == "" {
		a.emit("DENY-PIPE-TO-SHELL", c.text()+" (script path cannot be determined)")
		return
	}
	for _, s := range a.scriptStack {
		if s == norm {
			return // a script that runs itself; already under review
		}
	}
	if w, ok := a.written[norm]; ok {
		if !w.known {
			a.emit("DENY-PIPE-TO-SHELL", c.text()+" (runs "+norm+", which this action also writes, so its content cannot be reviewed)")
			return
		}
		a.runScriptText(w.content, depth, c)
		return
	}
	if a.e.o.Scripts == nil {
		return
	}
	a.scripts++
	if a.scripts > maxScripts {
		a.unparseable("too many scripts to review")
		return
	}
	head, size, exists, err := a.e.o.Scripts.Peek(a.ctx, norm, peekBytes)
	switch {
	case err != nil:
		a.unparseable("could not read " + norm + " to review it")
		return
	case !exists:
		return // the command will fail by itself
	case size > int64(len(head)) && len(head) >= peekBytes:
		a.unparseable(norm + " is larger than 64 KiB and cannot be fully reviewed")
		return
	case bytes.IndexByte(head, 0) >= 0:
		return // a binary: not analyzable (documented limitation)
	}
	if !explicitShell && bytes.HasPrefix(head, []byte("#!")) && !shebangIsShell(head) {
		return // python, perl, node...: not analyzed (documented limitation)
	}
	a.scriptStack = append(a.scriptStack, norm)
	a.runScriptText(string(head), depth, c)
	a.scriptStack = a.scriptStack[:len(a.scriptStack)-1]
}

func (a *analysis) runScriptText(text string, depth int, c *invocation) {
	base := baseFrom(c)
	base.piped = false
	a.analyzeText(text, depth+1, base)
}

// shebangIsShell reports whether the first line names a shell interpreter, directly or through env.
func shebangIsShell(head []byte) bool {
	line, _, _ := bytes.Cut(head, []byte("\n"))
	fields := strings.Fields(strings.TrimPrefix(string(line), "#!"))
	if len(fields) == 0 {
		return false
	}
	interp := baseName(fields[0])
	if interp == "env" {
		for _, f := range fields[1:] {
			if !strings.HasPrefix(f, "-") && !isAssignment(f) {
				interp = baseName(f)
				break
			}
		}
	}
	return shells[interp]
}

// recordWrites remembers the files this command writes so a later `bash file` in the same action is judged on
// what it will contain.
func (a *analysis) recordWrites(c *invocation) {
	known := map[string]bool{}
	for _, r := range c.redirs {
		if r.dynamic {
			continue
		}
		p := normalizePath(r.target, c.cwd, a.e.o.Home)
		if p == "" {
			continue
		}
		text, ok := knownOutput(c)
		switch {
		case ok && r.truncate:
			a.written[p] = &written{known: true, content: text}
		case ok:
			if prev := a.written[p]; prev != nil && prev.known {
				prev.content += text
			} else {
				a.written[p] = &written{}
			}
		default:
			a.written[p] = &written{}
		}
		known[p] = true
	}
	for _, p := range a.contentTargets(c) {
		if !known[p] {
			a.written[p] = &written{}
		}
	}
}

// knownOutput returns the text a command writes to stdout when that text is known in advance: `cat` fed by a
// literal here-document, and plain `echo`. printf and echo -e interpret escapes, so their output is not known.
func knownOutput(c *invocation) (string, bool) {
	ai := c.info()
	switch c.name {
	case "cat":
		if len(ai.pos) == 0 && c.hasHeredoc && !c.heredocDynamic {
			return c.heredoc, true
		}
	case "echo":
		for i := range c.argv {
			if c.dyn[i] {
				return "", false
			}
		}
		if ai.any("e", "E") {
			return "", false
		}
		return strings.Join(ai.pos, " ") + "\n", true
	}
	return "", false
}

// findExec reviews the commands find runs with -exec, -execdir, -ok and -okdir. {} stands for the files found, so
// it is replaced by each start path: `find /etc -exec rm -rf {} +` is reviewed as `rm -rf /etc`.
func (a *analysis) findExec(c *invocation, depth int) {
	roots := c.findRoots
	if len(roots) == 0 {
		roots = []string{"."}
	}
	for i := 0; i < len(c.argv); i++ {
		if c.dyn[i] {
			continue
		}
		switch c.argv[i] {
		case "-exec", "-execdir", "-ok", "-okdir":
		default:
			continue
		}
		j := i + 1
		for j < len(c.argv) && (c.dyn[j] || (c.argv[j] != ";" && c.argv[j] != "+")) {
			j++
		}
		inner, innerDyn := c.argv[i+1:j], c.dyn[i+1:j]
		i = j
		if len(inner) == 0 {
			continue
		}
		if innerDyn[0] {
			a.emit("DENY-DYNAMIC-CMD", c.text())
			continue
		}
		for _, root := range roots {
			argv := make([]string, len(inner))
			for k, tok := range inner {
				if !innerDyn[k] {
					tok = strings.ReplaceAll(tok, "{}", root)
				}
				argv[k] = tok
			}
			dyn := append([]bool(nil), innerDyn[1:]...)
			if baseName(argv[0]) == "rm" {
				// {} stands for every file under the start path, so rm there is a recursive delete of it.
				argv, dyn = append(argv, "-r"), append(dyn, false)
			}
			a.process(&invocation{
				exe: argv[0], name: baseName(argv[0]), argv: argv[1:], dyn: dyn,
				redirs: c.redirs, piped: c.piped, inPipeline: c.inPipeline, background: c.background, cwd: c.cwd,
			}, depth)
		}
	}
}

// maxBraceFields bounds how many fields one word may brace-expand into. {a,b}{a,b}... doubles with every group, so
// an unchecked expansion is a memory bomb; past the bound the word is treated as unknown instead.
const maxBraceFields = 256

// braceFields counts, without expanding, how many fields w brace-expands into (saturating well above the bound).
func braceFields(w *syntax.Word) int {
	cp := *w // SplitBraces replaces the Parts slice, so work on a copy as expand.Fields does
	if !syntax.SplitBraces(&cp) {
		return 1
	}
	return countBraceParts(cp.Parts)
}

const braceSaturation = 1 << 20

func countBraceParts(parts []syntax.WordPart) int {
	n := 1
	for _, p := range parts {
		if be, ok := p.(*syntax.BraceExp); ok {
			n *= braceExpCount(be)
			if n >= braceSaturation {
				return braceSaturation
			}
		}
	}
	return n
}

func braceExpCount(be *syntax.BraceExp) int {
	if be.Sequence {
		// {1..100} or {a..z} or {1..100..5}
		if len(be.Elems) < 2 {
			return braceSaturation
		}
		lo, errLo := strconv.Atoi(be.Elems[0].Lit())
		hi, errHi := strconv.Atoi(be.Elems[1].Lit())
		if errLo != nil || errHi != nil {
			return 52 // letters: at most a-z, A-Z
		}
		step := 1
		if len(be.Elems) > 2 {
			if st, err := strconv.Atoi(be.Elems[2].Lit()); err == nil && st != 0 {
				step = st
			}
		}
		if step < 0 {
			step = -step
		}
		span := hi - lo
		if span < 0 {
			span = -span
		}
		if span/step+1 > braceSaturation {
			return braceSaturation
		}
		return span/step + 1
	}
	total := 0
	for _, el := range be.Elems {
		total += countBraceParts(el.Parts)
		if total >= braceSaturation {
			return braceSaturation
		}
	}
	return total
}
