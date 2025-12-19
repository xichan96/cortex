package app

import (
	"log/slog"

	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/mongodb"
	"github.com/xichan96/cortex/pkg/redis"
)

func (a *agent) setupMemory(sessionID string) types.MemoryProvider {
	memCfg := a.config.Memory
	maxHistory := memCfg.MaxHistoryMessages
	if maxHistory <= 0 {
		maxHistory = 100
	}

	switch memCfg.Provider {
	case "redis":
		return a.initRedisMemory(sessionID, maxHistory)
	case "mongodb":
		return a.initMongoDBMemory(sessionID, maxHistory)
	case "simple", "langchain", "":
		return providers.NewSimpleMemoryProviderWithLimit(maxHistory)
	default:
		return providers.NewSimpleMemoryProviderWithLimit(maxHistory)
	}
}

func (a *agent) initRedisMemory(sessionID string, maxHistory int) types.MemoryProvider {
	cfg := a.config.Memory.Redis
	redisCfg := &redis.Config{
		Host:     cfg.Host,
		Port:     cfg.Port,
		DB:       cfg.DB,
		Username: cfg.Username,
		Password: cfg.Password,
	}

	client, err := redis.NewClient(redisCfg)
	if err != nil {
		a.logger.LogError("initRedisMemory", err,
			slog.String("fallback", "simple_memory"),
			slog.String("session_id", sessionID))
		return providers.NewSimpleMemoryProviderWithLimit(maxHistory)
	}

	provider := providers.NewRedisMemoryProviderWithLimit(client, sessionID, maxHistory)
	if cfg.KeyPrefix != "" {
		provider.SetKeyPrefix(cfg.KeyPrefix)
	}
	return provider
}

func (a *agent) initMongoDBMemory(sessionID string, maxHistory int) types.MemoryProvider {
	cfg := a.config.Memory.MongoDB
	opts := []mongodb.ClientOptionFunc{
		mongodb.SetURI(cfg.URI),
		mongodb.SetDatabase(cfg.Database),
	}

	if cfg.Username != "" && cfg.Password != "" {
		opts = append(opts, mongodb.SetBasicAuth(cfg.Username, cfg.Password))
	}

	if cfg.MaxPoolSize > 0 {
		opts = append(opts, mongodb.SetMaxPoolSize(cfg.MaxPoolSize))
	}

	if cfg.MinPoolSize > 0 {
		opts = append(opts, mongodb.SetMinPoolSize(cfg.MinPoolSize))
	}

	client, err := mongodb.NewClient(opts...)
	if err != nil {
		a.logger.LogError("initMongoDBMemory", err,
			slog.String("fallback", "simple_memory"),
			slog.String("session_id", sessionID))
		return providers.NewSimpleMemoryProviderWithLimit(maxHistory)
	}

	provider := providers.NewMongoDBMemoryProviderWithLimit(client, sessionID, maxHistory)
	if cfg.Collection != "" {
		provider.SetCollectionName(cfg.Collection)
	}
	return provider
}
