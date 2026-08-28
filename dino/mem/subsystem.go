package mem

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/memkit"
	memsqlite "github.com/xichan96/cortex/pkg/memkit/sqlite"
	"github.com/xichan96/cortex/pkg/memkit/utils"
)

// LongTermMem 是长期记忆的 live 子系统句柄。
//
// 语义（评审 B1）：当前运行时是 **per-session 记忆** —— 全仓没有任何代码写
// metadata.user_id，getGlobalUserID 永远回退到 sessionID。因此所有读写都以
// sessionID 作为 uid 锚定，跨 session 共享是未来「user 全局合并」独立任务，
// 不在本子系统内假设。
//
// LongTermMem 只持有全局的 Manager + 配置 + LLM factory，不持有任何 session
// 状态；session 粒度只体现为调用方传入的 uid。
type LongTermMem struct {
	cfg         *MemLongTermConfig
	persistDir  string
	persistFile string
	mgr         memkit.Manager
	log         *slog.Logger
	newLLM      func(ctx context.Context) (types.LLMProvider, error)

	mu     sync.Mutex // Start/Stop 幂等
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewLongTermMem 构造长期记忆子系统。mgr 复用进程内单例
// （SharedSQLiteManager 同 DB key 复用），不会开第二个 SQLite 连接。
// persistDir/persistFile 来自 cfg.Memory（长期记忆与短期记忆共享同一 SQLite）。
func NewLongTermMem(ctx context.Context, cfg *MemLongTermConfig, llm types.LLMProvider, log *slog.Logger, persistDir, persistFile string) (*LongTermMem, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	if log == nil {
		log = slog.Default()
	}
	mgr, err := SharedSQLiteManager(persistDir, persistFile)
	if err != nil {
		return nil, fmt.Errorf("open memory manager: %w", err)
	}
	if mgr == nil {
		return nil, fmt.Errorf("memory manager is nil")
	}

	l := &LongTermMem{
		cfg:         cfg,
		persistDir:  persistDir,
		persistFile: persistFile,
		mgr:         mgr,
		log:         log,
	}
	setProbePaths(persistDir, persistFile)
	l.newLLM = func(ctx context.Context) (types.LLMProvider, error) {
		if llm == nil {
			return nil, fmt.Errorf("long-term memory: llm provider not available")
		}
		return llm, nil
	}

	return l, nil
}

// Manager 返回共享 memkit.Manager（进程内单例）。
func (l *LongTermMem) Manager() memkit.Manager { return l.mgr }

// Start 启动后台 ingest loop（Phase 1）。幂等：重复调用无副作用。
// 返回 cancel 供调用方提前停止。
func (l *LongTermMem) Start() context.CancelFunc {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		return l.cancel
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	interval := l.cfg.IngestInterval
	if interval < 5*time.Second {
		interval = 2 * time.Minute
	}
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		RunIngestLoop(ctx, IngestLoopOptions{
			WaitReady: func(ctx context.Context) error { return nil },
			StartParams: func() (string, string, time.Duration, error) {
				return l.persistDir, l.persistFile, l.cfg.IngestInterval, nil
			},
			TickParams: func() (bool, int, int, IngestRuntime) {
				return l.cfg.Enabled, l.cfg.IngestBatchMax, l.cfg.IngestMinNew, l.runtime()
			},
			NewLLM: func(ctx context.Context, _ IngestRuntime) (types.LLMProvider, error) {
				return l.newLLM(ctx)
			},
		})
	}()
	if l.cfg.Phase2Merge {
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			runPhase2Loop(ctx, l.log, l.mgr, l.newLLM, l.cfg)
		}()
	}
	return cancel
}

// Stop 幂等停止后台 goroutine 并等待退出。
func (l *LongTermMem) Stop() {
	l.mu.Lock()
	cancel := l.cancel
	l.cancel = nil
	l.mu.Unlock()
	if cancel != nil {
		cancel()
		l.wg.Wait()
	}
}

func (l *LongTermMem) runtime() IngestRuntime {
	return IngestRuntime{
		Rules: nil,
		SystemExtra: l.cfg.SystemExtra + "\nNo-op is preferred over low-signal writes: " +
			"if nothing is durable or reusable (one-off queries, generic status updates, temporary facts, common knowledge), " +
			"output exactly one line: END.",
		ContentFilter: IngestContentFilter{
			EnableContentFilter: l.cfg.EnableContentFilter,
		},
	}
}

