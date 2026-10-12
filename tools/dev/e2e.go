// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package main

// cmdE2E runs the end-to-end tests, which are behind the e2e build tag because they boot a real VM. Extra
// arguments are passed to go test, e.g. `go run ./tools/dev e2e -run TestKill`.
func cmdE2E(args []string) error {
	goArgs := append([]string{"test", "-tags", "e2e", "-count=1", "-timeout", "30m"}, args...)
	goArgs = append(goArgs, "./...")
	return run(nil, "go", goArgs...)
}
