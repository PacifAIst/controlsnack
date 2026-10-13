// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package a11y

import (
	"context"
	"fmt"
)

// Dial has no backend here: the guest is always Linux (D-01), and AT-SPI is its accessibility service. The macOS and
// Windows accessibility APIs are not implemented.
func Dial(context.Context) (Bus, error) {
	return nil, fmt.Errorf("%w: accessibility is implemented for the Linux guest only", ErrUnsupported)
}
