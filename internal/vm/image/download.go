// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Limits of a download.
const (
	// DefaultMaxSize bounds a base image download; the Debian genericcloud image is about 330 MB.
	DefaultMaxSize = 4 << 30
	// DefaultIdleTimeout is how long a download may go without receiving a byte.
	DefaultIdleTimeout = 30 * time.Second
	// maxRedirects bounds the redirect chain (cloud.debian.org redirects to a mirror).
	maxRedirects = 5
)

// ErrChecksum means a downloaded file does not have the checksum the publisher lists.
var ErrChecksum = errors.New("checksum mismatch")

var errIdle = errors.New("the server stopped sending data")

// NewClient returns an HTTP client that follows redirects only over HTTPS, at most five times. A mirror may be
// anywhere, but a redirect to plain HTTP would take away the transport's integrity, so it is refused.
func NewClient() *http.Client {
	return &http.Client{CheckRedirect: checkRedirect}
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refusing to follow a redirect to %s: only https is allowed", req.URL.Redacted())
	}
	return nil
}

var reSumLine = regexp.MustCompile(`^([0-9a-fA-F]{128}) [ *](.+)$`)

// ParseSHA512SUMS returns the lowercase checksum listed for name in the content of a SHA512SUMS file
// ("<128 hex digits>  <file name>" per line). It is an error if name is missing or listed twice with different sums.
func ParseSHA512SUMS(data []byte, name string) (string, error) {
	var found string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		m := reSumLine.FindStringSubmatch(strings.TrimRight(sc.Text(), "\r"))
		if m == nil || m[2] != name {
			continue
		}
		sum := strings.ToLower(m[1])
		if found != "" && found != sum {
			return "", fmt.Errorf("the checksum file lists %s twice with different sums", name)
		}
		found = sum
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("the checksum file does not list %s", name)
	}
	return found, nil
}

// Downloader fetches files over HTTPS with resume and verifies their SHA-512.
type Downloader struct {
	Client      *http.Client  // default NewClient()
	IdleTimeout time.Duration // default DefaultIdleTimeout
	MaxSize     int64         // default DefaultMaxSize
	// Progress, when set, is called as bytes arrive with the bytes on disk so far and the total (0 if unknown).
	Progress func(done, total int64)
}

func (d Downloader) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return NewClient()
}

func (d Downloader) idle() time.Duration {
	if d.IdleTimeout > 0 {
		return d.IdleTimeout
	}
	return DefaultIdleTimeout
}

func (d Downloader) maxSize() int64 {
	if d.MaxSize > 0 {
		return d.MaxSize
	}
	return DefaultMaxSize
}

func requireHTTPS(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%q is not an https URL", rawURL)
	}
	return nil
}

// FetchText downloads a small text file (a checksum list) into memory.
func (d Downloader) FetchText(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	if err := requireHTTPS(rawURL); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", rawURL, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", rawURL, limit)
	}
	return data, nil
}

// DownloadResult says what a Download did.
type DownloadResult struct {
	Cached  bool  // dest already existed with the right checksum: nothing was fetched
	Resumed bool  // a partial file was continued
	Bytes   int64 // bytes fetched in this call
}

// Download fetches rawURL into dest and checks its SHA-512 against want. The bytes land in dest+".part"; an
// interrupted download is kept and continued next time (HTTP Range). The finished file is renamed to dest only
// after the checksum has matched, so dest is either absent or good. A checksum mismatch of a continued download is
// retried once from scratch (the file on the server may have been replaced in between); a mismatch of a complete
// fresh download is final.
func (d Downloader) Download(ctx context.Context, rawURL, dest, want string) (DownloadResult, error) {
	var res DownloadResult
	if err := requireHTTPS(rawURL); err != nil {
		return res, err
	}
	if !reHex128.MatchString(want) {
		return res, errors.New("the expected SHA-512 is not 128 lowercase hex digits")
	}
	if got, err := sha512File(ctx, dest); err == nil {
		if got == want {
			res.Cached = true
			return res, nil
		}
		_ = os.Remove(dest) // an old file with another checksum is not ours to keep
	} else if !errors.Is(err, os.ErrNotExist) {
		return res, err
	}

	part := dest + ".part"
	for attempt := 0; ; attempt++ {
		got, resumed, n, err := d.fetch(ctx, rawURL, part)
		res.Bytes += n
		res.Resumed = res.Resumed || resumed
		if err != nil {
			return res, err
		}
		if got == want {
			if err := os.Rename(part, dest); err != nil {
				return res, err
			}
			return res, nil
		}
		_ = os.Remove(part)
		if !resumed || attempt > 0 {
			return res, fmt.Errorf("%w: %s has SHA-512 %s…, the publisher lists %s…", ErrChecksum, rawURL, got[:16], want[:16])
		}
		// A continued download that does not add up: the server's file changed. Start over once.
	}
}

