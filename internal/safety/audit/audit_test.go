// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/PacifAIst/controlsnack-local/schema"
)

// stepClock returns a time that moves forward one second per call.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func newStepClock() *stepClock {
	return &stepClock{t: time.Date(2026, 10, 12, 5, 30, 0, 123456000, time.UTC)}
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

func openLog(t *testing.T, o Options) *Log {
	t.Helper()
	if o.Now == nil {
		o.Now = newStepClock().Now
	}
	l, err := Open(t.TempDir(), "s_7f3a9c21", o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// sample writes a believable session and returns the path.
func sample(t *testing.T, l *Log) string {
	t.Helper()
	recs := []Record{
		{Kind: KindLifecycle, Event: EventStart, Actor: "system", Data: ToRaw(map[string]any{"backend": "ollama", "remote_llm": false})},
		{Kind: KindConfig, Actor: "system", Data: ToRaw(map[string]any{"vm": map[string]any{"memory_mib": 2048}})},
		{Kind: KindUser, Event: "chat", Actor: "user", Reason: "open a terminal"},
		{Kind: KindAction, Actor: "llm", Turn: 1, ActionID: "t1.1", Tool: "exec", Args: ToRaw(map[string]any{"command": "uname -r"}),
			Reason: "check the kernel", Verdict: "allowed", Result: ToRaw(map[string]any{"ok": true, "exit_code": 0})},
		{Kind: KindAction, Actor: "llm", Turn: 2, ActionID: "t2.1", Tool: "exec", Args: ToRaw(map[string]any{"command": "rm -r /tmp/x"}),
			Verdict: "confirmed", Rules: []string{"CONFIRM-DELETE"}, ScreenshotSHA256: strings.Repeat("ab", 32)},
		{Kind: KindAction, Actor: "llm", Turn: 3, ActionID: "t3.1", Tool: "exec", Args: ToRaw(map[string]any{"command": "shutdown now"}),
			Verdict: "denied", Rules: []string{"DENY-POWER"}},
		{Kind: KindLifecycle, Event: EventEnd, Actor: "system"},
	}
	for _, r := range recs {
		if _, err := l.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	return l.Path()
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustVerify(t *testing.T, path string) *Report {
	t.Helper()
	rep, err := Verify(path)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestOpenCreatesAPrivateExclusiveFile(t *testing.T) {
	dir := t.TempDir()
	clk := newStepClock()
	l, err := Open(dir, "s_abc123", Options{Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	if want := filepath.Join(dir, "20261012-s_abc123.jsonl"); l.Path() != want {
		t.Errorf("path %q, want %q", l.Path(), want)
	}
	if l.Session() != "s_abc123" {
		t.Errorf("session %q", l.Session())
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(l.Path())
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode %v, want 0600", info.Mode().Perm())
		}
	}
	if _, err := Open(dir, "s_abc123", Options{Now: clk.Now}); err == nil {
		t.Error("a second log for the same session on the same day must be refused, never appended to")
	}
	for _, bad := range []string{"", "ab", "../evil", "a/b", `a\b`, "has space", "x" + strings.Repeat("y", 70), "a.b", "ünï"} {
		if l2, err := Open(t.TempDir(), bad, Options{}); err == nil {
			_ = l2.Close()
			t.Errorf("session id %q must be refused", bad)
		}
	}
	nested := filepath.Join(t.TempDir(), "x", "y", "audit")
	l3, err := Open(nested, "s_nested", Options{})
	if err != nil {
		t.Fatalf("missing parent directories are created: %v", err)
	}
	_ = l3.Close()
}

func TestChainIsBuiltAsSpecified(t *testing.T) {
	l := openLog(t, Options{})
	path := sample(t, l)
	lines := readLines(t, path)
	if len(lines) != 7 {
		t.Fatalf("%d lines", len(lines))
	}
	first := sha256.Sum256([]byte("controlsnack-audit-v1|s_7f3a9c21"))
	prev := hex.EncodeToString(first[:])
	for i, line := range lines {
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.V != 1 || rec.Seq != uint64(i+1) || rec.Session != "s_7f3a9c21" {
			t.Errorf("record %d header: %+v", i, rec)
		}
		if rec.Prev != prev {
			t.Errorf("record %d: prev %s, want %s", i, rec.Prev, prev)
		}
		// Recompute independently: SHA-256 of the line with the hash member removed.
		var m map[string]json.RawMessage
		_ = json.Unmarshal([]byte(line), &m)
		withoutHash := strings.Replace(line, `,"hash":"`+rec.Hash+`"`, "", 1)
		sum := sha256.Sum256([]byte(withoutHash))
		if hex.EncodeToString(sum[:]) != rec.Hash {
			t.Errorf("record %d: hash is not the SHA-256 of the record without its hash field", i)
		}
		prev = rec.Hash
		if !strings.HasSuffix(rec.TS, "Z") || len(rec.TS) != len("2026-10-12T05:30:01.123456Z") {
			t.Errorf("record %d: timestamp %q is not UTC with microseconds", i, rec.TS)
		}
	}
	rep := mustVerify(t, path)
	if !rep.OK() || !rep.Complete || rep.Records != 7 || rep.Session != "s_7f3a9c21" || rep.LastHash != prev {
		t.Fatalf("%+v", rep)
	}
	if !rep.Last.After(rep.First) {
		t.Errorf("first %v last %v", rep.First, rep.Last)
	}
}

func TestIncompleteLogIsIntactButNotComplete(t *testing.T) {
	l := openLog(t, Options{})
	_, _ = l.Append(Record{Kind: KindLifecycle, Event: EventStart})
	_, _ = l.Append(Record{Kind: KindUser, Event: "chat"})
	rep := mustVerify(t, l.Path())
	if !rep.OK() || rep.Complete {
		t.Fatalf("a running session's log is intact but not complete: %+v", rep)
	}
	_, _ = l.Append(Record{Kind: KindLifecycle, Event: EventKill, Data: ToRaw(map[string]any{"source": "button", "latency_ms": 12})})
	if rep := mustVerify(t, l.Path()); !rep.OK() || !rep.Complete {
		t.Fatalf("a kill record completes the log: %+v", rep)
	}
}

// --- tamper tests (PLAN.md P2-T07: edit, delete, reorder, truncate) -------------------------------------------

func tamperedReport(t *testing.T, mutate func(lines []string) []string) *Report {
	t.Helper()
	l := openLog(t, Options{})
	path := sample(t, l)
	_ = l.Close()
	writeLines(t, path, mutate(readLines(t, path)))
	return mustVerify(t, path)
}

func TestTamperingIsDetected(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(lines []string) []string
		want   string // substring of the first problem
		atLine int
	}{
		{"edited verdict", func(l []string) []string {
			l[5] = strings.Replace(l[5], `"verdict":"denied"`, `"verdict":"allowed"`, 1)
			return l
		}, "does not match its content", 6},
		{"edited command", func(l []string) []string {
			l[3] = strings.Replace(l[3], "uname -r", "uname -a", 1)
			return l
		}, "does not match its content", 4},
		{"removed a rule from a record", func(l []string) []string {
			l[4] = strings.Replace(l[4], `"rules":["CONFIRM-DELETE"],`, "", 1)
			return l
		}, "does not match its content", 5},
		{"deleted a record", func(l []string) []string { return append(l[:3:3], l[4:]...) }, "sequence jumps", 4},
		{"deleted the first record", func(l []string) []string { return l[1:] }, "sequence jumps", 1},
		{"reordered two records", func(l []string) []string { l[2], l[3] = l[3], l[2]; return l }, "sequence jumps", 3},
		{"duplicated a record", func(l []string) []string { return append(l[:4:4], append([]string{l[3]}, l[4:]...)...) }, "sequence jumps", 5},
		{"inserted a forged record", func(l []string) []string {
			forged := strings.Replace(l[3], `"seq":4`, `"seq":5`, 1)
			return append(l[:4:4], append([]string{forged}, l[4:]...)...)
		}, "", 5},
		{"extra field added", func(l []string) []string {
			l[2] = strings.Replace(l[2], `"prev":`, `"approved_by":"admin","prev":`, 1)
			return l
		}, "not a valid audit record", 3},
		{"whitespace added", func(l []string) []string { l[2] = strings.Replace(l[2], `"kind":`, `"kind" :`, 1); return l }, "canonical form", 3},
		{"keys reordered", func(l []string) []string {
			l[0] = strings.Replace(l[0], `"v":1,"seq":1,`, `"seq":1,"v":1,`, 1)
			return l
		}, "canonical form", 1},
		{"hash replaced", func(l []string) []string {
			var r Record
			_ = json.Unmarshal([]byte(l[3]), &r)
			l[3] = strings.Replace(l[3], r.Hash, strings.Repeat("0", 64), 1)
			return l
		}, "does not match its content", 4},
		{"record from another session", func(l []string) []string {
			l[3] = strings.Replace(l[3], "s_7f3a9c21", "s_deadbeef", 1)
			return l
		}, "", 4},
		{"last line cut in the middle", func(l []string) []string { l[6] = l[6][:len(l[6])/2]; return l }, "", 7},
		{"garbage line", func(l []string) []string { l[2] = "not json at all"; return l }, "not a valid audit record", 3},
		{"empty line in the middle", func(l []string) []string { l[2] = ""; return l }, "not a valid audit record", 3},
		{"version changed", func(l []string) []string { l[1] = strings.Replace(l[1], `"v":1`, `"v":2`, 1); return l }, "unsupported record version", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := tamperedReport(t, tt.mutate)
			if rep.OK() {
				t.Fatalf("tampering not detected: %+v", rep)
			}
			p := rep.Problems[0]
			if tt.want != "" && !strings.Contains(p.Msg, tt.want) {
				t.Errorf("problem %q should mention %q", p.Msg, tt.want)
			}
			if p.Line != tt.atLine {
				t.Errorf("problem reported at line %d, want %d (%s)", p.Line, tt.atLine, p.Msg)
			}
			if rep.Complete {
				t.Error("a damaged log is never complete")
			}
		})
	}
}

func TestTruncationAtARecordBoundaryReadsAsIncomplete(t *testing.T) {
	// The chain cannot tell a clean cut from a session that is still running; Complete is what exposes it.
	rep := tamperedReport(t, func(l []string) []string { return l[:5] })
	if !rep.OK() {
		t.Fatalf("a clean cut leaves a valid chain: %+v", rep.Problems)
	}
	if rep.Complete {
		t.Fatal("but it must not be reported as complete")
	}
	if rep.Records != 5 {
		t.Errorf("records = %d", rep.Records)
	}
}

func TestMissingFinalNewlineIsATruncation(t *testing.T) {
	l := openLog(t, Options{})
	path := sample(t, l)
	_ = l.Close()
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, bytes.TrimSuffix(data, []byte("\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := mustVerify(t, path)
	if rep.OK() || !strings.Contains(rep.Problems[0].Msg, "cut off") {
		t.Fatalf("%+v", rep)
	}
}

func TestEmptyAndUnreadableLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "20261012-s_empty.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if rep := mustVerify(t, path); rep.OK() {
		t.Error("an empty log is not a valid log")
	}
	if _, err := Verify(filepath.Join(t.TempDir(), "missing.jsonl")); err == nil {
		t.Error("a missing file is an I/O error")
	}
	huge := filepath.Join(t.TempDir(), "20261012-s_huge.jsonl")
	if err := os.WriteFile(huge, append(bytes.Repeat([]byte("x"), maxLineBytes+10), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if rep := mustVerify(t, huge); rep.OK() || !strings.Contains(rep.Problems[0].Msg, "longer than") {
		t.Errorf("an absurdly long line is damage: %+v", rep)
	}
}

func TestBackwardsTimestampsAreCaughtEvenWithAValidChain(t *testing.T) {
	// Someone who rebuilds the whole chain still cannot make time run backwards without it showing.
	session := "s_timewarp"
	prev := firstPrev(session)
	var lines []string
	stamps := []string{"2026-10-12T05:00:05.000000Z", "2026-10-12T05:00:09.000000Z", "2026-10-12T05:00:07.000000Z"}
	for i, ts := range stamps {
		r := Record{V: 1, Seq: uint64(i + 1), TS: ts, Session: session, Kind: KindUser, Event: "chat", Prev: prev}
		h, _ := computeHash(r)
		r.Hash = h
		data, _ := json.Marshal(r)
		lines = append(lines, string(data))
		prev = h
	}
	path := filepath.Join(t.TempDir(), "20261012-s_timewarp.jsonl")
	writeLines(t, path, lines)
	rep := mustVerify(t, path)
	if rep.OK() || !strings.Contains(rep.Problems[0].Msg, "earlier than the record before") || rep.Problems[0].Line != 3 {
		t.Fatalf("%+v", rep)
	}
}

// --- writing ------------------------------------------------------------------------------------------------

func TestRedactionAppliesToEveryStringField(t *testing.T) {
	redact := func(s string) string { return strings.ReplaceAll(s, "sk-SECRET", "***") }
	l := openLog(t, Options{Redact: redact})
	rec, err := l.Append(Record{
		Kind: KindAction, Tool: "exec", Verdict: "allowed",
		Reason: "using key sk-SECRET to log in",
		Args:   ToRaw(map[string]any{"command": "curl -H 'Authorization: sk-SECRET' x", "env": []string{"A=sk-SECRET", "B=1"}, "n": 5, "nested": map[string]any{"k": "sk-SECRET"}}),
		Result: ToRaw(map[string]any{"stdout": "token sk-SECRET"}),
		LLM:    ToRaw(map[string]any{"note": "sk-SECRET"}),
		Data:   ToRaw([]any{"sk-SECRET", 1, true, nil}),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(l.Path())
	if bytes.Contains(data, []byte("sk-SECRET")) {
		t.Fatalf("a secret reached the log file:\n%s", data)
	}
	if !strings.Contains(rec.Reason, "***") || !strings.Contains(string(rec.Args), `"n":5`) {
		t.Errorf("redaction must keep the structure: %s / %s", rec.Reason, rec.Args)
	}
	if rep := mustVerify(t, l.Path()); !rep.OK() {
		t.Errorf("a redacted record still verifies: %+v", rep.Problems)
	}
}

func TestCaps(t *testing.T) {
	l := openLog(t, Options{})
	huge := strings.Repeat("A", 20<<10)
	rec, err := l.Append(Record{
		Kind: KindAction, Tool: "exec", Verdict: "allowed", Reason: strings.Repeat("é", 2000),
		Args: ToRaw(map[string]any{"command": huge}), Result: ToRaw(map[string]any{"stdout": huge}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Reason) > MaxReasonBytes || !utf8.ValidString(rec.Reason) || !strings.HasSuffix(rec.Reason, "[truncated]") {
		t.Errorf("reason: %d bytes, valid=%v, %q", len(rec.Reason), utf8.ValidString(rec.Reason), rec.Reason[len(rec.Reason)-20:])
	}
	for name, raw := range map[string]json.RawMessage{"args": rec.Args, "result": rec.Result} {
		if len(raw) > 400 {
			t.Errorf("%s: %d bytes; the stand-in should be tiny", name, len(raw))
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil || m["_truncated"] != true || m["bytes"] == nil || len(m["sha256"].(string)) != 64 {
			t.Errorf("%s: %s", name, raw)
		}
	}
	// A value just under the cap is kept whole.
	ok := map[string]any{"command": strings.Repeat("B", 8000)}
	rec, _ = l.Append(Record{Kind: KindAction, Tool: "exec", Verdict: "allowed", Args: ToRaw(ok)})
	if !bytes.Contains(rec.Args, []byte(strings.Repeat("B", 8000))) {
		t.Error("an args value below 8 KiB must be stored in full")
	}
	// Broken JSON does not break the log.
	rec, err = l.Append(Record{Kind: KindError, Event: "x", Data: json.RawMessage(`{"unterminated`)})
	if err != nil || !bytes.Contains(rec.Data, []byte("_invalid_json")) {
		t.Errorf("invalid raw JSON: %v %s", err, rec.Data)
	}
	if rep := mustVerify(t, l.Path()); !rep.OK() {
		t.Errorf("%+v", rep.Problems)
	}
}

func TestWriteFailureIsStickyAndFatal(t *testing.T) {
	l := openLog(t, Options{})
	if _, err := l.Append(Record{Kind: KindLifecycle, Event: EventStart}); err != nil {
		t.Fatal(err)
	}
	_ = l.f.Close() // simulate a failing disk
	_, err := l.Append(Record{Kind: KindUser, Event: "chat"})
	if err == nil {
		t.Fatal("a failed write must surface")
	}
	if l.Err() == nil {
		t.Error("the error must be remembered")
	}
	_, err2 := l.Append(Record{Kind: KindUser, Event: "chat"})
	if err2 == nil || err2.Error() != err.Error() {
		t.Errorf("later appends must fail with the same error: %v vs %v", err2, err)
	}
	// What was written before the failure is still a valid chain.
	if rep := mustVerify(t, l.Path()); !rep.OK() || rep.Records != 1 {
		t.Errorf("%+v", rep)
	}
}

func TestAppendAfterClose(t *testing.T) {
	l := openLog(t, Options{})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("Close twice: %v", err)
	}
	if _, err := l.Append(Record{Kind: KindUser}); !errors.Is(err, ErrClosed) {
		t.Errorf("got %v", err)
	}
}

func TestConcurrentAppendsStayOrdered(t *testing.T) {
	l := openLog(t, Options{})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := l.Append(Record{Kind: KindUser, Event: "chat", Actor: "user"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	rep := mustVerify(t, l.Path())
	if !rep.OK() || rep.Records != 50 {
		t.Fatalf("%+v", rep)
	}
}

func TestAppendedRecordsFollowTheSchema(t *testing.T) {
	c := jsonschema.NewCompiler()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema.AuditRecord))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddResource("audit-record.json", doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("audit-record.json")
	if err != nil {
		t.Fatal(err)
	}
	l := openLog(t, Options{})
	path := sample(t, l)
	for i, line := range readLines(t, path) {
		inst, err := jsonschema.UnmarshalJSON(strings.NewReader(line))
		if err != nil {
			t.Fatal(err)
		}
		if err := sch.Validate(inst); err != nil {
			t.Errorf("record %d does not match schema/audit-record.schema.json: %v\n%s", i+1, err, line)
		}
	}
	// The schema also rejects what it should.
	for name, line := range map[string]string{
		"unknown kind":      `{"v":1,"seq":1,"ts":"2026-10-12T05:30:01.123456Z","session":"s_abc","kind":"bogus","prev":"` + strings.Repeat("0", 64) + `","hash":"` + strings.Repeat("0", 64) + `"}`,
		"action w/o tool":   `{"v":1,"seq":1,"ts":"2026-10-12T05:30:01.123456Z","session":"s_abc","kind":"action","prev":"` + strings.Repeat("0", 64) + `","hash":"` + strings.Repeat("0", 64) + `"}`,
		"lifecycle w/o evt": `{"v":1,"seq":1,"ts":"2026-10-12T05:30:01.123456Z","session":"s_abc","kind":"lifecycle","prev":"` + strings.Repeat("0", 64) + `","hash":"` + strings.Repeat("0", 64) + `"}`,
		"extra member":      `{"v":1,"seq":1,"ts":"2026-10-12T05:30:01.123456Z","session":"s_abc","kind":"user","x":1,"prev":"` + strings.Repeat("0", 64) + `","hash":"` + strings.Repeat("0", 64) + `"}`,
		"bad timestamp":     `{"v":1,"seq":1,"ts":"yesterday","session":"s_abc","kind":"user","prev":"` + strings.Repeat("0", 64) + `","hash":"` + strings.Repeat("0", 64) + `"}`,
	} {
		inst, _ := jsonschema.UnmarshalJSON(strings.NewReader(line))
		if err := sch.Validate(inst); err == nil {
			t.Errorf("%s: the schema should reject it", name)
		}
	}
}

// --- admin ---------------------------------------------------------------------------------------------------

func newAdminWithLogs(t *testing.T) (*FileAdmin, string, []string) {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for _, tc := range []struct {
		session string
		day     time.Time
		finish  bool
	}{
		{"s_old00001", time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC), true},
		{"s_mid00002", time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC), false},
		{"s_new00003", time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC), true},
	} {
		clk := &stepClock{t: tc.day}
		l, err := Open(dir, tc.session, Options{Now: clk.Now})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = l.Append(Record{Kind: KindLifecycle, Event: EventStart})
		_, _ = l.Append(Record{Kind: KindAction, Tool: "exec", Verdict: "denied", Rules: []string{"DENY-POWER"}, Reason: "shutdown"})
		if tc.finish {
			_, _ = l.Append(Record{Kind: KindLifecycle, Event: EventEnd})
		}
		_ = l.Close()
		paths = append(paths, l.Path())
	}
	a := NewAdmin(func() (string, error) { return dir, nil })
	a.now = func() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC) }
	return a, dir, paths
}

func TestAdminVerifyListShow(t *testing.T) {
	a, _, paths := newAdminWithLogs(t)
	ctx := context.Background()

	var out bytes.Buffer
	if err := a.Verify(ctx, &out, paths[0]); err != nil || !strings.Contains(out.String(), "ok, 3 record(s)") || !strings.Contains(out.String(), "complete") {
		t.Fatalf("verify by path: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := a.Verify(ctx, &out, "s_mid00002"); err != nil || !strings.Contains(out.String(), "no end record") {
		t.Fatalf("verify by session id: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := a.Verify(ctx, &out, "s_new"); err != nil {
		t.Fatalf("a unique prefix of the session id is enough: %v", err)
	}
	if err := a.Verify(ctx, &out, "nonexistent"); err == nil {
		t.Error("an unknown log is an error")
	}

	// List
	out.Reset()
	if err := a.List(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"s_old00001", "s_mid00002", "s_new00003", "complete", "open or cut", "2026-08-01"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list output misses %q:\n%s", want, out.String())
		}
	}

	// Show: readable and raw
	out.Reset()
	if err := a.Show(ctx, &out, "s_old00001", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "action") || !strings.Contains(out.String(), "DENY-POWER") || !strings.Contains(out.String(), "-> denied") {
		t.Errorf("show:\n%s", out.String())
	}
	out.Reset()
	_ = a.Show(ctx, &out, "s_old00001", true)
	raw, _ := os.ReadFile(paths[0])
	if out.String() != string(raw) {
		t.Error("--raw must print the stored lines exactly")
	}

	// Tamper with one and watch verify and list say so.
	lines := readLines(t, paths[2])
	lines[1] = strings.Replace(lines[1], `"denied"`, `"allowed"`, 1)
	writeLines(t, paths[2], lines)
	out.Reset()
	err := a.Verify(ctx, &out, "s_new00003")
	if err == nil || !strings.Contains(out.String(), "FAILED") || !strings.Contains(out.String(), "edited") {
		t.Fatalf("verify of a tampered log: %v\n%s", err, out.String())
	}
	out.Reset()
	_ = a.List(ctx, &out)
	if !strings.Contains(out.String(), "DAMAGED") {
		t.Errorf("list should flag the damaged log:\n%s", out.String())
	}
}

func TestAdminPrune(t *testing.T) {
	a, dir, paths := newAdminWithLogs(t)
	var out bytes.Buffer
	if err := a.Prune(context.Background(), &out, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "removed 1 audit log(s)") || !strings.Contains(out.String(), "kept 2") {
		t.Errorf("%s", out.String())
	}
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Error("the August log is older than 30 days and must be gone")
	}
	for _, p := range paths[1:] {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s must be kept", p)
		}
	}
	// Foreign files in the directory are never touched.
	other := filepath.Join(dir, "notes.txt")
	_ = os.WriteFile(other, []byte("x"), 0o600)
	_ = a.Prune(context.Background(), &out, time.Hour)
	if _, err := os.Stat(other); err != nil {
		t.Error("prune must only remove audit logs")
	}
	if err := a.Prune(context.Background(), &out, 0); err == nil {
		t.Error("a non-positive age is a usage error")
	}
}

func TestAdminWithNoLogsOrNoDirectory(t *testing.T) {
	a := NewAdmin(func() (string, error) { return filepath.Join(t.TempDir(), "never-created"), nil })
	var out bytes.Buffer
	if err := a.List(context.Background(), &out); err != nil || !strings.Contains(out.String(), "no audit logs") {
		t.Errorf("%v %q", err, out.String())
	}
	if err := a.Prune(context.Background(), &out, time.Hour); err != nil {
		t.Errorf("pruning nothing is fine: %v", err)
	}
	if err := a.Show(context.Background(), &out, "s_none", false); err == nil {
		t.Error("showing a log that does not exist is an error")
	}
	bad := NewAdmin(func() (string, error) { return "", os.ErrPermission })
	if err := bad.List(context.Background(), &out); err == nil {
		t.Error("a directory lookup failure must surface")
	}
}

func TestAdminAmbiguousReference(t *testing.T) {
	dir := t.TempDir()
	for _, s := range []string{"s_abcd0001", "s_abcd0002"} {
		l, err := Open(dir, s, Options{})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = l.Append(Record{Kind: KindLifecycle, Event: EventStart})
		_ = l.Close()
	}
	a := NewAdmin(func() (string, error) { return dir, nil })
	var out bytes.Buffer
	if err := a.Show(context.Background(), &out, "s_abcd", false); err == nil || !strings.Contains(err.Error(), "matches 2") {
		t.Errorf("an ambiguous prefix must be refused, got %v", err)
	}
	if err := a.Show(context.Background(), &out, "s_abcd0002", false); err != nil {
		t.Errorf("the full id works: %v", err)
	}
}

func TestSummaryAndSizes(t *testing.T) {
	if got := humanSize(512); got != "512 B" {
		t.Error(got)
	}
	if got := humanSize(2048); got != "2.0 KiB" {
		t.Error(got)
	}
	if got := humanSize(3 << 20); got != "3.0 MiB" {
		t.Error(got)
	}
	s := summarize(Record{Seq: 7, TS: "2026-10-12T05:30:01.123456Z", Kind: KindAction, Actor: "llm", Tool: "exec", Verdict: "denied", Rules: []string{"A", "B"}, Reason: "x"})
	for _, want := range []string{"7", "action", "[llm]", "exec", "-> denied", "(A,B)", `"x"`} {
		if !strings.Contains(s, want) {
			t.Errorf("%q misses %q", s, want)
		}
	}
	if capText("short", 100) != "short" {
		t.Error("capText must leave short text alone")
	}
}

func TestToRawAndProblemString(t *testing.T) {
	if ToRaw(nil) != nil {
		t.Error("nil stays nil")
	}
	if got := string(ToRaw(make(chan int))); !strings.Contains(got, "_unmarshalable") {
		t.Errorf("an unmarshalable value gets a marker: %s", got)
	}
	if got := (Problem{Line: 3, Seq: 2, Msg: "m"}).String(); got != "line 3 (record 2): m" {
		t.Error(got)
	}
	if got := (Problem{Line: 3, Msg: "m"}).String(); got != "line 3: m" {
		t.Error(got)
	}
}

func TestShowNeverPutsAControlSequenceOfARecordOnTheTerminal(t *testing.T) {
	const esc = "\x1b"
	dir := t.TempDir()
	l, err := Open(dir, "s_hostile1", Options{})
	if err != nil {
		t.Fatal(err)
	}
	// A model can name a tool that does not exist, and a rule's reason can hold words of the guest's.
	if _, err := l.Append(Record{Kind: KindAction, Event: "decision", Actor: "model" + esc + "[31m", Tool: esc + "]52;c;ZXZpbA==\x07rm", Verdict: "denied" + esc + "[2J",
		Rules: []string{"DENY-X" + esc + "[H"}, Reason: "the command " + esc + "]0;owned\x07 was refused"}); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	a := NewAdmin(func() (string, error) { return dir, nil })
	var out bytes.Buffer
	if err := a.Show(context.Background(), &out, "s_hostile1", false); err != nil {
		t.Fatal(err)
	}
	for _, r := range out.String() {
		if r < ' ' && r != '\n' {
			t.Fatalf("a control character %q reaches the terminal:\n%q", r, out.String())
		}
	}
	for _, want := range []string{"model[31m", "52;c;ZXZpbA==rm", "denied[2J", "DENY-X[H", "0;owned was refused"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the readable text is gone: %q is not in\n%s", want, out.String())
		}
	}
	// What is stored is unchanged, and it is escaped as JSON.
	out.Reset()
	if err := a.Show(context.Background(), &out, "s_hostile1", true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), esc) || !strings.Contains(out.String(), `\u001b`) {
		t.Errorf("the raw log: %q", out.String())
	}
	// A file that is not a log of ours can say what it likes in its mistakes.
	forged := filepath.Join(dir, "forged.jsonl")
	if err := os.WriteFile(forged, []byte("{\"\u001b[2Jfield\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Show(context.Background(), &out, forged, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), esc) || !strings.Contains(out.String(), "not a valid record") {
		t.Errorf("a forged line: %q", out.String())
	}
	// The time, the kind and the event are as much a file's to say as the rest.
	if err := os.WriteFile(forged, []byte(`{"seq":1,"ts":"\u001b[2Jtime","kind":"\u001b[Hkind","event":"\u001b[3Jevent"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Show(context.Background(), &out, forged, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), esc) || !strings.Contains(out.String(), "[2Jtime") || !strings.Contains(out.String(), "[Hkind") || !strings.Contains(out.String(), "[3Jevent") {
		t.Errorf("a forged record: %q", out.String())
	}
}
