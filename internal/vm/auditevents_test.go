// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/safety/audit"
)

func TestLaunchWritesTheAcceleratorDecisionsToARealAuditLog(t *testing.T) {
	dir := t.TempDir()
	log, err := audit.Open(dir, "sess-123", audit.Options{})
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, map[string]scenario{
		"kvm": {exitCode: 1, out: "Could not access KVM kernel module: Permission denied\nfailed to initialize kvm: Permission denied"},
		"tcg": {},
	})
	fwd := &events{}
	ev := &AuditEvents{Log: log, Forward: fwd}
	vm, err := Launch(context.Background(), opts(r, AccelKVM, ev))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vm.Kill() }()
	if ev.Err() != nil {
		t.Fatal(ev.Err())
	}
	if !strings.Contains(strings.Join(fwd.seq, ","), "selected,fallback") {
		t.Errorf("the forwarded listener hears about both: %v", fwd.seq)
	}
	_ = log.Close()

	rep, err := audit.Verify(log.Path())
	if err != nil || !rep.OK() || rep.Records != 2 {
		t.Fatalf("%+v %v", rep, err)
	}
	data, err := os.ReadFile(log.Path())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var sel, fb audit.Record
	if json.Unmarshal([]byte(lines[0]), &sel) != nil || json.Unmarshal([]byte(lines[1]), &fb) != nil {
		t.Fatal("unreadable log")
	}
	if sel.Kind != audit.KindLifecycle || sel.Event != audit.EventAccelSelected || sel.Actor != "system" {
		t.Errorf("%+v", sel)
	}
	var res AccelResult
	if err := json.Unmarshal(sel.Data, &res); err != nil || res.Chosen != "kvm" || res.Requested != "auto" {
		t.Errorf("accel_selected carries {requested, chosen, tried[]}: %s", sel.Data)
	}
	if fb.Event != audit.EventAccelFallback || !strings.Contains(fb.Reason, "exited with status 1") {
		t.Errorf("%+v", fb)
	}
	var f Fallback
	if err := json.Unmarshal(fb.Data, &f); err != nil || f.Used != "tcg" || f.From != "kvm" || !strings.Contains(f.Stderr, "Permission denied") {
		t.Errorf("accel_fallback carries {requested, used, reason, stderr}: %s", fb.Data)
	}
}

type failingLog struct{ n int }

func (l *failingLog) Append(audit.Record) (audit.Record, error) {
	l.n++
	return audit.Record{}, errors.New("disk full")
}

func TestAuditEventsRemembersAWriteError(t *testing.T) {
	l := &failingLog{}
	ev := &AuditEvents{Log: l}
	ev.AccelSelected(AccelResult{Chosen: "tcg"})
	ev.AccelFallback(Fallback{Used: "tcg"})
	if l.n != 2 || ev.Err() == nil || !strings.Contains(ev.Err().Error(), "disk full") {
		t.Errorf("%d %v", l.n, ev.Err())
	}
}