// fetch brings part up to the full file and returns the SHA-512 of all of it.
func (d Downloader) fetch(ctx context.Context, rawURL, part string) (sum string, resumed bool, fetched int64, err error) {
	f, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // inside the cache
	if err != nil {
		return "", false, 0, err
	}
	defer f.Close()
	h := sha512.New()
	have, err := io.Copy(h, f) // what is already there counts toward the checksum
	if err != nil {
		return "", false, 0, err
	}
	if have > d.maxSize() {
		have = 0
		h.Reset()
		if err := f.Truncate(0); err != nil {
			return "", false, 0, err
		}
	}

	for tries := 0; ; tries++ {
		total, body, status, err := d.open(ctx, rawURL, have)
		if err != nil {
			return "", false, fetched, err
		}
		switch {
		case status == http.StatusOK && have > 0: // the server ignored the Range header: start from the beginning
			have = 0
			h.Reset()
			if err := f.Truncate(0); err != nil {
				_ = body.Close()
				return "", false, fetched, err
			}
		case status == http.StatusRequestedRangeNotSatisfiable:
			_ = body.Close()
			if total == have && have > 0 { // the file on disk is already the whole file
				return hex.EncodeToString(h.Sum(nil)), have > 0, fetched, nil
			}
			if tries > 0 {
				return "", false, fetched, errors.New("the server rejects every range request for the partial file")
			}
			have = 0
			h.Reset()
			if err := f.Truncate(0); err != nil {
				return "", false, fetched, err
			}
			continue
		}
		resumed = have > 0
		if _, err := f.Seek(have, io.SeekStart); err != nil {
			_ = body.Close()
			return "", false, fetched, err
		}
		n, err := d.copyBody(ctx, io.MultiWriter(f, h), body, have, total)
		_ = body.Close()
		fetched += n
		have += n
		if err != nil {
			return "", resumed, fetched, err
		}
		if total > 0 && have != total {
			return "", resumed, fetched, fmt.Errorf("the download ended after %d of %d bytes (it can be continued)", have, total)
		}
		if err := f.Sync(); err != nil {
			return "", resumed, fetched, err
		}
		return hex.EncodeToString(h.Sum(nil)), resumed, fetched, nil
	}
}

// open starts a request from offset and returns the body and the total size of the file (0 if unknown).
func (d Downloader) open(ctx context.Context, rawURL string, offset int64) (total int64, body io.ReadCloser, status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, 0, err
	}
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return 0, nil, 0, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		total = resp.ContentLength
	case http.StatusPartialContent:
		start, _, tot, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != offset {
			_ = resp.Body.Close()
			return 0, nil, 0, fmt.Errorf("the server answered the range request with Content-Range %q", resp.Header.Get("Content-Range"))
		}
		total = tot
	case http.StatusRequestedRangeNotSatisfiable:
		_, _, tot, _ := parseContentRange(resp.Header.Get("Content-Range"))
		return tot, resp.Body, resp.StatusCode, nil
	default:
		_ = resp.Body.Close()
		return 0, nil, 0, fmt.Errorf("%s: %s", rawURL, resp.Status)
	}
	if total > d.maxSize() {
		_ = resp.Body.Close()
		return 0, nil, 0, fmt.Errorf("%s is %d bytes, more than the %d allowed", rawURL, total, d.maxSize())
	}
	return total, resp.Body, resp.StatusCode, nil
}

var reContentRange = regexp.MustCompile(`^bytes (\d+)-(\d+)/(\d+|\*)$|^bytes \*/(\d+)$`)

// parseContentRange reads "bytes 100-199/1000" or "bytes */1000".
func parseContentRange(h string) (start, end, total int64, ok bool) {
	m := reContentRange.FindStringSubmatch(h)
	if m == nil {
		return 0, 0, 0, false
	}
	if m[4] != "" {
		total, _ = strconv.ParseInt(m[4], 10, 64)
		return 0, 0, total, true
	}
	start, _ = strconv.ParseInt(m[1], 10, 64)
	end, _ = strconv.ParseInt(m[2], 10, 64)
	if m[3] != "*" {
		total, _ = strconv.ParseInt(m[3], 10, 64)
	}
	return start, end, total, true
}

// copyBody streams the body, giving up when no byte arrives for the idle timeout or the file outgrows its limit.
func (d Downloader) copyBody(ctx context.Context, dst io.Writer, body io.Reader, have, total int64) (int64, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var stalled atomic.Bool
	timer := time.AfterFunc(d.idle(), func() { stalled.Store(true); cancel(errIdle) })
	defer timer.Stop()

	// A canceled context closes the body, which unblocks a read that is waiting for the server.
	closer, _ := body.(io.Closer)
	stop := context.AfterFunc(ctx, func() {
		if closer != nil {
			_ = closer.Close()
		}
	})
	defer stop()

	buf := make([]byte, 256<<10)
	var n int64
	for {
		r, rerr := body.Read(buf)
		if r > 0 {
			timer.Reset(d.idle())
			if have+n+int64(r) > d.maxSize() {
				return n, fmt.Errorf("the download is larger than the %d bytes allowed", d.maxSize())
			}
			if _, werr := dst.Write(buf[:r]); werr != nil {
				return n, werr
			}
			n += int64(r)
			if d.Progress != nil {
				d.Progress(have+n, total)
			}
		}
		if rerr == io.EOF {
			return n, nil
		}
		if rerr != nil {
			if stalled.Load() {
				return n, fmt.Errorf("%w for %s (the partial file is kept and can be continued)", errIdle, d.idle())
			}
			if cerr := ctx.Err(); cerr != nil {
				return n, context.Cause(ctx)
			}
			return n, rerr
		}
	}
}

func sha512File(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // a cache path
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha512.New()
	if _, err := copyCtx(ctx, h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
