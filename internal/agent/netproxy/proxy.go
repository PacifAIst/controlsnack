// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Package netproxy is the guest's way out when the session allows a network: an HTTP proxy on the loopback address that
// the browser and command-line programs use, whose every connection is a multiplexed stream to the host. The guest has
// no network device and resolves no names; the host decides, connects and resolves (PLAN.md section 7.8).
package netproxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/PacifAIst/controlsnack/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack/internal/platform"
)

// Port is where the proxy listens, on the guest's loopback (127.0.0.1), never on an address another machine could reach.
const Port = 3128

// Limits and timing of the proxy.
const (
	maxHeaderBytes = 64 << 10
	// DefaultMaxConns bounds the connections open at once: the multiplexed port is one pipe, and a page that opens
	// hundreds of connections must not starve the others.
	DefaultMaxConns = 128
	// DefaultIdle is how long a connection may be silent before it is closed.
	DefaultIdle = 10 * time.Minute
	// half-close grace: after one direction of a tunnel has ended, the other gets this long to finish.
	halfCloseGrace = 3 * time.Second
	maxReasonRunes = 300
)

// Dialer opens a connection to host:port through the host.
type Dialer func(ctx context.Context, host string, port int) (net.Conn, error)

// Proxy is the HTTP proxy. The zero value is not usable: set Dial.
type Proxy struct {
	Dial     Dialer
	MaxConns int
	Idle     time.Duration
	Log      *slog.Logger

	active atomic.Int32
	srv    *http.Server
	rp     *httputil.ReverseProxy
}

// New returns a proxy that connects with dial.
func New(dial Dialer, log *slog.Logger) *Proxy {
	p := &Proxy{Dial: dial, MaxConns: DefaultMaxConns, Idle: DefaultIdle, Log: log}
	if p.Log == nil {
		p.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.Header.Del("Proxy-Connection")
			pr.Out.Header.Del("Proxy-Authorization")
			pr.Out.Close = true // Connection: close, as the plan says: one connection for each request
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				host, port, err := splitHostPort(addr, 80)
				if err != nil {
					return nil, err
				}
				return p.Dial(ctx, host, port)
			},
			DisableKeepAlives:     true,
			DisableCompression:    true, // what the server sends is what the client gets
			ResponseHeaderTimeout: 2 * time.Minute,
			ExpectContinueTimeout: time.Second,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) { p.fail(w, err) },
	}
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second, MaxHeaderBytes: maxHeaderBytes}
	return p
}

// Run listens on the loopback address and serves until the context ends or the listener fails.
func (p *Proxy) Run(ctx context.Context) error {
	l, err := platform.ListenLoopback(ctx, Port)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = p.Close() })
	defer stop()
	return p.Serve(l)
}

// Serve serves the proxy on l until it is closed.
func (p *Proxy) Serve(l net.Listener) error {
	err := p.srv.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the proxy and closes the connections it has.
func (p *Proxy) Close() error { return p.srv.Close() }

// Active is how many connections are open now.
func (p *Proxy) Active() int { return int(p.active.Load()) }

// ServeHTTP answers a request made to the proxy.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err != nil || !net.ParseIP(host).IsLoopback() {
		plain(w, http.StatusForbidden, "The proxy serves programs of this machine only")
		return
	}
	if int(p.active.Add(1)) > p.MaxConns {
		p.active.Add(-1)
		plain(w, http.StatusServiceUnavailable, "Too many connections are open; try again in a moment")
		return
	}
	defer p.active.Add(-1)

	switch {
	case r.Method == http.MethodConnect:
		p.connect(w, r)
	case r.URL.IsAbs() && strings.EqualFold(r.URL.Scheme, "http"):
		p.forward(w, r)
	case r.URL.IsAbs():
		plain(w, http.StatusBadRequest, "Only http addresses are forwarded; https goes through CONNECT")
	default:
		plain(w, http.StatusBadRequest, "This is a proxy: send the request with the full address (http://host/path), or CONNECT")
	}
}

