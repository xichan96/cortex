package http

// MessageRequest defines the structure for message requests
type MessageRequest struct {
	Message string `json:"message" binding:"required"`
}

// ErrorResponse defines the structure for error responses
type ErrorResponse struct {
	Status int    `json:"status"`
	Msg    string `json:"msg"`
}

// SSEvent defines the structure for SSE events
type SSEvent struct {
	Type    string      `json:"type"`
	Content string      `json:"content,omitempty"`
	Error   string      `json:"error,omitempty"`
	End     bool        `json:"end,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}
