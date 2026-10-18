// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package httpx

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
)

// Kind says what went wrong with a call to a backend, in terms the session acts on.
type Kind int

// The kinds of failure. Unavailable, Timeout, RateLimited and Server end in a pause with a notice once the retries are
// used up; Auth, BadRequest and NotFound need the user (or, for BadRequest, another way of asking, see the probe).
const (
	// KindUnavailable: nothing is listening at the address, or its name does not resolve.
	KindUnavailable Kind = iota + 1
	// KindTimeout: the backend did not answer in time.
	KindTimeout
	// KindRateLimited: HTTP 429.
	KindRateLimited
	// KindServer: HTTP 5xx, or a connection that broke while the backend was answering.
	KindServer
	// KindAuth: HTTP 401 or 403: the key is missing or wrong.
	KindAuth
	// KindBadRequest: HTTP 400 or 422 (and other 4xx): the backend refused what was asked, for example a tool call or an
	// image it does not support.
	KindBadRequest
	// KindNotFound: HTTP 404: no such model or endpoint.
	KindNotFound
	// KindProtocol: an answer that is not what the API sends.
	KindProtocol
)

func (k Kind) String() string {
	switch k {
	case KindUnavailable:
		return "unavailable"
	case KindTimeout:
		return "timeout"
	case KindRateLimited:
		return "rate_limited"
	case KindServer:
		return "server"
	case KindAuth:
		return "auth"
	case KindBadRequest:
		return "bad_request"
	case KindNotFound:
		return "not_found"
	case KindProtocol:
		return "protocol"
	}
	return "unknown"
}

