package sql

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	gormlogger "gorm.io/gorm/logger"

	"github.com/xichan96/cortex/pkg/logger"
)

const (
	defaultLevel         = gormlogger.Info
	defaultSlowThreshold = time.Duration(1)
)

const (
	InfoLevel    = "info"
	WarnLevel    = "warn"
	WarningLevel = "warning"
	ErrorLevel   = "error"
	SilentLevel  = "silent"
)

var levelM = map[string]gormlogger.LogLevel{
	InfoLevel:    gormlogger.Info,
	WarnLevel:    gormlogger.Warn,
	WarningLevel: gormlogger.Warn,
	ErrorLevel:   gormlogger.Error,
	SilentLevel:  gormlogger.Silent,
}

type LogConfig struct {
	Level         string        `json:"level,omitempty"`
	SlowThreshold time.Duration `json:"slow_threshold,omitempty"`
}

type GormLogger struct {
	LogLevel      gormlogger.LogLevel
	SlowThreshold time.Duration
}

func NewLogger(cfg *LogConfig) gormlogger.Interface {
	if cfg == nil {
		cfg = &LogConfig{}
	}
	level, ok := levelM[cfg.Level]
	if !ok {
		level = defaultLevel
	}
	slow := cfg.SlowThreshold
	if slow <= 0 {
		slow = defaultSlowThreshold
	}

	return &GormLogger{
		LogLevel:      level,
		SlowThreshold: slow * time.Second,
	}
}

var SilentLogger = NewLogger(&LogConfig{
	Level:         SilentLevel,
	SlowThreshold: defaultSlowThreshold,
})

// LogMode implementation
func (l *GormLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	newLogger := *l
	newLogger.LogLevel = level
	return &newLogger
}

// Info implementation
func (l *GormLogger) Info(ctx context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= gormlogger.Info {
		logger.Info(fmt.Sprintf(msg, data...))
	}
}

// Warn implementation
func (l *GormLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= gormlogger.Warn {
		logger.Warn(fmt.Sprintf(msg, data...))
	}
}

// Error implementation
func (l *GormLogger) Error(ctx context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= gormlogger.Error {
		logger.Error(fmt.Sprintf(msg, data...))
	}
}

// Trace implementation
func (l *GormLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.LogLevel <= gormlogger.Silent {
		return
	}

	elapsed := time.Since(begin)
	switch {
	case err != nil && l.LogLevel >= gormlogger.Error && !errors.Is(err, gormlogger.ErrRecordNotFound):
		sql, rows := fc()
		logger.Error("Trace", slog.String("err", err.Error()), slog.String("sql", sql), slog.Int64("rows", rows), slog.Duration("elapsed", elapsed))
	case elapsed > l.SlowThreshold && l.SlowThreshold != 0 && l.LogLevel >= gormlogger.Warn:
		sql, rows := fc()
		logger.Warn("Slow SQL", slog.String("sql", sql), slog.Int64("rows", rows), slog.Duration("elapsed", elapsed))
	case l.LogLevel == gormlogger.Info:
		sql, rows := fc()
		logger.Info("Trace", slog.String("sql", sql), slog.Int64("rows", rows), slog.Duration("elapsed", elapsed))
	}
}
