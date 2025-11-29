package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/tools/mcp"
)

// ServerConfig contains server configuration settings
type ServerConfig struct {
	Port int
}

// Server represents the main server structure
type Server struct {
	config       *ServerConfig
	router       *gin.Engine
	customRouter Router
	agentEngine  *engine.AgentEngine
	mcpClient    *mcp.Client
	mu           sync.Mutex
}

// AgentProvider defines the interface for agent engine providers
type AgentProvider interface {
	GetAgentEngine() *engine.AgentEngine
	GetMCPClient() *mcp.Client
}

// NewServer creates a new server instance
func NewServer(config *ServerConfig) *Server {
	return &Server{
		config: config,
		router: gin.Default(),
	}
}

// SetAgentEngine sets the agent engine
func (s *Server) SetAgentEngine(agentEngine *engine.AgentEngine) {
	s.agentEngine = agentEngine
}

// SetMCPClient sets the MCP client
func (s *Server) SetMCPClient(mcpClient *mcp.Client) {
	s.mcpClient = mcpClient
}

// SetAgentProvider sets the agent provider
func (s *Server) SetAgentProvider(provider AgentProvider) {
	s.agentEngine = provider.GetAgentEngine()
	s.mcpClient = provider.GetMCPClient()
}

// MessageRequest defines the structure for message requests
type MessageRequest struct {
	Message string `json:"message" binding:"required"`
}

// SSEvent defines the structure for SSE events
type SSEvent struct {
	Type    string      `json:"type"`
	Content string      `json:"content,omitempty"`
	Error   string      `json:"error,omitempty"`
	End     bool        `json:"end,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

// SetRouter sets a custom router
func (s *Server) SetRouter(router Router) {
	s.customRouter = router
}

// Engine returns the Gin engine instance
func (s *Server) Engine() *gin.Engine {
	return s.router
}

// GetRouter returns the currently used router
func (s *Server) GetRouter() *gin.Engine {
	return s.router
}

// Run starts the server (alias for Start method)
func (s *Server) Run() error {
	return s.Start()
}

// handleChat processes regular chat requests
func (s *Server) handleChat(c *gin.Context) {
	var req MessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request parameters"})
		return
	}

	// Execute agent engine
	result, err := s.agentEngine.Execute(req.Message, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to execute agent engine: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"content":           result.Output,
		"toolCalls":         result.ToolCalls,
		"intermediateSteps": result.IntermediateSteps,
	})
}

// handleChatStream processes streaming chat requests
func (s *Server) handleChatStream(c *gin.Context) {
	var req MessageRequest
	var err error

	// Support both GET and POST requests
	if c.Request.Method == "GET" {
		// Get message from URL parameters
		req.Message = c.Query("message")
		if req.Message == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Message parameter cannot be empty"})
			return
		}
	} else {
		// Get message from JSON request body
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request parameters"})
			return
		}
	}

	// Set SSE response headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	// Create context with cancellation support
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	// Use channel to listen for client disconnection
	notify := c.Writer.CloseNotify()
	go func() {
		<-notify
		cancel()
	}()

	// Execute streaming agent engine
	stream, err := s.agentEngine.ExecuteStream(req.Message, nil)
	if err != nil {
		s.sendSSEvent(c, SSEvent{
			Type:  "error",
			Error: fmt.Sprintf("Failed to execute agent engine: %v", err),
		})
		return
	}

	// Process streaming results
	for result := range stream {
		select {
		case <-ctx.Done():
			return
		default:
			switch result.Type {
			case "chunk":
				s.sendSSEvent(c, SSEvent{
					Type:    "chunk",
					Content: result.Content,
				})
			case "error":
				s.sendSSEvent(c, SSEvent{
					Type:  "error",
					Error: fmt.Sprintf("Streaming execution error: %v", result.Error),
				})
			case "end":
				s.sendSSEvent(c, SSEvent{
					Type: "end",
					End:  true,
					Data: result.Result,
				})
			}
		}
	}
}

// sendSSEvent sends an SSE event
func (s *Server) sendSSEvent(c *gin.Context, event SSEvent) {
	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("Failed to serialize SSE event: %v", err)
		return
	}

	// Write SSE formatted data
	fmt.Fprintf(c.Writer, "data: %s\n\n", data)

	// Flush buffer
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Start begins the server execution
func (s *Server) Start() error {
	// Check if required components are set
	if s.agentEngine == nil {
		return fmt.Errorf("Agent engine not set")
	}

	if s.mcpClient == nil {
		return fmt.Errorf("MCP client not set")
	}

	// Start the server
	addr := fmt.Sprintf(":%d", s.config.Port)
	return s.router.Run(addr)
}
