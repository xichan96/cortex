package pkg

import (
	"encoding/base64"
	"strings"

	"github.com/xichan96/cortex/agent/types"
)

type UserAttachment struct {
	Type          string `json:"type"`
	MIMEType      string `json:"mime_type,omitempty"`
	Name          string `json:"name,omitempty"`
	Content       string `json:"content"`
	ContentFormat string `json:"content_format,omitempty"`
}

func BuildUserMessage(content, text string, baseParts []types.MessagePart, attachments []UserAttachment) types.Message {
	c := strings.TrimSpace(content)
	if c == "" {
		c = strings.TrimSpace(text)
	}
	parts := append([]types.MessagePart(nil), baseParts...)
	for _, att := range attachments {
		if att.Type != "image" || att.Content == "" || att.ContentFormat != "base64" {
			continue
		}
		data := att.Content
		if strings.HasPrefix(data, "data:") {
			dataParts := strings.SplitN(data, ",", 2)
			if len(dataParts) == 2 {
				data = dataParts[1]
			}
		}
		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			continue
		}
		mimeType := att.MIMEType
		if strings.HasPrefix(att.Content, "data:") {
			mimeType = strings.Split(strings.TrimPrefix(att.Content, "data:"), ";")[0]
		}
		parts = append(parts, types.ImageDataPart{
			Data:     decoded,
			MIMEType: mimeType,
		})
	}
	return types.Message{Content: c, Parts: parts}
}
