// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// recording returns a client that does not wait between attempts and remembers how long it was asked to.
func recording(c *Client) (*Client, *[]time.Duration) {
	var slept []time.Duration
	c.Sleep = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		return ctx.Err()
	}
	return c, &slept
}

func asHTTPError(t *testing.T, err error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("not an *Error: %v", err)
	}
	return e
}

func TestPostSendsAJSONBodyAndReturnsTheAnswer(t *testing.T) {
	var got struct {
		method, ctype, accept, auth, body string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.ctype, got.accept, got.auth = r.Method, r.Header.Get("Content-Type"), r.Header.Get("Accept"), r.Header.Get("Authorization")
		b := make([]byte, 100)
		n, _ := r.Body.Read(b)
		got.body = string(b[:n])
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c, _ := recording(New("test"))
	out, err := c.Post(context.Background(), srv.URL+"/api/chat", map[string]string{"Authorization": "Bearer k"}, []byte(`{"a":1}`))
	if err != nil || string(out) != `{"ok":true}` {
		t.Fatalf("%q %v", out, err)
	}
	if got.method != "POST" || got.ctype != "application/json" || got.accept != "application/json" || got.auth != "Bearer k" || got.body != `{"a":1}` {
		t.Errorf("%+v", got)
	}
}

func TestRetriesAServerThatFailsThenAnswers(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(w, `{"error":"overloaded"}`, http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("fine"))
	}))
	defer srv.Close()
	c, slept := recording(New("test"))
	out, err := c.Post(context.Background(), srv.URL, nil, []byte(`{}`))
	if err != nil || string(out) != "fine" || calls.Load() != 3 {
		t.Fatalf("%q %v after %d calls", out, err, calls.Load())
	}
	if len(*slept) != 2 || (*slept)[0] != time.Second || (*slept)[1] != 2*time.Second {
		t.Errorf("pauses %v, want 1 s then 2 s", *slept)
	}
}

func TestGivesUpAfterThreeRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":{"message":"the model crashed"}}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	c, slept := recording(New("test"))
	_, err := c.Post(context.Background(), srv.URL, nil, []byte(`{}`))
	e := asHTTPError(t, err)
	if e.Kind != KindServer || e.Status != 500 || e.Message != "the model crashed" || calls.Load() != 4 {
		t.Errorf("%+v after %d calls", e, calls.Load())
	}
	if len(*slept) != 3 || (*slept)[2] != 4*time.Second {
		t.Errorf("pauses %v, want 1, 2 and 4 s", *slept)
	}
	if !strings.Contains(e.Error(), "test: server (HTTP 500): the model crashed") {
		t.Errorf("%s", e)
	}
}

func TestHonoursRetryAfterUpToTheLimit(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "2")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("fine"))
	}))
	defer srv.Close()
	c, slept := recording(New("test"))
	if _, err := c.Post(context.Background(), srv.URL, nil, []byte(`{}`)); err != nil || len(*slept) != 1 || (*slept)[0] != 2*time.Second {
		t.Fatalf("%v, pauses %v", err, *slept)
	}

	// A request to wait longer than the limit is not waited for: the session pauses and says so.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		http.Error(w, "quota used up", http.StatusTooManyRequests)
	}))
	defer srv2.Close()
	c, slept = recording(New("test"))
	_, err := c.Post(context.Background(), srv2.URL, nil, []byte(`{}`))
	e := asHTTPError(t, err)
	if e.Kind != KindRateLimited || e.RetryAfter != 120*time.Second || len(*slept) != 0 {
		t.Errorf("%+v, pauses %v", e, *slept)
	}
}

