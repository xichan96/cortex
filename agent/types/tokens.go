package types

import (
	"encoding/json"
)

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
		switch t := p.(type) {
		case TextPart:
			n += RoughTokenEstimate(t.Text)
		case ImageURLPart:
			n += RoughTokenEstimate(t.URL) + RoughTokenEstimate(t.Detail)
		case ImageDataPart:
			if len(t.Data) > 0 {
				n += len(t.Data)/4 + 64
			}
			n += RoughTokenEstimate(t.MIMEType)
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
