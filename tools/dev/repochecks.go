// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

// spdxHeader must open every Go source file (PLAN.md section 0.6, D-00, D-36).
const spdxHeader = "// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador\n// SPDX-License-Identifier: AGPL-3.0-only\n"

// listenAllowlist names the only non-test files that may open a network listener. Everything that listens goes
// through one helper that binds 127.0.0.1 and refuses anything else (PLAN.md section 6.1, invariant 1).
var listenAllowlist = map[string]bool{
	"internal/platform/listen.go": true,
}

var (
	// listenCall matches the standard library's ways of opening a listener.
	listenCall = regexp.MustCompile(`\b(?:net\.Listen\w*|tls\.Listen\w*|http\.ListenAndServe\w*)\(|\.Listen(?:TCP|UDP|Packet|Unix)?\(`)
	// wildcardAddr matches a string literal that is exactly a wildcard bind address (not a CIDR such as "0.0.0.0/8").
	wildcardAddr = regexp.MustCompile(`"(?:0\.0\.0\.0|\[::\]|::)(?::\d+)?"`)
	// codeGenerated matches the standard marker for generated files.
	codeGenerated = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)
)

// checkSource applies the repository invariants to one Go file and returns a list of problems.
// path uses forward slashes and is relative to the module root.
func checkSource(path string, content []byte) []string {
	var problems []string
	generated := codeGenerated.Match(content[:min(len(content), 1024)])
	if !generated && !bytes.HasPrefix(content, []byte(spdxHeader)) {
		problems = append(problems, path+": missing the SPDX header (see CLAUDE.md > Source headers)")
	}
	if strings.HasSuffix(path, "_test.go") || strings.HasPrefix(path, "tools/") {
		return problems
	}
	for i, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		if wildcardAddr.MatchString(line) {
			problems = append(problems, fmt.Sprintf("%s:%d: wildcard bind address; listeners are 127.0.0.1 only", path, i+1))
		}
		if listenCall.MatchString(line) && !listenAllowlist[path] {
			problems = append(problems, fmt.Sprintf("%s:%d: opens a listener outside internal/platform/listen.go", path, i+1))
		}
	}
	return problems
}

// stepRepo runs the repository-wide invariants that no linter knows about.
func stepRepo() (string, error) {
	var problems []string
	err := walkGo(func(path string, content []byte) error {
		problems = append(problems, checkSource(path, content)...)
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(problems) > 0 {
		return "", fmt.Errorf("repository checks failed:\n  %s", strings.Join(problems, "\n  "))
	}
	return "", nil
}
