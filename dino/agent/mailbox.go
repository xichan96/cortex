package agent

import (
	"sync"
	"time"
)

// Mailbox 承载"子代理 → 父代理"的完成通知（模型可见投递，非 eventbus 旁路）。
// 设计 docs/design/subagent-s3s4.md §4。评审修正：
//   - 独立可注入组件，每 session 一个实例，不挂 SubagentManager（评审 R5）。
//   - 键 = task_id，wait_agent 用 task_id 精确取回；session 隔离靠实例隔离 + Drop 兜底。
//   - 一把锁串起 Put/Peek/Drain/SubscribeOnce，杜绝 "Put 与订阅" 竞态窗口（评审 §5.3）。
//
// 消费仲裁（评审 BLOCKER 2）：wait_agent 是 Drain(taskID) 的**唯一**消费者（turn 内阻塞
// 订阅）；SubscribeAll（B2 唤醒适配器）只做"有消息"通知、不取走消息，由 session 端在
// onSubagentCompletion 里 DrainAll。这样两个消费方永不竞争同一条目。
type Mailbox struct {
	mu   sync.RWMutex
	msgs map[string]*MailboxEntry // key = task_id
	seq  int64                    // 单调递增，B2 唤醒信号 + DrainAll 到达序

	// perTask 订阅：map[taskID]map[subID]func()。Put 时触发该 taskID 的订阅者并删除。
	perTask map[string]map[string]func()
	// all 全局订阅：有任意 Put（未被 wait_agent Drain 拿走前）触发。B2 适配器用。
	// 注意：触发后并不 Drain，只是通知"有新消息"；消费方（session）自行 DrainAll。
	all map[string]func()

	cap int           // 默认 64；cap<=0 时 Put 直接返回 error
	ttl time.Duration // 默认 0（不启用）；>0 时 Peek/Len 惰性清理
}

// MailboxEntry 单条完成记录（含元数据，供 Drop/TTL 诊断）。
type MailboxEntry struct {
	Result      *DelegateResult
	CreatedAtMS int64
	ExpireAtMS  int64 // 0 = 不失效
	seq         int64 // 到达序（Put 单调递增），DrainAll 排序用
}

// DefaultMailboxCap mailbox 容量默认值（对齐 codex 会话线程上限量级）。
const DefaultMailboxCap = 64

// NewMailbox 构造 mailbox。cap<=0 用 DefaultMailboxCap；ttl<=0 表示不启用过期。
func NewMailbox(cap int, ttl time.Duration) *Mailbox {
	if cap <= 0 {
		cap = DefaultMailboxCap
	}
	return &Mailbox{
		msgs:    make(map[string]*MailboxEntry),
		perTask: make(map[string]map[string]func()),
		all:     make(map[string]func()),
		cap:     cap,
		ttl:     ttl,
	}
}

// Put 存入一条完成记录。cap 满时返回 error（notifier 降级为只发事件）。
// 同一 taskID 重复 Put 视为更新（覆盖旧记录），不触发 cap 增长。
// 触发该 taskID 的 wait_agent 订阅者与全局订阅者；所有回调在锁内同步触发，
// 回调实现必须非阻塞（select+drop 或只做信号），否则会阻塞 Put 调用方。
func (mb *Mailbox) Put(taskID string, r *DelegateResult) error {
	if mb == nil || taskID == "" {
		return nil
	}
	mb.mu.Lock()
	defer mb.mu.Unlock()

	if _, exists := mb.msgs[taskID]; !exists {
		if len(mb.msgs) >= mb.cap {
			return &mailboxFullError{}
		}
	}

	now := time.Now()
	entry := &MailboxEntry{
		Result:      r,
		CreatedAtMS: now.UnixMilli(),
		seq:         mb.seq,
	}
	mb.seq++
	if mb.ttl > 0 {
		entry.ExpireAtMS = now.Add(mb.ttl).UnixMilli()
	}
	mb.msgs[taskID] = entry

	// 触发 per-task 订阅者（wait_agent 阻塞 channel），并清理。
	if subs := mb.perTask[taskID]; len(subs) > 0 {
		for _, fn := range subs {
			if fn != nil {
				fn()
			}
		}
		delete(mb.perTask, taskID)
	}
	// 触发全局订阅者（B2 适配器）。回调只通知不取走消息。
	for _, fn := range mb.all {
		if fn != nil {
			fn()
		}
	}
	return nil
}

// mailboxFullError 表示 mailbox 容量已满。notifier 收到后降级为只发旁路事件。
type mailboxFullError struct{}

func (e *mailboxFullError) Error() string { return "mailbox full" }

// IsMailboxFullError 供 notifier 判断降级路径。
func IsMailboxFullError(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(*mailboxFullError)
	return ok
}

// Peek 只读，不删除。wait_agent 首次快速检查用。
func (mb *Mailbox) Peek(taskID string) *DelegateResult {
	if mb == nil {
		return nil
	}
	mb.mu.Lock()
	defer mb.mu.Unlock()
	mb.sweepExpiredLocked()
	if e := mb.msgs[taskID]; e != nil {
		return e.Result
	}
	return nil
}

