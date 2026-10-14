// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package fakellm

import (
	"regexp"
	"strconv"
	"strings"
)

// Element is a line of the tree or of the recognized text of an observation, as the model reads it.
type Element struct {
	// ID is a17, w3 or o5; Role is empty for a line of recognized text.
	ID, Role string
	// Name is the element's name, or the text of the line.
	Name string
	// Bounds is x, y, width, height in the pixels of the picture.
	Bounds [4]int
}

// Center is the middle of the element, where a model would click.
func (e Element) Center() (x, y int) { return e.Bounds[0] + e.Bounds[2]/2, e.Bounds[1] + e.Bounds[3]/2 }

var (
	reNode = regexp.MustCompile(`(?m)^([aw][0-9]+) (\S+) ("(?:[^"\\]|\\.)*") \[(-?[0-9]+),(-?[0-9]+),(-?[0-9]+),(-?[0-9]+)\]`)
	reLine = regexp.MustCompile(`(?m)^(o[0-9]+) ("(?:[^"\\]|\\.)*") \[(-?[0-9]+),(-?[0-9]+),(-?[0-9]+),(-?[0-9]+)\]`)
)

func bounds(g []string) [4]int {
	var b [4]int
	for i := range b {
		b[i], _ = strconv.Atoi(g[i])
	}
	return b
}

// Elements lists the elements of the tree an observation shows, in order.
func Elements(observation string) []Element {
	var out []Element
	for _, g := range reNode.FindAllStringSubmatch(observation, -1) {
		name, err := strconv.Unquote(g[3])
		if err != nil {
			continue
		}
		out = append(out, Element{ID: g[1], Role: g[2], Name: name, Bounds: bounds(g[4:8])})
	}
	return out
}

// Lines lists the lines of recognized text an observation shows, in order.
func Lines(observation string) []Element {
	var out []Element
	for _, g := range reLine.FindAllStringSubmatch(observation, -1) {
		text, err := strconv.Unquote(g[2])
		if err != nil {
			continue
		}
		out = append(out, Element{ID: g[1], Name: text, Bounds: bounds(g[3:7])})
	}
	return out
}

// Find returns the first element whose role is the given one (any role when empty) and whose name contains the text, ignoring
// case. A script uses it to click what the observation shows without knowing its id beforehand.
func Find(observation, role, name string) (Element, bool) {
	for _, e := range Elements(observation) {
		if (role == "" || e.Role == role) && strings.Contains(strings.ToLower(e.Name), strings.ToLower(name)) {
			return e, true
		}
	}
	return Element{}, false
}

// FindText returns the first line of recognized text that contains the text, ignoring case.
func FindText(observation, text string) (Element, bool) {
	for _, e := range Lines(observation) {
		if strings.Contains(strings.ToLower(e.Name), strings.ToLower(text)) {
			return e, true
		}
	}
	return Element{}, false
}
