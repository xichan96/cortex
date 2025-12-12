package server

import (
	"io/ioutil"
	"net/http"
	"path/filepath"
	"runtime"

	"github.com/gin-gonic/gin"
	httptrigger "github.com/xichan96/cortex/trigger/http"
)

func SetupRoutes(r *gin.Engine, httpHandler httptrigger.Handler) {
	_, b, _, _ := runtime.Caller(0)
	serverDir := filepath.Dir(b)

	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	})

	r.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/index.html")
	})

	r.GET("/index.html", func(c *gin.Context) {
		content, err := ioutil.ReadFile(filepath.Join(serverDir, "index.html"))
		if err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", content)
	})

	r.GET("/style.css", func(c *gin.Context) {
		http.ServeFile(c.Writer, c.Request, filepath.Join(serverDir, "style.css"))
	})

	r.GET("/script.js", func(c *gin.Context) {
		http.ServeFile(c.Writer, c.Request, filepath.Join(serverDir, "script.js"))
	})

	r.GET("/static/*filepath", func(c *gin.Context) {
		http.ServeFile(c.Writer, c.Request, filepath.Join(serverDir, "static", c.Param("filepath")))
	})

	r.POST("/chat", httpHandler.ChatAPI)
	r.GET("/chat/stream", httpHandler.StreamChatAPI)
	r.POST("/chat/stream", httpHandler.StreamChatAPI)
}
