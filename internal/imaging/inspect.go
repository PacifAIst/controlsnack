// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package imaging

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg" // registered so image.DecodeConfig can read their headers
	_ "image/png"

	_ "golang.org/x/image/webp"
)

// MaxDimension is the largest width or height the host accepts in a picture from the guest (PLAN.md section 5.1): the
// guest screen is at most 1920x1080, so anything bigger is a lie or an attack on the decoder.
const MaxDimension = 4096

// Inspect reads only the header of an image that came from the guest and checks it against what the guest said about
// it, before anything decodes the pixels: the format must be the declared one, the size the declared one, and neither
// side may exceed MaxDimension (a small file can announce a gigantic picture, and the decoder would try to allocate
// it).
func Inspect(data []byte, declared Format, w, h int) error {
	cfg, name, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("the picture is not a readable %s image: %w", declared, err)
	}
	if name != string(declared) {
		return fmt.Errorf("the guest declared a %s picture but sent a %s one", declared, name)
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxDimension || cfg.Height > MaxDimension {
		return fmt.Errorf("the picture is %dx%d pixels, the limit is %d on each side", cfg.Width, cfg.Height, MaxDimension)
	}
	if cfg.Width != w || cfg.Height != h {
		return fmt.Errorf("the guest declared a %dx%d picture but sent a %dx%d one", w, h, cfg.Width, cfg.Height)
	}
	return nil
}