func TestRetryAfterFormats(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Duration{
		"":     0,
		"5":    5 * time.Second,
		" 7 ":  7 * time.Second,
		"-3":   0,
		"soon": 0,
		now.Add(9 * time.Second).Format(http.TimeFormat):  9 * time.Second,
		now.Add(-9 * time.Second).Format(http.TimeFormat): 0,
	} {
		if got := retryAfter(in, now); got != want {
			t.Errorf("retryAfter(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestOtherStatusesAreNotRetried(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		kind   Kind
		msg    string
	}{
		{400, `{"error":"registry.ollama.ai/library/x does not support tools"}`, KindBadRequest, "does not support tools"},
		{422, `{"detail":"bad schema"}`, KindBadRequest, "bad schema"},
		{401, `{"error":{"message":"invalid api key"}}`, KindAuth, "invalid api key"},
		{403, `{"message":"forbidden"}`, KindAuth, "forbidden"},
		{404, `{"error":"model 'x' not found"}`, KindNotFound, "not found"},
		{418, `a teapot`, KindBadRequest, "a teapot"},
	} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			http.Error(w, c.body, c.status)
		}))
		cl, slept := recording(New("test"))
		_, err := cl.Post(context.Background(), srv.URL, nil, []byte(`{}`))
		srv.Close()
		e := asHTTPError(t, err)
		if e.Kind != c.kind || e.Status != c.status || !strings.Contains(e.Message, c.msg) || calls.Load() != 1 || len(*slept) != 0 {
			t.Errorf("%d: %+v after %d calls", c.status, e, calls.Load())
		}
	}
}

func TestMessageOfAnErrorBody(t *testing.T) {
	for in, want := range map[string]string{
		`{"error":"plain"}`:                       "plain",
		`{"error":{"message":"nested","code":1}}`: "nested",
		`{"message":"top"}`:                       "top",
		`{"detail":"fastapi"}`:                    "fastapi",
		`{"error":{"type":"x"}}`:                  `{"error":{"type":"x"}}`,
		`  not json  `:                            "not json",
		``:                                        "",
	} {
		if got := message([]byte(in)); got != want {
			t.Errorf("message(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestATimeoutIsRetriedAndFirstCallsGetMoreTime(t *testing.T) {
	var delay atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Duration(delay.Load()))
		_, _ = w.Write([]byte("late"))
	}))
	defer srv.Close()
	c, slept := recording(New("test"))
	c.First, c.Timeout = 2*time.Second, 80*time.Millisecond

	// The first call may be loading the model: it has the long deadline.
	delay.Store(int64(300 * time.Millisecond))
	if out, err := c.Post(context.Background(), srv.URL, nil, []byte(`{}`)); err != nil || string(out) != "late" {
		t.Fatalf("the first call: %q %v", out, err)
	}
	// Once one has succeeded the model is loaded: the same delay is a timeout, retried, then reported.
	_, err := c.Post(context.Background(), srv.URL, nil, []byte(`{}`))
	e := asHTTPError(t, err)
	if e.Kind != KindTimeout || len(*slept) != 3 {
		t.Errorf("%+v, pauses %v", e, *slept)
	}
}

func TestNothingListeningIsNotRetried(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	c, slept := recording(New("test"))
	_, err := c.Post(context.Background(), addr, nil, []byte(`{}`))
	if e := asHTTPError(t, err); e.Kind != KindUnavailable || e.Status != 0 || len(*slept) != 0 {
		t.Errorf("%+v, pauses %v", e, *slept)
	}
	c, _ = recording(New("test"))
	if _, err := c.Post(context.Background(), "http://nothing.invalid:1/", nil, []byte(`{}`)); asHTTPError(t, err).Kind != KindUnavailable {
		t.Errorf("a name that does not resolve: %v", err)
	}
}

func TestABrokenConnectionIsRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close() // the server dies in the middle of the answer
			return
		}
		_, _ = w.Write([]byte("back"))
	}))
	defer srv.Close()
	c, slept := recording(New("test"))
	out, err := c.Post(context.Background(), srv.URL, nil, []byte(`{}`))
	if err != nil || string(out) != "back" || len(*slept) != 1 {
		t.Errorf("%q %v pauses %v", out, err, *slept)
	}
}

func TestAnUntrustedCertificateIsSaidSo(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	c, slept := recording(New("test"))
	_, err := c.Post(context.Background(), srv.URL, nil, []byte(`{}`))
	e := asHTTPError(t, err)
	if e.Kind != KindUnavailable || !strings.Contains(e.Message, "certificate") || len(*slept) != 0 {
		t.Errorf("%+v", e)
	}
}

func TestAnAnswerThatIsTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 5000)))
	}))
	defer srv.Close()
	c, slept := recording(New("test"))
	c.MaxBody = 1000
	_, err := c.Post(context.Background(), srv.URL, nil, []byte(`{}`))
	if e := asHTTPError(t, err); e.Kind != KindProtocol || !strings.Contains(e.Message, "1000 bytes") || len(*slept) != 0 {
		t.Errorf("%+v", e)
	}
}

func TestACanceledContextStopsTheCall(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // the server notices a client that goes away once it has read the request
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	c, slept := recording(New("test"))
	_, err := c.Post(ctx, srv.URL, nil, []byte(`{}`))
	if !errors.Is(err, context.Canceled) || len(*slept) != 0 {
		t.Errorf("%v, pauses %v", err, *slept)
	}

	// ... and so does one that ends while the client waits to try again.
	ctx2, cancel2 := context.WithCancel(context.Background())
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer srv2.Close()
	c2 := New("test")
	c2.Sleep = func(ctx context.Context, d time.Duration) error { cancel2(); return ctx.Err() }
	if _, err := c2.Post(ctx2, srv2.URL, nil, []byte(`{}`)); !errors.Is(err, context.Canceled) {
		t.Errorf("while waiting: %v", err)
	}
}

func TestWhatTheBackendSaysIsCleanedAndKeepsNoKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad key sk-secret-123\nsecond line\u001b[31m `+strings.Repeat("x", 1000)+`"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	c, _ := recording(New("test"))
	c.Redact = func(s string) string { return strings.ReplaceAll(s, "sk-secret-123", "[key]") }
	_, err := c.Post(context.Background(), srv.URL, map[string]string{"Authorization": "Bearer sk-secret-123"}, []byte(`{}`))
	e := asHTTPError(t, err)
	if strings.Contains(e.Error(), "sk-secret") || !strings.Contains(e.Message, "[key]") || strings.ContainsAny(e.Message, "\n\x1b") || len([]rune(e.Message)) > maxMessageRunes {
		t.Errorf("%q", e.Message)
	}
}

func TestGetAsksOnceWithItsOwnDeadline(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 0 {
			time.Sleep(300 * time.Millisecond)
		}
		_, _ = w.Write([]byte("slow"))
	}))
	defer srv.Close()
	c, slept := recording(New("test"))
	_, err := c.Get(context.Background(), srv.URL, nil, 50*time.Millisecond)
	if e := asHTTPError(t, err); e.Kind != KindTimeout || calls.Load() != 1 || len(*slept) != 0 {
		t.Errorf("%+v after %d calls, pauses %v", e, calls.Load(), *slept)
	}
	out, err := c.Get(context.Background(), srv.URL, nil, 5*time.Second)
	if err != nil || string(out) != "slow" {
		t.Errorf("%q %v", out, err)
	}
}

func TestValidateBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:11434":      "http://127.0.0.1:11434",
		"http://localhost:1234/v1":    "http://localhost:1234/v1",
		"https://api.openai.com/v1/":  "https://api.openai.com/v1",
		" https://api.anthropic.com ": "https://api.anthropic.com",
		"http://[::1]:8080/v1":        "http://[::1]:8080/v1",
	} {
		u, err := ValidateBaseURL(in)
		if err != nil || u.String() != want {
			t.Errorf("ValidateBaseURL(%q) = %v, %v; want %s", in, u, err, want)
		}
	}
	for _, c := range []struct{ in, word string }{
		{"", "http or https"},
		{"ftp://example.com", "http or https"},
		{"example.com:80", "http or https"},
		{"http://", "no host"},
		{"http://user:secret@example.com/v1", "api-key-env"},
		{"http://example.com/v1?key=secret", "query"},
		{"http://example.com/v1#frag", "fragment"},
		{"://bad", "not a URL"},
	} {
		_, err := ValidateBaseURL(c.in)
		if err == nil || !strings.Contains(err.Error(), c.word) || strings.Contains(err.Error(), "secret") {
			t.Errorf("ValidateBaseURL(%q): %v, want a refusal mentioning %q and no secret", c.in, err, c.word)
		}
	}
}