// forward relays a plain HTTP request.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request) {
	if r.URL.Hostname() == "" {
		plain(w, http.StatusBadRequest, "The address has no host")
		return
	}
	p.Log.Debug("http", "host", r.URL.Host)
	p.rp.ServeHTTP(w, r)
}

// connect tunnels a connection for CONNECT host:port.
func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	host, port, err := splitHostPort(r.Host, 0)
	if err != nil || port == 0 {
		plain(w, http.StatusBadRequest, "CONNECT needs host:port")
		return
	}
	up, err := p.Dial(r.Context(), host, port)
	if err != nil {
		p.Log.Debug("connect refused", "host", host, "port", port, "error", err)
		p.fail(w, err)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = up.Close()
		plain(w, http.StatusInternalServerError, "The proxy cannot take over the connection")
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		_ = up.Close()
		return
	}
	// Whatever the client sent after its request line and headers is the start of the tunnel's bytes.
	rd := buf.Reader // the read half: the writer half has a Buffered of its own
	if n := rd.Buffered(); n > 0 {
		pending, _ := rd.Peek(n)
		if _, err := up.Write(pending); err != nil {
			_ = client.Close()
			_ = up.Close()
			return
		}
	}
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = client.Close()
		_ = up.Close()
		return
	}
	p.Log.Debug("tunnel", "host", host, "port", port)
	splice(client, up, p.Idle)
}

// fail answers a request whose connection could not be made, with a sentence a model can read.
func (p *Proxy) fail(w http.ResponseWriter, err error) {
	var denied *DeniedError
	switch {
	case errors.As(err, &denied):
		w.Header().Set("X-ControlSnack-Reason", headerSafe(denied.Reason))
		plain(w, http.StatusForbidden, "Denied: "+clean(denied.Reason))
	case errors.Is(err, ErrDisabled):
		w.Header().Set("X-ControlSnack-Reason", headerSafe(DisabledMessage))
		plain(w, http.StatusForbidden, DisabledMessage)
	case errors.Is(err, ErrBridgeDown):
		plain(w, http.StatusBadGateway, "The network bridge to the host is not connected")
	case errors.Is(err, context.Canceled):
		plain(w, http.StatusBadGateway, "The request was canceled")
	default:
		plain(w, http.StatusBadGateway, "Could not connect: "+clean(err.Error()))
	}
}

// plain writes a plain-text answer.
func plain(w http.ResponseWriter, code int, text string) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "close")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, text+"\n")
}

// clean makes a reason from the host fit to show: one line, no control characters, bounded.
func clean(s string) string { return textclean.Line(s, maxReasonRunes) }

// headerSafe keeps a reason to printable ASCII for a header value.
func headerSafe(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range clean(s) {
		if r >= 0x20 && r < 0x7f {
			out = append(out, r)
		}
	}
	return strings.Join(strings.Fields(string(out)), " ")
}

// splitHostPort splits "host:port", "[v6]:port" or, when def is not zero and there is no port, host alone.
func splitHostPort(addr string, def int) (string, int, error) {
	host, ps, err := net.SplitHostPort(addr)
	if err != nil {
		if def == 0 {
			return "", 0, err
		}
		host, ps = strings.Trim(addr, "[]"), strconv.Itoa(def)
	}
	port, err := strconv.Atoi(ps)
	if err != nil || port < 0 || port > 65535 || (port == 0 && def != 0) {
		return "", 0, errors.New("bad port")
	}
	if host == "" || strings.ContainsAny(host, " \t\r\n\x00/\\@") {
		return "", 0, errors.New("bad host")
	}
	// A request line may carry a URL whose host has user information; the transport already took it apart.
	if _, err := url.Parse("http://" + net.JoinHostPort(host, "80")); err != nil {
		return "", 0, errors.New("bad host")
	}
	return host, port, nil
}
