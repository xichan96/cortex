package main

import (
	"context"
	"log"

	"github.com/xichan96/cortex/examples/chat-web/server"
)

func main() {
	log.Println("=== AI Training Service MCP Integration Test ===")
	log.Println("Starting chat server...")

	// 1. Create Agent Engine (created externally)
	log.Println("Creating Agent Engine...")
	agentEngine, err := createAgentEngine()
	if err != nil {
		log.Fatalf("Failed to create Agent Engine: %v", err)
	}

	// 2. Create MCP Client (created externally)
	log.Println("Creating MCP Client...")
	mcpClient, err := createMCPClient(agentEngine)
	if err != nil {
		log.Fatalf("Failed to create MCP Client: %v", err)
	}
	defer mcpClient.Disconnect(context.Background())

	// 3. Create custom Agent Provider
	agentProvider := &MyAgentProvider{
		agentEngine: agentEngine,
		mcpClient:   mcpClient,
	}

	// 4. Create server configuration
	serverConfig := &server.ServerConfig{
		Port: 8765,
	}

	// 5. Create server instance - similar to s := ginx.NewServer()
	log.Println("Creating server instance...")
	s := server.NewServer(serverConfig)

	// 6. Register Agent Provider to server
	log.Println("Registering Agent Provider to server...")
	s.SetAgentProvider(agentProvider)

	// 7. Register API routes - similar to router.RegisterAPIRouter(s.Engine)
	log.Println("Registering API routes...")
	server.RegisterAPIRouter(s.Engine(), s)

	// 8. Start server - similar to s.Run()
	log.Printf("Server will start on port %d", serverConfig.Port)
	log.Printf("Chat interface: http://localhost:%d/chat", serverConfig.Port)
	log.Printf("Streaming chat interface: http://localhost:%d/chat/stream", serverConfig.Port)
	log.Println("Starting server...")

	if err := s.Run(); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
