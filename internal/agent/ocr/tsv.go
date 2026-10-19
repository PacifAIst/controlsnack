// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package ocr

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/PacifAIst/controlsnack/internal/agent/textclean"
)

// Word is one recognized word with its box, in the pixels of the picture that was read.
type Word struct {
	Block, Par, Line int
	Num              int
	X, Y, W, H       int
	Conf             int
	Text             string
}

// tsvColumns is what tesseract's tsv output has: level page block par line word left top width height conf text.
const tsvColumns = 12

// levelWord is the level of a row that is a word (the others are pages, blocks, paragraphs and lines).
const levelWord = 5

// Limits of what is read from tesseract: a screen has a few thousand words at most.
const (
	maxTSVRows  = 20000
	maxWordRune = 200
)

// ParseTSV reads tesseract's tsv output and returns its words, in the order tesseract gave them. Rows that are not
// words, and words with no text or no confidence, are skipped. A malformed row is an error: the output of a program
// that is not tesseract, or that failed half way, must not be taken for text from the screen.
func ParseTSV(r io.Reader) ([]Word, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("no output")
	}
	if !strings.HasPrefix(sc.Text(), "level\t") {
		return nil, fmt.Errorf("the first line is not tesseract's tsv header: %.60q", sc.Text())
	}
	var words []Word
	rows := 0
	for sc.Scan() {
		if rows++; rows > maxTSVRows {
			return nil, fmt.Errorf("more than %d rows", maxTSVRows)
		}
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", tsvColumns)
		if len(f) < tsvColumns-1 {
			return nil, fmt.Errorf("row %d has %d columns, want %d", rows, len(f), tsvColumns)
		}
		level, err := strconv.Atoi(f[0])
		if err != nil {
			return nil, fmt.Errorf("row %d: level %q", rows, f[0])
		}
		if level != levelWord {
			continue
		}
		if len(f) < tsvColumns {
			continue // a word row without its text column is an empty word
		}
		var n [10]int
		for i := range n {
			v, err := strconv.ParseFloat(f[i+1], 64) // conf is written as 96.123 or -1
			if err != nil {
				return nil, fmt.Errorf("row %d: column %d is %q", rows, i+2, f[i+1])
			}
			n[i] = int(v)
		}
		text := textclean.Line(f[11], maxWordRune)
		// n is page, block, paragraph, line, word, left, top, width, height, confidence.
		if text == "" || n[5] < 0 || n[6] < 0 || n[7] <= 0 || n[8] <= 0 || n[9] < 0 {
			continue
		}
		words = append(words, Word{Block: n[1], Par: n[2], Line: n[3], Num: n[4], X: n[5], Y: n[6], W: n[7], H: n[8], Conf: n[9], Text: text})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return words, nil
}

// Line is a run of words on one line of the screen with no wide gap in it: something a person would point at.
type Line struct {
	Text       string
	X, Y, W, H int
	Conf       int
}

// Grouping parameters.
const (
	// MinConf drops words tesseract is not sure about: noise from icons, borders and anti-aliased edges.
	MinConf = 40
	// minGap and gapFactor decide when two words on a line are two things (menu entries, toolbar labels): a gap wider
	// than gapFactor times the taller word's height, and at least minGap pixels.
	minGap    = 12
	gapFactor = 1.5
	maxLine   = 500
)

// Group turns words into lines: words of one tesseract line are joined left to right, and a wide gap starts a new
// line (so "File   Edit   View" is three things to point at, not one). Words below minConf are dropped first. The lines
// come back top to bottom, then left to right, which is the order a person reads them in.
//
// Tesseract's own lines are respected, and words of different tesseract lines are never joined, even when they sit on
// one row of the screen. Row-based grouping was tried and merged neighboring interface items ("Tabs" and "Help" of a
// menu bar, a launcher and a task button of a panel) into one box whose middle is between the two things to point at.
// For pointing, two boxes for one phrase do less harm than one box for two things.
func Group(words []Word, minConf int) []Line {
	type key struct{ block, par, line int }
	groups := map[key][]Word{}
	var order []key
	for _, w := range words {
		if w.Conf < minConf {
			continue
		}
		k := key{w.Block, w.Par, w.Line}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], w)
	}
	var lines []Line
	for _, k := range order {
		ws := groups[k]
		sort.SliceStable(ws, func(i, j int) bool { return ws[i].X < ws[j].X })
		start := 0
		for i := 1; i <= len(ws); i++ {
			if i < len(ws) && !splits(ws[i-1], ws[i]) {
				continue
			}
			lines = append(lines, join(ws[start:i]))
			start = i
		}
	}
	sort.SliceStable(lines, func(i, j int) bool {
		// The same row of text is one row even if boxes differ by a pixel or two.
		if dy := lines[i].Y - lines[j].Y; dy > rowTolerance(lines[i], lines[j]) || dy < -rowTolerance(lines[i], lines[j]) {
			return lines[i].Y < lines[j].Y
		}
		return lines[i].X < lines[j].X
	})
	return lines
}

func rowTolerance(a, b Line) int { return min(a.H, b.H) / 2 }

// splits reports whether the gap between two neighboring words is wide enough to make two lines.
func splits(a, b Word) bool {
	gap := b.X - (a.X + a.W)
	limit := int(gapFactor * float64(max(a.H, b.H)))
	return gap > max(limit, minGap)
}

// join makes one Line of words that belong together.
func join(ws []Word) Line {
	x0, y0, x1, y1 := ws[0].X, ws[0].Y, ws[0].X+ws[0].W, ws[0].Y+ws[0].H
	conf := 0
	parts := make([]string, len(ws))
	for i, w := range ws {
		x0, y0 = min(x0, w.X), min(y0, w.Y)
		x1, y1 = max(x1, w.X+w.W), max(y1, w.Y+w.H)
		conf += w.Conf
		parts[i] = w.Text
	}
	return Line{Text: textclean.Line(strings.Join(parts, " "), maxLine), X: x0, Y: y0, W: x1 - x0, H: y1 - y0, Conf: conf / len(ws)}
}