// IngestNow 手动触发一次 Phase 1 ingest（测试/内部用）。
func (l *LongTermMem) IngestNow(ctx context.Context) error {
	runIngestOnce(ctx, l.log, func(ctx context.Context, _ IngestRuntime) (types.LLMProvider, error) {
		return l.newLLM(ctx)
	}, l.persistDir, l.persistFile, l.cfg.IngestBatchMax, l.cfg.IngestMinNew, l.runtime())
	return nil
}

// MemoryToolsForSession 构造当前 session 的模型可见记忆工具列表。
// 内部复用 SharedSQLiteManager 单例，不会开第二个连接。
func (l *LongTermMem) MemoryToolsForSession(sessionID string, opts ...MemoryToolOption) []types.Tool {
	o := MemoryToolOptions{
		SessionID:           sessionID,
		PersistDir:          l.persistDir,
		SQLiteFile:          l.persistFile,
		Log:                 l.log,
		ToolName:            l.cfg.ToolName,
		WriteKnowledgeTag:   l.cfg.WriteKnowledgeTag,
		Description:         l.cfg.ToolDescription,
		ExposeSearchIndexes: l.cfg.ExposeSearchIndexes,
	}
	for _, f := range opts {
		f(&o)
	}
	return MemoryTools(o)
}

// MemoryToolOption 允许按 session 覆盖 MemoryTools 参数。
type MemoryToolOption func(*MemoryToolOptions)

func WithOmitTimeTool(v bool) MemoryToolOption {
	return func(o *MemoryToolOptions) { o.OmitTimeTool = v }
}

// WithToolNameOverride 覆盖默认工具名。
func WithToolNameOverride(name string) MemoryToolOption {
	return func(o *MemoryToolOptions) { o.ToolName = name }
}

