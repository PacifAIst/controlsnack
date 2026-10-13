// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package ocr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for tesseract: Tesseract{Path: os.Args[0], Env: CS_FAKE_TESSERACT=mode} runs
// this instead of the tests, so the process handling (standard input, timeout, output limits, errors) is the real one.
func TestMain(m *testing.M) {
	if mode := os.Getenv("CS_FAKE_TESSERACT"); mode != "" {
		fakeTesseract(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeTesseract(mode string) {
	in, _ := io.ReadAll(os.Stdin)
	row := func(num int, text string) string {
		return fmt.Sprintf("5\t1\t1\t1\t1\t%d\t%d\t10\t50\t12\t90.5\t%s\n", num, 10+60*num, text)
	}
	const head = "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n"
	switch mode {
	case "ok":
		if !bytes.HasPrefix(in, []byte("\x89PNG")) {
			fmt.Fprintln(os.Stderr, "not a png")
			os.Exit(3)
		}
		fmt.Fprint(os.Stderr, "Estimating resolution as 70\n")
		fmt.Print(head + row(0, "Hello") + row(1, "world"))
	case "args":
		fmt.Print(head + row(0, strings.Join(os.Args[1:], "_")) + row(1, "threads="+os.Getenv("OMP_THREAD_LIMIT")))
	case "slow":
		time.Sleep(30 * time.Second)
	case "fail":
		fmt.Fprintln(os.Stderr, "Error in pixReadStream: Unknown format\nsecond line")
		os.Exit(1)
	case "lang":
		fmt.Fprintln(os.Stderr, "Failed loading language 'xyz'\nTesseract couldn't load any languages!")
		os.Exit(1)
	case "garbage":
		fmt.Print("this is not tsv\n")
	case "flood":
		chunk := strings.Repeat("x", 1<<20)
		for range 10 {
			fmt.Print(chunk)
		}
	case "control":
		fmt.Fprintln(os.Stderr, "bad\x1b[31m\x07 thing")
		os.Exit(1)
	}
}

func fake(mode string) Tesseract {
	return Tesseract{Path: os.Args[0], Env: []string{"CS_FAKE_TESSERACT=" + mode}}
}

var pngBytes = []byte("\x89PNG\r\n\x1a\nrest")

func TestTesseractReadsWords(t *testing.T) {
	words, err := fake("ok").Recognize(context.Background(), pngBytes, "eng")
	if err != nil {
		t.Fatal(err)
	}
	if len(words) != 2 || words[0].Text != "Hello" || words[1].Text != "world" || words[0].Conf != 90 || words[1].X != 70 {
		t.Errorf("%+v", words)
	}
}

func TestTesseractIsRunWithTheRightArguments(t *testing.T) {
	words, err := fake("args").Recognize(context.Background(), pngBytes, "eng+deu")
	if err != nil || len(words) != 2 {
		t.Fatalf("%v %v", words, err)
	}
	if words[0].Text != "stdin_stdout_-l_eng+deu_--psm_11_tsv" {
		t.Errorf("arguments %q", words[0].Text)
	}
	if words[1].Text != "threads=1" {
		t.Errorf("tesseract runs on one thread: %q", words[1].Text)
	}
}

func TestTesseractRefusesALanguageThatIsNotAName(t *testing.T) {
	for _, lang := range []string{"", "en", "ENG", "eng;rm", "eng deu", "../eng", "eng+", strings.Repeat("abc+", 9)} {
		if _, err := fake("ok").Recognize(context.Background(), pngBytes, lang); !errors.Is(err, ErrLanguage) {
			t.Errorf("%q: %v", lang, err)
		}
	}
	for _, lang := range []string{"eng", "deu", "chi_sim", "eng+deu", "chi_sim+eng"} {
		if !ValidLanguage(lang) {
			t.Errorf("%q is a language", lang)
		}
	}
}

func TestTesseractTimesOut(t *testing.T) {
	tess := fake("slow")
	tess.Timeout = 300 * time.Millisecond
	begin := time.Now()
	_, err := tess.Recognize(context.Background(), pngBytes, "eng")
	if !errors.Is(err, ErrTimeout) || time.Since(begin) > 10*time.Second {
		t.Errorf("%v after %s", err, time.Since(begin))
	}
}

func TestTesseractStopsWhenTheRequestIsCanceled(t *testing.T) {
	tess := fake("slow")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	begin := time.Now()
	if _, err := tess.Recognize(ctx, pngBytes, "eng"); !errors.Is(err, context.Canceled) || time.Since(begin) > 10*time.Second {
		t.Errorf("%v after %s", err, time.Since(begin))
	}
}

func TestTesseractErrors(t *testing.T) {
	if _, err := (Tesseract{Path: "/nonexistent/tesseract"}).Recognize(context.Background(), pngBytes, "eng"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("not installed: %v", err)
	}
	if _, err := (Tesseract{Path: "no-such-program-cs"}).Recognize(context.Background(), pngBytes, "eng"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("not in PATH: %v", err)
	}
	if _, err := fake("lang").Recognize(context.Background(), pngBytes, "xyz"); !errors.Is(err, ErrLanguage) {
		t.Errorf("a language that is not installed: %v", err)
	}
	_, err := fake("fail").Recognize(context.Background(), pngBytes, "eng")
	if err == nil || !strings.Contains(err.Error(), "pixReadStream") || strings.Contains(err.Error(), "second line") {
		t.Errorf("a failure names the first line of what tesseract said: %v", err)
	}
	_, err = fake("control").Recognize(context.Background(), pngBytes, "eng")
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\x07") {
		t.Errorf("what tesseract says is cleaned: %q", err)
	}
	if _, err = fake("garbage").Recognize(context.Background(), pngBytes, "eng"); err == nil || !strings.Contains(err.Error(), "tsv header") {
		t.Errorf("output that is not tsv: %v", err)
	}
	if _, err = fake("flood").Recognize(context.Background(), pngBytes, "eng"); err == nil || !strings.Contains(err.Error(), "too much") {
		t.Errorf("output over the limit: %v", err)
	}
}

func TestLimitedWriter(t *testing.T) {
	l := limited{max: 10}
	if n, err := l.Write([]byte("12345")); n != 5 || err != nil || l.over {
		t.Errorf("%d %v %v", n, err, l.over)
	}
	if n, err := l.Write([]byte("6789012345")); n != 10 || err != nil || !l.over || l.String() != "1234567890" {
		t.Errorf("%d %v %v %q", n, err, l.over, l.String())
	}
	if n, err := l.Write([]byte("more")); n != 4 || err != nil || l.String() != "1234567890" {
		t.Errorf("%d %v %q", n, err, l.String())
	}
}
