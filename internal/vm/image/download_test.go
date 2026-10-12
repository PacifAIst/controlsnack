// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// payload is a reproducible 1 MiB "image".
func payload(seed byte) []byte {
	b := make([]byte, 1<<20)
	for i := range b {
		b[i] = byte(i*31+i>>8) ^ seed
	}
	return b
}

func sum512(b []byte) string { s := sha512.Sum512(b); return hex.EncodeToString(s[:]) }

// server runs a TLS test server and returns a Downloader that trusts it.
func server(t *testing.T, h http.Handler) (*httptest.Server, Downloader) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	c := srv.Client()
	c.CheckRedirect = checkRedirect
	return srv, Downloader{Client: c, IdleTimeout: 2 * time.Second}
}

// serving serves content with full Range support and counts requests.
type serving struct {
	content  []byte
	requests atomic.Int32
	mu       sync.Mutex
	ranges   []string
}

func (s *serving) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.requests.Add(1)
	s.mu.Lock()
	s.ranges = append(s.ranges, r.Header.Get("Range"))
	s.mu.Unlock()
	http.ServeContent(w, r, "image.qcow2", time.Time{}, strings.NewReader(string(s.content)))
}

func TestParseSHA512SUMS(t *testing.T) {
	a, b := strings.Repeat("a", 128), strings.Repeat("B", 128)
	data := []byte(a + "  debian-13-genericcloud-amd64.qcow2\n" +
		b + " *debian-13-genericcloud-arm64.qcow2\r\n" +
		strings.Repeat("c", 128) + "  debian-13-genericcloud-amd64.qcow2.json\n" +
		"not a checksum line\n\n" +
		strings.Repeat("d", 100) + "  short.qcow2\n")
	got, err := ParseSHA512SUMS(data, "debian-13-genericcloud-amd64.qcow2")
	if err != nil || got != a {
		t.Errorf("%q %v", got, err)
	}
	got, err = ParseSHA512SUMS(data, "debian-13-genericcloud-arm64.qcow2")
	if err != nil || got != strings.ToLower(b) {
		t.Errorf("binary marker and CRLF, normalized to lower case: %q %v", got, err)
	}
	for _, name := range []string{"debian-13-genericcloud-amd64", "genericcloud-amd64.qcow2", "short.qcow2", "missing.qcow2", ""} {
		if _, err := ParseSHA512SUMS(data, name); err == nil {
			t.Errorf("%q: found something it should not have", name)
		}
	}
	dup := []byte(a + "  x.qcow2\n" + a + "  x.qcow2\n")
	if got, err := ParseSHA512SUMS(dup, "x.qcow2"); err != nil || got != a {
		t.Errorf("the same line twice is fine: %v", err)
	}
	conflict := []byte(a + "  x.qcow2\n" + strings.Repeat("e", 128) + "  x.qcow2\n")
	if _, err := ParseSHA512SUMS(conflict, "x.qcow2"); err == nil {
		t.Error("two different sums for one file")
	}
}

func TestDownloadFresh(t *testing.T) {
	content := payload(1)
	h := &serving{content: content}
	srv, d := server(t, h)
	var last [2]int64
	d.Progress = func(done, total int64) { last = [2]int64{done, total} }
	dest := filepath.Join(t.TempDir(), "sub", "img.qcow2")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := d.Download(context.Background(), srv.URL+"/img", dest, sum512(content))
	if err != nil || res.Cached || res.Resumed || res.Bytes != int64(len(content)) {
		t.Fatalf("%+v %v", res, err)
	}
	if got, _ := os.ReadFile(dest); string(got) != string(content) {
		t.Error("content differs")
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Errorf("the partial file is gone: %v", err)
	}
	if last != [2]int64{int64(len(content)), int64(len(content))} {
		t.Errorf("progress ends at done == total: %v", last)
	}

	// A second call finds the verified file and does not touch the network.
	before := h.requests.Load()
	res, err = d.Download(context.Background(), srv.URL+"/img", dest, sum512(content))
	if err != nil || !res.Cached || h.requests.Load() != before {
		t.Errorf("%+v %v (%d new requests)", res, err, h.requests.Load()-before)
	}

	// A file with another checksum at the destination is replaced, not trusted.
	if err := os.WriteFile(dest, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, err := d.Download(context.Background(), srv.URL+"/img", dest, sum512(content)); err != nil || res.Cached {
		t.Errorf("%+v %v", res, err)
	}
}

func TestDownloadResumes(t *testing.T) {
	content := payload(2)
	h := &serving{content: content}
	srv, d := server(t, h)
	dest := filepath.Join(t.TempDir(), "img.qcow2")
	if err := os.WriteFile(dest+".part", content[:400_000], 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := d.Download(context.Background(), srv.URL, dest, sum512(content))
	if err != nil || !res.Resumed || res.Bytes != int64(len(content)-400_000) {
		t.Fatalf("%+v %v", res, err)
	}
	if h.ranges[0] != "bytes=400000-" {
		t.Errorf("range %q", h.ranges)
	}
	if got, _ := os.ReadFile(dest); sum512(got) != sum512(content) {
		t.Error("the resumed file is not the whole file")
	}
}

func TestDownloadWhenTheServerIgnoresRange(t *testing.T) {
	content := payload(3)
	srv, d := server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content) // always the whole file, status 200
	}))
	dest := filepath.Join(t.TempDir(), "img.qcow2")
	if err := os.WriteFile(dest+".part", content[:100_000], 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := d.Download(context.Background(), srv.URL, dest, sum512(content))
	if err != nil || res.Bytes != int64(len(content)) {
		t.Fatalf("a server without range support means starting again, not a corrupt file: %+v %v", res, err)
	}
	if got, _ := os.ReadFile(dest); sum512(got) != sum512(content) {
		t.Error("corrupt")
	}
}

