// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package policy

import (
	"regexp"
	"strings"
)

// detectors are the checks that need more than the declarative match conditions can say. A rule refers to one by
// name in its `detect` field.
var detectors = map[string]func(a *analysis, c *invocation) bool{
	"root-wipe":         detectRootWipe,
	"mass-kill":         detectMassKill,
	"chmod-setuid":      detectChmodSetuid,
	"truncate-redirect": detectTruncateRedirect,
	"pipe-to-shell":     detectPipeToShell,
}

// detectRootWipe: recursive rm of /, the home directory or a system directory, rm --no-preserve-root, or find
// over such a path with -delete. (find -exec rm is analyzed as an rm command with {} replaced by the start paths.)
func detectRootWipe(a *analysis, c *invocation) bool {
	ai := c.info()
	switch c.name {
	case "rm":
		if ai.flags["no-preserve-root"] {
			return true
		}
		if !ai.any("r", "R", "recursive") {
			return false
		}
		for i, p := range ai.pos {
			if !ai.posDyn[i] && a.e.ps.isProtectedRoot(p, c.cwd) {
				return true
			}
		}
	case "find":
		if !ai.flags["delete"] {
			return false
		}
		for _, root := range c.findRoots {
			if a.e.ps.isProtectedRoot(root, c.cwd) {
				return true
			}
		}
	}
	return false
}

// detectMassKill: kill -9 -1, kill -s KILL -1, kill 1 and the like. A "-1" is the pid "every process" only when it
// is the last word, follows another option or follows --; `kill -1 1234` sends SIGHUP (signal 1) to one process.
func detectMassKill(_ *analysis, c *invocation) bool {
	if c.name != "kill" {
		return false
	}
	for i, tok := range c.argv {
		if c.dyn[i] {
			continue
		}
		switch tok {
		case "1":
			return true
		case "-1":
			prevOption := i > 0 && !c.dyn[i-1] && (strings.HasPrefix(c.argv[i-1], "-") || c.argv[i-1] == "--")
			if i == len(c.argv)-1 || prevOption {
				return true
			}
		}
	}
	return false
}

var (
	reSymbolicSetid = regexp.MustCompile(`^[ugoa]*[+=][rwxXt]*s`)
	reOctalMode     = regexp.MustCompile(`^[0-7]{4}$`)
)

// detectChmodSetuid: chmod (or install -m) giving a file the setuid or setgid bit.
func detectChmodSetuid(_ *analysis, c *invocation) bool {
	if c.name != "chmod" && c.name != "install" {
		return false
	}
	for i, tok := range c.argv {
		if c.dyn[i] {
			continue
		}
		// In a four-digit octal mode the first digit holds setuid (4), setgid (2) and sticky (1).
		if reOctalMode.MatchString(tok) && (tok[0]-'0')&6 != 0 {
			return true
		}
		for _, clause := range strings.Split(tok, ",") {
			if reSymbolicSetid.MatchString(clause) {
				return true
			}
		}
	}
	return false
}

// detectTruncateRedirect: a > redirect (including >|, &> and >& FILE) to a file outside /tmp. Whether the file
// exists is not known here, so creating a new file counts too. Output to the usual pseudo-files is harmless.
func detectTruncateRedirect(a *analysis, c *invocation) bool {
	for _, r := range c.redirs {
		if !r.truncate {
			continue
		}
		if r.dynamic {
			return true
		}
		p := normalizePath(r.target, c.cwd, a.e.o.Home)
		switch {
		case p == "":
			return true
		case strings.HasPrefix(p, "/tmp/"), strings.HasPrefix(p, "/dev/fd/"), strings.HasPrefix(p, "/proc/self/fd/"):
		case p == "/dev/null", p == "/dev/stdout", p == "/dev/stderr", p == "/dev/tty", p == "/dev/zero":
		default:
			return true
		}
	}
	return false
}

// shell usage kinds
const (
	useNone = iota
	useCString
	useScript
	useStdin
)

type shellUse struct {
	kind    int
	text    string // the -c string, or the script path
	dynamic bool
}

// shellUsage works out how a shell command line is using the shell: running a -c string, a script file, or code
// read from stdin.
func shellUsage(c *invocation) shellUse {
	hasC, hasS := false, false
	i := 0
loop:
	for ; i < len(c.argv); i++ {
		tok := c.argv[i]
		if c.dyn[i] {
			break
		}
		switch {
		case tok == "--":
			i++
			break loop
		case tok == "-o" || tok == "-O" || tok == "+o" || tok == "+O" || tok == "--rcfile" || tok == "--init-file":
			i++ // the option's value follows
		case strings.HasPrefix(tok, "--"):
		case len(tok) > 1 && tok[0] == '-':
			if strings.ContainsRune(tok, 'c') {
				hasC = true
			}
			if strings.ContainsRune(tok, 's') {
				hasS = true
			}
		case len(tok) > 1 && tok[0] == '+':
		default:
			break loop
		}
	}
	switch {
	case hasC:
		if i >= len(c.argv) {
			return shellUse{kind: useNone}
		}
		return shellUse{kind: useCString, text: c.argv[i], dynamic: c.dyn[i]}
	case hasS || i >= len(c.argv) || c.argv[i] == "-":
		return shellUse{kind: useStdin}
	default:
		return shellUse{kind: useScript, text: c.argv[i], dynamic: c.dyn[i]}
	}
}

// interpreterReadsStdin reports whether a non-shell interpreter takes its program from stdin: no script, and none
// of the options that carry the code (-c, -e, -m, -r, -p, -E).
func interpreterReadsStdin(c *invocation) bool {
	ai := c.info()
	if ai.any("c", "e", "E", "m", "r", "p", "eval", "print", "version", "V", "h", "help") {
		return false
	}
	return len(ai.pos) == 0 || ai.pos[0] == "-"
}

// detectPipeToShell: code that cannot be reviewed before it runs. That is eval, a shell or interpreter whose
// program arrives through a pipe, a shell given a -c string or script path built by an expansion (including
// bash <(curl ...)), sourcing such a path or a stream, and a shell fed a here-document with expansions.
func detectPipeToShell(_ *analysis, c *invocation) bool {
	switch {
	case c.name == "eval":
		return true
	case shells[c.name]:
		u := shellUsage(c)
		switch u.kind {
		case useCString, useScript:
			return u.dynamic
		case useStdin:
			return c.piped || (c.hasHeredoc && c.heredocDynamic) || c.stdinFileDynamic
		}
	case c.name == "source" || c.name == ".":
		ai := c.info()
		if len(ai.pos) == 0 {
			return false
		}
		return ai.posDyn[0] || isStdinPath(ai.pos[0])
	case interpreters[c.name]:
		return c.piped && interpreterReadsStdin(c)
	}
	return false
}
