// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qemu

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for QEMU: given `-fake-qemu=MODE` it behaves like a QEMU that is asked for QMP on a
// loopback socket, in the way MODE says, and never runs the tests. (The run keeps no environment from the caller, so the mode
// travels as an argument.)
func TestMain(m *testing.M) {
	for _, a := range os.Args {
		if mode, ok := strings.CutPrefix(a, "-fake-qemu="); ok {
			os.Exit(fakeQEMU_(mode))
		}
	}
	os.Exit(m.Run())
}

// fakeQEMU_ plays QEMU: it finds `-qmp tcp:127.0.0.1:PORT,...`, listens there, greets, and exits when it is told to quit.
// Modes: "ok"; "late" (starts listening after a while); "fail" (exits 3 with a complaint, never listening); "hang" (listens,
// greets and never quits); "stdio" (refuses a QMP that is not on a socket, as QEMU for Windows fails to serve one on a pipe).
func fakeQEMU_(mode string) int {
	spec := ""
	for i, a := range os.Args {
		if a == "-qmp" && i+1 < len(os.Args) {
			spec = os.Args[i+1]
		}
	}
	switch {
	case mode == "fail":
		fmt.Fprintln(os.Stderr, "qemu: -accel whpx: Failed to create partition")
		return 3
	case !strings.HasPrefix(spec, "tcp:127.0.0.1:"):
		fmt.Fprintln(os.Stderr, "the fake QEMU got -qmp", spec)
		return 4
	}
	addr := strings.TrimPrefix(strings.SplitN(spec, ",", 2)[0], "tcp:")
	if mode == "late" {
		time.Sleep(400 * time.Millisecond)
	}
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp4", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 5
	}
	conn, err := l.Accept()
	if err != nil {
		return 6
	}
	fmt.Fprintln(conn, `{"QMP": {"version": {}, "capabilities": []}}`)
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		if strings.Contains(sc.Text(), `"quit"`) && mode != "hang" {
			fmt.Fprintln(conn, `{"return": {}}`)
			return 0
		}
	}
	return 7
}

func TestAScriptForQMPOnStandardInputIsMovedToALoopbackSocket(t *testing.T) {
	args := []string{"-accel", "tcg", "-machine", "q35", "-S", "-qmp", "stdio"}
	got := qmpOverTCP(args, 4242)
	if want := []string{"-accel", "tcg", "-machine", "q35", "-S", "-qmp", "tcp:127.0.0.1:4242,server=on,wait=off"}; !slices.Equal(got, want) {
		t.Errorf("got %v", got)
	}
	if args[len(args)-1] != "stdio" {
		t.Error("the arguments given were changed")
	}
	if same := qmpOverTCP([]string{"-S", "-display", "none"}, 1); !slices.Equal(same, []string{"-S", "-display", "none"}) {
		t.Errorf("without stdio nothing changes: %v", same)
	}
	for args, want := range map[string]bool{"-qmp stdio": true, "-S -qmp stdio -m 128": true, "-qmp tcp:127.0.0.1:1": false, "stdio": false, "-serial stdio": false, "-qmp": false, "": false} {
		if got := usesQMPStdio(strings.Fields(args)); got != want {
			t.Errorf("%q: %v", args, got)
		}
	}
}

func fakeRun(t *testing.T, mode string) (RunResult, time.Duration, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	began := time.Now()
	res, err := runQMPScript(ctx, os.Args[0], quitScript, []string{"-fake-qemu=" + mode, "-accel", "tcg", "-qmp", "stdio"})
	return res, time.Since(began), err
}

func TestTheScriptReachesQEMUOnTheSocketAndQEMUQuits(t *testing.T) {
	res, took, err := fakeRun(t, "ok")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if took > 3*time.Second {
		t.Errorf("it took %s", took)
	}
}

func TestAQEMUThatListensLateIsWaitedFor(t *testing.T) {
	res, took, err := fakeRun(t, "late")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if took < 350*time.Millisecond {
		t.Errorf("it did not wait for QEMU to listen: %s", took)
	}
}

func TestAQEMUThatDiesBeforeItListensIsReportedByItsExitStatusAndWords(t *testing.T) {
	res, took, err := fakeRun(t, "fail")
	if err != nil || res.ExitCode != 3 || !strings.Contains(res.Stderr, "Failed to create partition") {
		t.Fatalf("%+v %v", res, err)
	}
	if took > 3*time.Second {
		t.Errorf("a QEMU that is gone is not waited for: %s", took)
	}
}

func TestAQEMUThatNeverQuitsEndsWithTheContextAndIsStopped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	began := time.Now()
	_, err := runQMPScript(ctx, os.Args[0], quitScript, []string{"-fake-qemu=hang", "-qmp", "stdio"})
	if err == nil || ctx.Err() == nil {
		t.Fatalf("the run ended with %v while the context was %v", err, ctx.Err())
	}
	if took := time.Since(began); took > 4*time.Second {
		t.Errorf("it took %s to give up", took)
	}
}

func TestAProgramThatCannotBeStartedIsAnError(t *testing.T) {
	if _, err := runQMPScript(context.Background(), "/no/such/qemu", quitScript, []string{"-qmp", "stdio"}); err == nil {
		t.Error("no error")
	}
}
