// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package policy

import "strings"

// wrapper describes a command that runs another command given in its arguments (env, nohup, timeout, xargs...).
// The engine peels wrappers off so the rules see the command that really runs; each wrapper is itself also
// matched against the rules (sudo is denied in its own right).
type wrapper struct {
	// valueFlags are options that consume the next word as their value (exact match).
	valueFlags map[string]bool
	// reject are options that hide the command from analysis (env -S "split string"); using one is unanalyzable.
	reject map[string]bool
	// skip is the number of positional words before the command (timeout's duration, chroot's new root).
	skip int
	// skipUnless disables skip when that option was given (taskset -c LIST makes the mask word go away).
	skipUnless string
	// assigns allows NAME=value words before the command (env).
	assigns bool
	// none means the wrapper can describe instead of run (command -v); those options end analysis harmlessly.
	describeFlags map[string]bool
	// shellString means the remaining words are one shell command line (watch without -x).
	shellString bool
	// extraDynamic appends an unknown argument (xargs adds its stdin items).
	extraDynamic bool
}

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

var wrappers = map[string]*wrapper{
	"command": {describeFlags: set("-v", "-V")},
	"builtin": {},
	"exec":    {valueFlags: set("-a")},
	"env":     {valueFlags: set("-u", "--unset", "-C", "--chdir"), reject: set("-S", "--split-string"), assigns: true},
	"nohup":   {},
	"nice":    {valueFlags: set("-n", "--adjustment")},
	"ionice":  {valueFlags: set("-c", "-n", "-p", "-P", "-u", "--class", "--classdata", "--pid", "--pgid", "--uid")},
	"timeout": {valueFlags: set("-s", "-k", "--signal", "--kill-after"), skip: 1},
	"stdbuf":  {valueFlags: set("-i", "-o", "-e", "--input", "--output", "--error")},
	"time":    {valueFlags: set("-f", "--format", "-o", "--output")},
	"setsid":  {},
	"xargs": {
		valueFlags: set("-n", "-P", "-I", "-L", "-d", "-E", "-s", "-a", "--max-args", "--max-procs", "--delimiter",
			"--eof", "--max-chars", "--arg-file", "--max-lines", "--process-slot-var"),
		extraDynamic: true,
	},
	"busybox": {},
	"sudo":    {valueFlags: set("-u", "-g", "-h", "-p", "-C", "-r", "-t", "-U", "-D", "-R", "-T", "--user", "--group", "--host", "--prompt")},
	"doas":    {valueFlags: set("-u", "-C")},
	"chroot":  {valueFlags: set("--userspec", "--groups"), skip: 1},
	"unshare": {},
	"taskset": {valueFlags: set("-c", "--cpu-list"), skip: 1, skipUnless: "-c"},
	"watch":   {valueFlags: set("-n", "--interval"), shellString: true},
	"flock":   {valueFlags: set("-w", "--timeout", "-E", "--conflict-exit-code"), skip: 1},
}

// shells are interpreters of shell code: `bash -c STRING` is analyzed recursively.
var shells = set("sh", "bash", "dash", "zsh", "ksh", "ash", "mksh", "rbash")

// interpreters read a program from stdin when given no script, -c, -e or -m.
var interpreters = set("python", "python2", "python3", "perl", "ruby", "node", "nodejs", "php", "lua", "luajit",
	"tclsh", "wish", "pwsh", "Rscript", "julia")

func isAssignment(tok string) bool {
	i := strings.IndexByte(tok, '=')
	if i <= 0 {
		return false
	}
	for j, r := range tok[:i] {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (j > 0 && r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// unwrapResult is what peeling one wrapper layer produced.
type unwrapResult struct {
	inner *invocation // the command the wrapper runs; nil if there is none to analyze
	shell string      // alternatively a shell command line to analyze (watch)
	hasSh bool
	// unanalyzable is true when the wrapper hides the command (env -S).
	unanalyzable bool
}

// unwrap peels one layer of wrapper w off inv.
func (w *wrapper) unwrap(inv *invocation) unwrapResult {
	argv, dyn := inv.argv, inv.dyn
	i := 0
	seen := map[string]bool{}
	for i < len(argv) {
		tok := argv[i]
		if dyn[i] {
			break // an unknown word where an option or the command should be: the command name will be flagged
		}
		if tok == "--" {
			i++
			break
		}
		if strings.HasPrefix(tok, "-") && len(tok) > 1 {
			if w.reject[tok] || (len(tok) > 2 && w.reject[tok[:2]]) {
				return unwrapResult{unanalyzable: true}
			}
			if w.describeFlags[tok] {
				return unwrapResult{}
			}
			seen[tok] = true
			if w.valueFlags[tok] {
				i += 2
			} else {
				i++
			}
			continue
		}
		if w.assigns && isAssignment(tok) {
			i++
			continue
		}
		break
	}
	if w.skip > 0 && (w.skipUnless == "" || !seen[w.skipUnless]) {
		i += w.skip
	}
	if i >= len(argv) {
		return unwrapResult{}
	}

	if w.shellString && !seen["-x"] && !seen["--exec"] {
		return unwrapResult{shell: strings.Join(argv[i:], " "), hasSh: true}
	}

	rest, restDyn := argv[i:], dyn[i:]
	if inv.name == "flock" {
		// flock [options] FILE -c COMMAND: the command line comes after the file, or after -c among the options.
		switch {
		case seen["-c"] || seen["--command"]:
			return unwrapResult{shell: strings.Join(rest, " "), hasSh: true}
		case len(rest) > 1 && !restDyn[0] && (rest[0] == "-c" || rest[0] == "--command"):
			return unwrapResult{shell: strings.Join(rest[1:], " "), hasSh: true}
		}
	}

	inner := &invocation{
		exe: rest[0], name: baseName(rest[0]),
		argv: append([]string(nil), rest[1:]...), dyn: append([]bool(nil), restDyn[1:]...),
		redirs: inv.redirs, piped: inv.piped, inPipeline: inv.inPipeline, background: inv.background,
		heredoc: inv.heredoc, hasHeredoc: inv.hasHeredoc, heredocDynamic: inv.heredocDynamic, cwd: inv.cwd,
		stdinFile: inv.stdinFile, stdinFileDynamic: inv.stdinFileDynamic,
	}
	if restDyn[0] {
		inner.exe, inner.name = dynMarker, dynMarker
	}
	if w.extraDynamic {
		// xargs appends the items it reads from stdin; with -I the replacement string stands for them.
		repl := ""
		for j := 0; j+1 < len(argv); j++ {
			if argv[j] == "-I" {
				repl = argv[j+1]
			} else if strings.HasPrefix(argv[j], "-I") && len(argv[j]) > 2 {
				repl = argv[j][2:]
			}
		}
		for j, a := range inner.argv {
			if repl != "" && a == repl {
				inner.argv[j], inner.dyn[j] = dynMarker, true
			}
		}
		inner.argv, inner.dyn = append(inner.argv, dynMarker), append(inner.dyn, true)
	}
	return unwrapResult{inner: inner}
}
