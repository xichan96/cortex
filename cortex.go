package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xichan96/cortex/internal/app"
	"github.com/xichan96/cortex/internal/config"
)

func chatHandler(c *gin.Context) {
	agent := app.NewAgent()
	httpTrigger := agent.HttpTrigger()
	req, err := httpTrigger.GetMessageRequest(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	engine, err := agent.Engine(req.SessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	httpTrigger.ChatAPI(c, engine, req)
}

func streamChatHandler(c *gin.Context) {
	agent := app.NewAgent()
	httpTrigger := agent.HttpTrigger()
	req, err := httpTrigger.GetMessageRequest(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	engine, err := agent.Engine(req.SessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	httpTrigger.StreamChatAPI(c, engine, req)
}

func mcpHandler(c *gin.Context) {
	agent := app.NewAgent()
	mcpTrigger, err := agent.McpTrigger()
	if err != nil {
		return
	}
	mcpTrigger.Agent()(c)
}

func router(r *gin.Engine) {
	r.POST("/chat", chatHandler)
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
