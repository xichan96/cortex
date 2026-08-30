package dino

import (
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

// registerCustomProvider registers a throwaway custom provider under a unique
// name and returns it. Unique names keep these tests independent of the shared
// registry, which other tests mutate (e.g. TestClearProviderRegistry).
func registerCustomProvider(t *testing.T) string {
	t.Helper()
	name := "cfgtest-custom"
	RegisterLLMProvider(name, func(cfg *Config) (types.LLMProvider, error) {
		return newMockLLMProvider([]string{"test response"}), nil
	})
	return name
}

func TestValidateConfig_Nil(t *testing.T) {
	if err := ValidateConfig(nil); err != nil {
		t.Fatalf("nil config should validate clean, got %v", err)
	}
}

// 合法配置（DefaultConfig + 显式 model）必须通过。
func TestValidateConfig_Valid(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DefaultModel = "gpt-4o-mini"
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

// MCP / memory 等其它配置项不应被 provider 校验误报（ValidateConfig 只碰 provider）。
func TestValidateConfig_OtherSectionsNotFlagged(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Memory.PersistEnabled = true
	cfg.Memory.Type = "sqlite"
	cfg.MCP.Enabled = true
	cfg.MCP.Servers = map[string]MCPServerConfig{
		"files": {Type: "stdio", Command: "ls"},
	}
	cfg.LongTermMemory.Enabled = true
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("MCP/memory/long-term-memory config must not trip provider validation: %v", err)
	}
}

// Provider.Type 为空 → 报错且列出支持列表。
func TestValidateConfig_EmptyProviderType(t *testing.T) {
	cfg := &Config{Provider: ProviderConfig{Type: ""}}
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("empty provider type must error")
	}
	if !strings.Contains(err.Error(), "supported:") {
		t.Errorf("error should mention supported provider list, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "openai") {
		t.Errorf("supported list should include a builtin provider, got %q", err.Error())
	}
}

// Provider.Type 未知 → 报错且列出支持列表。
func TestValidateConfig_UnknownProviderType(t *testing.T) {
	cfg := &Config{Provider: ProviderConfig{Type: "cfgtest-does-not-exist"}}
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("unknown provider type must error")
	}
	if !strings.Contains(err.Error(), "unknown llm provider type") {
		t.Errorf("error should mention unknown type, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "supported:") {
		t.Errorf("error should mention supported provider list, got %q", err.Error())
	}
}

// 自定义/已注册 provider 不强制 DefaultModel（代理/网关可能不依赖 model）。
func TestValidateConfig_CustomProviderNoModel(t *testing.T) {
	name := registerCustomProvider(t)
	cfg := &Config{Provider: ProviderConfig{Type: name}}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("custom provider without DefaultModel must pass, got %v", err)
	}
}

// 内置 provider 但 DefaultModel 为空 → 报错。
func TestValidateConfig_BuiltinEmptyModel(t *testing.T) {
	cfg := &Config{Provider: ProviderConfig{Type: "openai"}}
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("builtin provider with empty DefaultModel must error")
	}
	if !strings.Contains(err.Error(), "default model is empty") {
		t.Errorf("error should mention empty default model, got %q", err.Error())
	}
}

func TestValidateConfig_BuiltinWithModel(t *testing.T) {
	for _, typ := range []string{"openai", "anthropic", "deepseek", "volce"} {
		cfg := &Config{Provider: ProviderConfig{Type: typ}, DefaultModel: "some-model"}
		if err := ValidateConfig(cfg); err != nil {
			t.Errorf("builtin provider %q with model must pass, got %v", typ, err)
		}
	}
}
