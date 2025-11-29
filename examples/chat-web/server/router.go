package server

import (
	"io/ioutil"
	"log"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/gin-gonic/gin"
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
	// Disable all redirects
	engine.RedirectTrailingSlash = false
	engine.RedirectFixedPath = false
	engine.HandleMethodNotAllowed = true

	// Add request logging middleware to track request handling flow
	engine.Use(func(c *gin.Context) {
		log.Printf("Received request: %s %s", c.Request.Method, c.Request.URL.Path)
		// Continue processing the request
		c.Next()
		// Log response status after request processing is complete
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

	// Get absolute path of the current file's directory (independent of working directory)
	_, b, _, _ := runtime.Caller(0)
	serverDir := filepath.Dir(b)
	log.Printf("Using server directory: %s", serverDir)

	// Provide direct access to index.html - ensure no redirects
	engine.GET("/index.html", func(c *gin.Context) {
		log.Println("Executing /index.html handler")
		indexPath := filepath.Join(serverDir, "index.html")
		log.Printf("Reading index.html from: %s", indexPath)
		// Manually read file content for complete control over response process
		content, err := ioutil.ReadFile(indexPath)
		if err != nil {
			log.Printf("Error reading index.html: %v", err)
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		// Set response headers
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Header("Content-Length", strconv.Itoa(len(content)))
		// Ensure no Location header is set (redirect)
		c.Header("Location", "")
		// Write response content
		c.Writer.WriteHeader(http.StatusOK)
		c.Writer.Write(content)
		log.Println("Successfully served index.html with 200 status")
	})

	// Root path route, providing chat interface (redirects to index.html)
	engine.GET("/", func(c *gin.Context) {
		log.Println("Executing root path handler, redirecting to /index.html")
		c.Redirect(http.StatusMovedPermanently, "/index.html")
	})

	// Add middleware to handle double slash requests
	engine.Use(func(c *gin.Context) {
		path := c.Request.URL.Path
		if len(path) > 1 && path[0:2] == "//" {
			// Correct double slash path
			correctedPath := "/" + path[2:]
			log.Printf("Detected double slash in path '%s', redirecting to '%s'", path, correctedPath)
			c.Redirect(http.StatusMovedPermanently, correctedPath)
			return
		}
		c.Next()
	})

	// Manually handle static file requests, avoiding Gin's Static method
	engine.GET("/static/*filepath", func(c *gin.Context) {
		// Get file path
		filePath := c.Param("filepath")
		// Security check: prevent path traversal attacks
		cleanPath := filepath.Clean(filePath)
		// If cleaned path still contains "..", reject the request
		if filepath.Base(cleanPath) == ".." || filepath.Dir(cleanPath) == ".." {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		// Join to form full path
		fullPath := filepath.Join(serverDir, "static"+filePath)
		log.Printf("Serving static file from: %s", fullPath)
		// Serve file
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
	engine.POST("/chat", server.handleChat)

	// SSE chat route (supports both GET and POST)
	engine.GET("/chat/stream", server.handleChatStream)
	engine.POST("/chat/stream", server.handleChatStream)
}

// RegisterAPIRouter registers API routes (external interface)
func RegisterAPIRouter(engine *gin.Engine, server *Server) {
	// Create default router instance and set up routes
	defaultRouter := NewDefaultRouter()
	defaultRouter.SetupRoutes(engine, server)
}
