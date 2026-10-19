// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/PacifAIst/controlsnack/internal/safety/secret"
)

// CookieName is the session cookie of the viewer.
const CookieName = "cs_session"

// maxPending is how many login links can wait to be used at once. The launcher needs one, a second window one more.
const maxPending = 16

// tokenBytes is the size of every secret the viewer makes (login links, the cookie, the CSRF value).
const tokenBytes = 32

// outcome is what became of a login link.
type outcome int

const (
	linkOK       outcome = iota // good and now used
	linkUnknown                 // never issued
	linkExpired                 // issued, not used within its time
	linkConsumed                // used before: the tripwire
)

// auth holds the login links and the one cookie that is valid at a time.
type auth struct {
	mu      sync.Mutex
	now     func() time.Time
	ttl     time.Duration
	random  func(n int) (string, error)
	pending map[string]time.Time // hash of the link -> when it stops being good
	used    map[string]struct{}  // hash of the links that were used
	cookie  string               // the cookie of the window that logged in, "" until one has
	// life ends when a window logs in over another: whatever the old window had open (its socket) is stopped.
	life    context.Context
	endLife context.CancelFunc
}

func newAuth(now func() time.Time, ttl time.Duration, random func(n int) (string, error)) *auth {
	a := &auth{now: now, ttl: ttl, random: random, pending: map[string]time.Time{}, used: map[string]struct{}{}}
	a.life, a.endLife = context.WithCancel(context.Background())
	return a
}

// hash is what the tables keep: a link is looked up by its digest, so no comparison of the secret itself is made.
func hash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// issue makes a login link token. It is good once, for the time to live.
func (a *auth) issue() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.now()
	for k, exp := range a.pending { // links that nobody used are forgotten after a while
		if t.After(exp.Add(a.ttl)) {
			delete(a.pending, k)
		}
	}
	if len(a.pending) >= maxPending {
		return "", errors.New("too many login links are waiting to be used")
	}
	token, err := a.random(tokenBytes)
	if err != nil {
		return "", err
	}
	a.pending[hash(token)] = t.Add(a.ttl)
	return token, nil
}

// consume uses a login link. On success it makes the cookie of the window that logs in, which replaces the one before.
func (a *auth) consume(token string) (cookie string, out outcome, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	h := hash(token)
	if _, was := a.used[h]; was {
		return "", linkConsumed, nil
	}
	exp, ok := a.pending[h]
	if !ok {
		return "", linkUnknown, nil
	}
	delete(a.pending, h)
	if a.now().After(exp) {
		return "", linkExpired, nil
	}
	cookie, err = a.random(tokenBytes)
	if err != nil {
		return "", linkUnknown, err
	}
	a.used[h] = struct{}{}
	a.cookie = cookie
	a.endLife() // a window that was logged in before is out
	a.life, a.endLife = context.WithCancel(context.Background())
	return cookie, linkOK, nil
}

// valid reports whether the request carries the cookie of the window that is logged in, and the context that ends when
// that window is replaced (or the viewer closes).
func (a *auth) valid(r *http.Request) (context.Context, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cookie == "" || !secret.Equal(c.Value, a.cookie) {
		return nil, false
	}
	return a.life, true
}

// close ends the life of the window that is logged in.
func (a *auth) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.endLife()
	a.cookie = ""
}
