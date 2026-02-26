package logger

import (
	"io"
	"log/slog"
	"os"
	"sync"
	"time"
)

var (
	defaultLogger *Logger
	once          sync.Once
)

func init() {
	once.Do(func() {
		defaultLogger = NewLogger()
	})
}

// GetLogger returns the global logger instance
func GetLogger() *Logger {
	return defaultLogger
}

// SetGlobalLogger sets the global logger instance
func SetGlobalLogger(l *Logger) {
	defaultLogger = l
}

// LoggerConfig logging configuration
type LoggerConfig struct {
	Silent   bool
	FilePath string
}

// Logger structured logger
type Logger struct {
	logger *slog.Logger
}

// NewLogger creates a new logger
func NewLogger() *Logger {
	return NewLoggerWithConfig(nil)
}

// NewLoggerWithConfig creates a new logger with specific configuration
func NewLoggerWithConfig(cfg *LoggerConfig) *Logger {
	if cfg == nil {
		cfg = &LoggerConfig{}
	}

	if cfg.Silent {
		return &Logger{
			logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
	}

	var w io.Writer = os.Stdout
	if cfg.FilePath != "" {
		f, err := os.OpenFile(cfg.FilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			w = f
		} else {
			slog.Error("Failed to open log file, falling back to stdout", "error", err, "path", cfg.FilePath)
		}
	}

	return &Logger{
		logger: slog.New(slog.NewTextHandler(w, nil)),
	}
}

// LogExecution logs execution information
func (l *Logger) LogExecution(operation string, iteration int, message string, attrs ...slog.Attr) {
	l.logger.Info(message,
		slog.String("operation", operation),
		slog.Int("iteration", iteration),
		slog.Time("timestamp", time.Now()),
	)
}

// LogToolExecution logs tool execution information
func (l *Logger) LogToolExecution(toolName string, success bool, duration time.Duration, attrs ...slog.Attr) {
	status := "success"
	if !success {
		status = "failed"
	}
	l.logger.Info("Tool execution",
		slog.String("tool", toolName),
		slog.String("status", status),
		slog.Duration("duration", duration),
		slog.Time("timestamp", time.Now()),
	)
}

// LogError logs error information
func (l *Logger) LogError(operation string, err error, attrs ...slog.Attr) {
	l.logger.Error("Operation failed",
		slog.String("operation", operation),
		slog.String("error", err.Error()),
		slog.Time("timestamp", time.Now()),
	)
}

// Info logs informational message
func (l *Logger) Info(message string, attrs ...slog.Attr) {
	allAttrs := make([]any, 0, len(attrs)*2+2)
	allAttrs = append(allAttrs, slog.Time("timestamp", time.Now()))
	for _, attr := range attrs {
		allAttrs = append(allAttrs, attr)
	}
	l.logger.Info(message, allAttrs...)
}

// Warn logs warning message
func (l *Logger) Warn(message string, attrs ...slog.Attr) {
	allAttrs := make([]any, 0, len(attrs)*2+2)
	allAttrs = append(allAttrs, slog.Time("timestamp", time.Now()))
	for _, attr := range attrs {
		allAttrs = append(allAttrs, attr)
	}
	l.logger.Warn(message, allAttrs...)
}

// Error logs error message
func (l *Logger) Error(message string, attrs ...slog.Attr) {
	allAttrs := make([]any, 0, len(attrs)*2+2)
	allAttrs = append(allAttrs, slog.Time("timestamp", time.Now()))
	for _, attr := range attrs {
		allAttrs = append(allAttrs, attr)
	}
	l.logger.Error(message, allAttrs...)
}

// Global helper functions

// LogExecution logs execution information using the global logger
func LogExecution(operation string, iteration int, message string, attrs ...slog.Attr) {
	defaultLogger.LogExecution(operation, iteration, message, attrs...)
}

// LogToolExecution logs tool execution information using the global logger
func LogToolExecution(toolName string, success bool, duration time.Duration, attrs ...slog.Attr) {
	defaultLogger.LogToolExecution(toolName, success, duration, attrs...)
}

// LogError logs error information using the global logger
func LogError(operation string, err error, attrs ...slog.Attr) {
	defaultLogger.LogError(operation, err, attrs...)
}

// Info logs informational message using the global logger
func Info(message string, attrs ...slog.Attr) {
	defaultLogger.Info(message, attrs...)
}

// Warn logs warning message using the global logger
func Warn(message string, attrs ...slog.Attr) {
	defaultLogger.Warn(message, attrs...)
}

// Error logs error message using the global logger
func Error(message string, attrs ...slog.Attr) {
	defaultLogger.Error(message, attrs...)
}
