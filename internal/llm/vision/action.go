// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vision

import (
	"errors"

	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
)

// Guest returns the action with the coordinates the model gave, which are pixels of the picture it was shown, converted
// to pixels of the guest screen: the position of mouse and scroll and the region of read_screen_text. Any other action is
// returned as it is. The action given is not changed (the audit log keeps what the model said).
//
// A coordinate a little beyond the picture is taken as its edge; one further out makes the action invalid, with a problem
// for each coordinate that is, in words the model can act on (an *actions.Error of code invalid_args).
func (m Mapper) Guest(a actions.Action) (actions.Action, error) {
	var problems []actions.Problem
	add := func(path string, err error) {
		var re *RangeError
		if errors.As(err, &re) {
			problems = append(problems, actions.Problem{Path: path, Message: re.Error()})
		} else if err != nil {
			problems = append(problems, actions.Problem{Path: path, Message: err.Error()})
		}
	}
	out := a
	switch args := a.Args.(type) {
	case *actions.MouseArgs:
		c := *args
		var err error
		c.X, err = m.GuestX("x", args.X)
		add("args.x", err)
		c.Y, err = m.GuestY("y", args.Y)
		add("args.y", err)
		if args.ToX != nil {
			v, err := m.GuestX("to_x", *args.ToX)
			add("args.to_x", err)
			c.ToX = &v
		}
		if args.ToY != nil {
			v, err := m.GuestY("to_y", *args.ToY)
			add("args.to_y", err)
			c.ToY = &v
		}
		out.Args = &c
	case *actions.ScrollArgs:
		c := *args
		var err error
		c.X, err = m.GuestX("x", args.X)
		add("args.x", err)
		c.Y, err = m.GuestY("y", args.Y)
		add("args.y", err)
		out.Args = &c
	case *actions.ReadScreenTextArgs:
		if args.Region != nil {
			g, errs := m.GuestRect(*args.Region)
			for _, err := range errs {
				add("args.region", err)
			}
			c := *args
			c.Region = &g
			out.Args = &c
		}
	}
	if len(problems) > 0 {
		return a, actions.NewError(actions.CodeInvalidArgs, a.Tool, problems...)
	}
	return out, nil
}
