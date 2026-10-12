// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package killswitch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/platform"
	"github.com/PacifAIst/controlsnack-local/internal/safety/secret"
)

// ControlKillPath is the endpoint the viewer's server mounts for `controlsnack kill`. It accepts POST with the
// session's kill token as a bearer token.
const ControlKillPath = "/control/kill"

// Client is what `controlsnack kill` uses to stop a session running in another process.
type Client interface {
	// Kill asks the active session to die and reports once QEMU is gone. It is safe to call when nothing is
	// running.
	Kill(ctx context.Context, w io.Writer) error
}

// ControlClient is the Client that talks to the session through its lock file.
type ControlClient struct {
	reg *Registry
	// HTTP is used for the control request; it never uses a proxy.
	HTTP *http.Client
	// AckTimeout is how long to wait for the session to acknowledge (PLAN.md section 6.8: 1 s) before the
	// processes are ended directly.
	AckTimeout time.Duration
	// GoneTimeout is how long to wait for QEMU to disappear after the acknowledgement.
	GoneTimeout time.Duration
}

// NewClient returns a client that finds the session through the lock file in the directory sessionsDir() names.
// The lookup happens when the command runs, so `kill` works even if other parts of the configuration are broken.
func NewClient(sessionsDir func() (string, error)) Client {
	return &lazyClient{dir: sessionsDir}
}

type lazyClient struct{ dir func() (string, error) }

func (c *lazyClient) Kill(ctx context.Context, w io.Writer) error {
	dir, err := c.dir()
	if err != nil {
		return err
	}
	return NewControlClient(NewRegistry(dir, nil)).Kill(ctx, w)
}

// NewControlClient creates a ControlClient over a registry.
func NewControlClient(reg *Registry) *ControlClient {
	return &ControlClient{
		reg:         reg,
		HTTP:        &http.Client{Transport: &http.Transport{Proxy: nil}},
		AckTimeout:  time.Second,
		GoneTimeout: 3 * time.Second,
	}
}

// Kill stops the active session.
func (c *ControlClient) Kill(ctx context.Context, w io.Writer) error {
	entry, err := c.reg.Read()
	if err != nil {
		return err
	}
	if entry == nil {
		fmt.Fprintln(w, "no session is running")
		return nil
	}
	if c.reg.IsStale(entry) {
		reaped, err := c.reg.ReapStale()
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "session %s is no longer running; cleaned up its lock", entry.SessionID)
		if reaped != nil && reaped.KilledQEMU {
			fmt.Fprint(w, " and ended its orphaned QEMU")
		}
		fmt.Fprintln(w)
		return nil
	}

	start := time.Now()
	ackErr := c.askToDie(ctx, entry)
	if ackErr == nil {
		if c.waitGone(ctx, entry) {
			fmt.Fprintf(w, "killed session %s (QEMU gone after %d ms)\n", entry.SessionID, time.Since(start).Milliseconds())
			return nil
		}
		fmt.Fprintf(w, "session %s acknowledged but QEMU is still running; ending the processes directly\n", entry.SessionID)
	} else {
		fmt.Fprintf(w, "session %s did not answer (%v); ending the processes directly\n", entry.SessionID, ackErr)
	}

	// Fallback: end QEMU, then the controller, but only the very processes the lock recorded.
	var errs []error
	if entry.QEMUPID > 0 && platform.SameProcess(c.reg.proc, entry.QEMUPID, entry.QEMUStarted) {
		if err := c.reg.proc.Kill(entry.QEMUPID); err != nil && !errors.Is(err, platform.ErrNoSuchProcess) {
			errs = append(errs, fmt.Errorf("QEMU (pid %d): %w", entry.QEMUPID, err))
		}
	}
	if platform.SameProcess(c.reg.proc, entry.ControllerPID, entry.ControllerStarted) {
		if err := c.reg.proc.Kill(entry.ControllerPID); err != nil && !errors.Is(err, platform.ErrNoSuchProcess) {
			errs = append(errs, fmt.Errorf("controller (pid %d): %w", entry.ControllerPID, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("could not end the session: %w", errors.Join(errs...))
	}
	if _, err := c.reg.ReapStale(); err != nil {
		return err
	}
	fmt.Fprintf(w, "killed session %s (%d ms)\n", entry.SessionID, time.Since(start).Milliseconds())
	return nil
}

// askToDie sends the authenticated kill request and waits for the acknowledgement. The token is only ever sent
// to a loopback address: a tampered lock file must not be able to make it travel.
func (c *ControlClient) askToDie(ctx context.Context, e *Entry) error {
	target, err := controlURL(e.ControlURL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, c.AckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+e.KillToken)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the session answered %s", resp.Status)
	}
	return nil
}

// controlURL turns the recorded control URL into the kill endpoint, refusing anything but plain http to a loopback
// IP address.
func controlURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return "", fmt.Errorf("the lock holds no usable control URL (%q)", raw)
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("refusing to send the kill token to the non-loopback address %q", u.Host)
	}
	u.Path = ControlKillPath
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return u.String(), nil
}

// waitGone polls until QEMU (if any) has gone or the timeout passes.
func (c *ControlClient) waitGone(ctx context.Context, e *Entry) bool {
	if e.QEMUPID <= 0 {
		return true
	}
	deadline := time.Now().Add(c.GoneTimeout)
	for {
		if !platform.SameProcess(c.reg.proc, e.QEMUPID, e.QEMUStarted) {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ControlHandler returns the HTTP handler for ControlKillPath. It accepts POST with the correct bearer token only,
// fires the kill switch, and answers once QEMU is gone (or after at most 800 ms), so the client's one-second
// acknowledgement window is always met.
func ControlHandler(k *Killer, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		const prefix = "Bearer "
		auth := r.Header.Get("Authorization")
		if len(auth) <= len(prefix) || auth[:len(prefix)] != prefix || !secret.Equal(auth[len(prefix):], token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		start := time.Now()
		k.TriggerAsync(SourceCLI, "controlsnack kill")
		gone := false
		select {
		case <-k.QEMUGone():
			gone = true
		case <-time.After(800 * time.Millisecond):
		case <-r.Context().Done():
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"killed": true, "qemu_gone": gone, "latency_ms": time.Since(start).Milliseconds()})
	})
}
