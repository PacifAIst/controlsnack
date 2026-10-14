// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/schema"
)

// JSONMode is the document `controlsnack schema --format jsonmode` prints: the schema of a response, and under "tools"
// the schema of each tool's arguments, indented. maxActions is the response's limit on actions.
func JSONMode(maxActions int) ([]byte, error) {
	envelope, err := Envelope(maxActions)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString(`{"response":`)
	b.Write(envelope)
	b.WriteString(`,"tools":{`)
	for i, t := range Tools() {
		if i > 0 {
			b.WriteByte(',')
		}
		name, _ := json.Marshal(t.Name)
		b.Write(name)
		b.WriteByte(':')
		b.Write(t.Schema)
	}
	b.WriteString(`}}`)
	var out bytes.Buffer
	if err := json.Indent(&out, b.Bytes(), "", "  "); err != nil {
		return nil, fmt.Errorf("writing the schema: %w", err)
	}
	return out.Bytes(), nil
}

type printer struct{}

// NewPrinter returns what `controlsnack schema` prints with.
func NewPrinter() schema.Printer { return printer{} }

// Print writes the schema in a backend's format.
func (printer) Print(w io.Writer, format string) error {
	switch format {
	case "jsonmode":
		doc, err := JSONMode(DefaultMaxActions)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%s\n", doc)
		return err
	default:
		return exitcode.Pending("P5-T02")
	}
}
