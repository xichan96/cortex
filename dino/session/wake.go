package session

// B2 turn 唤醒（S4，subagent-s3s4 §7）。dino/session 不依赖 dino/agent（§1.4 包解耦）：
// session 只见纯数据 WakePayload + WakeSource 接口，mailbox 的 DrainAll/Truncated 全在
// dino/factory.go 的 sessionWakeSource 适配器完成。
//
// 评审 BLOCKER-2 修正（实现偏差，记录）：设计 §7.2 原案让适配器在 SubscribeAll 回调里
// DrainAll，与 wait_agent 的 Drain(taskID) 竞争同一 mailbox 条目。本实现改为：
//   - `Wake() <-chan struct{}` 只产"有新消息"信号（适配器回调里 select+drop 非阻塞投递，
//     不取走消息；评审 RECOMMENDED-2：不阻塞 notifier / 子代理 goroutine）。
//   - `Collect() []WakePayload` 由 session 在 `onSubagentCompletion` 里调用（此时 session
//     select 循环 idle，wait_agent 只在 turn 内阻塞 → 两者结构互斥，无竞态）。Collect 内
//     才 DrainAll + Truncated，消息留在 mailbox 直到被取 → 唤醒可重入、不丢消息（§7.4）。

// WakePayload 已完成子代理的紧凑注入单元（纯数据，session 不接触 agent 类型）。
type WakePayload struct {
	// TaskID DelegateResult.TaskID（spawn_agent 返回的 task_id）。
	TaskID string
	// Text DelegateResult.Truncated() 结果，直接注入下一 turn。
	Text string
}

// WakeSource 有新完成通知时产出信号；session idle 时经 Collect 取走 payload。
type WakeSource interface {
	// Wake 有新完成通知时产出信号（不携带 payload；payload 经 Collect 获取）。
	Wake() <-chan struct{}
	// Collect 取走全部未读完成（DrainAll + Truncated）。仅 session idle 时调用
	// （onSubagentCompletion），与 wait_agent（turn 内阻塞）互斥。
	Collect() []WakePayload
}

// noWake 无唤醒源（WakeOnCompletion=false 时用，行为同现状）。
type noWake struct{}

// NoWakeSource 返回永不产出的唤醒源。
func NoWakeSource() WakeSource { return (*noWake)(nil) }

func (*noWake) Wake() <-chan struct{}      { return nil }
func (*noWake) Collect() []WakePayload     { return nil }
