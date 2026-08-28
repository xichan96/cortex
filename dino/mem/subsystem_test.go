package mem

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/chatstore"
)

func testConfig(dir string) *MemLongTermConfig {
	return &MemLongTermConfig{
		Enabled:             true,
		ToolName:            "memory",
		WriteKnowledgeTag:   "memory_tool_write",
		ExposeSearchIndexes: false,
		IngestInterval:      2 * time.Minute,
		IngestBatchMax:      50,
		IngestMinNew:        2,
		EnableContentFilter: true,
		PromptMaxTokens:     2500,
		Phase2Merge:         true,
		Phase2LLMMerge:      false,
		MaxUnusedDays:       30,
		UseSameLLMForIngest: true,
	}
}

func testPersistDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "dino_sessions")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// 空 LLM（应导致 ingest 抽取被跳过，但 Manager 仍可用）。
type noopLLM struct{}

func (noopLLM) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
	return types.Message{Role: "assistant", Content: "END"}, nil
}
func (noopLLM) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	return nil, nil
}
func (noopLLM) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return types.Message{}, nil
}
func (noopLLM) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	return nil, nil
}
func (noopLLM) GetModelName() string  { return "noop" }
func (noopLLM) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{Name: "noop"}
}

func TestNewLongTermMem(t *testing.T) {
	dir := testPersistDir(t)
	cfg := testConfig(dir)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ltm, err := NewLongTermMem(context.Background(), cfg, noopLLM{}, log, dir, chatstore.DefaultSharedDBFile)
	if err != nil {
		t.Fatalf("NewLongTermMem: %v", err)
	}
	if ltm == nil {
		t.Fatal("nil LongTermMem")
	}
	if ltm.Manager() == nil {
		t.Fatal("nil Manager")
	}
	// 同一路径复用单例。
	ltm2, err := NewLongTermMem(context.Background(), cfg, noopLLM{}, log, dir, chatstore.DefaultSharedDBFile)
	if err != nil {
		t.Fatalf("NewLongTermMem #2: %v", err)
	}
	if ltm.Manager() != ltm2.Manager() {
		t.Fatal("expected same manager singleton for same DB path")
	}
}

func TestLongTermMemStartStopIdempotent(t *testing.T) {
	dir := testPersistDir(t)
	cfg := testConfig(dir)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ltm, err := NewLongTermMem(context.Background(), cfg, noopLLM{}, log, dir, chatstore.DefaultSharedDBFile)
	if err != nil {
		t.Fatalf("NewLongTermMem: %v", err)
	}
	_ = ltm.Start()
	_ = ltm.Start() // 幂等：第二次调用不应重置 cancel
	ltm.Stop()
	ltm.Stop() // 幂等，不 panic
	_ = ltm.Start()
	ltm.Stop() // Stop 后还能重新 Start
}

func TestBuildLayeredPrompt(t *testing.T) {
	dir := testPersistDir(t)
	cfg := testConfig(dir)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ltm, err := NewLongTermMem(context.Background(), cfg, noopLLM{}, log, dir, chatstore.DefaultSharedDBFile)
	if err != nil {
		t.Fatalf("NewLongTermMem: %v", err)
	}
	ctx := context.Background()
	uid := "test-session"

	// 空记忆返回空。
	if got := ltm.BuildLayeredPrompt(ctx, uid); got != "" {
		t.Fatalf("empty memory should produce empty prompt, got: %q", got)
	}

	// 写入偏好 + 知识。
	mgr := ltm.Manager()
	if err := mgr.SetUserPreference(ctx, uid, "user", "name", "喜欢简体中文"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.AddKnowledgeWithCategory(ctx, uid, "该项目用 Go + SQLite", "project", "lang:go"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.AddKnowledgeWithCategory(ctx, uid, "API 基础路径为 /api/v1", "reference", "url:api"); err != nil {
		t.Fatal(err)
	}

	got := ltm.BuildLayeredPrompt(ctx, uid)
	if got == "" {
		t.Fatal("expected non-empty prompt after writes")
	}
	if !contains(got, "# Long-term memory") {
		t.Fatal("missing header")
	}
	if !contains(got, "## Preferences") {
		t.Fatalf("missing preferences header: %s", got)
	}
	if !contains(got, "user.name") {
		t.Fatalf("missing preference line: %s", got)
	}
	if !contains(got, "Go + SQLite") {
		t.Fatalf("missing knowledge line: %s", got)
	}
	if !contains(got, "/api/v1") {
		t.Fatalf("missing reference line: %s", got)
	}
}

func TestBuildLayeredPromptTokenBudget(t *testing.T) {
	dir := testPersistDir(t)
	cfg := testConfig(dir)
	cfg.PromptMaxTokens = 100 // 极小预算
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ltm, err := NewLongTermMem(context.Background(), cfg, noopLLM{}, log, dir, chatstore.DefaultSharedDBFile)
	if err != nil {
		t.Fatalf("NewLongTermMem: %v", err)
	}
	ctx := context.Background()
	uid := "budget-session"
	mgr := ltm.Manager()
	for i := 0; i < 20; i++ {
		_ = mgr.AddKnowledgeWithCategory(ctx, uid, "这是一条很长的知识条目用于测试 token 预算上限是否生效第N条", "project")
		_ = mgr.SetUserPreference(ctx, uid, "user", "k", "这是偏好值用于测试 token 预算")
	}
	got := ltm.BuildLayeredPrompt(ctx, uid)
	// 不崩溃、不超过预算（EstimateTokens 粗估，这里仅验证非空且有限）。
	if got == "" {
		t.Fatal("expected non-empty prompt")
	}
	if len(got) > cfg.PromptMaxTokens*4 {
		t.Fatalf("prompt too long for budget: %d chars", len(got))
	}
}

func TestRunPhase2MergeClaimAndPrune(t *testing.T) {
	dir := testPersistDir(t)
	cfg := testConfig(dir)
	cfg.MaxUnusedDays = 30
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ltm, err := NewLongTermMem(context.Background(), cfg, noopLLM{}, log, dir, chatstore.DefaultSharedDBFile)
	if err != nil {
		t.Fatalf("NewLongTermMem: %v", err)
	}
	ctx := context.Background()
	uid := "phase2-session"
	mgr := ltm.Manager()

	// 写入：一条 40 天前的未用条目 + 一条新条目（重复内容）。
	if err := mgr.AddKnowledgeWithCategory(ctx, uid, "旧条目应被剪枝", "project", "old"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.AddKnowledgeWithCategory(ctx, uid, "用户使用 Go", "project", "lang"); err != nil {
		t.Fatal(err)
	}
	// 直接把第一条的 updated_at 改旧。
	db, err := mgrDB(ctx, mgr)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-40 * 24 * time.Hour)
	if _, err := db.ExecContext(ctx,
		`UPDATE knowledge SET updated_at = ? WHERE tags = 'old'`, old); err != nil {
		t.Fatal(err)
	}

	// 手动触发一次 Phase 2。
	runPhase2Merge(ctx, log, mgr, func(c context.Context) (types.LLMProvider, error) {
		return noopLLM{}, nil
	}, cfg)

	// 40 天前的未用条目应被剪枝。
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge WHERE user_id = ?`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row after prune (only Go entry), got %d", n)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
