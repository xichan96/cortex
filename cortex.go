package main

import (
	"flag"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/xichan96/cortex/internal/app"
	"github.com/xichan96/cortex/internal/config"
)

func chatHandler(c *gin.Context) {
	sessionID := c.Query("session_id")
	if sessionID == "" {
		sessionID = uuid.New().String()
	}
	agent := app.NewAgent()
	if err := agent.Build(sessionID); err != nil {
		panic(err)
	}
	agent.HttpTrigger().ChatAPI(c)
}

func streamChatHandler(c *gin.Context) {
	sessionID := c.Query("session_id")
	if sessionID == "" {
		sessionID = uuid.New().String()
	}
	agent := app.NewAgent()
	if err := agent.Build(sessionID); err != nil {
		panic(err)
	}
	agent.HttpTrigger().StreamChatAPI(c)
}

func mcpHandler(c *gin.Context) {
	agent := app.NewAgent()
	if err := agent.Build("default"); err != nil {
		panic(err)
	}
	agent.McpTrigger().Agent()(c)
}

func router(r *gin.Engine) {
	r.POST("/chat", chatHandler)
	r.GET("/chat/stream", streamChatHandler)
	r.POST("/chat/stream", streamChatHandler)
	r.Any("/mcp", mcpHandler)
}

func main() {
	log.Println("Loading config...")
	configPath := flag.String("config", "cortex.yaml", "path to config file")
	flag.Parse()
	if err := config.Load(*configPath); err != nil {
		panic(err)
	}
	log.Println("Starting Cortex...")
	r := gin.Default()
	router(r)
	r.Run(":5678")
}
