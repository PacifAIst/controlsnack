// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package prompt

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
)

func newConv(t testing.TB, mode backend.Mode, window, keep int) *Conversation {
	t.Helper()
	set := mustSet(t, actions.ProfileCompact)
	sys, err := System(Params{Nonce: testNonce, Width: 1024, Height: 640, MaxActions: 3, Vision: true, Mode: mode, Set: set})
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewConversation(sys.Text, Options{Backend: "ollama", Mode: mode, Set: set, Renderer: textRenderer(), ContextTokens: window, KeepTurns: keep})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var testImage = &backend.Image{MIME: backend.MIMEWebP, Data: []byte("RIFFxxxxWEBPfake")}

// obs makes the observation of a turn with a small tree.
func obs(turn int) Observation {
	o := planObservation()
	o.Turn, o.Results = turn, nil
	return o
}

func calls(names ...string) []parse.ToolCall {
	var out []parse.ToolCall
	for i, n := range names {
		out = append(out, parse.ToolCall{ID: fmt.Sprintf("call_%d", i+1), Name: n, Args: json.RawMessage(`{"x":1}`)})
	}
	return out
}

func results(names ...string) []Result {
	var out []Result
	for i, n := range names {
		out = append(out, Result{CallID: fmt.Sprintf("call_%d", i+1), Tool: n, Action: n + " {\"x\":1}", OK: true, Detail: "done"})
	}
	return out
}

// step plays one turn: the observation, the answer with native calls, and their results.
func step(t testing.TB, c *Conversation, turn int, names ...string) {
	t.Helper()
	c.Begin(obs(turn), testImage)
	if err := c.Assistant("reason "+fmt.Sprint(turn), calls(names...)); err != nil {
		t.Fatal(err)
	}
	if err := c.Results(results(names...), Limits{}); err != nil {
		t.Fatal(err)
	}
}

func dump(msgs []backend.Message) string {
	var b strings.Builder
	for i, m := range msgs {
		fmt.Fprintf(&b, "--- %d %s", i+1, m.Role)
		if m.Role == backend.RoleTool {
			fmt.Fprintf(&b, " %s %s", m.Tool, m.CallID)
		}
		if len(m.Images) > 0 {
			fmt.Fprintf(&b, " (%d image)", len(m.Images))
		}
		b.WriteByte('\n')
		if m.Role == backend.RoleSystem {
			b.WriteString("(system prompt)\n")
		} else {
			b.WriteString(m.Text + "\n")
		}
		for _, c := range m.Calls {
			fmt.Fprintf(&b, "call %s %s %s\n", c.ID, c.Name, c.Args)
		}
	}
	return b.String()
}

func TestANativeConversationAlternatesAndKeepsOnePicture(t *testing.T) {
	c := newConv(t, backend.ModeNative, 0, 0)
	c.Begin(obs(1), testImage)
	req, rep, err := c.Request(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != backend.RoleSystem || req.Messages[1].Role != backend.RoleUser || len(req.Messages[1].Images) != 1 {
		t.Fatalf("first request: %s", dump(req.Messages))
	}
	if req.Mode != backend.ModeNative || req.MaxActions != 3 || req.Set == nil || rep.KeptTurns != 0 || rep.ImageTokens == 0 {
		t.Fatalf("request = %+v report = %+v", req, rep)
	}
	if err := c.Assistant("I open it. <think>hmm</think>", calls("exec", "key")); err != nil {
		t.Fatal(err)
	}
	if err := c.Results(results("exec", "key"), Limits{}); err != nil {
		t.Fatal(err)
	}
	c.Begin(obs(2), testImage)
	req, _, err = c.Request(3)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "conversation-native.golden", dump(req.Messages))
	images := 0
	for _, m := range req.Messages {
		images += len(m.Images)
	}
	if images != 1 || len(req.Messages[len(req.Messages)-1].Images) != 1 {
		t.Fatalf("only the latest picture stays in context: %d", images)
	}
	if strings.Contains(req.Messages[1].Text, "UI generation") || strings.Contains(req.Messages[1].Text, "TASK:") {
		t.Fatalf("a past observation is compact:\n%s", req.Messages[1].Text)
	}
	if !strings.Contains(req.Messages[len(req.Messages)-1].Text, "UI generation") {
		t.Fatal("the current observation is whole")
	}
	if strings.Contains(req.Messages[2].Text, "think") {
		t.Fatalf("a reasoning block is kept: %q", req.Messages[2].Text)
	}
}

func TestAJSONConversationShowsTheResultsInTheNextObservation(t *testing.T) {
	c := newConv(t, backend.ModeJSON, 0, 0)
	c.Begin(obs(1), testImage)
	if err := c.Assistant(`{"thought":"x","actions":[{"tool":"exec","args":{"command":"uname -r"}}]}`, nil); err != nil {
		t.Fatal(err)
	}
	res := []Result{{Tool: "exec", Action: `exec {"command":"uname -r"}`, OK: true, Detail: "exit=0 31ms", Parts: []Part{{Source: "exec.stdout", Text: "6.12.x-amd64\n"}}}}
	if err := c.Results(res, Limits{}); err != nil {
		t.Fatal(err)
	}
	c.Note("Not run, because it was not valid:\n- action 2 (key): args.keys: is required")
	c.UserMessage("please hurry")
	c.Begin(obs(2), testImage)
	req, _, err := c.Request(3)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "conversation-json.golden", dump(req.Messages))
	for _, m := range req.Messages {
		if m.Role == backend.RoleTool || len(m.Calls) > 0 {
			t.Fatalf("no tool messages without native calls: %s", dump(req.Messages))
		}
	}
	last := req.Messages[len(req.Messages)-1].Text
	for _, want := range []string{"LAST RESULTS\n1. exec ok exit=0 31ms", "6.12.x-amd64", "NOTE: Not run, because it was not valid:\n  - action 2 (key)", "USER: please hurry"} {
		if !strings.Contains(last, want) {
			t.Errorf("the observation lacks %q:\n%s", want, last)
		}
	}
	// What was queued is used once.
	c.Begin(obs(3), testImage)
	req, _, _ = c.Request(3)
	if last := req.Messages[len(req.Messages)-1].Text; strings.Contains(last, "please hurry") || strings.Contains(last, "NOTE:") || strings.Contains(last, "LAST RESULTS") {
		t.Fatalf("queued text shown twice:\n%s", last)
	}
}

