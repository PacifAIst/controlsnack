// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package prompt

import (
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack/internal/llm/backend"
)

// A model that points on a scale of 0 to 1000 (the Qwen-VL family) is told that scale, and the size of the picture in
// pixels, which it would only misread as the range of its answers, is left out.
func TestAModelThatPointsOnTheRelativeScaleIsToldSo(t *testing.T) {
	p := baseParams(t)
	p.Width, p.Height, p.Relative, p.Marks = 1000, 1000, true, true
	r, err := System(p)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "system-native-vision-relative.golden", r.Text)
	for _, want := range []string{
		"relative to the image you were last given", "x runs from 0 at its left edge to 1000 at its right edge", "y from 0 at its top edge to 1000 at its bottom edge",
		"whatever the size of the image in pixels", "The middle of the image is (500, 500)", "Numbered boxes drawn on the image",
	} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("the prompt lacks %q", want)
		}
	}
	if strings.Contains(r.Text, "pixels of the image you were last given") || strings.Contains(r.Text, "1000x1000") {
		t.Errorf("the prompt still speaks of pixels:\n%s", r.Text)
	}

	// And the same model in text-only mode (nothing to point at in a picture) reads screen pixels as any other.
	p.Vision, p.Mode = false, backend.ModeJSON
	r, err = System(p)
	if err != nil || strings.Contains(r.Text, "relative to the image") {
		t.Errorf("text-only: %v\n%s", err, r.Text)
	}
}

func TestASessionWithoutATreeIsToldThereAreNoIds(t *testing.T) {
	for _, vision := range []bool{true, false} {
		p := baseParams(t)
		p.NoTree, p.Vision = true, vision
		p.Set = p.Set.WithoutTree()
		r, err := System(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"no accessibility tree, so there are no ids to click", "use the mouse at its place"} {
			if !strings.Contains(r.Text, want) {
				t.Errorf("vision=%v: the prompt lacks %q", vision, want)
			}
		}
		if strings.Contains(r.Text, "Next prefer the ids") || strings.Contains(r.Text, "never make one up") {
			t.Errorf("vision=%v: the prompt still recommends ids:\n%s", vision, r.Text)
		}
		if strings.Contains(r.Text, "(click_element, set_text)") {
			t.Errorf("vision=%v: the prompt names tools that are not offered", vision)
		}
	}
	p := baseParams(t)
	if r, _ := System(p); strings.Contains(r.Text, "no accessibility tree") {
		t.Error("a session with a tree is not told otherwise")
	}
}
