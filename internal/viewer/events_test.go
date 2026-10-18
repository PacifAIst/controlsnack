// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer

import (
	"testing"
	"time"
)

func TestEventsOfOneKindAreLimitedAndCounted(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	var got []Event
	e := newEvents(func() time.Time { return now }, func(ev Event) { got = append(got, ev) })

	e.send(EventRefused, "one")
	e.send(EventRefused, "two")
	e.send(EventRefused, "three")
	e.send(EventAuthDenied, "other kind")
	if len(got) != 2 || got[0].Detail != "one" || got[1].Kind != EventAuthDenied {
		t.Fatalf("events: %+v", got)
	}
	now = now.Add(eventGap)
	e.send(EventRefused, "four")
	if len(got) != 3 || got[2].Detail != "four" || got[2].Suppressed != 2 {
		t.Fatalf("after the gap: %+v", got)
	}
	now = now.Add(eventGap)
	e.send(EventRefused, "five")
	if got[3].Suppressed != 0 {
		t.Errorf("the count was not reset: %+v", got[3])
	}
}

func TestTheTripwireAndALoginAreNeverLeftOut(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	n := 0
	e := newEvents(func() time.Time { return now }, func(Event) { n++ })
	for range 5 {
		e.send(EventTripwire, "x")
		e.send(EventAuth, "x")
	}
	if n != 10 {
		t.Errorf("%d of 10 events were delivered", n)
	}
}

func TestAClockThatGoesBackDoesNotSilenceTheLog(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	n := 0
	e := newEvents(func() time.Time { return now }, func(Event) { n++ })
	e.send(EventRefused, "x")
	now = now.Add(-time.Hour)
	e.send(EventRefused, "y")
	if n != 2 {
		t.Errorf("%d delivered", n)
	}
}

func TestNoReceiverIsFine(t *testing.T) {
	newEvents(time.Now, nil).send(EventRefused, "x")
}
