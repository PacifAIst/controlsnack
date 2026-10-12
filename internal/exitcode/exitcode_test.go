// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package exitcode

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestOf(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, OK},
		{"plain error", errors.New("boom"), Failure},
		{"usage", Usagef("bad flag %q", "--x"), Usage},
		{"environment", New(Environment, errors.New("no qemu")), Environment},
		{"image missing", New(ImageMissing, errors.New("run image build")), ImageMissing},
		{"coded error survives wrapping", fmt.Errorf("outer: %w", New(Environment, errors.New("inner"))), Environment},
		{"pending stub", Pending("P3-T08"), NotImplemented},
		{"bare ErrNotImplemented", ErrNotImplemented, NotImplemented},
		{"wrapped ErrNotImplemented", fmt.Errorf("x: %w", ErrNotImplemented), NotImplemented},
		{"canceled context means killed", context.Canceled, Killed},
		{"deadline means timeout", fmt.Errorf("session: %w", context.DeadlineExceeded), Timeout},
		{"explicit code wins over wrapped cancel", New(Usage, context.Canceled), Usage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Of(tt.err); got != tt.want {
				t.Fatalf("Of(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestNewKeepsNil(t *testing.T) {
	if New(Failure, nil) != nil {
		t.Fatal("New(code, nil) must stay nil, or callers would turn success into failure")
	}
}

func TestPendingNamesTheTask(t *testing.T) {
	err := Pending("P2-T07")
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatal("Pending must wrap ErrNotImplemented")
	}
	if got := err.Error(); got != "not implemented yet (planned for P2-T07)" {
		t.Fatalf("message = %q", got)
	}
}

func TestCodesMatchThePlan(t *testing.T) {
	// PLAN.md P1-T05: 0 ok, 1 error, 2 usage, 3 environment, 4 image missing, 5 killed, 6 timeout, 70 not implemented.
	want := map[string]int{"OK": OK, "Failure": Failure, "Usage": Usage, "Environment": Environment,
		"ImageMissing": ImageMissing, "Killed": Killed, "Timeout": Timeout, "NotImplemented": NotImplemented}
	exp := map[string]int{"OK": 0, "Failure": 1, "Usage": 2, "Environment": 3, "ImageMissing": 4, "Killed": 5, "Timeout": 6, "NotImplemented": 70}
	for k, v := range exp {
		if want[k] != v {
			t.Errorf("%s = %d, want %d", k, want[k], v)
		}
	}
}
