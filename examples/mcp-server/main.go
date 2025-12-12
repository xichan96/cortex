package main

import (
	"log"

	"github.com/gin-gonic/gin"
	"github.com/xichan96/cortex/trigger/mcp"
)

func main() {
	mcpOpt := mcp.Options{
		Server: mcp.Metadata{
			Name:    "cortex-mcp",
			Version: "0.1.0",
		},
		Tool: mcp.Metadata{
			Name:        "chat",
			Description: "a task self-check assistant: Users may send you task_id or links. You need to: 1. Collect information as needed. If previous information is insufficient to analyze the problem, continue collecting (task details, logs, workflow, task monitoring statistics, etc.) 2. Comprehensive analysis of problems or evaluation of resource utilization of training tasks based on the collected information",
		},
	}
	agentEngine, err := createAgentEngine()
	if err != nil {
		log.Fatalf("Failed to create agent engine: %v", err)
	}
	mcpHandler := mcp.NewHandler(agentEngine, mcpOpt)
	r := gin.Default()
	mcpGroup := r.Group("/mcp")
	mcpGroup.Any("", mcpHandler.Agent())
	log.Println("Starting MCP server...")
	r.Run()
}
