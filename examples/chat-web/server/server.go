package server

import (
	"fmt"

	"github.com/gin-gonic/gin"
)

// ServerConfig contains server configuration settings
type ServerConfig struct {
	Port int
}

// Server represents the main server structure
type Server struct {
	config *ServerConfig
	router *gin.Engine
}

// NewServer creates a new server instance
func NewServer(config *ServerConfig) *Server {
	return &Server{
		config: config,
		router: gin.Default(),
	}
}

// Engine returns the Gin engine instance
func (s *Server) Engine() *gin.Engine {
	return s.router
}

// Run starts the server (alias for Start method)
func (s *Server) Run() error {
	return s.Start()
}

// Start begins the server execution
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.config.Port)
	return s.router.Run(addr)
}
