// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package schema

import _ "embed"

// AuditRecord is the JSON Schema of one audit log line (schema/audit-record.schema.json).
//
//go:embed audit-record.schema.json
var AuditRecord []byte
