package main

import (
	"context"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/xichan96/cortex/examples/chat-web/server"
	httptrigger "github.com/xichan96/cortex/trigger/http"
)

func main() {
	agentEngine, err := createAgentEngine()
	if err != nil {
		log.Fatalf("Failed to create agent engine: %v", err)
	}

	mcpClient, err := createMCPClient(agentEngine)
	if err != nil {
		log.Fatalf("Failed to create MCP client: %v", err)
	}
	defer mcpClient.Disconnect(context.Background())

	httpHandler := httptrigger.NewHandler(agentEngine)
	r := gin.Default()
	server.SetupRoutes(r, httpHandler)
	log.Println("Starting chat server on :8765")
	r.Run(":8765")
}
