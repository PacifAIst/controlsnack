// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package browser

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits of what the model may ask for.
const (
	maxURLBytes      = 2048
	maxSelectorRunes = 500
	// MaxTypeRunes is the longest text one request types into a page.
	MaxTypeRunes = 4000
)

// CheckURL accepts the addresses the browser tool opens: http, https and file, nothing else (javascript:, data:, chrome:,
// devtools: and the rest are refused here as well as by the policy). It returns the address as the browser should be
// given it.
func CheckURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return "", fail(KindInvalid, "url is empty")
	case len(raw) > maxURLBytes:
		return "", fail(KindInvalid, "url is longer than %d bytes", maxURLBytes)
	case !utf8.ValidString(raw):
		return "", fail(KindInvalid, "url is not valid UTF-8")
	}
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", fail(KindInvalid, "url contains a space or a control character; write it percent-encoded")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fail(KindInvalid, "url is not valid: %v", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Hostname() == "" {
			return "", fail(KindInvalid, "url has no host")
		}
		if u.User != nil {
			return "", fail(KindInvalid, "url must not carry a user name or a password")
		}
	case "file":
		if u.Host != "" && u.Host != "localhost" {
			return "", fail(KindInvalid, "a file url has no host (file:///path)")
		}
		if u.Path == "" {
			return "", fail(KindInvalid, "a file url needs a path (file:///path)")
		}
	case "":
		return "", fail(KindInvalid, "url has no scheme; write http://, https:// or file://")
	default:
		return "", fail(KindInvalid, "scheme %q is not allowed; only http, https and file are", u.Scheme)
	}
	return raw, nil
}

// CheckSelector accepts a CSS selector a person could have written: bounded, one line, no control characters.
func CheckSelector(sel string) error {
	if strings.TrimSpace(sel) == "" {
		return fail(KindInvalid, "the selector is empty")
	}
	if utf8.RuneCountInString(sel) > maxSelectorRunes {
		return fail(KindInvalid, "the selector is longer than %d characters", maxSelectorRunes)
	}
	if !utf8.ValidString(sel) {
		return fail(KindInvalid, "the selector is not valid UTF-8")
	}
	for _, r := range sel {
		if unicode.IsControl(r) {
			return fail(KindInvalid, "the selector contains a control character")
		}
	}
	return nil
}

// CheckText accepts text for a field: valid UTF-8, line breaks and tabs, no other control characters, bounded.
func CheckText(text string) error {
	if !utf8.ValidString(text) {
		return fail(KindInvalid, "text is not valid UTF-8")
	}
	if utf8.RuneCountInString(text) > MaxTypeRunes {
		return fail(KindInvalid, "text is longer than %d characters", MaxTypeRunes)
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return fail(KindInvalid, "text contains a control character (U+%04X)", r)
		}
	}
	return nil
}
