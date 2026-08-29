package agent

import (
	"log/slog"

	"github.com/xichan96/cortex/pkg/logger"
)

// CompletionNotifier 子代理完成 → 回发父代理（S3，subagent-s3s4 §6）。
//
// 评审 BLOCKER-1 修正：`dino/agent` **不 import 根 `dino` 包**（根包 import 本包），
// 因此不持 `*dino.Bus`。改为：
//   - getMailbox：按父 session 取 mailbox（mailbox.Put = 模型可见投递）；
//   - publish：factory 注入的 `func(eventType, sessionID string, data interface{})`
//     回调（signature 与 `dino.Bus.Publish` 完全一致，factory 用闭包接上即可）。
//
// 职责：Spawn 后台 goroutine 完成后调用 Notify，干两件事（均不得阻塞）：
//  1. mailbox.Put（wait_agent / S4 唤醒消费）；cap 满降级为只发事件（§12 风险 3）。
//  2. 旁路事件 "subagent.completed"（UI/审计；publish 为 nil 或 NotifyCompletion=false 跳过）。
type CompletionNotifier struct {
	getMailbox func(parentSessionID string) *Mailbox
	publish    func(eventType string, sessionID string, data interface{}) // nil = 不发事件
	enabled    bool
}

// NewCompletionNotifier 构造 notifier。publish 为 nil 或 !enabled 时不发事件
// （OPTIONAL-1：NotifyCompletion=false → notifier 直接跳过，不 Put 不发事件）。
func NewCompletionNotifier(getMailbox func(string) *Mailbox, publish func(eventType string, sessionID string, data interface{}), enabled bool) *CompletionNotifier {
	return &CompletionNotifier{
		getMailbox: getMailbox,
		publish:    publish,
		enabled:    enabled,
	}
}

// Notify 在子代理 goroutine 内调用（不得阻塞）。
// mailbox.Put 失败仅降级为事件 + 日志；事件载荷是完整 *DelegateResult（不进 LLM 上下文）。
func (n *CompletionNotifier) Notify(parentSessionID, taskID string, env *DelegateResult) {
	if n == nil || !n.enabled {
		return
	}
	if mb := n.getMailbox; mb != nil {
		if mb := mb(parentSessionID); mb != nil {
			if err := mb.Put(taskID, env); err != nil {
				logger.Warn("[completion] mailbox full, degraded to event-only",
					slog.String("session_id", parentSessionID),
					slog.String("task_id", taskID),
					slog.String("error", err.Error()))
			}
		}
	}
	if n.publish != nil {
		n.publish("subagent.completed", parentSessionID, env)
	}
}
