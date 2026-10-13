// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package a11y

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// brokenBus is a fakeBus whose connection has died: asking for the applications fails.
type brokenBus struct {
	*fakeBus
	closed atomic.Bool
}

func (b *brokenBus) Apps(context.Context) ([]App, error) { return nil, errors.New("connection closed") }
func (b *brokenBus) Close() error                        { b.closed.Store(true); return nil }

func TestLazyBusDialsWhenFirstUsed(t *testing.T) {
	var dials atomic.Int32
	good := newFakeBus()
	good.apps = []App{{Name: "App", PID: 7}}
	l := NewLazyBus(func(context.Context) (Bus, error) { dials.Add(1); return good, nil })
	if dials.Load() != 0 {
		t.Error("it dials on use, not on creation")
	}
	for range 3 {
		if apps, err := l.Apps(context.Background()); err != nil || len(apps) != 1 {
			t.Fatalf("%v %v", apps, err)
		}
	}
	if _, err := l.Info(context.Background(), ref("x")); !errors.Is(err, ErrGone) {
		t.Errorf("calls reach the bus: %v", err)
	}
	if dials.Load() != 1 {
		t.Errorf("%d dials", dials.Load())
	}
}

func TestLazyBusTriesAgainAfterAFailedDial(t *testing.T) {
	var dials atomic.Int32
	l := NewLazyBus(func(context.Context) (Bus, error) {
		if dials.Add(1) == 1 {
			return nil, ErrNoBus
		}
		return newFakeBus(), nil
	})
	if _, err := l.Apps(context.Background()); !errors.Is(err, ErrNoBus) {
		t.Fatalf("%v", err)
	}
	if _, err := l.Children(context.Background(), ref("x")); err != nil && errors.Is(err, ErrNoBus) {
		t.Errorf("the bus came up since: %v", err)
	}
	if dials.Load() != 2 {
		t.Errorf("%d dials", dials.Load())
	}
}

func TestLazyBusReconnectsWhenTheConnectionBroke(t *testing.T) {
	broken := &brokenBus{fakeBus: newFakeBus()}
	good := newFakeBus()
	good.apps = []App{{Name: "Back", PID: 1}}
	var dials atomic.Int32
	l := NewLazyBus(func(context.Context) (Bus, error) {
		if dials.Add(1) == 1 {
			return broken, nil
		}
		return good, nil
	})
	apps, err := l.Apps(context.Background())
	if err != nil || len(apps) != 1 || apps[0].Name != "Back" {
		t.Fatalf("%v %v", apps, err)
	}
	if !broken.closed.Load() || dials.Load() != 2 {
		t.Errorf("the dead connection is closed (%v) and replaced (%d dials)", broken.closed.Load(), dials.Load())
	}
}

func TestLazyBusDoesNotReconnectForACanceledRequest(t *testing.T) {
	var dials atomic.Int32
	b := &brokenBus{fakeBus: newFakeBus()}
	l := NewLazyBus(func(context.Context) (Bus, error) { dials.Add(1); return b, nil })
	if _, err := l.Apps(context.Background()); err == nil {
		t.Fatal("a bus that stays broken stays an error")
	}
	if dials.Load() != 2 {
		t.Fatalf("one retry: %d dials", dials.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.Apps(ctx); err == nil || dials.Load() != 2 {
		t.Errorf("a canceled request is not a reason to reconnect: %v, %d dials", err, dials.Load())
	}
}

func TestLazyBusConcurrentUseDialsOnce(t *testing.T) {
	var dials atomic.Int32
	good := newFakeBus()
	l := NewLazyBus(func(context.Context) (Bus, error) { dials.Add(1); return good, nil })
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = l.Apps(context.Background())
			_, _ = l.Number(context.Background(), ref("x"))
		}()
	}
	wg.Wait()
	if dials.Load() != 1 {
		t.Errorf("%d dials", dials.Load())
	}
	if err := l.Close(); err != nil {
		t.Error(err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("closing twice: %v", err)
	}
}

func TestLazyBusDelegatesEverything(t *testing.T) {
	w := newWorld()
	l := NewLazyBus(func(context.Context) (Bus, error) { return w.bus, nil })
	svc := NewService(l, w.desktop, w.driver)
	res, err := svc.Tree(context.Background(), "", Options{})
	if err != nil || len(res.Entries) != 4 {
		t.Fatalf("%v %v", ids(res), err)
	}
	if _, err := svc.Act(context.Background(), "a2", res.Generation, OpClick, ""); err != nil {
		t.Error(err)
	}
	if _, err := svc.Act(context.Background(), "a4", res.Generation, OpSetText, "x"); err != nil {
		t.Error(err)
	}
	if _, err := svc.Act(context.Background(), "a4", res.Generation, OpFocus, ""); err != nil {
		t.Error(err)
	}
	if _, err := svc.Act(context.Background(), "a3", res.Generation, OpClick, ""); err != nil {
		t.Error(err)
	}
	if len(w.bus.done) != 1 || len(w.bus.setTexts) != 1 || len(w.bus.focused) != 1 {
		t.Errorf("done %v set %v focused %v", w.bus.done, w.bus.setTexts, w.bus.focused)
	}
}
