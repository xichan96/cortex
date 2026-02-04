package sql

import (
	"log"
	"os"
	"time"

	"gorm.io/gorm/logger"
)

const (
	defaultLevel         = logger.Info
	defaultSlowThreshold = time.Duration(1)
)

const (
	InfoLevel    = "info"
	WarnLevel    = "warn"
	WarningLevel = "warning"
	ErrorLevel   = "error"
	SilentLevel  = "silent"
)

var levelM = map[string]logger.LogLevel{
	InfoLevel:    logger.Info,
	WarnLevel:    logger.Warn,
	WarningLevel: logger.Warn,
	ErrorLevel:   logger.Error,
	SilentLevel:  logger.Silent,
}

var stdoutLogger = log.New(os.Stdout, "\r\n", log.LstdFlags)

type LogConfig struct {
	Level         string        `json:"level,omitempty"`
	SlowThreshold time.Duration `json:"slow_threshold,omitempty"`
}

func NewLogger(cfg *LogConfig) logger.Interface {
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
	loggerCfg := logger.Config{
		SlowThreshold: slow * time.Second,
		LogLevel:      level,
	}
	return logger.New(stdoutLogger, loggerCfg)
}

var SilentLogger = NewLogger(&LogConfig{
	Level:         SilentLevel,
	SlowThreshold: defaultSlowThreshold,
})
