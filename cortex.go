package main

import (
	"flag"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/xichan96/cortex/internal/app"
	"github.com/xichan96/cortex/internal/config"
)

func chatHandler(c *gin.Context) {
	agent := app.NewAgent()
	httptrigger := agent.HttpTrigger()
	req, err := httptrigger.GetMessageRequest(c)
	if err != nil {
		return
	}

	engine, err := agent.Engine(req.SessionID)
	if err != nil {
		return
	}
	httptrigger.ChatAPI(c, engine, req)
}

func streamChatHandler(c *gin.Context) {
	agent := app.NewAgent()
	httptrigger := agent.HttpTrigger()
	req, err := httptrigger.GetMessageRequest(c)
	if err != nil {
		return
	}
	engine, err := agent.Engine(req.SessionID)
	if err != nil {
		return
	}
	httptrigger.StreamChatAPI(c, engine, req)
}

func mcpHandler(c *gin.Context) {
	agent := app.NewAgent()
	mcptrigger, err := agent.McpTrigger()
	if err != nil {
		return
	}
	mcptrigger.Agent()(c)
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