func TestDownloadPartialAlreadyComplete(t *testing.T) {
	content := payload(4)
	srv, d := server(t, &serving{content: content}) // answers 416 to a range at the end of the file
	dest := filepath.Join(t.TempDir(), "img.qcow2")
	if err := os.WriteFile(dest+".part", content, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := d.Download(context.Background(), srv.URL, dest, sum512(content))
	if err != nil || res.Bytes != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Error(err)
	}
}

func TestDownloadChecksumMismatch(t *testing.T) {
	content := payload(5)
	srv, d := server(t, &serving{content: content})
	dest := filepath.Join(t.TempDir(), "img.qcow2")
	wrong := sum512([]byte("something else"))
	_, err := d.Download(context.Background(), srv.URL, dest, wrong)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("%v", err)
	}
	for _, p := range []string{dest, dest + ".part"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("a file that failed its checksum must not stay: %s (%v)", p, err)
		}
	}
}

func TestDownloadRestartsOnceWhenAResumedFileDoesNotAddUp(t *testing.T) {
	// The partial file is from an older version of the image; the server now serves a newer one.
	oldC, newC := payload(6), payload(7)
	h := &serving{content: newC}
	srv, d := server(t, h)
	dest := filepath.Join(t.TempDir(), "img.qcow2")
	if err := os.WriteFile(dest+".part", oldC[:300_000], 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := d.Download(context.Background(), srv.URL, dest, sum512(newC))
	if err != nil || !res.Resumed {
		t.Fatalf("%+v %v", res, err)
	}
	if got, _ := os.ReadFile(dest); sum512(got) != sum512(newC) {
		t.Error("the final file must be the new image")
	}
	if n := h.requests.Load(); n != 2 {
		t.Errorf("one resumed attempt, one fresh: %d requests", n)
	}
}

func TestDownloadTruncatedKeepsThePartForResume(t *testing.T) {
	content := payload(8)
	var calls atomic.Int32
	srv, d := server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 { // the first connection dies half way through
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			_, _ = w.Write(content[:300_000])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					conn.Close()
				}
			}
			return
		}
		http.ServeContent(w, r, "x", time.Time{}, strings.NewReader(string(content)))
	}))
	dest := filepath.Join(t.TempDir(), "img.qcow2")
	_, err := d.Download(context.Background(), srv.URL, dest, sum512(content))
	if err == nil {
		t.Fatal("a truncated download is an error")
	}
	fi, serr := os.Stat(dest + ".part")
	if serr != nil || fi.Size() == 0 || fi.Size() >= int64(len(content)) {
		t.Fatalf("the part is kept: %v %v", fi, serr)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("an incomplete file is never the destination")
	}
	res, err := d.Download(context.Background(), srv.URL, dest, sum512(content))
	if err != nil || !res.Resumed {
		t.Fatalf("the next call continues: %+v %v", res, err)
	}
}

func TestDownloadStalledServer(t *testing.T) {
	content := payload(9)
	srv, d := server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		_, _ = w.Write(content[:50_000])
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	d.IdleTimeout = 200 * time.Millisecond
	dest := filepath.Join(t.TempDir(), "img.qcow2")
	start := time.Now()
	_, err := d.Download(context.Background(), srv.URL, dest, sum512(content))
	if err == nil || !errors.Is(err, errIdle) || time.Since(start) > 5*time.Second {
		t.Errorf("%v after %v", err, time.Since(start))
	}
	if fi, err := os.Stat(dest + ".part"); err != nil || fi.Size() != 50_000 {
		t.Errorf("what arrived is kept: %v %v", fi, err)
	}
}

func TestDownloadCanceled(t *testing.T) {
	content := payload(10)
	release := make(chan struct{})
	srv, d := server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		_, _ = w.Write(content[:10_000])
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()
	dest := filepath.Join(t.TempDir(), "img.qcow2")
	start := time.Now()
	_, err := d.Download(ctx, srv.URL, dest, sum512(content))
	if !errors.Is(err, context.Canceled) || time.Since(start) > 5*time.Second {
		t.Errorf("%v after %v", err, time.Since(start))
	}
	if _, err := os.Stat(dest + ".part"); err != nil {
		t.Errorf("the part is kept for the next time: %v", err)
	}
}

