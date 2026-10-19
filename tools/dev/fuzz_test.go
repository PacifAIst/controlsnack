// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package main

import "testing"

func TestAFuzzRunThatOnlyRanOutOfTimeIsNotAFinding(t *testing.T) {
	timeOut := "fuzz: elapsed: 31s, execs: 1130574 (0/sec), new interesting: 108 (total: 116)\n--- FAIL: FuzzInspect (31.00s)\n    context deadline exceeded\nFAIL\n"
	if !fuzzRanOutOfTime(timeOut) {
		t.Error("a run that ended with a deadline and found nothing is not a failure")
	}
	for name, out := range map[string]string{
		"a failing input": timeOut + "    Failing input written to testdata/fuzz/FuzzInspect/abc\n",
		"a panic":         "panic: runtime error: index out of range\n--- FAIL: FuzzInspect\n    context deadline exceeded\n",
		"another failure": "--- FAIL: FuzzInspect (3.00s)\n    inspect.go:12: something else\n",
		"nothing":         "",
	} {
		if fuzzRanOutOfTime(out) {
			t.Errorf("%s was taken for a run that only ran out of time", name)
		}
	}
}
