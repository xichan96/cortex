// Package redis 对于redis进行封装
package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	cerrors "github.com/xichan96/cortex/pkg/errors"
)

// redis默认超时时间
const (
	defaultIdleTimeout = time.Second * 120
	defaultIdleConns   = 3
	defaultActiveConns = 10
)

// Config 表示redis配置项
type Config struct {
	Host             string     `json:"host"`
	Port             int        `json:"port"`
	DB               int        `json:"db"`
	Username         string     `json:"username,omitempty"`
	Password         string     `json:"password,omitempty"`
	KeyPrefix        string     `json:"key_prefix,omitempty"` // 这个值只提供给反序列化使用，对client实际无意义
	DisableLogHook   bool       `json:"disable_log_hook,omitempty"`
	DisableErrorHook bool       `json:"disable_error_hook,omitempty"`
	LogConfig        *LogConfig `json:"log_level,dive,omitempty"`
}

// Client 对redis client进行封装
type Client struct {
	*redis.Client
}

type Option func(*redis.Options)

// NewClient 初始化redisClient
func NewClient(cfg *Config, ops ...Option) (c *Client, err error) {

	options := redis.Options{
		Addr:            fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Username:        cfg.Username,
		Password:        cfg.Password,
		DB:              cfg.DB,
		MaxIdleConns:    defaultIdleConns,
		MaxActiveConns:  defaultActiveConns,
		ConnMaxIdleTime: defaultIdleTimeout,
	}

	for _, op := range ops {
		op(&options)
	}

	var client *redis.Client
	client = redis.NewClient(&options)

	if !cfg.DisableErrorHook {
		client.AddHook(NewErrorHook())
	}
	if !cfg.DisableLogHook {
		client.AddHook(NewLogHook(cfg.LogConfig))
	}

	c = &Client{
		Client: client,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	if _, err := c.Ping(ctx).Result(); err != nil {
		e := fmt.Sprintf("ping redis %s:%d %s", cfg.Host, cfg.Port, err.Error())
		return nil, cerrors.NewError(cerrors.EC_CONNECTION_FAILED.Code, e).Wrap(err)
	}

	return c, nil
}