func TestResultsMustAnswerEveryCall(t *testing.T) {
	c := newConv(t, backend.ModeNative, 0, 0)
	if err := c.Results(nil, Limits{}); err == nil {
		t.Error("results with no turn")
	}
	if err := c.Assistant("x", nil); err == nil {
		t.Error("an answer with no turn")
	}
	if err := c.Notice("x"); err == nil {
		t.Error("a notice with no turn")
	}
	c.Begin(obs(1), nil)
	if err := c.Assistant("", calls("exec", "key", "done")); err != nil {
		t.Fatal(err)
	}
	if err := c.Results(results("exec", "key"), Limits{}); err == nil || !strings.Contains(err.Error(), "every call is answered") {
		t.Errorf("two results for three calls: %v", err)
	}
	if err := c.Results(results("exec", "mouse", "done"), Limits{}); err == nil || !strings.Contains(err.Error(), "result 2 is for mouse") {
		t.Errorf("a result for another tool: %v", err)
	}
	if err := c.Results(results("exec", "key", "done"), Limits{}); err != nil {
		t.Fatal(err)
	}
	// A conversation that leaves a call unanswered is refused by the request, not by a provider.
	c2 := newConv(t, backend.ModeNative, 0, 0)
	c2.Begin(obs(1), nil)
	_ = c2.Assistant("", calls("exec"))
	c2.Begin(obs(2), nil)
	if _, _, err := c2.Request(3); err == nil || !strings.Contains(err.Error(), "not one a backend takes") {
		t.Errorf("an unanswered call: %v", err)
	}
}