func TestDownloadRefusals(t *testing.T) {
	content := payload(11)
	want := sum512(content)
	dest := filepath.Join(t.TempDir(), "img.qcow2")

	t.Run("not https", func(t *testing.T) {
		if _, err := (Downloader{}).Download(context.Background(), "http://example.org/x", dest, want); err == nil || !strings.Contains(err.Error(), "https") {
			t.Errorf("%v", err)
		}
		if _, err := (Downloader{}).Download(context.Background(), "file:///etc/passwd", dest, want); err == nil {
			t.Error("file URL")
		}
		if _, err := (Downloader{}).FetchText(context.Background(), "http://example.org/SHA512SUMS", 100); err == nil {
			t.Error("http checksum list")
		}
	})
	t.Run("bad expected checksum", func(t *testing.T) {
		if _, err := (Downloader{}).Download(context.Background(), "https://example.org/x", dest, "abc"); err == nil {
			t.Error("accepted")
		}
	})
	t.Run("redirect to http", func(t *testing.T) {
		srv, d := server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://example.org/image", http.StatusFound)
		}))
		_, err := d.Download(context.Background(), srv.URL, dest, want)
		if err == nil || !strings.Contains(err.Error(), "only https") {
			t.Errorf("%v", err)
		}
	})
	t.Run("endless redirects", func(t *testing.T) {
		var srv *httptest.Server
		srv, d := server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, srv.URL+"/again", http.StatusFound)
		}))
		_, err := d.Download(context.Background(), srv.URL, dest, want)
		if err == nil || !strings.Contains(err.Error(), "redirects") {
			t.Errorf("%v", err)
		}
	})
	t.Run("redirect within https works", func(t *testing.T) {
		var srv *httptest.Server
		srv, d := server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/latest" {
				http.Redirect(w, r, srv.URL+"/mirror/img", http.StatusFound)
				return
			}
			http.ServeContent(w, r, "x", time.Time{}, strings.NewReader(string(content)))
		}))
		if _, err := d.Download(context.Background(), srv.URL+"/latest", filepath.Join(t.TempDir(), "i"), want); err != nil {
			t.Errorf("a mirror redirect, as cloud.debian.org does: %v", err)
		}
	})
	t.Run("not found", func(t *testing.T) {
		srv, d := server(t, http.NotFoundHandler())
		if _, err := d.Download(context.Background(), srv.URL, dest, want); err == nil || !strings.Contains(err.Error(), "404") {
			t.Errorf("%v", err)
		}
	})
	t.Run("announced size over the limit", func(t *testing.T) {
		srv, d := server(t, &serving{content: content})
		d.MaxSize = 1000
		if _, err := d.Download(context.Background(), srv.URL, dest, want); err == nil || !strings.Contains(err.Error(), "more than") {
			t.Errorf("%v", err)
		}
		if _, err := os.Stat(dest + ".part"); err == nil {
			if fi, _ := os.Stat(dest + ".part"); fi.Size() != 0 {
				t.Errorf("nothing is written for an announced oversize file")
			}
		}
	})
	t.Run("more data than announced limit", func(t *testing.T) {
		srv, d := server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Transfer-Encoding", "chunked") // no Content-Length to check up front
			_, _ = w.Write(content)
		}))
		d.MaxSize = 100_000
		if _, err := d.Download(context.Background(), srv.URL, filepath.Join(t.TempDir(), "i"), want); err == nil || !strings.Contains(err.Error(), "larger than") {
			t.Errorf("%v", err)
		}
	})
	t.Run("a range answer for the wrong place", func(t *testing.T) {
		srv, d := server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Range", "bytes 0-9/1000")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(make([]byte, 10))
		}))
		part := filepath.Join(t.TempDir(), "i")
		if err := os.WriteFile(part+".part", content[:500], 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Download(context.Background(), srv.URL, part, want); err == nil || !strings.Contains(err.Error(), "Content-Range") {
			t.Errorf("%v", err)
		}
	})
}

func TestFetchText(t *testing.T) {
	srv, d := server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, strings.Repeat("x", 5000))
	}))
	if data, err := d.FetchText(context.Background(), srv.URL+"/SHA512SUMS", 10_000); err != nil || len(data) != 5000 {
		t.Errorf("%d %v", len(data), err)
	}
	if _, err := d.FetchText(context.Background(), srv.URL+"/SHA512SUMS", 1000); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("%v", err)
	}
	if _, err := d.FetchText(context.Background(), srv.URL+"/missing", 1000); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("%v", err)
	}
}

func TestParseContentRange(t *testing.T) {
	for in, want := range map[string][3]int64{"bytes 100-199/1000": {100, 199, 1000}, "bytes 0-0/1": {0, 0, 1}, "bytes 5-9/*": {5, 9, 0}, "bytes */1000": {0, 0, 1000}} {
		s, e, tot, ok := parseContentRange(in)
		if !ok || [3]int64{s, e, tot} != want {
			t.Errorf("%q: %d %d %d %v", in, s, e, tot, ok)
		}
	}
	for _, bad := range []string{"", "bytes", "100-199/1000", "bytes a-b/c", "items 1-2/3", "bytes 1-2"} {
		if _, _, _, ok := parseContentRange(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}
