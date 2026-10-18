// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package schema

import "embed"

// AuditRecord is the JSON Schema of one audit log line (schema/audit-record.schema.json).
//
//go:embed audit-record.schema.json
var AuditRecord []byte

// Response is the JSON Schema of a model's answer in JSON mode (schema/response.schema.json).
//
//go:embed response.schema.json
var Response []byte

// Observation is the JSON Schema of what the model receives each turn with --obs-format json
// (schema/observation.schema.json).
//
//go:embed observation.schema.json
var Observation []byte

// Tools holds the JSON Schema of each tool's arguments, one file for each: tools/<tool>.json.
//
//go:embed tools/*.json
var Tools embed.FS

// ViewerWS is the JSON Schema of the viewer's WebSocket messages (schema/viewer-ws.schema.json): the definitions
// server_message and client_message.
//
//go:embed viewer-ws.schema.json
var ViewerWS []byte
