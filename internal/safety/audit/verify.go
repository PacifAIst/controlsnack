// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// maxLineBytes bounds one log line; records are capped far below this, so a longer line is damage.
const maxLineBytes = 2 << 20

// Problem is one thing wrong with a log.
type Problem struct {
	Line int    `json:"line"`
	Seq  uint64 `json:"seq,omitempty"`
	Msg  string `json:"msg"`
}

func (p Problem) String() string {
	if p.Seq != 0 {
		return fmt.Sprintf("line %d (record %d): %s", p.Line, p.Seq, p.Msg)
	}
	return fmt.Sprintf("line %d: %s", p.Line, p.Msg)
}

// Report is the result of verifying one log file.
type Report struct {
	Path    string `json:"path"`
	Session string `json:"session,omitempty"`
	Records int    `json:"records"`
	// First and Last are the timestamps of the first and last good record.
	First time.Time `json:"first,omitempty"`
	Last  time.Time `json:"last,omitempty"`
	// Complete is true when the last record is the session's end or kill record. A log that is intact but not
	// complete belongs to a session that is still running, crashed, or had its tail cut off; the hash chain alone
	// cannot tell those apart.
	Complete bool `json:"complete"`
	// LastHash is the hash of the last good record. Keep it elsewhere to anchor the log: nobody can shorten or
	// rewrite the log without changing it.
	LastHash string    `json:"last_hash,omitempty"`
	Problems []Problem `json:"problems,omitempty"`
}

// OK reports whether the chain is intact: no edits, deletions, reordering or damaged lines.
func (r *Report) OK() bool { return len(r.Problems) == 0 }

// Verify checks a log file's hash chain. It returns an error only when the file cannot be read at all; damage in
// the log is reported in Report.Problems. Checking stops at the first problem, because everything after a broken
// link fails the chain too and would only add noise.
//
// What it detects: an edited field (hash mismatch), a deleted or inserted record (sequence gap, broken prev link),
// reordered records (sequence and prev), a record rewritten with extra or reordered fields (not canonical form), a
// partially written or cut-off last line (invalid JSON), and timestamps going backwards. What it cannot detect on
// its own: someone with write access who recomputes the whole chain, or a clean cut at a record boundary (the log
// then merely reads as incomplete). Anchoring LastHash somewhere else closes both.
func Verify(path string) (*Report, error) {
	f, err := os.Open(path) //nolint:gosec // the caller chooses which log to verify
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	rep := &Report{Path: path}
	br := bufio.NewReaderSize(f, 64<<10)
	var (
		prevHash string
		prevSeq  uint64
		prevTime time.Time
		lineNo   int
		last     Record
	)
	problem := func(seq uint64, format string, args ...any) {
		rep.Problems = append(rep.Problems, Problem{Line: lineNo, Seq: seq, Msg: fmt.Sprintf(format, args...)})
	}

	for {
		line, readErr := readLine(br)
		if len(line) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		lineNo++
		if errors.Is(readErr, errLineTooLong) {
			problem(0, "line is longer than %d bytes", maxLineBytes)
			return rep, nil
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		if readErr != nil || !bytes.HasSuffix(line, []byte("\n")) {
			problem(0, "the last line is not terminated: the log was cut off while writing")
			return rep, nil
		}
		line = bytes.TrimSuffix(line, []byte("\n"))

		rec, err := decodeStrict(line)
		if err != nil {
			problem(0, "not a valid audit record: %v", err)
			return rep, nil
		}
		if rec.V != Version {
			problem(rec.Seq, "unsupported record version %d", rec.V)
			return rep, nil
		}
		canonical, err := json.Marshal(rec)
		if err != nil || !bytes.Equal(canonical, line) {
			problem(rec.Seq, "the line is not in canonical form: it was edited or rewritten")
			return rep, nil
		}
		if rec.Seq != prevSeq+1 {
			problem(rec.Seq, "sequence jumps from %d to %d: records were deleted, inserted or reordered", prevSeq, rec.Seq)
			return rep, nil
		}
		if prevSeq == 0 {
			rep.Session = rec.Session
			prevHash = firstPrev(rec.Session)
		} else if rec.Session != rep.Session {
			problem(rec.Seq, "session changes from %q to %q", rep.Session, rec.Session)
			return rep, nil
		}
		if rec.Prev != prevHash {
			problem(rec.Seq, "the link to the previous record is broken: records were deleted, inserted or reordered")
			return rep, nil
		}
		want, err := computeHash(rec)
		if err != nil || rec.Hash != want {
			problem(rec.Seq, "the record's hash does not match its content: it was edited")
			return rep, nil
		}
		ts, err := time.Parse(timestampLayout, rec.TS)
		if err != nil {
			problem(rec.Seq, "invalid timestamp %q", rec.TS)
			return rep, nil
		}
		if ts.Before(prevTime) {
			problem(rec.Seq, "the timestamp %s is earlier than the record before it", rec.TS)
			return rep, nil
		}

		if rep.Records == 0 {
			rep.First = ts
		}
		rep.Last, prevTime = ts, ts
		prevHash, prevSeq, last = rec.Hash, rec.Seq, rec
		rep.Records++
		if errors.Is(readErr, io.EOF) {
			break
		}
	}

	rep.LastHash = prevHash
	if prevSeq == 0 {
		rep.LastHash = ""
		rep.Problems = append(rep.Problems, Problem{Line: 0, Msg: "the log is empty"})
		return rep, nil
	}
	rep.Complete = last.Kind == KindLifecycle && (last.Event == EventEnd || last.Event == EventKill)
	return rep, nil
}

var errLineTooLong = errors.New("line too long")

// readLine reads up to and including the next newline, refusing absurdly long lines.
func readLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxLineBytes {
			return nil, errLineTooLong
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}
