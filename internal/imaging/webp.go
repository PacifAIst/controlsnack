// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package imaging

import (
	"bytes"
	"fmt"
	"image"

	webp "github.com/deepteams/webp"
)

// webpMethod is the encoder's effort (0 fastest .. 6 best). The benchmark of D-11 on real guest screenshots showed
// that 4 is the point where more effort stops paying: methods 0 to 2 are slower and larger, 5 and 6 give the same
// bytes as 4, and 3 is only about 12 % faster for about 3 % larger files.
const webpMethod = 4

// encodeWebP writes a lossy WebP (VP8) of the image at the given quality (1..100). It is pure Go, so the agent stays
// a static binary, and about ten times faster than the libwebp-derived alternative on a 1080p screen (D-11).
func encodeWebP(img image.Image, quality int) ([]byte, error) {
	o := webp.DefaultOptions()
	o.Quality = float32(quality)
	o.Method = webpMethod
	var buf bytes.Buffer
	if err := webp.Encode(&buf, img, o); err != nil {
		return nil, fmt.Errorf("encoding webp: %w", err)
	}
	return buf.Bytes(), nil
}
