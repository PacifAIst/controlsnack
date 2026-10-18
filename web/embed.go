// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

// Static is the front-end as the viewer's server serves it: index.html, the script and the style sheet at the root.
func Static() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil { // the directory is embedded by the line above; this cannot fail unless the name there is changed
		panic(err)
	}
	return sub
}
