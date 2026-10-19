// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/PacifAIst/controlsnack/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack/internal/exitcode"
)

// Admin is what the `controlsnack audit` commands use; all output goes to w.
type Admin interface {
	// Verify checks the hash chain of one log file.
	Verify(ctx context.Context, w io.Writer, file string) error
	// List prints the known audit logs.
	List(ctx context.Context, w io.Writer) error
	// Show prints one log; ref is a file path or a session id. raw prints the JSON lines as stored.
	Show(ctx context.Context, w io.Writer, ref string, raw bool) error
	// Prune deletes logs older than the given age.
	Prune(ctx context.Context, w io.Writer, olderThan time.Duration) error
}

// FileAdmin implements Admin over the audit directory.
type FileAdmin struct {
	dir func() (string, error)
	now func() time.Time
}

// NewAdmin returns an Admin for the directory dir() names. The directory is looked up when a command runs, so
// commands that do not touch it work even if it cannot be determined.
func NewAdmin(dir func() (string, error)) *FileAdmin {
	return &FileAdmin{dir: dir, now: time.Now}
}

// Verify checks one log and prints the result. A log that fails the check makes the command exit non-zero.
func (a *FileAdmin) Verify(_ context.Context, w io.Writer, file string) error {
	path, err := a.resolve(file)
	if err != nil {
		return err
	}
	rep, err := Verify(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if !rep.OK() {
		for _, p := range rep.Problems {
			fmt.Fprintf(w, "%s: %s\n", path, p)
		}
		fmt.Fprintf(w, "%s: FAILED after %d good record(s)\n", path, rep.Records)
		return fmt.Errorf("the audit log %s has been altered or damaged", filepath.Base(path))
	}
	state := "complete"
	if !rep.Complete {
		state = "no end record (the session is still running, crashed, or the tail was removed)"
	}
	fmt.Fprintf(w, "%s: ok, %d record(s), session %s, %s\n  last hash %s\n", path, rep.Records, rep.Session, state, rep.LastHash)
	return nil
}

// List prints one line per log.
func (a *FileAdmin) List(_ context.Context, w io.Writer) error {
	dir, err := a.dir()
	if err != nil {
		return err
	}
	files, err := logFiles(dir)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Fprintf(w, "no audit logs in %s\n", dir)
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "DATE\tSESSION\tRECORDS\tSIZE\tSTATE\tFILE")
	for _, f := range files {
		state, records := "unreadable", 0
		if rep, err := Verify(f.path); err == nil {
			records = rep.Records
			switch {
			case !rep.OK():
				state = "DAMAGED"
			case rep.Complete:
				state = "complete"
			default:
				state = "open or cut"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n", f.date.Format("2006-01-02"), f.session, records, humanSize(f.size), state, filepath.Base(f.path))
	}
	return tw.Flush()
}

// Show prints a log. By default each record is one readable line; raw prints the stored JSON.
func (a *FileAdmin) Show(_ context.Context, w io.Writer, ref string, raw bool) error {
	path, err := a.resolve(ref)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path) //nolint:gosec // the user asked to show this log
	if err != nil {
		return err
	}
	if raw {
		_, err = w.Write(data)
		return err
	}
	for i, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		rec, err := decodeStrict([]byte(line))
		if err != nil {
			fmt.Fprintf(w, "line %d: not a valid record: %s\n", i+1, plain(err.Error()))
			continue
		}
		fmt.Fprintln(w, summarize(rec))
	}
	return nil
}

// plain makes text from a log fit for a terminal: what a record holds can come from the model or the guest (or from a file that is
// not a log of ours at all), and a control sequence in it would act on the terminal of the person who reads it.
func plain(s string) string { return textclean.Line(s, 400) }

func summarize(r Record) string {
	parts := []string{fmt.Sprintf("%4d", r.Seq), plain(r.TS), fmt.Sprintf("%-9s", plain(r.Kind))}
	if r.Event != "" {
		parts = append(parts, plain(r.Event))
	}
	if r.Actor != "" {
		parts = append(parts, "["+plain(r.Actor)+"]")
	}
	if r.Tool != "" {
		parts = append(parts, plain(r.Tool))
	}
	if r.Verdict != "" {
		parts = append(parts, "-> "+plain(r.Verdict))
	}
	if len(r.Rules) > 0 {
		rules := make([]string, len(r.Rules))
		for i, id := range r.Rules {
			rules[i] = plain(id)
		}
		parts = append(parts, "("+strings.Join(rules, ",")+")")
	}
	if r.Reason != "" {
		parts = append(parts, `"`+capText(plain(r.Reason), 80)+`"`)
	}
	return strings.Join(parts, " ")
}

// Prune deletes logs whose date is older than the given age.
func (a *FileAdmin) Prune(_ context.Context, w io.Writer, olderThan time.Duration) error {
	if olderThan <= 0 {
		return exitcode.Usagef("the age to prune must be positive")
	}
	dir, err := a.dir()
	if err != nil {
		return err
	}
	files, err := logFiles(dir)
	if err != nil {
		return err
	}
	cutoff := a.now().Add(-olderThan)
	removed, freed := 0, int64(0)
	for _, f := range files {
		if !f.date.Before(cutoff) {
			continue
		}
		if err := os.Remove(f.path); err != nil {
			return fmt.Errorf("removing %s: %w", f.path, err)
		}
		removed++
		freed += f.size
	}
	fmt.Fprintf(w, "removed %d audit log(s) older than %s (%s freed), kept %d\n", removed, olderThan, humanSize(freed), len(files)-removed)
	return nil
}

// resolve turns a file path or a session id into a log file path.
func (a *FileAdmin) resolve(ref string) (string, error) {
	if info, err := os.Stat(ref); err == nil && !info.IsDir() {
		return ref, nil
	}
	dir, err := a.dir()
	if err != nil {
		return "", err
	}
	files, err := logFiles(dir)
	if err != nil {
		return "", err
	}
	var matches []string
	for _, f := range files {
		if f.session == ref || filepath.Base(f.path) == ref || (len(ref) >= 4 && strings.HasPrefix(f.session, ref)) {
			matches = append(matches, f.path)
		}
	}
	switch len(matches) {
	case 0:
		return "", exitcode.Usagef("no audit log %q: give a file path or a session id (see `controlsnack audit ls`)", ref)
	case 1:
		return matches[0], nil
	default:
		return "", exitcode.Usagef("%q matches %d audit logs; give more of the session id", ref, len(matches))
	}
}

type logFile struct {
	path    string
	session string
	date    time.Time
	size    int64
}

// logFiles lists the audit logs in dir, oldest first. A missing directory is an empty list.
func logFiles(dir string) ([]logFile, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []logFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") || len(name) < len("20060102-x.jsonl") || name[8] != '-' {
			continue
		}
		date, err := time.Parse("20060102", name[:8])
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, logFile{
			path: filepath.Join(dir, name), session: strings.TrimSuffix(name[9:], ".jsonl"), date: date, size: info.Size(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