// BuildLayeredPrompt 构造分层披露的 L1 摘要块（总是加载）。
//
// 分层（评审 B2）：priority 全为 PriorityMedium 无区分度，因此 L1 的
// knowledge 选条按 `updated_at DESC` + 每 category 限额，而不是 priority。
// L1 = 所有 preferences + 每 category 最近更新的 N 条 knowledge 摘要。
// 总长受 PromptMaxTokens 硬顶（默认 2500 token）。
func (l *LongTermMem) BuildLayeredPrompt(ctx context.Context, uid string) string {
	if l == nil || l.mgr == nil {
		return ""
	}
	maxTokens := l.cfg.PromptMaxTokens
	if maxTokens <= 0 {
		maxTokens = 2500
	}
	const (
		maxPrefLineLen = 200
		maxKnowBodyLen = 300
		perCatCap      = 3
	)

	var parts []string
	tokens := 0
	addLine := func(s string) bool {
		t := utils.EstimateTokens(s)
		if tokens+t > maxTokens {
			return false
		}
		parts = append(parts, s)
		tokens += t
		return true
	}

	prefs, err := l.mgr.GetUserPreferences(ctx, uid)
	if err == nil && len(prefs) > 0 {
		// map 迭代无序；先排序保证输出稳定。
		keys := make([]string, 0, len(prefs))
		for k := range prefs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if addLine("## Preferences") {
			for _, k := range keys {
				v := prefs[k]
				if len(v) > maxPrefLineLen {
					v = v[:maxPrefLineLen] + "..."
				}
				if !addLine("- " + k + ": " + v) {
					break
				}
			}
		}
	}

	// knowledge：按 category 分组，每 group 取最近更新的前 perCatCap 条。
	res, err := l.mgr.SearchKnowledge(ctx, uid, "", 200)
	if err == nil && len(res) > 0 {
		byCat := make(map[string][]memkit.MemoryItem)
		for _, it := range res {
			cat := it.Key
			if cat == "" {
				cat = "general"
			}
			byCat[cat] = append(byCat[cat], it)
		}
		// SearchKnowledge 已按 updated_at DESC 排序（SQL 层），
		// 这里再做一次防御性排序。
		for _, items := range byCat {
			sort.Slice(items, func(i, j int) bool {
				return items[i].UpdatedAt.After(items[j].UpdatedAt)
			})
		}
		cats := make([]string, 0, len(byCat))
		for c := range byCat {
			cats = append(cats, c)
		}
		sort.Strings(cats)

		for _, cat := range cats {
			// 原子添加：先拼好 header + items，预算足够才整体写入，
			// 避免出现「有 header 无 item」的孤儿段。
			var block []string
			blockTokens := 0
			header := "### " + cat
			blockTokens += utils.EstimateTokens(header)
			block = append(block, header)
			n := 0
			for _, it := range byCat[cat] {
				if n >= perCatCap {
					break
				}
				body := strings.TrimSpace(it.Value)
				if body == "" {
					continue
				}
				if len(body) > maxKnowBodyLen {
					body = body[:maxKnowBodyLen] + "..."
				}
				line := "- " + body
				if tokens+blockTokens+utils.EstimateTokens(line) > maxTokens {
					break
				}
				block = append(block, line)
				blockTokens += utils.EstimateTokens(line)
				n++
			}
			if len(block) <= 1 {
				continue // 没有实际 item，不输出孤儿 header
			}
			for _, line := range block {
				parts = append(parts, line)
				tokens += utils.EstimateTokens(line)
			}
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return "# Long-term memory (auto)\n" + strings.Join(parts, "\n")
}

// ---- Phase 2 全局合并 ----

// runPhase2Loop 独立 goroutine：每 IngestInterval 尝试一次全局合并。
func runPhase2Loop(ctx context.Context, log *slog.Logger, mgr memkit.Manager, newLLM func(context.Context) (types.LLMProvider, error), cfg *MemLongTermConfig) {
	if log == nil {
		log = slog.Default()
	}
	interval := cfg.IngestInterval
	if interval < 5*time.Second {
		interval = 2 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !cfg.Phase2Merge {
				continue
			}
			if err := runPhase2Merge(ctx, log, mgr, newLLM, cfg); err != nil {
				log.Warn("memory_phase2: merge", "error", err)
			}
		}
	}
}

// runPhase2Merge 执行一次全局合并：认领全局锁 → 剪枝 → 合并/去重 → 记录冷却。
// 锁原语与剪枝在 pkg/memkit/sqlite（TryClaimPhase2 / PruneUnused），SQLite 方言，
// 租约与冷却分离（评审 R2）。
func runPhase2Merge(ctx context.Context, log *slog.Logger, mgr memkit.Manager, newLLM func(context.Context) (types.LLMProvider, error), cfg *MemLongTermConfig) error {
	db, err := mgrDB(ctx, mgr)
	if err != nil {
		return err
	}
	holder := fmt.Sprintf("%d", time.Now().UnixNano())
	claimed, err := memsqlite.TryClaimPhase2(ctx, db, holder, memsqlite.Phase2Lease, memsqlite.Phase2Cooldown)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	defer func() {
		_ = memsqlite.ReleasePhase2(ctx, db, holder)
	}()

	if cfg.MaxUnusedDays > 0 {
		if err := memsqlite.PruneUnused(ctx, db, cfg.MaxUnusedDays); err != nil {
			return err
		}
	}

	if err := mergeDuplicateKnowledge(ctx, db, mgr); err != nil {
		return err
	}

	// LLM 语义冲突消解（可选，默认关）：同 category 的相似条目交 LLM 判断
	// 是否应合并为一条（只做内容级去重时语义重复会并存）。
	if cfg.Phase2LLMMerge {
		if err := llmConflictMerge(ctx, log, mgr, newLLM, db); err != nil {
			log.Warn("memory_phase2: llm merge", "error", err)
		}
	}

	// 成功 → 更新冷却，其它实例在 cooldown_until 前不抢。
	return memsqlite.MarkPhase2Cooldown(ctx, db, holder, memsqlite.Phase2Cooldown)
}

// llmConflictMerge 对每 user 的同 category knowledge 做两两 LLM 相似判断，
// 确认合并后以其中一条为准，另一条删除（tags 并入保留条目）。
func llmConflictMerge(ctx context.Context, log *slog.Logger, mgr memkit.Manager, newLLM func(context.Context) (types.LLMProvider, error), db *sql.DB) error {
	prov, err := newLLM(ctx)
	if err != nil || prov == nil {
		return err
	}
	users, err := listKnowledgeUsers(ctx, db)
	if err != nil {
		return err
	}
	for _, uid := range users {
		rows, err := db.QueryContext(ctx,
			`SELECT id, category, content, tags FROM knowledge WHERE user_id = ? ORDER BY category, updated_at DESC`,
			uid)
		if err != nil {
			return err
		}
		var entries []phase2KnowledgeRow
		for rows.Next() {
			var e phase2KnowledgeRow
			var tagsStr string
			if err := rows.Scan(&e.id, &e.cat, &e.content, &tagsStr); err != nil {
				continue
			}
			e.tags = parseTagsFromDB(tagsStr)
			entries = append(entries, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		// 同 category 两两比较（限定 200 条以内，避免 O(n^2) 失控）。
		if len(entries) > 200 {
			entries = entries[:200]
		}
		for i := 0; i < len(entries); i++ {
			for j := i + 1; j < len(entries); j++ {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if entries[i].cat != entries[j].cat {
					continue
				}
				merge, err := llmShouldMerge(ctx, prov, entries[i].content, entries[j].content)
				if err != nil {
					continue
				}
				if !merge {
					continue
				}
				// 保留较新的那条（查询按 updated_at DESC，i<j 即较新），删除较旧。
				keep, drop := entries[i], entries[j]
				if err := mgr.AddKnowledgeWithCategory(ctx, uid, keep.content, keep.cat, drop.tags...); err != nil {
					continue
				}
				if err := mgr.Knowledge().Delete(ctx, drop.id); err != nil {
					continue
				}
			}
		}
	}
	return nil
}

// phase2KnowledgeRow 是 LLM 冲突消解用的一行 knowledge。
type phase2KnowledgeRow struct {
	id, cat, content string
	tags             []string
}

// llmShouldMerge 用 LLM 判断两条内容是否语义重复、应合并。
func llmShouldMerge(ctx context.Context, prov types.LLMProvider, a, b string) (bool, error) {
	prompt := `You judge whether two memory facts are semantically the same (a duplicate) and should be merged into one.
Reply with exactly one word: YES or NO.
Fact A: ` + a + `
Fact B: ` + b
	msg, err := prov.Chat(ctx, []types.Message{
		{Role: "system", Content: "You are a strict duplicate detector. Reply YES only if the facts refer to the same underlying fact with no meaningful difference."},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return false, err
	}
	reply := strings.ToLower(strings.TrimSpace(msg.Content))
	return strings.HasPrefix(reply, "yes"), nil
}

func listKnowledgeUsers(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT user_id FROM knowledge ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			continue
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// mgrDB 从 Manager 取底层 *sql.DB。
func mgrDB(ctx context.Context, mgr memkit.Manager) (*sql.DB, error) {
	if db := mgrKnowledgeDB(ctx, mgr); db != nil {
		return db, nil
	}
	return nil, fmt.Errorf("phase2: knowledge store does not expose *sql.DB")
}

func mgrKnowledgeDB(ctx context.Context, mgr memkit.Manager) *sql.DB {
	ks := mgr.Knowledge()
	if k, ok := ks.(interface{ DB() *sql.DB }); ok {
		return k.DB()
	}
	return nil
}

// mergeDuplicateKnowledge 对每 user 的内容级重复条目做标签合并。
// 依赖 memkit 的 Add 内容级去重：重写已存在的重复条目时合并 tags。
func mergeDuplicateKnowledge(ctx context.Context, db *sql.DB, mgr memkit.Manager) error {
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT user_id FROM knowledge ORDER BY user_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var users []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			continue
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, u := range users {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := reingestUserKnowledge(ctx, db, mgr, u); err != nil {
			return err
		}
	}
	return nil
}

// reingestUserKnowledge 把一个 user 的全部知识条目重写一遍，利用 memkit 的
// Add 内容级去重（相同 normalized content 只合并 tags，不覆盖 content）来收敛重复。
func reingestUserKnowledge(ctx context.Context, db *sql.DB, mgr memkit.Manager, uid string) error {
	rows, err := db.QueryContext(ctx,
		`SELECT id, category, content, tags FROM knowledge WHERE user_id = ? ORDER BY updated_at DESC`,
		uid)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		id, cat, content string
		tags             []string
	}
	var items []row
	for rows.Next() {
		var r row
		var tagsStr string
		if err := rows.Scan(&r.id, &r.cat, &r.content, &tagsStr); err != nil {
			continue
		}
		r.tags = parseTagsFromDB(tagsStr)
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, it := range items {
		if err := mgr.AddKnowledgeWithCategory(ctx, uid, it.content, it.cat, it.tags...); err != nil {
			return err
		}
	}
	return nil
}

// parseTagsFromDB 复刻 memsqlite 内部 tags 解析（未导出）。
func parseTagsFromDB(content string) []string {
	if content == "" {
		return nil
	}
	parts := strings.Split(content, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
