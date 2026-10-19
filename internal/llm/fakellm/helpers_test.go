// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package fakellm_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack/internal/imaging"
	"github.com/PacifAIst/controlsnack/internal/llm/actions"
	"github.com/PacifAIst/controlsnack/internal/llm/backend"
	"github.com/PacifAIst/controlsnack/internal/llm/backend/httpx"
	"github.com/PacifAIst/controlsnack/internal/llm/detect"
	"github.com/PacifAIst/controlsnack/internal/llm/fakellm"
)

func TestToolMakesArguments(t *testing.T) {
	type args struct {
		A int    `json:"a"`
		B string `json:"b"`
	}
	for _, tc := range []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, "{}"}, {"string", `{"x":1}`, `{"x":1}`}, {"bytes", []byte(`{"y":2}`), `{"y":2}`},
		{"struct", args{1, "z"}, `{"a":1,"b":"z"}`}, {"map", map[string]any{"k": true}, `{"k":true}`}, {"cannot be marshaled", make(chan int), "{}"},
	} {
		if c := fakellm.Tool("t", tc.in); c.Name != "t" || c.Args != tc.want {
			t.Errorf("%s: %+v", tc.name, c)
		}
	}
}

func TestARequestWithNothingInIt(t *testing.T) {
	var r fakellm.Request
	if r.System() != "" || r.Observation() != "" || r.Pictures() != 0 || r.HasTool("x") || !reflect.DeepEqual(r.Last("user"), fakellm.Message{}) {
		t.Errorf("%+v", r)
	}
}

func TestThePicturesOfARequestAreRecognizedByTheirBytes(t *testing.T) {
	s := fakellm.Start(t, fakellm.Options{Script: []fakellm.Step{fakellm.Say("seen")}})
	png, _ := imaging.Encode(testImage(), imaging.PNG, 0)
	webp, _ := imaging.Encode(testImage(), imaging.WebP, 0)
	jpeg, _ := imaging.Encode(testImage(), imaging.JPEG, 0)
	_, err := client(t, s, "").Complete(context.Background(), backend.Request{Mode: backend.ModeJSON, Messages: []backend.Message{{Role: backend.RoleUser, Text: "look", Images: []backend.Image{
		{MIME: backend.MIMEPNG, Data: png}, {MIME: backend.MIMEWebP, Data: webp}, {MIME: backend.MIMEJPEG, Data: jpeg}, {MIME: backend.MIMEPNG, Data: []byte("GIF89a?")},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	got := s.Requests()[0].Messages[0].Images
	if !reflect.DeepEqual(got, []string{"png", "webp", "jpeg", "unknown"}) || s.Requests()[0].Pictures() != 4 {
		t.Errorf("kinds = %v", got)
	}
}

func TestAStructuredRequestToAModelWithoutSchemasIsRefused(t *testing.T) {
	p := fakellm.AllCapabilities()
	p.Structured = false
	s, err := fakellm.Serve(fakellm.Options{Profile: &p})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	set, _ := actions.NewSet(actions.ProfileCompact)
	_, err = client(t, s, "").Complete(context.Background(), backend.Request{Mode: backend.ModeStructured, Set: set, Messages: []backend.Message{{Role: backend.RoleUser, Text: "x"}}})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Kind != httpx.KindBadRequest || !strings.Contains(he.Error(), "json_schema") || len(s.Errors()) != 1 {
		t.Errorf("err = %v errors = %q", err, s.Errors())
	}
}

func TestTheAnswerIsInTheShapeOfTheAPI(t *testing.T) {
	s := fakellm.Start(t, fakellm.Options{Script: []fakellm.Step{
		fakellm.Act(fakellm.Tool("exec", `{"command":"ls"}`), fakellm.Tool("key", `{"keys":"a"}`)),
		fakellm.Say("words"),
		fakellm.Fn(func(*fakellm.Request) fakellm.Reply { return fakellm.Reply{Text: "cut", Finish: "length"} }),
	}})
	post := func() map[string]any {
		t.Helper()
		resp := do(t, http.MethodPost, s.URL()+"/chat/completions", `{"model":"fake-vlm","messages":[{"role":"user","content":"x"}]}`)
		defer resp.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	choice := func(o map[string]any) map[string]any { return o["choices"].([]any)[0].(map[string]any) }

	a := post()
	msg := choice(a)["message"].(map[string]any)
	calls := msg["tool_calls"].([]any)
	id1, id2 := calls[0].(map[string]any)["id"].(string), calls[1].(map[string]any)["id"].(string)
	if choice(a)["finish_reason"] != "tool_calls" || msg["content"] != nil || id1 == "" || id2 == "" || id1 == id2 {
		t.Errorf("tool calls: %v", a)
	}
	if fn := calls[0].(map[string]any)["function"].(map[string]any); fn["name"] != "exec" || fn["arguments"] != `{"command":"ls"}` {
		t.Errorf("the arguments are a string that holds the object, as the API sends them: %v", fn)
	}
	b := post()
	if choice(b)["finish_reason"] != "stop" || choice(b)["message"].(map[string]any)["content"] != "words" || b["object"] != "chat.completion" || b["usage"] == nil {
		t.Errorf("words: %v", b)
	}
	if c := post(); choice(c)["finish_reason"] != "length" {
		t.Errorf("finish: %v", c)
	}
}

// do makes one HTTP request with a context, which is what the linters want of every call.
func do(t testing.TB, method, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// wantCoords is how the probe should find a profile to point: nothing for a model that does not see.
func wantCoords(p fakellm.Profile) string {
	switch {
	case !p.Vision:
		return ""
	case p.Relative:
		return detect.CoordsRelative
	}
	return detect.CoordsPixels
}