// Drain 读取并删除（wait_agent 唯一消费入口）。
func (mb *Mailbox) Drain(taskID string) *DelegateResult {
	if mb == nil {
		return nil
	}
	mb.mu.Lock()
	defer mb.mu.Unlock()
	mb.sweepExpiredLocked()
	e := mb.msgs[taskID]
	if e == nil {
		return nil
	}
	delete(mb.msgs, taskID)
	return e.Result
}

// DrainAll 取走全部未读（B2 session 端在 onSubagentCompletion 用）。
// 返回按到达序（seq 序）排序的结果。不触发订阅者（消费方已在通知回调外）。
func (mb *Mailbox) DrainAll() []*DelegateResult {
	if mb == nil {
		return nil
	}
	mb.mu.Lock()
	defer mb.mu.Unlock()
	mb.sweepExpiredLocked()
	return mb.drainAllLocked()
}

// sweepExpiredLocked 惰性清理 TTL 过期条目（Peek/Len 时调用）。持锁。
func (mb *Mailbox) sweepExpiredLocked() {
	if mb.ttl <= 0 || len(mb.msgs) == 0 {
		return
	}
	now := time.Now().UnixMilli()
	for id, e := range mb.msgs {
		if e.ExpireAtMS > 0 && e.ExpireAtMS <= now {
			delete(mb.msgs, id)
		}
	}
}

// Len 未读条目数（含过期清理）。
func (mb *Mailbox) Len() int {
	if mb == nil {
		return 0
	}
	mb.mu.Lock()
	defer mb.mu.Unlock()
	mb.sweepExpiredLocked()
	return len(mb.msgs)
}

// Drop session 关闭回收：清空消息与订阅，幂等。
func (mb *Mailbox) Drop() {
	if mb == nil {
		return
	}
	mb.mu.Lock()
	defer mb.mu.Unlock()
	mb.msgs = make(map[string]*MailboxEntry)
	mb.perTask = make(map[string]map[string]func())
	mb.all = make(map[string]func())
}

// SubscribeOnce 注册一次性的 per-task 订阅（wait_agent 阻塞用）。
// 若消息已存在则立即触发 done 并不注册（消除 "Drain 与订阅之间 Put 到达" 竞态）。
// 返回订阅 id（供 Unsubscribe）；消息已存在时返回 ""。
// done 回调在 Put 锁内同步触发，必须非阻塞。
func (mb *Mailbox) SubscribeOnce(taskID string, done func()) string {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	if e := mb.msgs[taskID]; e != nil {
		// 消息已在：直接触发，不注册。
		if done != nil {
			done()
		}
		return ""
	}
	subID := newSubID()
	if mb.perTask[taskID] == nil {
		mb.perTask[taskID] = make(map[string]func())
	}
	mb.perTask[taskID][subID] = done
	return subID
}

// Unsubscribe 反注册 per-task 订阅（wait_agent 结束/超时时清理）。
func (mb *Mailbox) Unsubscribe(taskID, id string) {
	if mb == nil || id == "" {
		return
	}
	mb.mu.Lock()
	defer mb.mu.Unlock()
	if subs := mb.perTask[taskID]; subs != nil {
		delete(subs, id)
		if len(subs) == 0 {
			delete(mb.perTask, taskID)
		}
	}
}

// SubscribeAll 注册全局订阅（B2 唤醒适配器用）：任意 Put 都触发。
// 回调只做"有消息"通知（如 select+default 往 channel 写），不取走消息。
// 返回订阅 id（供 UnsubscribeAll）。
func (mb *Mailbox) SubscribeAll(done func()) string {
	if mb == nil {
		return ""
	}
	mb.mu.Lock()
	defer mb.mu.Unlock()
	id := newSubID()
	mb.all[id] = done
	return id
}

// UnsubscribeAll 反注册全局订阅。
func (mb *Mailbox) UnsubscribeAll(id string) {
	if mb == nil || id == "" {
		return
	}
	mb.mu.Lock()
	defer mb.mu.Unlock()
	delete(mb.all, id)
}

// drainAllLocked 按 seq 到达序取走全部。持锁。
func (mb *Mailbox) drainAllLocked() []*DelegateResult {
	if len(mb.msgs) == 0 {
		return nil
	}
	bySeq := make([]*MailboxEntry, 0, len(mb.msgs))
	for _, e := range mb.msgs {
		bySeq = append(bySeq, e)
	}
	// 插入序排序（Put 时 seq 递增，map 无序，显式排序保证确定性）。
	for i := 1; i < len(bySeq); i++ {
		for j := i; j > 0 && bySeq[j].seq < bySeq[j-1].seq; j-- {
			bySeq[j], bySeq[j-1] = bySeq[j-1], bySeq[j]
		}
	}
	mb.msgs = make(map[string]*MailboxEntry)
	out := make([]*DelegateResult, 0, len(bySeq))
	for _, e := range bySeq {
		out = append(out, e.Result)
	}
	return out
}

// subCounter 订阅 id 计数器（进程内唯一即可）。
var subCounter int64

func newSubID() string {
	subCounter++
	return "sub-" + itoa(subCounter)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
