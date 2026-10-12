// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package policy

import (
	"path"
	"strings"
)

// dynMarker stands in for an argument whose value is only known at run time ($var, $(cmd), <(cmd), a glob that
// a rule cannot resolve). It contains a NUL byte, which can never occur in a real argument.
const dynMarker = "\x00dynamic"

// redirect is a file the command's output goes to. Redirects that duplicate descriptors or only read are not
// recorded.
type redirect struct {
	target   string // as written after expansion; empty when dynamic
	dynamic  bool
	truncate bool // > &> >| (and >&FILE) empty the file; >> &>> <> do not
}

// invocation is one command execution found in the shell AST, after quote removal, brace and tilde expansion.
type invocation struct {
	exe  string   // the command word as written, e.g. "/usr/bin/rm"
	name string   // its base name, e.g. "rm"
	argv []string // arguments after the command word; dynMarker where the value is unknown
	dyn  []bool   // dyn[i] reports whether argv[i] is unknown

	redirs     []redirect
	piped      bool // stdin comes from a pipe (the command is not the first stage)
	inPipeline bool // the command is a stage of some pipeline
	background bool
	// heredoc is the text fed to stdin by a here-document or here-string; heredocDynamic means it contains an
	// expansion and so is unknown.
	heredoc        string
	hasHeredoc     bool
	heredocDynamic bool
	// stdinFile is the file a `< file` redirect feeds to stdin; stdinFileDynamic means its name is unknown.
	stdinFile        string
	stdinFileDynamic bool

	cwd       string // tracked working directory; "" when unknown
	findRoots []string

	parsed *argInfo
}

// argInfo is the flag/positional view of an invocation's arguments.
type argInfo struct {
	flags  map[string]bool
	pos    []string
	posDyn []bool
}

// singleDashWords are commands whose options are whole words after one dash (find -name -delete), not bundles
// of letters.
var singleDashWords = map[string]bool{"find": true}

func (inv *invocation) info() *argInfo {
	if inv.parsed == nil {
		inv.parsed = parseArgs(inv.name, inv.argv, inv.dyn)
	}
	return inv.parsed
}

// parseArgs splits arguments into flags and positionals. Short flags are letters ("-rf" sets r and f), long flags
// are names without dashes ("--no-preserve-root" sets no-preserve-root). A value given as a separate word lands
// in the positionals; the rules are written with that in mind.
func parseArgs(name string, argv []string, dyn []bool) *argInfo {
	ai := &argInfo{flags: map[string]bool{}}
	words := singleDashWords[name]
	endOpts := false
	for i, tok := range argv {
		switch {
		case dyn[i]:
			ai.pos, ai.posDyn = append(ai.pos, tok), append(ai.posDyn, true)
		case endOpts || tok == "-" || !strings.HasPrefix(tok, "-"):
			ai.pos, ai.posDyn = append(ai.pos, tok), append(ai.posDyn, false)
		case tok == "--":
			endOpts = true
		case strings.HasPrefix(tok, "--"):
			long, _, _ := strings.Cut(tok[2:], "=")
			ai.flags[long] = true
		case words:
			ai.flags[tok[1:]] = true
		default:
			for _, r := range tok[1:] {
				if r == '=' {
					break
				}
				ai.flags[string(r)] = true
			}
		}
	}
	return ai
}

func (ai *argInfo) any(names ...string) bool {
	for _, n := range names {
		if ai.flags[n] {
			return true
		}
	}
	return false
}

// text renders the invocation for messages.
func (inv *invocation) text() string {
	parts := make([]string, 0, len(inv.argv)+1)
	exe := inv.exe
	if exe == dynMarker {
		exe = "<dynamic>" // a wrapper's inner command can be unknown too (nice $X)
	}
	parts = append(parts, exe)
	for i, a := range inv.argv {
		if inv.dyn[i] {
			a = "<dynamic>"
		}
		parts = append(parts, a)
	}
	s := strings.Join(parts, " ")
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}

// Commands whose positional arguments are all files they modify or delete.
var writesAllPositionals = map[string]bool{
	"mv": true, "truncate": true, "shred": true, "rm": true, "unlink": true, "rmdir": true, "chmod": true,
	"chown": true, "chgrp": true, "chattr": true, "setfacl": true, "patch": true, "ed": true, "sponge": true,
	"mkfifo": true, "mknod": true, "tee": true, "wipefs": true,
}

// Commands whose last positional argument is the destination.
var writesLastPositional = map[string]bool{"cp": true, "install": true, "rsync": true, "ln": true}

// writeTargets lists the normalized absolute paths this invocation writes, deletes or changes. Redirect targets
// are included. Targets whose value is unknown are left out: the rules that need them are about specific files,
// and an unknown path cannot be shown to be one of them.
func (a *analysis) writeTargets(inv *invocation) []string {
	var out []string
	add := func(p string) {
		if n := normalizePath(p, inv.cwd, a.e.o.Home); n != "" {
			out = append(out, n)
		}
	}
	for _, r := range inv.redirs {
		if !r.dynamic {
			add(r.target)
		}
	}
	ai := inv.info()
	positional := func() []string {
		var ps []string
		for i, p := range ai.pos {
			if !ai.posDyn[i] {
				ps = append(ps, p)
			}
		}
		return ps
	}

	switch {
	case inv.name == "dd":
		for i, tok := range inv.argv {
			if !inv.dyn[i] && strings.HasPrefix(tok, "of=") {
				add(strings.TrimPrefix(tok, "of="))
			}
		}
	case writesLastPositional[inv.name]:
		ps := positional()
		switch {
		case ai.any("t", "target-directory"):
			for _, p := range ps {
				add(p)
			}
		case len(ps) > 0:
			add(ps[len(ps)-1])
		}
	case inv.name == "sed" || inv.name == "perl" || inv.name == "ruby":
		if ai.any("i", "in-place") {
			for _, p := range positional() {
				add(p)
			}
		}
	case writesAllPositionals[inv.name]:
		for _, p := range positional() {
			add(p)
		}
	case inv.name == "curl":
		for _, p := range curlTargets(inv) {
			add(p)
		}
	case inv.name == "wget":
		for _, p := range wgetTargets(inv) {
			add(p)
		}
	}
	return out
}