// Error is a failed call to a backend. Its text never holds the request or a key: only what the backend said about the
// failure, as one clean short line.
type Error struct {
	Kind    Kind
	Backend string
	// Status is the HTTP status, or 0 when there was no answer.
	Status  int
	Message string
	// RetryAfter is how long the backend asked to wait, if it did.
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string {
	s := e.Backend + ": " + e.Kind.String()
	if e.Status != 0 {
		s += " (HTTP " + strconv.Itoa(e.Status) + ")"
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// Unwrap gives the error underneath, if any (a timeout, a refused connection).
func (e *Error) Unwrap() error { return e.Err }

// Defaults of a Client (PLAN.md section 9.1).
const (
	DefaultFirstTimeout  = 600 * time.Second
	DefaultTimeout       = 180 * time.Second
	DefaultMaxRetryAfter = 30 * time.Second
	DefaultMaxBody       = 64 << 20
	maxMessageRunes      = 300
)

// DefaultBackoff is the pause before each retry: 1 s, 2 s, 4 s, so a call is made at most four times.
var DefaultBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// Client makes the calls of one backend: it gives each attempt a deadline, retries what is worth retrying, and turns every
// failure into an *Error.
type Client struct {
	// Name is the backend's name in errors ("ollama").
	Name string
	HTTP *http.Client
	// First is the deadline of an attempt until a call has succeeded: the first call may have to load the model. Timeout
	// is the deadline of the attempts after that.
	First, Timeout time.Duration
	// Backoff is the pause before each retry. A Retry-After of the backend, up to MaxRetryAfter, replaces it.
	Backoff       []time.Duration
	MaxRetryAfter time.Duration
	// MaxBody limits the size of an answer.
	MaxBody int64
	// Redact is applied to the backend's words before they go into an error (to take a key out, should the backend echo
	// it); nil leaves them as they are.
	Redact func(string) string
	// Sleep waits between attempts; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error

	warm atomic.Bool
}

// New returns a client with the defaults of the plan.
func New(name string) *Client {
	return &Client{Name: name, HTTP: &http.Client{CheckRedirect: sameOrigin}, First: DefaultFirstTimeout, Timeout: DefaultTimeout, Backoff: DefaultBackoff,
		MaxRetryAfter: DefaultMaxRetryAfter, MaxBody: DefaultMaxBody, Sleep: sleepContext}
}

// maxRedirects is how many redirects within one origin a call follows.
const maxRedirects = 3

// sameOrigin lets a call follow redirects only within the address it was made to (same scheme and host). A model server
// answers where it is asked; a redirect elsewhere would take the key with it: Go drops the Authorization header when the host
// changes but not x-api-key, and it sends both on to plain http on the same host.
func sameOrigin(req *http.Request, via []*http.Request) error {
	first := via[0]
	if len(via) > maxRedirects || req.URL.Scheme != first.URL.Scheme || req.URL.Host != first.URL.Host {
		return http.ErrUseLastResponse
	}
	return nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Post sends a JSON body and returns the body of the answer. Headers are added to the standard ones; a key goes in as a
// header and is never logged. A context that ends stops the call and its error is the context's.
func (c *Client) Post(ctx context.Context, rawURL string, headers map[string]string, body []byte) ([]byte, error) {
	return c.do(ctx, http.MethodPost, rawURL, headers, body, 0, c.Backoff)
}

// Get fetches a URL with its own deadline (0 means the client's), without retries: it is for asking a server whether it
// is there.
func (c *Client) Get(ctx context.Context, rawURL string, headers map[string]string, timeout time.Duration) ([]byte, error) {
	return c.do(ctx, http.MethodGet, rawURL, headers, nil, timeout, nil)
}

func (c *Client) do(ctx context.Context, method, rawURL string, headers map[string]string, body []byte, timeout time.Duration, backoff []time.Duration) ([]byte, error) {
	var last *Error
	for attempt := 0; ; attempt++ {
		out, err := c.attempt(ctx, method, rawURL, headers, body, timeout)
		if err == nil {
			c.warm.Store(true)
			return out, nil
		}
		var e *Error
		if !errors.As(err, &e) {
			return nil, err // the context ended
		}
		last = e
		if !retryable(e) || attempt >= len(backoff) {
			return nil, last
		}
		wait := backoff[attempt]
		if e.RetryAfter > 0 {
			if e.RetryAfter > c.MaxRetryAfter {
				return nil, last // asked to wait longer than a session may: the session pauses and says so
			}
			wait = e.RetryAfter
		}
		if err := c.Sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// retryable says whether another attempt may help: the backend is busy or failing, or it was slow.
func retryable(e *Error) bool {
	switch e.Kind {
	case KindRateLimited, KindServer, KindTimeout:
		return true
	}
	return false
}

func (c *Client) attempt(ctx context.Context, method, rawURL string, headers map[string]string, body []byte, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		timeout = c.Timeout
		if !c.warm.Load() {
			timeout = c.First
		}
	}
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(actx, method, rawURL, reader)
	if err != nil {
		return nil, &Error{Kind: KindProtocol, Backend: c.Name, Message: "the request could not be made", Err: errors.Unwrap(err)}
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, c.transportError(ctx, actx, err)
	}
	defer resp.Body.Close()
	limit := c.MaxBody
	if limit <= 0 {
		limit = DefaultMaxBody
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, c.transportError(ctx, actx, err)
	}
	if int64(len(data)) > limit {
		return nil, &Error{Kind: KindProtocol, Backend: c.Name, Status: resp.StatusCode, Message: fmt.Sprintf("the answer is larger than %d bytes", limit)}
	}
	if resp.StatusCode/100 == 3 {
		return nil, c.redirectError(resp)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, c.statusError(resp, data)
	}
	return data, nil
}

// redirectError is the answer to a redirect that sameOrigin did not follow: it says where the server wanted to send the call.
func (c *Client) redirectError(resp *http.Response) *Error {
	where := "somewhere else"
	if loc, err := resp.Location(); err == nil && loc.Host != "" {
		where = loc.Scheme + "://" + loc.Host
	}
	return &Error{Kind: KindBadRequest, Backend: c.Name, Status: resp.StatusCode,
		Message: c.clean(fmt.Sprintf("the server redirected the call to %s, which is not followed (it would carry the key along); if that is the right address, use it as the base URL", where))}
}

// transportError says what a failure before or during the answer means. The context of the caller ending is not an error
// of the backend: it is returned as it is.
func (c *Client) transportError(ctx, attempt context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	e := &Error{Backend: c.Name, Err: err}
	var netErr net.Error
	switch {
	case errors.Is(attempt.Err(), context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		e.Kind, e.Message = KindTimeout, "no answer in time"
	case isCertificateError(err):
		e.Kind, e.Message = KindUnavailable, "the certificate of the server is not valid"
	case isUnavailable(err):
		e.Kind, e.Message = KindUnavailable, "nothing answers at that address"
	default:
		e.Kind, e.Message = KindServer, "the connection broke"
	}
	return e
}

// isCertificateError reports a server whose certificate was refused.
func isCertificateError(err error) bool {
	var cert *tls.CertificateVerificationError
	return errors.As(err, &cert)
}

// isUnavailable reports a connection that was refused, or an address that does not resolve.
func isUnavailable(err error) bool {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial" && !op.Timeout()
}

func (c *Client) statusError(resp *http.Response, body []byte) *Error {
	e := &Error{Backend: c.Name, Status: resp.StatusCode, Message: c.clean(message(body))}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		e.Kind = KindRateLimited
		e.RetryAfter = retryAfter(resp.Header.Get("Retry-After"), time.Now())
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		e.Kind = KindAuth
	case resp.StatusCode == http.StatusNotFound:
		e.Kind = KindNotFound
	case resp.StatusCode == http.StatusRequestTimeout:
		e.Kind = KindTimeout
	case resp.StatusCode >= 500:
		e.Kind = KindServer
		e.RetryAfter = retryAfter(resp.Header.Get("Retry-After"), time.Now())
	default:
		e.Kind = KindBadRequest
	}
	return e
}

func (c *Client) clean(s string) string {
	if c.Redact != nil {
		s = c.Redact(s)
	}
	return textclean.Line(s, maxMessageRunes)
}

// message finds what a backend says about an error in its answer: {"error": "..."}, {"error": {"message": "..."}},
// {"message": "..."} or {"detail": "..."}; failing that, the start of the body.
func message(body []byte) string {
	var doc struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  string          `json:"detail"`
	}
	if json.Unmarshal(body, &doc) == nil {
		var s string
		if json.Unmarshal(doc.Error, &s) == nil && s != "" {
			return s
		}
		var obj struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(doc.Error, &obj) == nil && obj.Message != "" {
			return obj.Message
		}
		if doc.Message != "" {
			return doc.Message
		}
		if doc.Detail != "" {
			return doc.Detail
		}
	}
	return strings.TrimSpace(string(body))
}

// retryAfter reads a Retry-After header: seconds, or a date.
func retryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return 0
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// ValidateBaseURL checks the address of a backend: http or https, a host, and nothing that could carry a secret or
// change where a request goes (no credentials, no query, no fragment). A key is given as a header, from the
// environment, never in the address. The result has no trailing slash on its path.
func ValidateBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("the address %q is not a URL", raw)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("the address %q is not http or https", redactURL(u))
	case u.Hostname() == "":
		return nil, fmt.Errorf("the address %q has no host", redactURL(u))
	case u.User != nil:
		return nil, errors.New("the address holds a user name or password: give the key with --api-key-env, not in the address")
	case u.RawQuery != "" || u.Fragment != "":
		return nil, fmt.Errorf("the address %q must not have a query or a fragment", redactURL(u))
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

func redactURL(u *url.URL) string {
	c := *u
	c.User = nil
	c.RawQuery, c.Fragment = "", ""
	return c.String()
}

// IsLoopback reports whether an address is on this machine: the name localhost, or a loopback IP address. A host name that
// resolves to the loopback is not trusted to (it may resolve elsewhere tomorrow).
func IsLoopback(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
