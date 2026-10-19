// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"errors"

	"github.com/PacifAIst/controlsnack/internal/agent/browser"
	"github.com/PacifAIst/controlsnack/internal/agent/server"
	"github.com/PacifAIst/controlsnack/internal/protocol"
)

// Browser serves `browser` requests (PLAN.md section 8): the guest's Chromium, tab by tab.
type Browser struct{ Service *browser.Service }

// NewBrowser returns the handler over a browser backend.
func NewBrowser(b browser.Backend) Browser { return Browser{Service: browser.NewService(b)} }

// Handle answers one `browser` request.
func (h Browser) Handle(ctx context.Context, req protocol.Frame) (any, []byte, error) {
	var r protocol.BrowserReq
	if err := protocol.DecodeBody(req, &r); err != nil {
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "%v", err)
	}
	res, err := h.Service.Do(ctx, r)
	if err != nil {
		return nil, nil, browserError(ctx, err)
	}
	return res, nil, nil
}

// browserError turns what the browser service said into the protocol's error codes.
func browserError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var be *browser.Error
	if errors.As(err, &be) {
		switch be.Kind {
		case browser.KindInvalid:
			return server.Fail(protocol.CodeInvalidArgs, "%s", be.Msg)
		case browser.KindNotFound:
			return server.Fail(protocol.CodeNotFound, "%s", be.Msg)
		case browser.KindStale:
			return server.Fail(protocol.CodeStaleElement, "%s", be.Msg)
		case browser.KindUnsupported:
			return server.Fail(protocol.CodeUnsupported, "%s", be.Msg)
		case browser.KindTimeout:
			return server.Fail(protocol.CodeTimeout, "%s", be.Msg)
		}
		return server.Fail(protocol.CodeInternal, "%s", be.Msg)
	}
	return server.Fail(protocol.CodeInternal, "browser: %v", err)
}
