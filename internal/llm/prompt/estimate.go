// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package prompt

// EstimateTokens guesses how many tokens a text takes: a quarter of its bytes for ASCII, and one token for every other
// character (Chinese, Japanese and accented text take about that, and a guess too high only trims earlier). The budget
// is made with it, not with a tokenizer, because every model has its own and the host can have none of them.
func EstimateTokens(s string) int {
	ascii, wide := 0, 0
	for _, r := range s {
		if r < 0x80 {
			ascii++
		} else {
			wide++
		}
	}
	return (ascii+3)/4 + wide
}

// ImageTokens guesses what a picture of w by h pixels costs a backend of the given kind ("ollama", "openai" or
// "anthropic"); it never guesses low.
func ImageTokens(backendName string, w, h int) int {
	if w < 1 || h < 1 {
		return 0
	}
	switch backendName {
	case "anthropic":
		// The API documents about width * height / 750 tokens, at most about 1600 (larger pictures are scaled down).
		return min(ceilDiv(w*h, 750), 1600)
	case "openai":
		// High detail: scaled to fit 2048 pixels, then the short side to 768, counted in 512-pixel tiles at 170 tokens
		// each plus 85 (the documented formula of the API).
		fw, fh := float64(w), float64(h)
		if m := max(fw, fh); m > 2048 {
			fw, fh = fw*2048/m, fh*2048/m
		}
		if s := min(fw, fh); s > 768 {
			fw, fh = fw*768/s, fh*768/s
		}
		tiles := int(ceilF(fw/512)) * int(ceilF(fh/512))
		return 85 + 170*tiles
	}
	// Local models cut a picture into patches of about 28 pixels (Qwen-VL) and spend a token on each; others (LLaVA,
	// Gemma) spend 256 to 600 on any size.
	return min(max(ceilDiv(w, 28)*ceilDiv(h, 28), 600), 2400)
}

func ceilDiv(a, b int) int { return (a + b - 1) / b }

func ceilF(f float64) float64 {
	n := float64(int(f))
	if n < f {
		return n + 1
	}
	return n
}
