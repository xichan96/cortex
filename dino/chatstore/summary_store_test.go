package chatstore

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// storeBackedProvider wraps a Provider and implements SummaryStore, mimicking a
// host-injected database-backed provider.
type storeBackedProvider struct {
	Provider
	mu          sync.Mutex
	summary     string
	lastSession string
	saves       int
	loads       int
}

func (p *storeBackedProvider) LoadSummary(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loads++
	p.lastSession = SummarySessionFromContext(ctx)
	return p.summary, nil
}

func (p *storeBackedProvider) SaveSummary(ctx context.Context, summary string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.saves++
	p.summary = summary
	p.lastSession = SummarySessionFromContext(ctx)
	return nil
}

func (p *storeBackedProvider) session() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastSession
}

func (p *storeBackedProvider) storedSummary() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.summary
}

func (p *storeBackedProvider) saveCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.saves
}

// compactCfg 让 3 条消息压缩后留下 2 条、older 非空（确定性/LLM 摘要都有输入）。
func compactCfg() *Config {
	cfg := DefaultConfig()
	cfg.EnableMemoryCompress = false
	cfg.KeepRecentCount = 2
	cfg.MaxRecentTailTokens = 2
	return cfg
}

func compactTestMessages() []Message {
	return []Message{
		{Role: "user", Content: "user one message"}, // token 估算超过预算 2，留在 older
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "u2"},
	}
}

func TestHybrid_SummaryStore_PersistsOnCompress(t *testing.T) {
	cfg := compactCfg()
	store := &storeBackedProvider{Provider: NewInMemory("s1", cfg)}
	h := NewHybrid("s1", store, &mockSummaryLLM{summary: "持久摘要"}, cfg)

	addMsgs(t, h, compactTestMessages())
	if err := h.Compress(context.Background()); err != nil {
		t.Fatalf("Compress: %v", err)
	}

	if got := store.storedSummary(); got != "持久摘要" {
		t.Fatalf("persisted summary = %q, want %q", got, "持久摘要")
	}
	if got := store.session(); got != "s1" {
		t.Fatalf("SummaryStore call session = %q, want %q", got, "s1")
	}
	if got, _ := h.GetSummary(context.Background()); got != "持久摘要" {
		t.Fatalf("Hybrid summary = %q, want %q", got, "持久摘要")
	}
	if !h.LastCompactUsedLLM() {
		t.Fatal("LLM 产出摘要时应报告 UsedLLM=true")
	}
}

func TestHybrid_SummaryStore_RestoresOnConstruction(t *testing.T) {
	cfg := compactCfg()
	store := &storeBackedProvider{Provider: NewInMemory("s1", cfg)}
	h := NewHybrid("s1", store, &mockSummaryLLM{summary: "重启后的摘要"}, cfg)
	addMsgs(t, h, compactTestMessages())
	if err := h.Compress(context.Background()); err != nil {
		t.Fatalf("Compress: %v", err)
	}

	// 用同一个 store 新建 Hybrid：摘要应被加载并通过 GetMessages 尾部注入。
	h2 := NewHybrid("s1", store, nil, cfg)
	if got, _ := h2.GetSummary(context.Background()); got != "重启后的摘要" {
		t.Fatalf("restored summary = %q, want %q", got, "重启后的摘要")
	}
	msgs, err := h2.GetMessages(context.Background(), 0)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected restored tail messages")
	}
	last := msgs[len(msgs)-1]
	if !strings.HasPrefix(last.Content, SummaryMarker) || !strings.Contains(last.Content, "重启后的摘要") {
		t.Fatalf("last message should carry the restored summary, got %q", last.Content)
	}
}

func TestHybrid_SummaryStore_ClearWipesPersistedSummary(t *testing.T) {
	cfg := compactCfg()
	store := &storeBackedProvider{Provider: NewInMemory("s1", cfg)}
	store.summary = "旧摘要"

	h := NewHybrid("s1", store, nil, cfg)
	if got, _ := h.GetSummary(context.Background()); got != "旧摘要" {
		t.Fatalf("summary before clear = %q, want %q", got, "旧摘要")
	}

	if err := h.Clear(context.Background()); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if got := store.storedSummary(); got != "" {
		t.Fatalf("persisted summary after Clear = %q, want empty", got)
	}
	if store.saveCount() == 0 {
		t.Fatal("Clear 应通过 SummaryStore 保存空摘要")
	}

	h2 := NewHybrid("s1", store, nil, cfg)
	if got, _ := h2.GetSummary(context.Background()); got != "" {
		t.Fatalf("restored summary after Clear = %q, want empty", got)
	}
}

func TestHybrid_NoSummaryStore_BehaviorUnchanged(t *testing.T) {
	cfg := compactCfg()
	// NewInMemory 不实现 SummaryStore。
	h := NewHybrid("s1", NewInMemory("s1", cfg), &mockSummaryLLM{summary: "内存摘要"}, cfg)

	addMsgs(t, h, compactTestMessages())
	if err := h.Compress(context.Background()); err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if got, _ := h.GetSummary(context.Background()); got != "内存摘要" {
		t.Fatalf("summary = %q, want %q", got, "内存摘要")
	}

	// Clear 仍正常工作（无持久化路径）。
	if err := h.Clear(context.Background()); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if got, _ := h.GetSummary(context.Background()); got != "" {
		t.Fatalf("summary after Clear = %q, want empty", got)
	}
}

func TestSQLite_SummaryStore(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PersistDirectory = t.TempDir()
	cfg.SQLiteFile = "summary_store_test.db"

	p, err := NewSQLite("session-summary-store", cfg)
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	store, ok := p.(SummaryStore)
	if !ok {
		t.Fatalf("%T does not implement SummaryStore", p)
	}

	ctx := context.Background()
	if err := store.SaveSummary(ctx, "sqlite 摘要"); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	if got, err := store.LoadSummary(ctx); err != nil || got != "sqlite 摘要" {
		t.Fatalf("LoadSummary = (%q, %v), want (%q, nil)", got, err, "sqlite 摘要")
	}

	// 空摘要等于「没有摘要」。
	if err := store.SaveSummary(ctx, ""); err != nil {
		t.Fatalf("SaveSummary(empty): %v", err)
	}
	if got, err := store.LoadSummary(ctx); err != nil || got != "" {
		t.Fatalf("LoadSummary after clear = (%q, %v), want (\"\", nil)", got, err)
	}
}
