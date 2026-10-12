// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qemu

import (
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestRingKeepsTheLastBytes(t *testing.T) {
	r := NewRing(10)
	if r.String() != "" {
		t.Error("empty ring")
	}
	_, _ = r.Write([]byte("abc"))
	_, _ = r.Write([]byte("def"))
	if r.String() != "abcdef" {
		t.Errorf("%q", r.String())
	}
	_, _ = r.Write([]byte("ghijkl")) // 12 bytes written: the oldest 2 are gone
	if r.String() != "cdefghijkl" {
		t.Errorf("%q", r.String())
	}
	_, _ = r.Write([]byte("0123456789ABCDEF")) // larger than the ring: only the last 10 survive
	if r.String() != "6789ABCDEF" {
		t.Errorf("%q", r.String())
	}
	n, err := r.Write(nil)
	if n != 0 || err != nil {
		t.Errorf("%d %v", n, err)
	}
	// Writes report the full length even when bytes are dropped, so io.Copy never sees a short write.
	if n, err := NewRing(4).Write([]byte("0123456789")); n != 10 || err != nil {
		t.Errorf("%d %v", n, err)
	}
}

func TestRingWrapsAtEveryOffset(t *testing.T) {
	for size := 1; size <= 9; size++ {
		for chunk := 1; chunk <= 12; chunk++ {
			r := NewRing(size)
			var all strings.Builder
			for i := 0; i < 40; i++ {
				b := strings.Repeat(string(rune('a'+i%26)), chunk)
				all.WriteString(b)
				_, _ = r.Write([]byte(b))
			}
			s := all.String()
			want := s[max(0, len(s)-size):]
			if r.String() != want {
				t.Fatalf("size %d chunk %d: got %q want %q", size, chunk, r.String(), want)
			}
		}
	}
}

func TestRingDefaultsAndTail(t *testing.T) {
	if len(NewRing(0).buf) != DefaultRingSize || len(NewRing(-5).buf) != DefaultRingSize {
		t.Error("default size")
	}
	r := NewRing(100)
	_, _ = r.Write([]byte("line one\nline two\nline three\n"))
	if got := r.Tail(12); got != "line three\n" {
		t.Errorf("a tail starts on a line boundary: %q", got)
	}
	if got := r.Tail(1000); got != "line one\nline two\nline three\n" {
		t.Errorf("%q", got)
	}
}

func TestRingNeverReturnsHalfACharacter(t *testing.T) {
	r := NewRing(7)
	_, _ = r.Write([]byte(strings.Repeat("é", 10))) // 2 bytes each; the ring starts in the middle of one
	if !utf8.ValidString(r.String()) || !utf8.ValidString(r.Tail(5)) {
		t.Errorf("%q", r.String())
	}
}

func TestRingIsSafeForConcurrentWriters(t *testing.T) {
	r := NewRing(1024)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				_, _ = r.Write([]byte("0123456789"))
				_ = r.String()
			}
		}()
	}
	wg.Wait()
	if len(r.Bytes()) != 1024 {
		t.Errorf("%d", len(r.Bytes()))
	}
}
