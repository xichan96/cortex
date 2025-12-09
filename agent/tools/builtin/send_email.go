package builtin

import (
	"fmt"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/email"
	"github.com/xichan96/cortex/pkg/errors"
)

type EmailTool struct {
	cfg *email.Config
}

func NewEmailTool(cfg *email.Config) types.Tool {
	return &EmailTool{cfg: cfg}
}

func (t *EmailTool) Name() string {
	return "send_email"
}

func (t *EmailTool) Description() string {
	return "Send an email to one or more recipients. Supports HTML, plain text, and markdown content types."
}

func (t *EmailTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"to": map[string]interface{}{
				"type":        "array",
				"description": "List of recipient email addresses",
				"items": map[string]interface{}{
					"type": "string",
				},
			},
			"subject": map[string]interface{}{
				"type":        "string",
				"description": "Email subject line",
			},
			"type": map[string]interface{}{
				"type":        "string",
				"description": "Content type: text/html, text/plain, or text/markdown",
				"enum":        []string{"text/html", "text/plain", "text/markdown"},
			},
			"message": map[string]interface{}{
				"type":        "string",
				"description": "Email message content",
			},
		},
		"required": []string{"to", "subject", "type", "message"},
	}
}

func (t *EmailTool) Execute(input map[string]interface{}) (interface{}, error) {
	to, ok := input["to"].([]interface{})
	if !ok {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid 'to' parameter: must be an array"))
	}
	if len(to) == 0 {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("'to' parameter cannot be empty"))
	}

	toEmails := make([]string, 0, len(to))
	for i, v := range to {
		emailStr, ok := v.(string)
		if !ok {
			return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid 'to' parameter at index %d: must be a string", i))
		}
		if emailStr == "" {
			return nil, errors.EC_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid 'to' parameter at index %d: email cannot be empty", i))
		}
		toEmails = append(toEmails, emailStr)
	}

	subject, ok := input["subject"].(string)
	if !ok {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid 'subject' parameter: must be a string"))
	}
	if subject == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("'subject' parameter cannot be empty"))
	}

	contentType, ok := input["type"].(string)
	if !ok {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid 'type' parameter: must be a string"))
	}
	validTypes := map[string]bool{
		"text/html":     true,
		"text/plain":    true,
		"text/markdown": true,
	}
	if !validTypes[contentType] {
		return nil, errors.EC_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid 'type' parameter: must be one of text/html, text/plain, text/markdown"))
	}

	message, ok := input["message"].(string)
	if !ok {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid 'message' parameter: must be a string"))
	}
	if message == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("'message' parameter cannot be empty"))
	}

	err := email.Do(t.cfg, toEmails, &email.Content{
		Title:   subject,
		Type:    contentType,
		Message: message,
	})
	if err != nil {
		return nil, errors.EC_EMAIL_SEND_FAILED.Wrap(err)
	}

	return fmt.Sprintf("Email sent successfully to %d recipient(s)", len(toEmails)), nil
}

func (t *EmailTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "email",
		IsFromToolkit:  false,
		ToolType:       "builtin",
	}
}
