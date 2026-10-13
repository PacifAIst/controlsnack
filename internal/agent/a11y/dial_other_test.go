// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package a11y

import (
	"context"
	"errors"
	"testing"
)

func TestDialIsAStubOutsideLinux(t *testing.T) {
	if _, err := Dial(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("%v", err)
	}
}
