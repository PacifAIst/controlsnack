// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// OverlaySize is the virtual size of the disk the image is built on; the sealed image is smaller (it only holds
// what was written), and sessions can grow to this size.
const OverlaySize = "12G"

// runQEMUImg runs qemu-img and returns its output as part of the error.
func runQEMUImg(ctx context.Context, img string, args ...string) error {
	cmd := exec.CommandContext(ctx, img, args...) //nolint:gosec // the qemu-img found by discovery
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg := strings.Join(strings.Fields(out.String()), " ")
		if len(msg) > 1024 {
			msg = msg[:1024] + "..."
		}
		return fmt.Errorf("qemu-img %s: %w: %s", args[0], err, msg)
	}
	return nil
}

// CreateOverlay makes a qcow2 file that stores only the changes to the backing image. The backing path must be
// absolute: qemu-img writes it into the overlay as given.
func CreateOverlay(ctx context.Context, img, backing, dest, size string) error {
	if !filepath.IsAbs(backing) {
		return fmt.Errorf("the backing image path %q is not absolute", backing)
	}
	args := []string{"create", "-f", "qcow2", "-F", "qcow2", "-b", backing, dest}
	if size != "" {
		args = append(args, size)
	}
	return runQEMUImg(ctx, img, args...)
}

// ConvertCompressed flattens src (an overlay and its backing chain) into one standalone, compressed qcow2 file.
func ConvertCompressed(ctx context.Context, img, src, dest string) error {
	return runQEMUImg(ctx, img, "convert", "-O", "qcow2", "-c", src, dest)
}
