// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package a11y

import (
	"context"
	"fmt"
	"os"
)

// Dial connects to the accessibility bus of the desktop session: it asks the session bus (DBUS_SESSION_BUS_ADDRESS)
// for the address of the accessibility bus, which is a bus of its own. AT_SPI_BUS_ADDRESS, when set, names that bus
// directly.
func Dial(ctx context.Context) (Bus, error) {
	if addr := os.Getenv("AT_SPI_BUS_ADDRESS"); addr != "" {
		return DialAddress(ctx, addr)
	}
	session := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if session == "" {
		return nil, fmt.Errorf("%w: DBUS_SESSION_BUS_ADDRESS is not set", ErrNoBus)
	}
	return DialSession(ctx, session)
}
