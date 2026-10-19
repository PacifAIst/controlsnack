// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/PacifAIst/controlsnack/internal/exitcode"
	"github.com/PacifAIst/controlsnack/schema"
)

// JSONMode is the document `controlsnack schema --format jsonmode` prints: the schema of a response ("response"), the
// same with the arguments of each action tied to its tool ("response_strict"), and under "tools" the schema of each tool's
// arguments, indented. maxActions is the response's limit on actions.
func (s *Set) JSONMode(maxActions int) ([]byte, error) {
	envelope, err := s.Envelope(maxActions, false)
	if err != nil {
		return nil, err
	}
	strict, err := s.Envelope(maxActions, true)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString(`{"response":`)
	b.Write(envelope)
	b.WriteString(`,"response_strict":`)
	b.Write(strict)
	b.WriteString(`,"tools":{`)
	for i, t := range s.tools {
		if i > 0 {
			b.WriteByte(',')
		}
		name, _ := json.Marshal(t.Name)
		b.Write(name)
		b.WriteByte(':')
		b.Write(t.Schema)
	}
	b.WriteString(`}}`)
	return indent(b.Bytes())
}

func indent(doc []byte) ([]byte, error) {
	var out bytes.Buffer
	if err := json.Indent(&out, doc, "", "  "); err != nil {
		return nil, fmt.Errorf("writing the schema: %w", err)
	}
	return out.Bytes(), nil
}

type printer struct{}

// NewPrinter returns what `controlsnack schema` prints with.
func NewPrinter() schema.Printer { return printer{} }

// Print writes the schema of a tool profile (full or compact) in a backend's format.
func (printer) Print(w io.Writer, format, profile string) error {
	set, err := NewSet(profile)
	if err != nil {
		return exitcode.Usagef("%v", err)
	}
	var doc []byte
	if format == FormatJSONMode {
		doc, err = set.JSONMode(DefaultMaxActions)
	} else {
		var raw json.RawMessage
		if raw, err = set.Render(format); err == nil {
			doc, err = indent(raw)
		}
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", doc)
	return err
}
