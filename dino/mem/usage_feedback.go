package mem

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/xichan96/cortex/pkg/memkit"
)

// 引用反馈（评审 B3）：不使用「search_knowledge 返回即计数」——那会把读操作
// 污染成自反馈环（高频被搜但从不被用的条目会永远霸榜、永不被剪枝）。
//
// 默认实现：`search_knowledge` 把「返回给模型的条目」登记到 per-session probe；
// turn 结束后，若模型最终输出（assistant text）实际包含某条目的内容/标签片段，
// 才算一次使用（usage_count + 1）。语义是「模型实际引用」，而非「被检索」。

const (
	usageProbeMaxPerSession = 64  // 每 session 最多保留的候选条目
	usageProbeTTL           = 10 * time.Minute
	usageProbeContentLen    = 160 // 匹配用的内容片段长度（足够辨识、避免长文误命中）
)

type usageProbeEntry struct {
	id      string
	snippet string // 归一化后的内容片段
	tags    string // 归一化后的标签片段
	ts      time.Time
}

var (
	usageProbeMu       sync.Mutex
	usageProbeBySession = map[string][]usageProbeEntry{}
)

// recordSearchResults 登记一次 search_knowledge 返回的条目（候选集）。
func recordSearchResults(sessionID string, items []memkit.MemoryItem) {
	if sessionID == "" || len(items) == 0 {
		return
	}
	now := time.Now()
	entries := make([]usageProbeEntry, 0, len(items))
	for _, it := range items {
		entries = append(entries, usageProbeEntry{
			id:      it.ID,
			snippet: normalizeProbeText(it.Value),
			tags:    normalizeProbeText(strings.Join(it.Tags, ",")),
			ts:      now,
		})
	}
	usageProbeMu.Lock()
	defer usageProbeMu.Unlock()
	old := usageProbeBySession[sessionID]
	// 保留既有未过期候选 + 新候选，超限时丢最旧的。
	kept := make([]usageProbeEntry, 0, len(old)+len(entries))
	for _, e := range old {
		if now.Sub(e.ts) <= usageProbeTTL {
			kept = append(kept, e)
		}
	}
	kept = append(kept, entries...)
	if len(kept) > usageProbeMaxPerSession {
		kept = kept[len(kept)-usageProbeMaxPerSession:]
	}
	usageProbeBySession[sessionID] = kept
}

// ObserveAssistantFeedback 在 turn 结束后调用：对模型最终输出做子串/标签匹配，
// 命中即记录 usage。用短上下文（无超时）调 mgr.RecordKnowledgeUse，失败仅记日志。
func ObserveAssistantFeedback(ctx context.Context, sessionID, assistantText string) {
	if sessionID == "" || strings.TrimSpace(assistantText) == "" {
		return
	}
	usageProbeMu.Lock()
	entries := usageProbeBySession[sessionID]
	// 清理过期项。
	now := time.Now()
	valid := entries[:0]
	for _, e := range entries {
		if now.Sub(e.ts) <= usageProbeTTL {
			valid = append(valid, e)
		}
	}
	usageProbeBySession[sessionID] = valid
	usageProbeMu.Unlock()

	text := normalizeProbeText(assistantText)
	mgr, err := SharedSQLiteManager(probePersistDir(), probePersistFile())
	if err != nil || mgr == nil {
		return
	}
	var hits []string
	for _, e := range valid {
		if e.id == "" {
			continue
		}
		if (e.snippet != "" && strings.Contains(text, e.snippet)) ||
			(e.tags != "" && strings.Contains(text, e.tags)) {
			hits = append(hits, e.id)
		}
	}
	for _, id := range hits {
		if err := mgr.RecordKnowledgeUse(ctx, id); err != nil {
			// 失败不致命；下轮 probe 会重新登记。
			continue
		}
	}
}

// probePersistDir / probePersistFile 由 LongTermMem 构造时注入，
// 以便 ObserveAssistantFeedback 在无句柄时也能找到进程内单例 Manager。
var (
	probePathsMu     sync.RWMutex
	probePersistDirV = "./dino_sessions"
	probePersistFileV = "shared_chat.db"
)

func setProbePaths(dir, file string) {
	probePathsMu.Lock()
	defer probePathsMu.Unlock()
	if dir != "" {
		probePersistDirV = dir
	}
	if file != "" {
		probePersistFileV = file
	}
}

func probePersistDir() string {
	probePathsMu.RLock()
	defer probePathsMu.RUnlock()
	return probePersistDirV
}

func probePersistFile() string {
	probePathsMu.RLock()
	defer probePathsMu.RUnlock()
	return probePersistFileV
}

func normalizeProbeText(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
