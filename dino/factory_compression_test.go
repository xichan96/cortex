package dino

import (
	"context"
	"strings"
	"testing"

	"github.com/xichan96/cortex/dino/chatstore"
)

// memProviderOf 取出 CreateSession 装配的 memoryAdapter 的底层 provider。
func memProviderOf(t *testing.T, f DinoFactory, sessionID string) chatstore.Provider {
	t.Helper()
	sess := f.GetSession(sessionID)
	if sess == nil {
		t.Fatalf("session %q not found", sessionID)
	}
	adapter, ok := sess.GetAgent().GetMemory().(*memoryAdapter)
	if !ok {
		t.Fatalf("expected *memoryAdapter, got %T", sess.GetAgent().GetMemory())
	}
	return adapter.provider
}

func TestFactory_CreateSession_HybridWrapped(t *testing.T) {
	cfg := getTestConfig()
	// getTestConfig 默认 EnableLLMCompress=true（DefaultConfig）。
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	ctx := context.Background()
	if _, err := factory.CreateSession(ctx, "hybrid-session"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	provider := memProviderOf(t, factory, "hybrid-session")
	h, ok := provider.(*chatstore.Hybrid)
	if !ok {
		t.Fatalf("EnableLLMCompress 默认 true 时应包裹 Hybrid，got %T", provider)
	}
	if !h.TailSummaryEnabled() {
		t.Fatal("Hybrid 应默认尾部注入")
	}
}

func TestFactory_CreateSession_NoHybridWhenDisabled(t *testing.T) {
	cfg := getTestConfig()
	cfg.Memory.EnableLLMCompress = false
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	if _, err := factory.CreateSession(context.Background(), "plain-session"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	provider := memProviderOf(t, factory, "plain-session")
	if _, ok := provider.(*chatstore.Hybrid); ok {
		t.Fatal("EnableLLMCompress=false 时不应包裹 Hybrid")
	}
}

// TestFactory_GetSummary_SingleInjectionSource 断言「单一摘要注入」：
// Hybrid 活跃时 memoryAdapter.GetSummary 返回空（engine 头部注入禁用），
// 摘要只由 Hybrid.GetMessages 尾部注入。
func TestFactory_GetSummary_SingleInjectionSource(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	ctx := context.Background()
	if _, err := factory.CreateSession(ctx, "single-src"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	provider := memProviderOf(t, factory, "single-src")
	h, ok := provider.(*chatstore.Hybrid)
	if !ok {
		t.Fatalf("expected *Hybrid, got %T", provider)
	}

	// 直接经 memoryAdapter 验证头部注入被禁用
	adapter := &memoryAdapter{provider: provider}
	if got, _ := adapter.GetSummary(ctx); got != "" {
		t.Fatalf("Hybrid 尾部注入活跃时 memoryAdapter.GetSummary 应返回空，got %q", got)
	}

	// 尾部注入仍在：GetChatHistory 末尾带 [Summary]
	h.SetSummaryForTest("测试摘要")
	hist, err := adapter.GetChatHistory(ctx)
	if err != nil {
		t.Fatalf("GetChatHistory: %v", err)
	}
	if len(hist) == 0 {
		t.Fatal("history 应为空（未写消息）但摘要尾部追加后非空")
	}
	last := hist[len(hist)-1]
	if last.Role != "user" || !strings.HasPrefix(last.Content, chatstore.SummaryMarker) {
		t.Fatalf("history 末尾应为 [Summary] user 消息，got role=%q content=%q", last.Role, last.Content)
	}
}