// metadataOnly commands change a file's permissions or attributes, not what it contains.
var metadataOnly = map[string]bool{"chmod": true, "chown": true, "chgrp": true, "chattr": true, "setfacl": true}

// contentTargets are the files whose content this invocation may change: writeTargets without the commands that
// only change metadata. `chmod +x script.sh` must not make a script's known content unknown.
func (a *analysis) contentTargets(inv *invocation) []string {
	if metadataOnly[inv.name] {
		return nil
	}
	return a.writeTargets(inv)
}

// baseName is the command name without directories.
func baseName(exe string) string {
	exe = strings.TrimRight(exe, "/")
	return path.Base(exe)
}

// shortOptionValue reads the value of a short option inside a bundle of letters ("-fsSLo FILE", "-oFILE"):
// everything after the option letter is its value, or else the next word. It returns the value and how many
// extra words it consumed.
func shortOptionValue(argv []string, dyn []bool, i, letterIndex int) (string, int) {
	if rest := argv[i][1+letterIndex+1:]; rest != "" {
		return rest, 0
	}
	if i+1 < len(argv) && !dyn[i+1] {
		return argv[i+1], 1
	}
	return "", 0
}

// urlBasename is the file name curl -O and wget give a download: the last segment of the URL path.
func urlBasename(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
		if j := strings.IndexByte(u, '/'); j >= 0 {
			u = u[j:]
		} else {
			u = ""
		}
	}
	u = strings.TrimRight(u, "/")
	if k := strings.LastIndexByte(u, '/'); k >= 0 {
		u = u[k+1:]
	}
	if u == "" {
		return "index.html"
	}
	return u
}

func isURL(s string) bool { return strings.Contains(s, "://") }

// curlTargets lists the files curl writes: -o/--output FILE (also inside a bundle such as -fsSLo FILE) and, for
// -O/--remote-name, the base name of each URL in the current directory.
func curlTargets(inv *invocation) []string {
	var out []string
	remoteName := false
	for i := 0; i < len(inv.argv); i++ {
		tok := inv.argv[i]
		if inv.dyn[i] {
			continue
		}
		switch {
		case tok == "--output" && i+1 < len(inv.argv) && !inv.dyn[i+1]:
			out = append(out, inv.argv[i+1])
			i++
		case strings.HasPrefix(tok, "--output="):
			out = append(out, strings.TrimPrefix(tok, "--output="))
		case tok == "--remote-name" || tok == "--remote-name-all":
			remoteName = true
		case strings.HasPrefix(tok, "--") || tok == "-" || !strings.HasPrefix(tok, "-"):
		default:
		letters:
			for k := 0; k < len(tok)-1; k++ {
				switch tok[1+k] {
				case 'o':
					val, used := shortOptionValue(inv.argv, inv.dyn, i, k)
					if val != "" {
						out = append(out, val)
					}
					i += used
					break letters
				case 'O':
					remoteName = true
				}
			}
		}
	}
	if remoteName {
		for i, tok := range inv.argv {
			if !inv.dyn[i] && isURL(tok) {
				out = append(out, urlBasename(tok))
			}
		}
	}
	return out
}

// wgetTargets lists the files wget writes: -O/--output-document FILE, or else each URL's base name under -P DIR
// (default: the current directory).
func wgetTargets(inv *invocation) []string {
	var out []string
	prefix := ""
	hasOutput := false
	for i := 0; i < len(inv.argv); i++ {
		tok := inv.argv[i]
		if inv.dyn[i] {
			continue
		}
		switch {
		case (tok == "--output-document") && i+1 < len(inv.argv) && !inv.dyn[i+1]:
			out, hasOutput = append(out, inv.argv[i+1]), true
			i++
		case strings.HasPrefix(tok, "--output-document="):
			out, hasOutput = append(out, strings.TrimPrefix(tok, "--output-document=")), true
		case tok == "--directory-prefix" && i+1 < len(inv.argv) && !inv.dyn[i+1]:
			prefix = inv.argv[i+1]
			i++
		case strings.HasPrefix(tok, "--directory-prefix="):
			prefix = strings.TrimPrefix(tok, "--directory-prefix=")
		case strings.HasPrefix(tok, "--") || tok == "-" || !strings.HasPrefix(tok, "-"):
		default:
		letters:
			for k := 0; k < len(tok)-1; k++ {
				switch tok[1+k] {
				case 'O':
					val, used := shortOptionValue(inv.argv, inv.dyn, i, k)
					if val != "" && val != "-" {
						out = append(out, val)
					}
					hasOutput = true
					i += used
					break letters
				case 'P':
					val, used := shortOptionValue(inv.argv, inv.dyn, i, k)
					prefix = val
					i += used
					break letters
				}
			}
		}
	}
	if !hasOutput {
		for i, tok := range inv.argv {
			if !inv.dyn[i] && isURL(tok) {
				name := urlBasename(tok)
				if prefix != "" {
					name = strings.TrimRight(prefix, "/") + "/" + name
				}
				out = append(out, name)
			}
		}
	}
	return out
}
