// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package x11

import (
	"context"
	"fmt"

	"github.com/jezek/xgb/randr"
)

// MonitorInfo is one display of the screen, in screen pixels.
type MonitorInfo struct {
	ID         int
	X, Y, W, H int
	Primary    bool
}

// Monitors lists the active displays (RandR 1.5). A server without RandR, or without monitors, is one display the
// size of the screen.
func (c *Conn) Monitors(ctx context.Context) ([]MonitorInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.randrOK() {
		if r, err := randr.GetMonitors(c.c, c.root, true).Reply(); err == nil && len(r.Monitors) > 0 {
			out := make([]MonitorInfo, 0, len(r.Monitors))
			for i, m := range r.Monitors {
				out = append(out, MonitorInfo{ID: i, X: int(m.X), Y: int(m.Y), W: int(m.Width), H: int(m.Height), Primary: m.Primary})
			}
			markPrimary(out)
			return out, nil
		}
	}
	w, h, err := c.Size()
	if err != nil {
		return nil, fmt.Errorf("reading the screen size: %w", err)
	}
	return []MonitorInfo{{ID: 0, W: w, H: h, Primary: true}}, nil
}

// randrOK enables the RandR extension once and reports whether the server speaks version 1.5, which GetMonitors needs.
func (c *Conn) randrOK() bool {
	c.randrOnce.Do(func() {
		if err := randr.Init(c.c); err != nil {
			return
		}
		v, err := randr.QueryVersion(c.c, 1, 5).Reply()
		c.randr15 = err == nil && (v.MajorVersion > 1 || (v.MajorVersion == 1 && v.MinorVersion >= 5))
	})
	return c.randr15
}

// markPrimary makes the first monitor primary when RandR flags none: a server like the guest's, with one virtual
// output and no primary set, still has a main display.
func markPrimary(mons []MonitorInfo) {
	for _, m := range mons {
		if m.Primary {
			return
		}
	}
	if len(mons) > 0 {
		mons[0].Primary = true
	}
}
