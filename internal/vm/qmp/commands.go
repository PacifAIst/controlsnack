// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qmp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Status is the reply to query-status.
type Status struct {
	Status     string `json:"status"` // running, paused, prelaunch, shutdown, internal-error, ...
	Running    bool   `json:"running"`
	Singlestep bool   `json:"singlestep"`
}

// QueryStatus asks for the run state of the VM.
func (c *Client) QueryStatus(ctx context.Context) (Status, error) {
	var s Status
	err := c.Execute(ctx, "query-status", nil, &s)
	return s, err
}

// Quit asks QEMU to exit. QEMU may close the connection before it answers, which counts as success.
func (c *Client) Quit(ctx context.Context) error {
	err := c.Execute(ctx, "quit", nil, nil)
	if err != nil && c.Err() != nil && !errors.Is(err, ErrClosed) && !errors.Is(err, ctx.Err()) {
		return nil
	}
	return err
}

// SystemPowerdown asks the guest to shut down as if the power button were pressed.
func (c *Client) SystemPowerdown(ctx context.Context) error {
	return c.Execute(ctx, "system_powerdown", nil, nil)
}

// Stop pauses the VM.
func (c *Client) Stop(ctx context.Context) error { return c.Execute(ctx, "stop", nil, nil) }

// Cont resumes a paused VM (and starts one that was started with -S).
func (c *Client) Cont(ctx context.Context) error { return c.Execute(ctx, "cont", nil, nil) }

// Screendump makes QEMU write the current screen to path, as "png" or "ppm". The path is on the host, so the
// caller keeps it inside the session directory; it must be absolute and plain.
func (c *Client) Screendump(ctx context.Context, path, format string) error {
	switch {
	case format != "png" && format != "ppm":
		return fmt.Errorf("screendump format %q is not png or ppm", format)
	case path == "" || !isAbs(path) || strings.ContainsAny(path, "\x00\r\n"):
		return fmt.Errorf("screendump path %q is not a plain absolute path", path)
	}
	return c.Execute(ctx, "screendump", map[string]string{"filename": path, "format": format}, nil)
}

// InputSendEvent injects input into the guest's virtual devices (the fallback when the agent is down).
func (c *Client) InputSendEvent(ctx context.Context, events []InputEvent) error {
	if len(events) == 0 {
		return errors.New("no input events")
	}
	if len(events) > 256 {
		return fmt.Errorf("%d input events in one call; the limit is 256", len(events))
	}
	return c.Execute(ctx, "input-send-event", map[string]any{"events": events}, nil)
}

// isAbs accepts unix and Windows drive paths on every OS.
func isAbs(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	return len(p) >= 3 && (p[0]|0x20 >= 'a' && p[0]|0x20 <= 'z') && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

// IsConnectionEnd reports whether err means the QMP connection is gone (QEMU exited or the client was closed).
func IsConnectionEnd(err error) bool {
	return errors.Is(err, ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe)
}
