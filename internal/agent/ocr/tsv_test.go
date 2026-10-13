// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package ocr

import (
	"slices"
	"strings"
	"testing"
)

const header = "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n"

func texts(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Text
	}
	return out
}

func TestParseTSV(t *testing.T) {
	in := header +
		"1\t1\t0\t0\t0\t0\t0\t0\t640\t400\t-1\t\n" +
		"2\t1\t1\t0\t0\t0\t10\t10\t200\t30\t-1\t\n" +
		"3\t1\t1\t1\t0\t0\t10\t10\t200\t30\t-1\t\n" +
		"4\t1\t1\t1\t1\t0\t10\t10\t200\t30\t-1\t\n" +
		"5\t1\t1\t1\t1\t1\t10\t10\t60\t30\t96.512\tHello\n" +
		"5\t1\t1\t1\t1\t2\t80\t10\t130\t30\t91.2\tworld\r\n" +
		"5\t1\t1\t1\t1\t3\t220\t10\t20\t30\t-1\tnoise\n" + // no confidence
		"5\t1\t1\t1\t1\t4\t250\t10\t20\t30\t80\t \n" + // blank
		"5\t1\t1\t1\t1\t5\t280\t10\t20\t30\t80\n" + // no text column at all
		"5\t1\t1\t1\t1\t6\t300\t10\t0\t30\t80\tflat\n" + // no width
		"5\t1\t1\t1\t1\t7\t320\t10\t40\t30\t70\tbell\x07ring\n"
	words, err := ParseTSV(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []Word{
		{Block: 1, Par: 1, Line: 1, Num: 1, X: 10, Y: 10, W: 60, H: 30, Conf: 96, Text: "Hello"},
		{Block: 1, Par: 1, Line: 1, Num: 2, X: 80, Y: 10, W: 130, H: 30, Conf: 91, Text: "world"},
		{Block: 1, Par: 1, Line: 1, Num: 7, X: 320, Y: 10, W: 40, H: 30, Conf: 70, Text: "bellring"},
	}
	if !slices.Equal(words, want) {
		t.Errorf("got %+v\nwant %+v", words, want)
	}
}

func TestParseTSVRefusesWhatIsNotTesseractsOutput(t *testing.T) {
	for name, in := range map[string]string{
		"empty":            "",
		"not the header":   "hello world\n",
		"too few columns":  header + "5\t1\t1\n",
		"level not number": header + "x\t1\t1\t1\t1\t1\t0\t0\t1\t1\t90\tw\n",
		"left not number":  header + "5\t1\t1\t1\t1\t1\tleft\t0\t1\t1\t90\tw\n",
		"too many rows":    header + strings.Repeat("1\t1\t0\t0\t0\t0\t0\t0\t1\t1\t-1\t\n", maxTSVRows+1),
	} {
		if _, err := ParseTSV(strings.NewReader(in)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if words, err := ParseTSV(strings.NewReader(header)); err != nil || len(words) != 0 {
		t.Errorf("only the header, nothing on the screen: %v %v", words, err)
	}
}

func word(block, line, num, x, y, w, h, conf int, text string) Word {
	return Word{Block: block, Par: 1, Line: line, Num: num, X: x, Y: y, W: w, H: h, Conf: conf, Text: text}
}

func TestGroupJoinsWordsOfALine(t *testing.T) {
	lines := Group([]Word{word(1, 1, 1, 10, 10, 50, 12, 90, "Save"), word(1, 1, 2, 66, 10, 40, 12, 80, "your"), word(1, 1, 3, 112, 10, 60, 12, 70, "changes?")}, MinConf)
	if len(lines) != 1 || lines[0].Text != "Save your changes?" || lines[0].X != 10 || lines[0].Y != 10 || lines[0].W != 162 || lines[0].H != 12 || lines[0].Conf != 80 {
		t.Errorf("%+v", lines)
	}
}

func TestGroupDropsDoubtfulWords(t *testing.T) {
	lines := Group([]Word{word(1, 1, 1, 10, 10, 50, 12, 95, "Open"), word(1, 1, 2, 66, 10, 20, 12, 39, "|||"), word(1, 1, 3, 70, 10, 40, 12, 40, "File")}, MinConf)
	if got := texts(lines); !slices.Equal(got, []string{"Open File"}) {
		t.Errorf("%v", got)
	}
	if lines := Group([]Word{word(1, 1, 1, 0, 0, 10, 10, 10, "x")}, MinConf); len(lines) != 0 {
		t.Errorf("%+v", lines)
	}
	if lines := Group(nil, MinConf); len(lines) != 0 {
		t.Errorf("%+v", lines)
	}
}

func TestGroupSplitsAWideGap(t *testing.T) {
	// A menu bar: tesseract calls it one line, a person points at one entry.
	menu := []Word{
		word(1, 1, 1, 10, 60, 28, 12, 95, "File"), word(1, 1, 2, 58, 60, 28, 12, 95, "Edit"), word(1, 1, 3, 106, 60, 30, 12, 95, "View"),
		word(1, 1, 4, 160, 60, 20, 12, 95, "Go"), word(1, 1, 5, 184, 60, 12, 12, 95, "to"),
	}
	got := texts(Group(menu, MinConf))
	if want := []string{"File", "Edit", "View", "Go to"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestGroupReadsTopToBottomThenLeftToRight(t *testing.T) {
	words := []Word{
		word(2, 1, 1, 300, 52, 40, 12, 90, "right"), // a pixel or two off the row of "left"
		word(1, 1, 1, 10, 50, 40, 12, 90, "left"),
		word(3, 1, 1, 10, 200, 60, 12, 90, "bottom"),
		word(4, 1, 1, 10, 5, 60, 12, 90, "top"),
	}
	if got := texts(Group(words, MinConf)); !slices.Equal(got, []string{"top", "left", "right", "bottom"}) {
		t.Errorf("%v", got)
	}
}

func TestGroupSortsWordsOfALineByPosition(t *testing.T) {
	words := []Word{word(1, 1, 2, 60, 10, 40, 12, 90, "world"), word(1, 1, 1, 10, 10, 40, 12, 90, "hello")}
	if got := texts(Group(words, MinConf)); !slices.Equal(got, []string{"hello world"}) {
		t.Errorf("%v", got)
	}
}

func TestGroupCapsALine(t *testing.T) {
	var words []Word
	for i := range 200 {
		words = append(words, word(1, 1, i, 10+i*60, 10, 55, 12, 90, "abcdefgh"))
	}
	for _, l := range Group(words, MinConf) {
		if len([]rune(l.Text)) > maxLine {
			t.Errorf("a line of %d characters", len([]rune(l.Text)))
		}
	}
}

func TestGroupKeepsTesseractsSeparateFragmentsApart(t *testing.T) {
	// In sparse mode "Second line:" and "Hello World" on one row of a terminal come as two lines of two blocks. They stay
	// two boxes: merging by row also merged "Tabs" and "Help" of a menu bar.
	words := []Word{
		word(1, 1, 1, 7, 105, 70, 13, 92, "Second"), word(1, 1, 2, 80, 105, 42, 13, 92, "line:"),
		word(2, 1, 1, 137, 107, 50, 11, 96, "Hello"), word(2, 1, 2, 191, 107, 53, 11, 96, "World"),
		word(3, 1, 1, 205, 65, 24, 10, 96, "Tabs"), word(4, 1, 1, 246, 65, 27, 13, 96, "Help"),
	}
	if got := texts(Group(words, MinConf)); !slices.Equal(got, []string{"Tabs", "Help", "Second line:", "Hello World"}) {
		t.Errorf("%v", got)
	}
}

func TestGroupKeepsTheLinesOfAParagraphApart(t *testing.T) {
	words := []Word{
		word(1, 1, 1, 10, 10, 60, 12, 90, "first"), word(1, 1, 2, 74, 10, 60, 12, 90, "line"),
		word(1, 2, 1, 10, 26, 60, 12, 90, "second"), word(1, 2, 2, 74, 26, 60, 12, 90, "line"),
		word(1, 3, 1, 10, 42, 60, 12, 90, "third"),
	}
	if got := texts(Group(words, MinConf)); !slices.Equal(got, []string{"first line", "second line", "third"}) {
		t.Errorf("%v", got)
	}
}

func TestGroupDoesNotJoinWhatOnlyTouchesARow(t *testing.T) {
	// A word that overlaps a row by a pixel or two belongs to the next row.
	words := []Word{word(1, 1, 1, 10, 10, 60, 12, 90, "above"), word(2, 1, 1, 80, 21, 60, 12, 90, "below")}
	if got := texts(Group(words, MinConf)); !slices.Equal(got, []string{"above", "below"}) {
		t.Errorf("%v", got)
	}
}

func TestGroupColumnsAreReadRowByRow(t *testing.T) {
	// Two columns far apart: each row reads left column, then right column.
	words := []Word{
		word(1, 1, 1, 10, 10, 60, 12, 90, "L1"), word(2, 1, 1, 400, 10, 60, 12, 90, "R1"),
		word(1, 2, 1, 10, 30, 60, 12, 90, "L2"), word(2, 2, 1, 400, 30, 60, 12, 90, "R2"),
	}
	if got := texts(Group(words, MinConf)); !slices.Equal(got, []string{"L1", "R1", "L2", "R2"}) {
		t.Errorf("%v", got)
	}
}
