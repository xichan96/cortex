package server

import (
	"io/ioutil"
	"log"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/gin-gonic/gin"
	httptrigger "github.com/xichan96/cortex/trigger/http"
)

// Router defines the interface for route configuration
type Router interface {
	SetupRoutes(engine *gin.Engine, server *Server)
}

// DefaultRouter provides the default router implementation
type DefaultRouter struct{}

// NewDefaultRouter creates a new default router instance
func NewDefaultRouter() *DefaultRouter {
	return &DefaultRouter{}
}

// SetupRoutes configures all routes for the application
func (r *DefaultRouter) SetupRoutes(engine *gin.Engine, server *Server) {
	// This method is kept for compatibility but not used
}

// SetupRoutesWithHTTPTrigger configures all routes with HTTP trigger server
func SetupRoutesWithHTTPTrigger(engine *gin.Engine, httpHandler httptrigger.Handler) {
	// Disable all redirects
	engine.RedirectTrailingSlash = false
	engine.RedirectFixedPath = false
	engine.HandleMethodNotAllowed = true

	// Add request logging middleware
	engine.Use(func(c *gin.Context) {
		log.Printf("Received request: %s %s", c.Request.Method, c.Request.URL.Path)
		c.Next()
		log.Printf("Response status: %d for %s %s", c.Writer.Status(), c.Request.Method, c.Request.URL.Path)
	})

	// Set up CORS middleware
	engine.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	})

	// Get absolute path of the current file's directory
	_, b, _, _ := runtime.Caller(0)
	serverDir := filepath.Dir(b)
	log.Printf("Using server directory: %s", serverDir)

	// Provide direct access to index.html
	engine.GET("/index.html", func(c *gin.Context) {
		log.Println("Executing /index.html handler")
		indexPath := filepath.Join(serverDir, "index.html")
		log.Printf("Reading index.html from: %s", indexPath)
		content, err := ioutil.ReadFile(indexPath)
		if err != nil {
			log.Printf("Error reading index.html: %v", err)
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Header("Content-Length", strconv.Itoa(len(content)))
		c.Header("Location", "")
		c.Writer.WriteHeader(http.StatusOK)
		c.Writer.Write(content)
		log.Println("Successfully served index.html with 200 status")
	})

	// Root path route
	engine.GET("/", func(c *gin.Context) {
		log.Println("Executing root path handler, redirecting to /index.html")
		c.Redirect(http.StatusMovedPermanently, "/index.html")
	})

	// Add middleware to handle double slash requests
	engine.Use(func(c *gin.Context) {
		path := c.Request.URL.Path
		if len(path) > 1 && path[0:2] == "//" {
			correctedPath := "/" + path[2:]
			log.Printf("Detected double slash in path '%s', redirecting to '%s'", path, correctedPath)
			c.Redirect(http.StatusMovedPermanently, correctedPath)
			return
		}
		c.Next()
	})

	// Handle static file requests
	engine.GET("/static/*filepath", func(c *gin.Context) {
		filePath := c.Param("filepath")
		cleanPath := filepath.Clean(filePath)
		if filepath.Base(cleanPath) == ".." || filepath.Dir(cleanPath) == ".." {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		fullPath := filepath.Join(serverDir, "static"+filePath)
		log.Printf("Serving static file from: %s", fullPath)
		http.ServeFile(c.Writer, c.Request, fullPath)
	})

	// Directly handle CSS and JS file requests
	engine.GET("/style.css", func(c *gin.Context) {
		cssPath := filepath.Join(serverDir, "style.css")
		log.Printf("Serving style.css from: %s", cssPath)
		http.ServeFile(c.Writer, c.Request, cssPath)
	})

	engine.GET("/script.js", func(c *gin.Context) {
		jsPath := filepath.Join(serverDir, "script.js")
		log.Printf("Serving script.js from: %s", jsPath)
		http.ServeFile(c.Writer, c.Request, jsPath)
	})

	// Chat route
	engine.POST("/chat", httpHandler.ChatAPI)

	// SSE chat route (supports both GET and POST)
	engine.GET("/chat/stream", httpHandler.StreamChatAPI)
	engine.POST("/chat/stream", httpHandler.StreamChatAPI)
}

// RegisterAPIRouter registers API routes (external interface)
func RegisterAPIRouter(engine *gin.Engine, httpHandler httptrigger.Handler) {
	SetupRoutesWithHTTPTrigger(engine, httpHandler)
}
