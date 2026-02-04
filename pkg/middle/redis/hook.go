// Package redis 对于redis进行封装
package redis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	cerrors "github.com/xichan96/cortex/pkg/errors"
	"github.com/xichan96/cortex/pkg/logger"
)

const (
	defaultMaxValSize = 200
	defaultLogLevel   = "info"
)

type LogConfig struct {
	Level      string `json:"level,omitempty"`
	MaxValSize int    `json:"max_val_size,omitempty"`
}

// NewLogHook is 初始化日志hook
func NewLogHook(cfg *LogConfig) redis.Hook {
	if cfg == nil {
		cfg = &LogConfig{}
	}

	if len(cfg.Level) == 0 {
		cfg.Level = defaultLogLevel
	}
	if cfg.MaxValSize <= 0 {
		cfg.MaxValSize = defaultMaxValSize
	}

	return &LoggerHook{
		logger: logger.NewLogger(),
		cfg:    cfg,
	}
}

// LoggerHook is 日志
type LoggerHook struct {
	logger *logger.Logger
	cfg    *LogConfig
}

// DialHook 实现dial钩子
func (r *LoggerHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

// ProcessHook 实现处理钩子
func (r *LoggerHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {

	return func(ctx context.Context, cmd redis.Cmder) error {
		startTime := time.Now()

		nextErr := next(ctx, cmd)

		var logErr error

		if nextErr != nil {
			logErr = nextErr
		} else if cmd.Err() != nil {
			logErr = cmd.Err()
		}

		if r.cfg.Level != "debug" && r.cfg.Level != "info" && logErr == nil {
			return nextErr
		}

		var b strings.Builder
		var placeholder = "(value[%d])"
		b.WriteString(cmd.Args()[0].(string))
		for _, c := range cmd.Args()[1:] {
			var val string
			bytes, ok := c.([]byte)

			if ok {
				if len(bytes) <= r.cfg.MaxValSize {
					val = fmt.Sprintf("%s", bytes)
				} else {
					val = fmt.Sprintf(placeholder, len(bytes))
				}
			} else {
				val = fmt.Sprintf("%v", c)
				if len(val) > r.cfg.MaxValSize {
					val = fmt.Sprintf(placeholder, len(val))
				}
			}

			b.WriteString(" ")
			b.WriteString(val)
		}

		cost := roundFloat(time.Since(startTime).Seconds(), 3)
		attrs := []slog.Attr{
			slog.String("command", b.String()),
			slog.Float64("cost", cost),
		}

		if logErr != nil {
			attrs = append(attrs, slog.String("error", logErr.Error()))
			r.logger.LogError("redis command", logErr, attrs...)
		} else {
			r.logger.Info("redis command", attrs...)
		}

		return nextErr
	}
}

// ProcessPipelineHook 实现流水线钩子
func (r *LoggerHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		return next(ctx, cmds)
	}
}

// NewErrorHook 初始化错误hook
func NewErrorHook() *ErrorHook {
	return &ErrorHook{}
}

// ErrorHook 错误hook
type ErrorHook struct {
}

// DialHook 实现Dial钩子
func (r *ErrorHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

// ProcessHook 实现处理钩子
func (r *ErrorHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		return WrapRedisErr(next(ctx, cmd))
	}
}

// ProcessPipelineHook 实现流水线钩子
func (r *ErrorHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		return WrapRedisErr(next(ctx, cmds))
	}
}

// WrapRedisErr 包装redis错误
func WrapRedisErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, redis.Nil) {
		return cerrors.EC_DATA_NOT_FOUND
	}
	return cerrors.NewError(cerrors.EC_INTERNAL_ERROR.Code, "redis error").Wrap(err)
}

func roundFloat(val float64, precision int) float64 {
	multiplier := math.Pow(10, float64(precision))
	return math.Round(val*multiplier) / multiplier
}