func TestIsLoopback(t *testing.T) {
	for in, want := range map[string]bool{
		"http://127.0.0.1:11434": true, "http://127.9.9.9": true, "http://localhost:1234": true, "http://LOCALHOST": true,
		"http://[::1]:8080": true, "http://ollama.localhost": true,
		"http://192.168.1.5": false, "http://10.0.0.1": false, "https://api.openai.com": false, "http://127.0.0.1.example.com": false,
		"http://0.0.0.0:8080": false, "http://localhost.example.com": false,
	} {
		u, err := url.Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := IsLoopback(u); got != want {
			t.Errorf("IsLoopback(%s) = %v, want %v", in, got, want)
		}
	}
}

func TestKindNames(t *testing.T) {
	for k, want := range map[Kind]string{KindUnavailable: "unavailable", KindTimeout: "timeout", KindRateLimited: "rate_limited", KindServer: "server",
		KindAuth: "auth", KindBadRequest: "bad_request", KindNotFound: "not_found", KindProtocol: "protocol", 0: "unknown"} {
		if k.String() != want {
			t.Errorf("%d: %s", k, k)
		}
	}
	inner := errors.New("inner")
	if e := (&Error{Kind: KindTimeout, Backend: "b", Err: inner}); !errors.Is(e, inner) || e.Error() != "b: timeout" {
		t.Errorf("%v", e)
	}
}

func TestARedirectWithinTheSameAddressIsFollowed(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/v1" {
			http.Redirect(w, r, "/v1/", http.StatusTemporaryRedirect)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c, _ := recording(New("test"))
	out, err := c.Post(context.Background(), srv.URL+"/v1", nil, []byte(`{}`))
	if err != nil || string(out) != `{"ok":true}` || hits.Load() != 2 {
		t.Fatalf("%q %v after %d requests", out, err, hits.Load())
	}
}

func TestARedirectToAnotherAddressIsNotFollowedAndTheKeyStaysHome(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer other.Close()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c, slept := recording(New("test"))
	_, err := c.Post(context.Background(), srv.URL+"/v1/messages", map[string]string{"x-api-key": "secret-key"}, []byte(`{}`))
	e := asHTTPError(t, err)
	otherHost := strings.TrimPrefix(other.URL, "http://")
	if e.Kind != KindBadRequest || e.Status != http.StatusTemporaryRedirect || !strings.Contains(e.Message, otherHost) || !strings.Contains(e.Message, "not followed") {
		t.Errorf("error %+v", e)
	}
	if elsewhere.Load() != 0 {
		t.Errorf("the other server was called %d times", elsewhere.Load())
	}
	if calls.Load() != 1 || len(*slept) != 0 {
		t.Errorf("a redirect is not retried: %d calls, waits %v", calls.Load(), *slept)
	}
	if strings.Contains(e.Message, "secret-key") {
		t.Errorf("the message has the key: %q", e.Message)
	}
}

func TestARedirectToPlainHTTPOnTheSameHostIsNotFollowed(t *testing.T) {
	// A TLS server that sends the call to http:// on the same host:port is another scheme, so another address.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/downgraded", http.StatusFound)
	}))
	defer srv.Close()
	c, _ := recording(New("test"))
	c.HTTP = srv.Client()
	c.HTTP.CheckRedirect = sameOrigin
	_, err := c.Post(context.Background(), srv.URL+"/v1", map[string]string{"Authorization": "Bearer k"}, []byte(`{}`))
	e := asHTTPError(t, err)
	if e.Kind != KindBadRequest || !strings.Contains(e.Message, "redirected the call to http://") {
		t.Errorf("error %+v", e)
	}
}

func TestRedirectsWithinOneAddressStopAfterThree(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/again", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c, _ := recording(New("test"))
	_, err := c.Post(context.Background(), srv.URL+"/v1", nil, []byte(`{}`))
	e := asHTTPError(t, err)
	if e.Kind != KindBadRequest || hits.Load() != 4 {
		t.Errorf("error %+v after %d requests", e, hits.Load())
	}
}

func TestAnAnswerOfTheRedirectClassWithoutAPlaceToGoIsReportedToo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusMultipleChoices) }))
	defer srv.Close()
	c, _ := recording(New("test"))
	_, err := c.Get(context.Background(), srv.URL, nil, 0)
	if e := asHTTPError(t, err); e.Kind != KindBadRequest || e.Status != http.StatusMultipleChoices || !strings.Contains(e.Message, "somewhere else") {
		t.Errorf("error %+v", e)
	}
}
