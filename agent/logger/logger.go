package logger

import (
	"log/slog"
	"time"
)

// Logger structured logger
type Logger struct {
	logger *slog.Logger
}

// NewLogger creates a new logger
func NewLogger() *Logger {
	return &Logger{
		logger: slog.Default(),
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