func TestANoticeFollowsTheResultsInTheSameTurn(t *testing.T) {
	c := newConv(t, backend.ModeNative, 0, 0)
	c.Begin(obs(1), testImage)
	_ = c.Assistant("", calls("exec"))
	bad := []Result{{CallID: "call_1", Tool: "exec", Code: "invalid_args", Detail: "args.command: is required"}}
	if err := c.Results(bad, Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := c.Notice("Your last answer could not be used, so nothing was done."); err != nil {
		t.Fatal(err)
	}
	req, _, err := c.Request(3)
	if err != nil {
		t.Fatal(err)
	}
	msgs := req.Messages
	roles := ""
	for _, m := range msgs {
		roles += string(m.Role)[:1]
	}
	if roles != "suatu" {
		t.Fatalf("roles = %s\n%s", roles, dump(msgs))
	}
	if !strings.Contains(msgs[3].Text, "1. exec error invalid_args args.command: is required") {
		t.Errorf("the failed call is answered with its problem: %q", msgs[3].Text)
	}
	if len(msgs[1].Images) != 1 {
		t.Error("the current turn keeps its picture while it is repaired")
	}
}

func TestAnAssistantMessageIsCleanedAndBounded(t *testing.T) {
	c := newConv(t, backend.ModeJSON, 0, 0)
	c.Begin(obs(1), nil)
	long := "<think>" + strings.Repeat("secret ", 100) + "</think>" + strings.Repeat("y", 5000)
	if err := c.Assistant(long, nil); err != nil {
		t.Fatal(err)
	}
	m := c.last().msgs[0]
	if strings.Contains(m.Text, "secret") || len(m.Text) != maxAssistantRns {
		t.Fatalf("text of %d bytes: %.60q", len(m.Text), m.Text)
	}
}

func TestOlderTurnsShrinkToALine(t *testing.T) {
	c := newConv(t, backend.ModeNative, 0, 3)
	for i := 1; i <= 8; i++ {
		step(t, c, i, "exec")
	}
	c.Begin(obs(9), testImage)
	req, rep, err := c.Request(3)
	if err != nil {
		t.Fatal(err)
	}
	if rep.KeptTurns != 3 || rep.DroppedTurns != 5 {
		t.Fatalf("report = %+v", rep)
	}
	first := req.Messages[1].Text
	for _, want := range []string{"EARLIER TURNS", "Turn 1: exec {\"x\":1} -> ok", "Turn 5: exec"} {
		if !strings.Contains(first, want) {
			t.Errorf("the summary lacks %q:\n%s", want, first)
		}
	}
	if strings.Contains(first, "Turn 6") {
		t.Errorf("a turn that is kept in full is also summarized:\n%s", first)
	}
	if !strings.Contains(first, "TURN 6 ·") {
		t.Errorf("the first turn kept follows the summary:\n%s", first)
	}
	users := 0
	for _, m := range req.Messages {
		if m.Role == backend.RoleUser {
			users++
		}
	}
	if users != 4 {
		t.Errorf("%d observations in the request, want 3 kept and the current one", users)
	}
	// What can never be shown in full again is let go.
	if c.turns[0].msgs != nil || c.turns[0].results != nil || c.turns[0].summary == "" {
		t.Errorf("an old turn still holds its messages: %+v", c.turns[0])
	}
	if c.Turn() != 9 {
		t.Errorf("Turn = %d", c.Turn())
	}
}

func TestLongHistoriesListOnlyTheLastSummaries(t *testing.T) {
	c := newConv(t, backend.ModeJSON, 0, 1)
	for i := 1; i <= 50; i++ {
		c.Begin(obs(i), nil)
		_ = c.Assistant("{}", nil)
		_ = c.Results([]Result{{Tool: "key", OK: true}}, Limits{})
	}
	c.Begin(obs(51), nil)
	req, _, err := c.Request(3)
	if err != nil {
		t.Fatal(err)
	}
	text := req.Messages[1].Text
	if !strings.Contains(text, "(19 earlier turns not listed)") || strings.Contains(text, "Turn 19:") || !strings.Contains(text, "Turn 20:") || !strings.Contains(text, "Turn 49:") {
		t.Errorf("summary:\n%.600s", text)
	}
}

func TestSummariesOfTurns(t *testing.T) {
	if got := summarize(4, nil); got != "Turn 4: no action was run." {
		t.Error(got)
	}
	got := summarize(2, []Result{{Tool: "exec", Action: `exec {"command":"ls"}`, OK: true}, {Tool: "key", Code: "policy_denied"}, {Tool: "mouse", Code: CodeNotRun}, {Tool: "done", Code: CodeDropped}})
	want := `Turn 2: exec {"command":"ls"} -> ok; key -> error policy_denied; mouse -> not run; done -> dropped`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if long := summarize(1, []Result{{Tool: "exec", Action: "exec " + strings.Repeat("x", 500), OK: true}}); len([]rune(long)) > maxSummaryRunes {
		t.Errorf("a summary of %d characters", len([]rune(long)))
	}
}

func TestDescribe(t *testing.T) {
	set := mustSet(t, actions.ProfileCompact)
	a, err := set.Parse("exec", json.RawMessage(`{"command":"ls -la"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := Describe(a); !strings.HasPrefix(got, "exec {") || !strings.Contains(got, `"command":"ls -la"`) {
		t.Errorf("Describe = %q", got)
	}
	if got := Describe(actions.Action{Tool: "done"}); got != "done" {
		t.Errorf("Describe = %q", got)
	}
	if got := Describe(actions.Action{Tool: "x", Raw: json.RawMessage(`{}`)}); got != "x" {
		t.Errorf("Describe = %q", got)
	}
}

// bigObservation is a turn with a tree and screen text too big for a small context.
func bigObservation(turn int) Observation {
	o := obs(turn)
	o.UI.Nodes = nil
	for i := 1; i <= 200; i++ {
		n := Node{ID: fmt.Sprintf("a%d", i), Role: "label", Name: fmt.Sprintf("Label number %d of the window", i), Bounds: Rect{i, i, 50, 20}}
		if i%10 == 0 {
			n.Role, n.Actions = "push button", []string{"click"}
		}
		o.UI.Nodes = append(o.UI.Nodes, n)
	}
	o.OCR = &OCRBlock{Generation: "g1"}
	for i := 1; i <= 100; i++ {
		o.OCR.Lines = append(o.OCR.Lines, OCRLine{ID: fmt.Sprintf("o%d", i), Text: fmt.Sprintf("A line of recognized text, number %d", i), Bounds: Rect{0, i * 10, 400, 10}})
	}
	return o
}

func TestTheRequestFitsTheContextAndGivesUpThingsInTheOrderOfThePlan(t *testing.T) {
	type stage struct{ turns, nodes, ocr, parts, failed bool }
	seen := stage{}
	var order []string
	mark := func(name string, flag *bool, cond bool) {
		if cond && !*flag {
			*flag = true
			order = append(order, name)
		}
	}
	for window := 40000; window >= 1500; window -= 250 {
		c := newConv(t, backend.ModeNative, window, 6)
		for i := 1; i <= 6; i++ {
			step(t, c, i, "exec", "key")
		}
		o := bigObservation(7)
		o.Results = nil
		o.Results = []Result{{Tool: "exec", OK: true, Parts: []Part{{Source: "exec.stdout", Text: strings.Repeat("output line\n", 1500)}}}}
		c.Begin(o, testImage)
		c.opts.Mode = backend.ModeNative
		req, rep, err := c.Request(3)
		if err != nil {
			var ce *ContextError
			if !errors.As(err, &ce) || ce.Limit != window*85/100 || ce.Estimated <= ce.Limit {
				t.Fatalf("window %d: %v", window, err)
			}
			mark("error", &seen.failed, true)
			continue
		}
		if rep.Estimated > rep.Limit || rep.Limit != window*85/100 {
			t.Fatalf("window %d: estimated %d over the limit %d", window, rep.Estimated, rep.Limit)
		}
		if got := estimateRequest(c, req); got > rep.Limit+rep.ImageTokens*0 {
			t.Fatalf("window %d: the messages really take %d tokens, over the limit %d", window, got, rep.Limit)
		}
		mark("turns", &seen.turns, rep.DroppedTurns > 0)
		mark("nodes", &seen.nodes, rep.Nodes < rep.NodesTotal)
		mark("ocr", &seen.ocr, rep.OCRLines < rep.OCRTotal)
		mark("parts", &seen.parts, rep.ResultsCut)
		if rep.Nodes < rep.NodesTotal && rep.DroppedTurns < 6 {
			t.Fatalf("window %d: nodes were cut while %d turns were still kept", window, 6-rep.DroppedTurns)
		}
		if rep.OCRLines < rep.OCRTotal && rep.Nodes > minNodes {
			t.Fatalf("window %d: screen text was cut while %d nodes were still shown", window, rep.Nodes)
		}
		if rep.ResultsCut && rep.OCRLines > minOCRLines {
			t.Fatalf("window %d: results were cut while %d lines of screen text were still shown", window, rep.OCRLines)
		}
	}
	if strings.Join(order, ",") != "turns,nodes,ocr,parts,error" {
		t.Fatalf("things were given up in the order %v", order)
	}
}

func estimateRequest(c *Conversation, req backend.Request) int {
	n := c.fixed
	for _, m := range req.Messages[1:] {
		n += messageTokens(m)
	}
	return n
}

func TestTrimmingKeepsWhatCanBeActedOn(t *testing.T) {
	c := newConv(t, backend.ModeJSON, 3200, 6)
	o := bigObservation(1)
	o.Active = &Window{Class: "x", Title: "t", Bounds: Rect{0, 0, 2000, 2000}}
	c.Begin(o, nil)
	req, rep, err := c.Request(3)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Nodes >= rep.NodesTotal || rep.Nodes < minNodes {
		t.Fatalf("report = %+v", rep)
	}
	text := req.Messages[len(req.Messages)-1].Text
	buttons := strings.Count(text, "push_button")
	if buttons < min(rep.Nodes, 20)-1 {
		t.Errorf("%d buttons of 20 survive among %d nodes", buttons, rep.Nodes)
	}
	if !strings.Contains(text, fmt.Sprintf("%d/%d nodes", rep.Nodes, rep.NodesTotal)) {
		t.Errorf("the header does not say what was cut:\n%.300s", text)
	}
}

func TestTheImageIsCountedInTheBudget(t *testing.T) {
	with := newConv(t, backend.ModeJSON, 60000, 6)
	with.Begin(bigObservation(1), testImage)
	_, repWith, err := with.Request(3)
	if err != nil {
		t.Fatal(err)
	}
	without := newConv(t, backend.ModeJSON, 60000, 6)
	without.Begin(bigObservation(1), nil)
	_, repWithout, err := without.Request(3)
	if err != nil {
		t.Fatal(err)
	}
	if repWith.ImageTokens < 600 || repWithout.ImageTokens != 0 || repWith.Estimated-repWithout.Estimated != repWith.ImageTokens {
		t.Fatalf("with a picture %+v, without %+v", repWith, repWithout)
	}
}

func TestToolDefinitionsAreCountedByMode(t *testing.T) {
	jsonMode := newConv(t, backend.ModeJSON, 0, 0)
	native := newConv(t, backend.ModeNative, 0, 0)
	structured := newConv(t, backend.ModeStructured, 0, 0)
	j, _ := jsonMode.toolTokens()
	n, _ := native.toolTokens()
	s, _ := structured.toolTokens()
	if j != 0 || n < 1500 || s < 1000 {
		t.Errorf("tool tokens: json %d native %d structured %d", j, n, s)
	}
}

func TestContextErrorSaysWhatToDo(t *testing.T) {
	c := newConv(t, backend.ModeNative, 1000, 6)
	c.Begin(obs(1), testImage)
	_, _, err := c.Request(3)
	var ce *ContextError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"1000 tokens", "850", "--num-ctx", "shorten the task"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

func TestANewConversationIsChecked(t *testing.T) {
	set := mustSet(t, actions.ProfileCompact)
	for name, opts := range map[string]Options{
		"no set":    {Mode: backend.ModeNative, Renderer: textRenderer()},
		"no fencer": {Mode: backend.ModeNative, Set: set},
		"bad mode":  {Mode: "x", Set: set, Renderer: textRenderer()},
	} {
		if _, err := NewConversation("system", opts); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	c, err := NewConversation("system", Options{Mode: backend.ModeJSON, Set: set, Renderer: textRenderer()})
	if err != nil || c.opts.ContextTokens != DefaultContextTokens || c.opts.KeepTurns != DefaultKeepTurns {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	if _, _, err := c.Request(3); err == nil {
		t.Error("a request before the first observation")
	}
	if c.Turn() != 0 {
		t.Error("Turn before the first")
	}
}

func TestEveryRequestIsValidAndWithinTheBudget(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for iter := range 300 {
		mode := []backend.Mode{backend.ModeNative, backend.ModeStructured, backend.ModeJSON}[rng.Intn(3)]
		window := 2000 + rng.Intn(30000)
		c := newConv(t, mode, window, 1+rng.Intn(8))
		turns := 1 + rng.Intn(12)
		for n := 1; n <= turns; n++ {
			o := obs(n)
			if rng.Intn(2) == 0 {
				o = bigObservation(n)
			}
			var img *backend.Image
			if rng.Intn(3) > 0 {
				img = testImage
			}
			c.Begin(o, img)
			if n == turns {
				break
			}
			k := rng.Intn(4)
			var names []string
			for range k {
				names = append(names, []string{"exec", "key", "mouse", "done"}[rng.Intn(4)])
			}
			var cs []parse.ToolCall
			if mode == backend.ModeNative {
				cs = calls(names...)
			}
			if err := c.Assistant("answer", cs); err != nil {
				t.Fatal(err)
			}
			rs := results(names...)
			if mode != backend.ModeNative {
				rs = rs[:0]
				for _, n := range names {
					rs = append(rs, Result{Tool: n, OK: rng.Intn(2) == 0, Detail: "d", Parts: []Part{{Source: "exec.stdout", Text: strings.Repeat("o", rng.Intn(3000))}}})
				}
			}
			if err := c.Results(rs, Limits{}); err != nil {
				t.Fatalf("iteration %d: %v", iter, err)
			}
			if rng.Intn(4) == 0 {
				c.Note("a note")
			}
		}
		req, rep, err := c.Request(1 + rng.Intn(5))
		if err != nil {
			var ce *ContextError
			if !errors.As(err, &ce) {
				t.Fatalf("iteration %d (%s, window %d): %v", iter, mode, window, err)
			}
			continue
		}
		if rep.Estimated > rep.Limit {
			t.Fatalf("iteration %d: %d over %d", iter, rep.Estimated, rep.Limit)
		}
		images := 0
		for _, m := range req.Messages {
			images += len(m.Images)
		}
		if images > 1 {
			t.Fatalf("iteration %d: %d pictures in context", iter, images)
		}
		if req.Messages[0].Role != backend.RoleSystem {
			t.Fatalf("iteration %d: the system message is not first", iter)
		}
		if got := estimateRequest(c, req); got > rep.Limit {
			t.Fatalf("iteration %d: really %d tokens, limit %d", iter, got, rep.Limit)
		}
	}
}

func TestTimeLeftIsShownInTheBudgetLine(t *testing.T) {
	o := obs(1)
	o.Budget.TimeLeft = 90 * time.Second
	if got := textRenderer().Observation(o, Limits{}); !strings.Contains(got, "time_left=2m") {
		t.Errorf("budget line:\n%s", got)
	}
}

func TestWhatACallCostsIsCounted(t *testing.T) {
	plain := messageTokens(backend.Message{Role: backend.RoleAssistant, Text: "x"})
	withCall := messageTokens(backend.Message{Role: backend.RoleAssistant, Text: "x", Calls: []parse.ToolCall{{Name: "type_text", Args: json.RawMessage(`{"text":"` + strings.Repeat("abcd", 500) + `"}`)}}})
	if withCall-plain < 500 {
		t.Errorf("a call of 2000 characters costs %d tokens more than none", withCall-plain)
	}
}

func TestTheFixedPartIsTheSystemPromptAndTheToolDefinitions(t *testing.T) {
	for _, mode := range []backend.Mode{backend.ModeNative, backend.ModeStructured, backend.ModeJSON} {
		c := newConv(t, mode, 0, 0)
		tools, err := c.toolTokens()
		if err != nil {
			t.Fatal(err)
		}
		if want := EstimateTokens(c.system) + messageOverhead + tools; c.fixed != want {
			t.Errorf("%s: fixed = %d, want %d", mode, c.fixed, want)
		}
	}
}

func TestAPastTurnLetsGoOfItsPicture(t *testing.T) {
	c := newConv(t, backend.ModeNative, 0, 0)
	c.Begin(obs(1), testImage)
	if c.turns[0].img == nil {
		t.Fatal("the current turn has its picture")
	}
	c.Begin(obs(2), testImage)
	if c.turns[0].img != nil || c.turns[1].img == nil {
		t.Errorf("pictures: %v %v", c.turns[0].img, c.turns[1].img)
	}
}

// A model of native mode can still give its actions in words (JSON, or calls written as functions when the server did not
// recognize its tool-call format). There is no native call to answer then, and the results go in the next observation.
func TestResultsOfAnAnswerWithoutNativeCallsAreShownInTheNextObservation(t *testing.T) {
	c := newConv(t, backend.ModeNative, 0, 0)
	c.Begin(obs(1), testImage)
	if err := c.Assistant(`click_element(id='a3')`, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Results([]Result{{Tool: "click_element", OK: true, Detail: "clicked a3"}}, Limits{}); err != nil {
		t.Fatal(err)
	}
	c.Begin(obs(2), testImage)
	req, _, err := c.Request(3)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range req.Messages {
		if m.Role == backend.RoleTool {
			t.Fatalf("a tool message with no call to answer: %s", dump(req.Messages))
		}
	}
	if last := req.Messages[len(req.Messages)-1].Text; !strings.Contains(last, "LAST RESULTS\n1. click_element ok clicked a3") {
		t.Errorf("the observation lacks the result:\n%s", last)
	}
}
