package types

import (
	"encoding/json"
)

// ImageEstimateTokens is the fixed token cost attributed to one image part.
//
// Visual tokens are a function of the image's pixel dimensions, not of the
// size of the payload that carries it: the same picture costs the same whether
// it arrives as a 40KB JPEG or a 3MB base64 data URL. Estimating images from
// len(Data)/4 (the old behavior) therefore charged a single 400KB screenshot
// ~100k tokens and tripped compaction thresholds over an image that really
// costs a small fraction of that.
//
// 1600 is the conservative value for the common 1568px long-edge cap used by
// vision providers, and it matches Dojo's UI estimate (imageEstimateTokens in
// context_snapshot.go) so the two sides agree on when a session is close to
// the auto-compact budget.
const ImageEstimateTokens = 1600

func RoughTokenEstimate(text string) int {
	asciiCount := 0
	nonAsciiCount := 0
	for _, r := range text {
		if r < 128 {
			asciiCount++
		} else {
			nonAsciiCount++
		}
	}
	return (asciiCount / 4) + (nonAsciiCount * 2)
}

func RoughTokensForMessage(m Message) int {
	n := RoughTokenEstimate(m.Content)
	n += RoughTokenEstimate(m.Name)
	n += RoughTokenEstimate(m.ToolCallID)
	for _, p := range m.Parts {
		switch p := p.(type) {
		case TextPart:
			n += RoughTokenEstimate(p.Text)
		case ImageURLPart, ImageDataPart:
			// Fixed per-image cost; neither the URL nor the base64 payload
			// size is a proxy for visual tokens (see ImageEstimateTokens).
			// Even an image part with an empty payload counts: serializers
			// still turn it into an image block, so under-counting would be
			// the riskier direction.
			n += ImageEstimateTokens
		}
	}
	for _, tc := range m.ToolCalls {
		n += RoughTokenEstimate(tc.ID)
		n += RoughTokenEstimate(tc.Type)
		n += RoughTokenEstimate(tc.Function.Name)
		if tc.Function.Arguments != nil {
			if b, err := json.Marshal(tc.Function.Arguments); err == nil {
				n += RoughTokenEstimate(string(b))
			}
		}
	}
	return n
}
