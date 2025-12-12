package main

import (
	"context"
	"log"

	"github.com/xichan96/cortex/examples/chat-web/server"
	httptrigger "github.com/xichan96/cortex/trigger/http"
)

func main() {
	log.Println("=== AI Training Service MCP Integration Test ===")
	log.Println("Starting chat server...")

	log.Println("Creating Agent Engine...")
	agentEngine, err := createAgentEngine()
	if err != nil {
		log.Fatalf("Failed to create Agent Engine: %v", err)
	}

	log.Println("Creating MCP Client...")
	mcpClient, err := createMCPClient(agentEngine)
	if err != nil {
		log.Fatalf("Failed to create MCP Client: %v", err)
	}
	defer mcpClient.Disconnect(context.Background())

	serverConfig := &server.ServerConfig{
		Port: 8765,
	}

	log.Println("Creating server instance...")
	s := server.NewServer(serverConfig)

	log.Println("Creating HTTP trigger server...")
	httpHandler := httptrigger.NewHandler(agentEngine)

	log.Println("Registering API routes...")
	server.RegisterAPIRouter(s.Engine(), httpHandler)

	log.Printf("Server will start on port %d", serverConfig.Port)
	log.Printf("Chat interface: http://localhost:%d/chat", serverConfig.Port)
	log.Printf("Streaming chat interface: http://localhost:%d/chat/stream", serverConfig.Port)
	log.Println("Starting server...")

	if err := s.Run(); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
