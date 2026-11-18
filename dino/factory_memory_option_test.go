package dino

import (
	"context"
	"sync"
	"testing"

	"github.com/xichan96/cortex/dino/chatstore"
)

// fakeMemoryProvider 记录 AddMessage，用于验证 WithMemoryProvider 注入线路。
type fakeMemoryProvider struct {
	mu       sync.Mutex
	messages []chatstore.Message
}

func (f *fakeMemoryProvider) AddMessage(ctx context.Context, msg chatstore.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, msg)
	return nil
}

func (f *fakeMemoryProvider) GetMessages(ctx context.Context, limit int) ([]chatstore.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]chatstore.Message(nil), f.messages...), nil
}

func (f *fakeMemoryProvider) GetSummary(ctx context.Context) (string, error) { return "", nil }
func (f *fakeMemoryProvider) Compress(ctx context.Context) error             { return nil }

func (f *fakeMemoryProvider) Clear(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = nil
	return nil
}

func (f *fakeMemoryProvider) GetStats() chatstore.Stats { return chatstore.Stats{} }

func (f *fakeMemoryProvider) StoredMessageCount(ctx context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.messages), nil
}

func (f *fakeMemoryProvider) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.messages)
}

func (f *fakeMemoryProvider) last() chatstore.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.messages) == 0 {
		return chatstore.Message{}
	}
	return f.messages[len(f.messages)-1]
}

// TestWithMemoryProvider 注入 fake provider（默认 EnableLLMCompress 开启 → Hybrid 包裹），
// 经 memoryAdapter 写消息应落到 fake 上，且不创建 SQLite/InMemory。
func TestWithMemoryProvider(t *testing.T) {
	cfg := getTestConfig()
	// 故意配置 sqlite + 持久化：注入后这两项必须失效。
	cfg.Memory.Type = "sqlite"
	cfg.Memory.PersistEnabled = true
	cfg.Memory.PersistDirectory = t.TempDir()

	fake := &fakeMemoryProvider{}
	factory, err := NewDinoFactory(cfg, WithMemoryProvider(fake))
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	ctx := context.Background()
	if _, err := factory.CreateSession(ctx, "injected-memory"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	provider := memProviderOf(t, factory, "injected-memory")
	// 默认 EnableLLMCompress=true → 注入的 provider 被 Hybrid 包裹。
	if _, ok := provider.(*chatstore.Hybrid); !ok {
		t.Fatalf("EnableLLMCompress 默认 true 时应包裹 Hybrid，got %T", provider)
	}

	// 经 memoryAdapter 写入（Hybrid → fake）。
	adapter, ok := factory.GetSession("injected-memory").GetAgent().GetMemory().(*memoryAdapter)
	if !ok {
		t.Fatalf("expected *memoryAdapter, got %T", factory.GetSession("injected-memory").GetAgent().GetMemory())
	}
	if err := adapter.SaveContext(ctx, map[string]interface{}{"input": "hello", "role": "user"}, nil); err != nil {
		t.Fatalf("SaveContext: %v", err)
	}
	if fake.count() != 1 {
		t.Fatalf("注入 provider 应收到 1 条消息，got %d", fake.count())
	}
	if got := fake.last(); got.Content != "hello" || got.Role != "user" {
		t.Fatalf("unexpected message: role=%q content=%q", got.Role, got.Content)
	}
}

// TestWithMemoryProvider_NoHybrid：关闭 LLM 压缩时注入 provider 直接使用。
func TestWithMemoryProvider_NoHybrid(t *testing.T) {
	cfg := getTestConfig()
	cfg.Memory.EnableLLMCompress = false

	fake := &fakeMemoryProvider{}
	factory, err := NewDinoFactory(cfg, WithMemoryProvider(fake))
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	if _, err := factory.CreateSession(context.Background(), "injected-plain"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	provider := memProviderOf(t, factory, "injected-plain")
	if provider != chatstore.Provider(fake) {
		t.Fatalf("EnableLLMCompress=false 时应直接使用注入 provider，got %T", provider)
	}

	adapter := &memoryAdapter{provider: provider}
	if err := adapter.SaveContext(context.Background(), map[string]interface{}{"input": "ping"}, nil); err != nil {
		t.Fatalf("SaveContext: %v", err)
	}
	if fake.count() != 1 || fake.last().Content != "ping" {
		t.Fatalf("fake 应收到 1 条 \"ping\"，got %d %+v", fake.count(), fake.last())
	}
}
