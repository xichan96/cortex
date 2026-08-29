package dino

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/hooks"
	agentproviders "github.com/xichan96/cortex/agent/providers"
	agentTools "github.com/xichan96/cortex/agent/tools/builtin/fs"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/logger"

	agentskills "github.com/xichan96/cortex/agent/skills"
	agentutils "github.com/xichan96/cortex/agent/utils"
	dinoAgent "github.com/xichan96/cortex/dino/agent"
	"github.com/xichan96/cortex/dino/chatstore"
	dinoMem "github.com/xichan96/cortex/dino/mem"
	"github.com/xichan96/cortex/dino/permission"
	"github.com/xichan96/cortex/dino/session"
	dinoTools "github.com/xichan96/cortex/dino/tools"
)

func parseShellCommand(cmd string) []string {
	var args []string
	var current strings.Builder
	inQuote := false
	var quoteChar rune

	for i := 0; i < len(cmd); i++ {
		c := rune(cmd[i])

		if !inQuote && (c == '"' || c == '\'') {
			inQuote = true
			quoteChar = c
		} else if inQuote && c == quoteChar {
			inQuote = false
		} else if !inQuote && c == ' ' {
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		} else {
			current.WriteRune(c)
		}
	}

	if current.Len() > 0 {
		args = append(args, current.String())
	}

	return args
}

type Budget = agentutils.Budget
type Cost = agentutils.Cost
type BudgetRequest = agentutils.BudgetRequest
type BudgetResult = agentutils.BudgetResult
type BudgetState = agentutils.BudgetState

func NewBudget(cfg *BudgetConfig) Budget {
	if cfg == nil {
		return agentutils.NewBudget(nil)
	}
	return agentutils.NewBudget(&agentutils.BudgetConfig{
		Enabled:      cfg.Enabled,
		MaxTokens:    cfg.MaxTokens,
		MaxToolCalls: cfg.MaxToolCalls,
		MaxTimeMs:    cfg.MaxTimeMs,
	})
}

type memoryAdapter struct {
	provider chatstore.Provider
}
type memProvSaver struct {
	p chatstore.Provider
}

func (s *memProvSaver) AddMessage(ctx context.Context, msg types.Message) error {
	return s.p.AddMessage(ctx, msg)
}

func (m *memoryAdapter) LoadMemoryVariables(ctx context.Context) (map[string]interface{}, error) {
	return nil, nil
}

func (m *memoryAdapter) SaveContext(ctx context.Context, input, output map[string]interface{}) error {
	if input != nil {
		if inputMsg, ok := input["input"].(string); ok {
			role, _ := input["role"].(string)
			if role == "" {
				role = "user"
			}
			parts, _ := input["parts"].([]types.MessagePart)
			if len(parts) > 0 {
				sp, serr := types.SerializeMessageParts(parts)
				if serr != nil {
					logger.Warn("[memoryAdapter] serialize parts", slog.String("error", serr.Error()))
					sp = "[]"
				}
				var wrap struct {
					Input string          `json:"input"`
					Role  string          `json:"role,omitempty"`
					Parts json.RawMessage `json:"parts,omitempty"`
				}
				wrap.Input = inputMsg
				wrap.Role = role
				if sp != "" {
					wrap.Parts = json.RawMessage(sp)
				}
				inputJSON, err := json.Marshal(wrap)
				if err != nil {
					logger.Warn("[memoryAdapter] failed to marshal input", slog.String("error", err.Error()))
					inputJSON = []byte(fmt.Sprintf("%v", input))
				}
				if err := m.provider.AddMessage(ctx, chatstore.Message{
					Role:    role,
					Content: fmt.Sprintf("Input: %s", string(inputJSON)),
				}); err != nil {
					return err
				}
			} else {
				msg := types.Message{Role: role, Content: inputMsg}
				if err := m.provider.AddMessage(ctx, msg); err != nil {
					return err
				}
			}
		} else {
			var role string
			if r, ok := input["role"].(string); ok {
				role = r
			} else {
				role = "user"
			}
			inputJSON, err := json.Marshal(input)
			if err != nil {
				logger.Warn("[memoryAdapter] failed to marshal input", slog.String("error", err.Error()))
				inputJSON = []byte(fmt.Sprintf("%v", input))
			}
			if err := m.provider.AddMessage(ctx, chatstore.Message{
				Role:    role,
				Content: fmt.Sprintf("Input: %s", string(inputJSON)),
			}); err != nil {
				return err
			}
		}
	}
	if output == nil {
		return nil
	}
	return agentproviders.SaveOutputWithToolSteps(ctx, &memProvSaver{m.provider}, output)
}

func (m *memoryAdapter) Clear(ctx context.Context) error {
	return m.provider.Clear(ctx)
}

// GetSummary 转发到 chatstore.Provider.GetSummary（压缩摘要注入）。
// 不加到 MemoryProvider 接口体（避免破坏 agent/providers 其它实现），
// 由 engine 在 prepareMessages 用类型断言探测（评审 R3）。
//
// 评审 B1 单一注入源：Hybrid 活跃（EnableLLMCompress 且底层被 Hybrid 包裹）时，
// 摘要由 Hybrid.GetMessages 尾部注入，此处返回空禁用 engine 头部注入，避免双注入。
// 非 Hybrid 底层（新路径之外的内存/SQLite provider）仍走头部注入。
func (m *memoryAdapter) GetSummary(ctx context.Context) (string, error) {
	if h, ok := m.provider.(*chatstore.Hybrid); ok && h.TailSummaryEnabled() {
		return "", nil
	}
	return m.provider.GetSummary(ctx)
}

func (m *memoryAdapter) GetChatHistory(ctx context.Context) ([]types.Message, error) {
	msgs, err := m.provider.GetMessages(ctx, 0)
	if err != nil {
		return nil, err
	}
	result := make([]types.Message, len(msgs))
	for i, msg := range msgs {
		result[i] = types.Message{
			Role:       msg.Role,
			Content:    msg.Content,
			Name:       msg.Name,
			Parts:      msg.Parts,
			ToolCalls:  msg.ToolCalls,
			ToolCallID: msg.ToolCallID,
		}
	}
	return result, nil
}

func (m *memoryAdapter) StoredMessageCount(ctx context.Context) (int, error) {
	return m.provider.StoredMessageCount(ctx)
}

func (m *memoryAdapter) CompressMemory(ctx context.Context, llm types.LLMProvider, maxMessages int) error {
	// llm 已由 engine 传入（agent_execution.go ae.model）；Hybrid 在 factory 构造时已
	// 注入 LLMSummaryAdapter，无需在此二次传递（避免双源）。maxMessages（keepWindow）
	// 留作尾部预算上限的提示——本版沿用 provider 内部 Config + LLMSummaryAdapter 输入
	// 条数上限（评审 R3），不强耦合。
	return m.provider.Compress(ctx)
}

func (m *memoryAdapter) ReplayMessages(ctx context.Context, messages []types.Message) error {
	if err := m.provider.Clear(ctx); err != nil {
		return err
	}
	for _, msg := range messages {
		mm := chatstore.Message{
			Role:       msg.Role,
			Content:    msg.Content,
			Name:       msg.Name,
			Parts:      msg.Parts,
			ToolCalls:  msg.ToolCalls,
			ToolCallID: msg.ToolCallID,
		}
		if err := m.provider.AddMessage(ctx, mm); err != nil {
			return err
		}
	}
	return nil
}

type delegateParentMemoryTool struct {
	inner  types.Tool
	sid    string
	getMem func() types.MemoryProvider
}

func newDelegateParentMemoryTool(inner types.Tool, sessionID string, getMem func() types.MemoryProvider) types.Tool {
	return &delegateParentMemoryTool{inner: inner, sid: sessionID, getMem: getMem}
}

func (t *delegateParentMemoryTool) Name() string                   { return t.inner.Name() }
func (t *delegateParentMemoryTool) Description() string            { return t.inner.Description() }
func (t *delegateParentMemoryTool) Schema() map[string]interface{} { return t.inner.Schema() }
func (t *delegateParentMemoryTool) Metadata() types.ToolMetadata   { return t.inner.Metadata() }
func (t *delegateParentMemoryTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if t.getMem != nil {
		if m := t.getMem(); m != nil {
			ctx = dinoAgent.WithParentMemory(ctx, &dinoAgent.ParentMemoryContext{
				SessionID: t.sid,
				Memory:    m,
			})
		}
	}
	return t.inner.Execute(ctx, input)
}

// sessionWakeSource 订阅 mailbox 完成通知，转成 session.WakeSource（S4/B2，subagent-s3s4 §7.2）。
//
// 评审 BLOCKER-2 修正：SubscribeAll 回调**只发信号、不 DrainAll**（回调里 select+drop
// 非阻塞投递 dirty，不阻塞 notifier / 子代理 goroutine——评审 RECOMMENDED-2）。session
// 在 onSubagentCompletion（idle）里调 Collect() 才 DrainAll + Truncated，与 wait_agent
// 的 Drain(taskID) 结构互斥（turn 内 session 不在 select 循环）。
//
// 无后台 goroutine（信号在回调里同步投递，payload 由 session 拉取）→ 无需 Close 停 goroutine；
// Close 只反注册 SubscribeAll（评审 RECOMMENDED-7：生命周期跟随 session）。
type sessionWakeSource struct {
	mb       *dinoAgent.Mailbox
	maxRunes int
	dirty    chan struct{}
	subID    string
}

func newSessionWakeSource(mb *dinoAgent.Mailbox, maxRunes int) *sessionWakeSource {
	if mb == nil {
		return nil
	}
	s := &sessionWakeSource{
		mb:       mb,
		maxRunes: maxRunes,
		dirty:    make(chan struct{}, 1),
	}
	s.subID = mb.SubscribeAll(func() {
		// 非阻塞信号：buffer 1，完成密集时只留一个信号（payload 不丢，仍在 mailbox）。
		select {
		case s.dirty <- struct{}{}:
		default:
		}
	})
	return s
}

func (s *sessionWakeSource) Wake() <-chan struct{} { return s.dirty }

// Collect 取走全部未读完成并截断（session idle 时调用）。
func (s *sessionWakeSource) Collect() []session.WakePayload {
	if s == nil || s.mb == nil {
		return nil
	}
	envs := s.mb.DrainAll()
	if len(envs) == 0 {
		return nil
	}
	payloads := make([]session.WakePayload, 0, len(envs))
	for _, env := range envs {
		payloads = append(payloads, session.WakePayload{
			TaskID: env.TaskID,
			Text:   env.Truncated(s.maxRunes),
		})
	}
	return payloads
}

// Close 反注册订阅（session 关闭时由 factory CloseSession 调用）。
func (s *sessionWakeSource) Close() {
	if s == nil || s.mb == nil || s.subID == "" {
		return
	}
	s.mb.UnsubscribeAll(s.subID)
}

type toolEventSenderAdapter struct {
	sender StreamEventSender
}

func (a *toolEventSenderAdapter) SendToolCall(sessionID, toolCallID, toolName string, input map[string]interface{}) {
	if a.sender != nil {
		a.sender.SendStreamEvent(sessionID, session.Event{
			Type:       session.EventTypeToolCall,
			ToolCallID: toolCallID,
			ToolName:   toolName,
			ToolInput:  input,
			SessionID:  sessionID,
		})
	}
}

func (a *toolEventSenderAdapter) SendToolResult(sessionID, toolCallID, toolName string, result string) {
	if a.sender != nil {
		a.sender.SendStreamEvent(sessionID, session.Event{
			Type:       session.EventTypeToolResult,
			ToolCallID: toolCallID,
			ToolName:   toolName,
			ToolOutput: result,
			SessionID:  sessionID,
		})
	}
}

func (a *toolEventSenderAdapter) SendToolError(sessionID, toolCallID, toolName string, err string) {
	if a.sender != nil {
		a.sender.SendStreamEvent(sessionID, session.Event{
			Type:       session.EventTypeError,
			ToolCallID: toolCallID,
			ToolName:   toolName,
			Error:      err,
			SessionID:  sessionID,
		})
	}
}

type DinoFactory interface {
	CreateSession(ctx context.Context, sessionID string, opts ...session.Option) (*session.Session, error)
	GetSession(sessionID string) *session.Session
	CloseSession(sessionID string)
	CloseAll()
	GetTools() []types.Tool
	GetSkills() []*Skill
	Shutdown(ctx context.Context) error
	GetLLMProvider() types.LLMProvider
	GetPlannerConfig() *PlannerModeConfig
	Budget() Budget
	RespondToolApproval(requestID string, approved bool)
	SaveSessionSnapshot(ctx context.Context, sessionID string, dir string) (string, error)
	RestoreSessionSnapshot(ctx context.Context, sessionID string, snapPath string) error
}

type dinoFactory struct {
	config               *Config
	llmProvider          types.LLMProvider
	tools                *dinoTools.Registry
	loopDetector         agentutils.LoopDetector
	budget               Budget
	cortexSkills         []*agentskills.Skill
	approvalStore        *ApprovalStore
	sessions             map[string]*session.Session
	mu                   sync.RWMutex
	streamSender         StreamEventSender
	subagentManager      *dinoAgent.SubagentManager
	mcpManager           *dinoTools.MCPManager
	longTermMem          *dinoMem.LongTermMem // 长期记忆子系统句柄（nil = 未启用）
	sessionToolsProvider func(sessionID string) []types.Tool
	hooks                hooks.Hooks

	// —— S3/S4 mailbox（subagent-s3s4 §4/§7.2）——
	// bus 实例级事件总线（notifier 旁路事件用，评审 BLOCKER-1：factory 层注入回调，
	// dino/agent 不引根包）。每个 factory 一个实例，避免 GetGlobalBus 多会话串扰。
	bus *Bus
	// notifier 完成通知（mailbox.Put + bus 旁路事件），跨 session 共享一份。
	notifier *dinoAgent.CompletionNotifier
	// sessionMailboxes 每 session 一个 mailbox，key = sessionID。CloseSession Drop + 删除。
	sessionMailboxes map[string]*dinoAgent.Mailbox
	// sessionWakes 每 session 一个唤醒适配器（S4a，WakeOnCompletion 时构造）。
	sessionWakes map[string]*sessionWakeSource
}

// cloneToolTimeouts 深拷贝 ToolTimeouts map，避免 CreateSession 注入 wait_agent
// 超时污染共享的 cfg.ToolTimeouts（评审 BLOCKER-3 改的是 per-session agentConfig）。
func cloneToolTimeouts(src map[string]time.Duration) map[string]time.Duration {
	if src == nil {
		return nil
	}
	dst := make(map[string]time.Duration, len(src)+1)
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (f *dinoFactory) LoopDetector() agentutils.LoopDetector {
	return f.loopDetector
}

func (f *dinoFactory) Budget() Budget {
	return f.budget
}

func (f *dinoFactory) GetLLMProvider() types.LLMProvider {
	return f.llmProvider
}

func (f *dinoFactory) GetPlannerConfig() *PlannerModeConfig {
	c := f.config.PlannerMode
	return &c
}

func (f *dinoFactory) GetSubagentManager() *dinoAgent.SubagentManager {
	return f.subagentManager
}

func (f *dinoFactory) GetMCPManager() *dinoTools.MCPManager {
	return f.mcpManager
}

func (f *dinoFactory) GetAgent(name string) (*dinoAgent.Info, bool) {
	info, exists := dinoAgent.DefaultAgents()[name]
	return info, exists
}

func (f *dinoFactory) RecordLoop(sessionID string, action agentutils.LoopDetectAction) {
	f.loopDetector.Record(sessionID, action)
}

func (f *dinoFactory) RecordTokens(ctx context.Context, sessionID string, tokens int) {
	f.budget.RecordTokens(ctx, sessionID, tokens)
}

func (f *dinoFactory) Detect(ctx context.Context, sessionID string, action agentutils.LoopDetectAction) *agentutils.LoopDetectResult {
	return f.loopDetector.Detect(ctx, sessionID, action)
}

type FactoryOption func(*dinoFactory)

func WithStreamEventSender(sender StreamEventSender) FactoryOption {
	return func(f *dinoFactory) {
		f.streamSender = sender
	}
}

func WithApprovalSender(sender ApprovalSender) FactoryOption {
	return func(f *dinoFactory) {
		if sender != nil && f.approvalStore != nil {
			f.approvalStore.SetSender(sender)
		}
	}
}

func WithSessionTools(fn func(sessionID string) []types.Tool) FactoryOption {
	return func(f *dinoFactory) {
		f.sessionToolsProvider = fn
	}
}

// WithHooks sets lifecycle hooks that will be applied to every session
// created by this factory. This is a convenience alternative to calling
// session.GetAgent().SetHooks() after CreateSession.
func WithHooks(h hooks.Hooks) FactoryOption {
	return func(f *dinoFactory) {
		f.hooks = h
	}
}

func NewDinoFactory(cfg *Config, opts ...FactoryOption) (DinoFactory, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	llmProvider, err := createLLMProvider(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create LLM provider: %w", err)
	}

	// Apply prompt caching at the shared provider level so every engine
	// (session + subagents) inherits it. The engine also re-applies it at
	// NewAgentEngine via types.AgentConfig.PromptCaching.
	ConfigurePromptCache(llmProvider, cfg.PromptCacheOptions())

	toolRegistry := dinoTools.NewRegistry()
	if err := loadBuiltinTools(toolRegistry, cfg.WorkspaceRoot); err != nil {
		return nil, fmt.Errorf("load builtin tools: %w", err)
	}

	var loadedCortexSkills []*agentskills.Skill
	skillRegistry := agentskills.NewRegistry()
	if cfg.Skills.Path != "" && cfg.Skills.AutoLoad {
		if err := skillRegistry.LoadFromDirs(context.Background(), logger.GetLogger(), []string{cfg.Skills.Path}); err != nil {
			logger.Warn("failed to load skills", slog.String("path", cfg.Skills.Path), slog.String("error", err.Error()))
		} else {
			cortexSkills := skillRegistry.All()
			logger.Info("loaded skills", slog.Int("count", len(cortexSkills)), slog.String("path", cfg.Skills.Path))
			for _, s := range cortexSkills {
				logger.Info("skill loaded", slog.String("name", s.Name))
			}
			loadedCortexSkills = cortexSkills
			if len(cortexSkills) > 0 {
				if err := toolRegistry.Register(dinoTools.NewSkillTool(cortexSkills)); err != nil {
					logger.Warn("failed to register skill tool", slog.String("error", err.Error()))
				}
			}
		}
	}

	approvalStore := NewApprovalStore(5 * time.Minute)

	f := &dinoFactory{
		config:      cfg,
		llmProvider: llmProvider,
		tools:       toolRegistry,
		loopDetector: agentutils.NewLoopDetector(&agentutils.LoopDetectConfig{
			Enabled:             cfg.LoopDetection.Enabled,
			MaxRepeats:          cfg.LoopDetection.MaxRepeats,
			SimilarityThreshold: cfg.LoopDetection.SimilarityThreshold,
		}),
		budget:           NewBudget(&cfg.Budget),
		cortexSkills:     loadedCortexSkills,
		approvalStore:    approvalStore,
		sessions:         make(map[string]*session.Session),
		bus:              NewBus(),
		sessionMailboxes: make(map[string]*dinoAgent.Mailbox),
		sessionWakes:     make(map[string]*sessionWakeSource),
	}

	f.subagentManager = dinoAgent.NewSubagentManager(&cfg.Subagent, f)
	if f.subagentManager != nil {
		// S3/S4 接线（评审 BLOCKER-1：notifier 拆成纯数据 + factory 注入回调，
		// dino/agent 不引根 dino 包）。
		subCfg := cfg.Subagent
		f.subagentManager.SetMaxConcurrentSpawns(subCfg.MaxConcurrentSpawns)
		f.notifier = dinoAgent.NewCompletionNotifier(
			// getMailbox：按父 session 取 mailbox。
			func(sessionID string) *dinoAgent.Mailbox {
				f.mu.RLock()
				defer f.mu.RUnlock()
				return f.sessionMailboxes[sessionID]
			},
			// publish：实例级 Bus 旁路事件（subagent.completed），UI/审计用，不进 LLM 上下文。
			func(eventType string, sessionID string, data interface{}) {
				f.bus.Publish(eventType, sessionID, data)
			},
			subCfg.NotifyCompletion,
		)
		f.subagentManager.SetNotifier(f.notifier)
	}

	if cfg.MCP.Enabled {
		f.mcpManager = dinoTools.NewMCPManager()
		for name, serverCfg := range cfg.MCP.Servers {
			if serverCfg.Enabled {
				logger.Info("[DinoFactory] Initializing MCP server", slog.String("name", name), slog.String("type", serverCfg.Type))
				serverConfig := &dinoTools.ServerConfig{
					Name:      name,
					URL:       serverCfg.URL,
					Transport: serverCfg.Type,
					Headers:   serverCfg.Headers,
					Type:      serverCfg.Type,
					Command:   parseShellCommand(serverCfg.Command),
					Env:       serverCfg.Env,
				}
				if err := f.mcpManager.AddServer(context.Background(), name, serverConfig); err != nil {
					logger.Warn("[DinoFactory] Failed to add MCP server", slog.String("name", name), slog.String("error", err.Error()))
				}
			}
		}
		// Register tools discovered from all connected MCP servers so the agent
		// can call them directly without going through the generic mcp_client tool.
		for _, t := range f.mcpManager.GetAllMCPTools() {
			if err := toolRegistry.Register(t); err != nil {
				logger.Warn("[DinoFactory] Failed to register MCP tool", slog.String("tool", t.Name()), slog.String("error", err.Error()))
			} else {
				logger.Info("[DinoFactory] Registered MCP tool", slog.String("tool", t.Name()))
			}
		}
	}
	if cfg.LongTermMemory.Enabled {
		ltm, err := dinoMem.NewLongTermMem(context.Background(), &cfg.LongTermMemory, llmProvider, logger.GetLogger().Slog(),
			cfg.Memory.PersistDirectory, cfg.Memory.PersistFileName)
		if err != nil {
			logger.Warn("[DinoFactory] long-term memory disabled", slog.String("error", err.Error()))
		} else {
			f.longTermMem = ltm
			f.longTermMem.Start()
			logger.Info("[DinoFactory] long-term memory enabled",
				slog.String("persist_dir", cfg.Memory.PersistDirectory),
				slog.String("persist_file", cfg.Memory.PersistFileName))
		}
	}

	for _, opt := range opts {
		opt(f)
	}
	return f, nil
}

func (f *dinoFactory) CreateSession(ctx context.Context, sessionID string, opts ...session.Option) (*session.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if s, exists := f.sessions[sessionID]; exists {
		return s, nil
	}

	if f.config.MaxSessions > 0 && len(f.sessions) >= f.config.MaxSessions {
		return nil, fmt.Errorf("max sessions limit reached: %d", f.config.MaxSessions)
	}

	cfg := session.DefaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	agentConfig := types.NewAgentConfig()
	systemPrompt := f.config.SystemPrompt
	if len(f.cortexSkills) > 0 {
		systemPrompt += agentskills.BuildSystemPromptInjectionWithTriggers(f.cortexSkills)
	}
	// 长期记忆 user 全局合并（P3.2）：
	//   - UserMergeEnabled=true 时，把 session 归属到 user（WithUserID > DefaultUserID
	//     > "default"），写 metadata 'user_id'（INSERT OR IGNORE 固化）；失败仅记日志，
	//     不影响会话（评审 R8：不吞错，记日志便于排查双源漂移）。
	//   - 工具/L1 的 uid 一律读 metadata.user_id（SessionUserID / UserIDForSession
	//     单一事实源，评审 B3 修法），未开启或读不到时回退 sessionID（per-session
	//     语义不变）。
	userID := ""
	if f.longTermMem != nil && f.config.LongTermMemory.UserMergeEnabled {
		resolved := dinoMem.ResolveUserID(cfg.UserID, f.config.LongTermMemory.DefaultUserID)
		if err := f.longTermMem.SetSessionUser(ctx, sessionID, resolved); err != nil {
			logger.Warn("[DinoFactory] set session user",
				slog.String("session_id", sessionID),
				slog.String("user_id", resolved),
				slog.String("error", err.Error()))
		}
		userID = f.longTermMem.SessionUserID(ctx, sessionID)
	}

	// 长期记忆 L1 分层披露：session 级快照，CreateSession 时计算一次，
	// 不随 turn 刷新（评审 R7）。uid = userID（user 全局合并）或 sessionID
	// （per-session，UserMergeEnabled=false 时 userID 为空）。
	if f.longTermMem != nil {
		l1UID := userID
		if l1UID == "" {
			l1UID = sessionID
		}
		if l1 := f.longTermMem.BuildLayeredPrompt(ctx, l1UID); l1 != "" {
			systemPrompt += "\n\n" + l1
		}
	}
	agentConfig.SystemMessage = systemPrompt
	agentConfig.MaxIterations = f.config.MaxIterations
	agentConfig.Timeout = f.config.Timeout
	agentConfig.ToolExecutionTimeout = f.config.ToolExecutionTimeout
	agentConfig.ToolTimeouts = cloneToolTimeouts(f.config.ToolTimeouts)
	// 评审 BLOCKER-3：wait_agent 内部默认 30s，engine 外壳必须配得更高，否则
	// engine ToolExecutionTimeout(60s)/ToolTimeouts 先 cancel → 父代理拿 tool error
	// 而非 {timed_out:true} 信封。这里抬高外壳到 200s（wait 内部 30s 先返回）。
	if agentConfig.ToolTimeouts == nil {
		agentConfig.ToolTimeouts = make(map[string]time.Duration)
	}
	if _, ok := agentConfig.ToolTimeouts["wait_agent"]; !ok {
		agentConfig.ToolTimeouts["wait_agent"] = dinoAgent.WaitAgentToolShellTimeout
	}
	agentConfig.ToolTimeoutCalculator = f.config.ToolTimeoutCalculator
	agentConfig.DoomLoopThreshold = f.config.LoopDetection.MaxRepeats
	agentConfig.Temperature = f.config.Temperature
	agentConfig.MaxCompletionTokens = f.config.MaxTokens
	agentConfig.TopP = f.config.TopP
	agentConfig.MaxBudgetTokens = f.config.Memory.MaxBudgetTokens
	agentConfig.CompactAfterTurns = f.config.Memory.CompactAfterTurns
	agentConfig.CompactionPrefix = f.config.Memory.CompactionPrefix
	agentConfig.CacheAnchorTokens = f.config.Memory.CacheAnchorTokens
	agentConfig.PromptCaching = f.config.PromptCaching.Enabled
	agentConfig.ToolParallelismLimit = f.config.Tools.MaxToolParallelism
	agentConfig.StreamBufferSize = f.config.Tools.StreamBufferSize
	sid := sessionID
	agentConfig.RemainPromptTokens = func() int {
		if !f.config.Budget.Enabled || f.config.Budget.MaxTokens <= 0 {
			return -1
		}
		st := f.budget.GetState(sid)
		r := st.MaxTokens - st.UsedTokens
		if r < 0 {
			return 0
		}
		return r
	}
	compressTh := f.config.Memory.CompressThreshold
	if compressTh <= 0 && f.config.Budget.MaxToolCalls > 0 {
		compressTh = f.config.Budget.MaxToolCalls / 2
	}
	if compressTh <= 0 {
		compressTh = types.NewAgentConfig().MemoryCompressThreshold
	}
	agentConfig.EnableMemoryCompress = f.config.Memory.EnableCompress
	agentConfig.MemoryCompressThreshold = compressTh

	agent := engine.NewAgentEngine(f.llmProvider, agentConfig)
	// 三段式前缀保留（P3.1）：SummaryGenerator 由构造方注入确定性压缩闭包，
	// engine 自身不 import dino/chatstore（评审 BLOCKER B1）。P3.4 换 LLM 摘要
	// 时只需替换此闭包，engine 侧接口（func(existingSummary, mid)) 不变（R3）。
	if agentConfig.CompactionPrefix {
		agent.SetCompactionOptions(&engine.CompactionOptions{
			SummaryGenerator: func(existingSummary string, mid []types.Message) string {
				return chatstore.DeterministicCompact(existingSummary, mid, chatstore.DefaultCompactConfig())
			},
		})
	}
	if f.hooks != nil {
		agent.SetHooks(ctx, f.hooks)
	}

	sessionTools := f.tools.GetAll()

	// 长期记忆工具：与 f.longTermMem.Manager() 复用同一进程内单例，不会开第二个连接。
	// uid = userID（user 全局合并，metadata 单一事实源）或回退 sessionID。
	if f.longTermMem != nil {
		ltmTools := f.longTermMem.MemoryToolsForSession(sessionID,
			dinoMem.WithToolNameOverride("memory"),
			dinoMem.WithUserID(userID))
		sessionTools = append(sessionTools, ltmTools...)
		logger.Info("[DinoFactory] Added long-term memory tools", slog.Int("count", len(ltmTools)))
	}

	logger.Info("[DinoFactory] Total tools in registry", slog.Int("count", len(sessionTools)))
	var ruleset permission.Ruleset
	if len(f.config.Permission) > 0 {
		ruleset = permission.Merge(permission.FromConfig(f.config.Permission), permission.DefaultRuleset())
	} else {
		ruleset = permission.Merge(
			permission.FromAllowDenyAsk(f.config.Tools.Denied, f.config.Tools.ApprovalRequired, f.config.Tools.Allowed),
			permission.DefaultRuleset(),
		)
	}
	evaluator := permission.NewEvaluator(ruleset)
	senderAdapter := &toolEventSenderAdapter{sender: f.streamSender}
	var wrappedTools []types.Tool
	needApproval := make(map[string]bool)
	for _, t := range sessionTools {
		name := t.Name()
		action := evaluator.Evaluate(name, nil)
		if action == permission.ActionDeny {
			logger.Info("[DinoFactory] Tool denied by permission", slog.String("tool", name))
			continue
		}
		if action == permission.ActionAsk {
			needApproval[name] = true
		}
		logger.Info("[DinoFactory] Adding tool", slog.String("tool", name), slog.String("permission", string(action)))
		wrappedTools = append(wrappedTools, f.wrapSessionTool(t, sessionID, senderAdapter, needApproval))
	}

	if f.sessionToolsProvider != nil {
		for _, t := range f.sessionToolsProvider(sessionID) {
			if t == nil {
				continue
			}
			name := t.Name()
			action := evaluator.Evaluate(name, nil)
			if action == permission.ActionDeny {
				logger.Info("[DinoFactory] Tool denied by permission", slog.String("tool", name))
				continue
			}
			if action == permission.ActionAsk {
				needApproval[name] = true
			}
			logger.Info("[DinoFactory] Adding tool", slog.String("tool", name), slog.String("permission", string(action)))
			wrappedTools = append(wrappedTools, f.wrapSessionTool(t, sessionID, senderAdapter, needApproval))
		}
	}

	if f.subagentManager != nil {
		delegateTool := dinoAgent.NewSubagentTool(f.subagentManager)
		registerDelegate := types.Tool(delegateTool)
		if f.config.Subagent.ReplayToParentMemory {
			registerDelegate = newDelegateParentMemoryTool(delegateTool, sessionID, func() types.MemoryProvider {
				return agent.GetMemory()
			})
		}
		// Subagent delegate is never approval-gated; apply limiter + nonFatal so
		// oversized results and errors behave like every other session tool.
		wrapped := dinoTools.WrapToolResultLimiter(registerDelegate, f.config.Tools.ResultLimiterMaxBytes, f.config.Tools.ResultLimiterMaxStringBytes)
		wrapped = dinoTools.WrapNonFatalTool(wrapped)
		wrapped = dinoTools.WrapLoopDetection(wrapped, sessionID, f.loopDetector, senderAdapter)
		wrappedTools = append(wrappedTools, wrapped)
		logger.Info("[DinoFactory] Adding tool", slog.String("tool", registerDelegate.Name()))

		// —— S3：spawn_agent / wait_agent 工具族（subagent-s3s4 §3/§5.2）——
		// 每 session 一个 mailbox，工具用闭包注入（不持 session 状态，实例可复用）。
		mb := dinoAgent.NewMailbox(dinoAgent.DefaultMailboxCap, 0)
		f.sessionMailboxes[sessionID] = mb

		spawnTool := dinoAgent.NewSpawnAgentTool(f.subagentManager,
			func() *dinoAgent.Mailbox { return f.sessionMailbox(sessionID) },
			func() dinoAgent.AgentPath { return dinoAgent.RootAgentPath() },
			func() string { return sessionID })
		waitTool := dinoAgent.NewWaitAgentTool(f.subagentManager,
			func() *dinoAgent.Mailbox { return f.sessionMailbox(sessionID) },
			dinoAgent.DefaultWaitAgentTimeout)

		registerSpawn := types.Tool(spawnTool)
		registerWait := types.Tool(waitTool)
		// 与 delegate 同包装链：limiter + nonFatal（wait 的 {timed_out} 信封是正常结果，
		// nonFatal 不会吞；spawn 的 {task_id} 同理）。
		for _, t := range []types.Tool{registerSpawn, registerWait} {
			wrapped := dinoTools.WrapToolResultLimiter(t, f.config.Tools.ResultLimiterMaxBytes, f.config.Tools.ResultLimiterMaxStringBytes)
			wrapped = dinoTools.WrapNonFatalTool(wrapped)
			wrapped = dinoTools.WrapLoopDetection(wrapped, sessionID, f.loopDetector, senderAdapter)
			wrappedTools = append(wrappedTools, wrapped)
			logger.Info("[DinoFactory] Adding tool", slog.String("tool", t.Name()))
		}
	}

	logger.Info("[DinoFactory] Total tools added to agent", slog.Int("count", len(wrappedTools)))

	agent.AddTools(ctx, wrappedTools)

	if f.config.Memory.MaxHistoryMessages > 0 || f.config.Memory.EnableCompress {
		memConfig := &chatstore.Config{
			MaxHistoryMessages:      f.config.Memory.MaxHistoryMessages,
			EnableMemoryCompress:    f.config.Memory.EnableCompress,
			MemoryCompressThreshold: compressTh,
			KeepRecentCount:         f.config.Memory.KeepRecentCount,
			PersistDirectory:        f.config.Memory.PersistDirectory,
			SQLiteFile:              f.config.Memory.PersistFileName,
		}

		// 评审 B2/§5.3：EnableLLMCompress 时压缩统一由 Hybrid/engine 驱动，关底层
		// provider 的自触发（InMemory.compressAsync / SQLite.compressAsync），避免
		// DeterministicCompact 与 Hybrid.Compress 竞争写 metadata/删行。
		// 注意 memConfig 是共享指针：Hybrid.AddMessage 的自触发同样被关闭 → 压缩只走
		// engine 路径（A5 选项二，评审 R4）。
		if f.config.Memory.EnableLLMCompress {
			memConfig.EnableMemoryCompress = false
		}

		var memProvider chatstore.Provider
		var err error

		if f.config.Memory.Type == "sqlite" || f.config.Memory.PersistEnabled {
			memProvider, err = chatstore.NewSQLite(sessionID, memConfig)
			if err != nil {
				logger.Warn("[DinoFactory] Failed to create SQLite memory, falling back to in-memory", slog.String("error", err.Error()))
				memProvider = chatstore.NewInMemory(sessionID, memConfig)
			} else {
				logger.Info("[DinoFactory] SQLite memory enabled", slog.String("session_id", sessionID))
			}
		} else {
			memProvider = chatstore.NewInMemory(sessionID, memConfig)
		}

		if f.config.Memory.EnableLLMCompress {
			var lsm chatstore.LLMProvider
			if f.llmProvider != nil {
				lsm = chatstore.NewLLMSummaryAdapter(f.llmProvider)
			}
			memProvider = chatstore.NewHybrid(sessionID, memProvider, lsm, memConfig)
			logger.Info("[DinoFactory] Hybrid memory wrapper enabled",
				slog.String("session_id", sessionID),
				slog.Bool("llm_summary", lsm != nil))
		}

		agent.SetMemory(ctx, &memoryAdapter{provider: memProvider})
		logger.Info("[DinoFactory] Memory enabled",
			slog.String("session_id", sessionID),
			slog.Int("max_history", f.config.Memory.MaxHistoryMessages),
			slog.String("type", f.config.Memory.Type))
	}

	var toolSchemas []map[string]interface{}
	for _, t := range wrappedTools {
		toolSchemas = append(toolSchemas, t.Schema())
	}

	plannerHelper := session.NewPlannerHelper(
		f.config.PlannerMode.Enabled,
		f.config.PlannerMode.PromptPlan,
		f.config.PlannerMode.AutoApprove,
		f.llmProvider,
		toolSchemas,
	)

	// S4/B2（评审 RECOMMENDED-1 灰度开关）：WakeOnCompletion=true 才构造唤醒适配器。
	// 默认 false → NoWakeSource（nil 行为，不动调度）。唯一动 Session.run 的一刀。
	var wake session.WakeSource = session.NoWakeSource()
	if f.config.Subagent.WakeOnCompletion {
		if mb := f.sessionMailboxes[sessionID]; mb != nil {
			wake = newSessionWakeSource(mb, f.config.Subagent.CompletionMaxRunes)
			f.sessionWakes[sessionID] = wake.(*sessionWakeSource)
		}
	}

	sess := session.NewSession(sessionID, agent, f, ctx, cfg, plannerHelper, f.budget, wake)
	f.sessions[sessionID] = sess

	sess.Start()

	return sess, nil
}

func (f *dinoFactory) GetSession(sessionID string) *session.Session {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.sessions[sessionID]
}

// sessionMailbox 返回指定 session 的 mailbox（工具闭包用；读锁保护）。
func (f *dinoFactory) sessionMailbox(sessionID string) *dinoAgent.Mailbox {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.sessionMailboxes[sessionID]
}

func (f *dinoFactory) CloseSession(sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if sess, exists := f.sessions[sessionID]; exists {
		sess.Close()
		delete(f.sessions, sessionID)
	}
	// S3（评审 R5）：session 关闭 → mailbox Drop（显式回收孤儿结果）。
	if mb, exists := f.sessionMailboxes[sessionID]; exists {
		mb.Drop()
		delete(f.sessionMailboxes, sessionID)
	}
	// S4（评审 RECOMMENDED-7）：唤醒适配器反注册 SubscribeAll。
	if w, exists := f.sessionWakes[sessionID]; exists {
		w.Close()
		delete(f.sessionWakes, sessionID)
	}
	// S3/B1 铺路（评审 B2 BLOCKER）：释放该 session 派生的所有子代理 cancel。
	// S1 阶段无 spawn，此钩子是接口预留；S3 spawn_agent 落地后生效。
	if f.subagentManager != nil {
		f.subagentManager.CloseSession(sessionID)
	}
}

func (f *dinoFactory) CloseAll() {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, sess := range f.sessions {
		sess.Close()
	}
	f.sessions = make(map[string]*session.Session)
	for sid, mb := range f.sessionMailboxes {
		mb.Drop()
		delete(f.sessionMailboxes, sid)
	}
	for sid, w := range f.sessionWakes {
		w.Close()
		delete(f.sessionWakes, sid)
	}
}

func (f *dinoFactory) GetTools() []types.Tool {
	return f.tools.GetAll()
}

func (f *dinoFactory) GetSkills() []*Skill {
	result := make([]*Skill, len(f.cortexSkills))
	for i, s := range f.cortexSkills {
		result[i] = &Skill{
			Name:        s.Name,
			Description: s.Description,
			Prompt:      s.Content,
		}
	}
	return result
}

func (f *dinoFactory) RespondToolApproval(requestID string, approved bool) {
	if f.approvalStore != nil {
		f.approvalStore.Respond(requestID, approved)
	}
}

// SaveSessionSnapshot writes JSON with GetChatHistory-shaped messages (windowed), not a full DB export.
func (f *dinoFactory) SaveSessionSnapshot(ctx context.Context, sessionID string, dir string) (string, error) {
	f.mu.RLock()
	s := f.sessions[sessionID]
	f.mu.RUnlock()
	if s == nil {
		return "", fmt.Errorf("session not found: %s", sessionID)
	}
	snap, err := session.BuildSnapshotFromSession(ctx, s)
	if err != nil {
		return "", err
	}
	if dir == "" {
		dir = filepath.Join(f.config.Memory.PersistDirectory, "snapshots")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, sessionID+".json")
	if err := session.SaveFileSnapshot(path, snap); err != nil {
		return "", err
	}
	return path, nil
}

func (f *dinoFactory) RestoreSessionSnapshot(ctx context.Context, sessionID string, snapPath string) error {
	snap, err := session.LoadFileSnapshot(snapPath)
	if err != nil {
		return err
	}
	if snap.SessionID != sessionID {
		return fmt.Errorf("snapshot session_id %q does not match %q", snap.SessionID, sessionID)
	}
	f.mu.RLock()
	s := f.sessions[sessionID]
	f.mu.RUnlock()
	if s == nil {
		var cerr error
		// 快照恢复同样走归属写入（评审 R3）：UserMergeEnabled 时把恢复的 session
		// 归到 DefaultUserID/"default"，避免落进错误的 user 桶。
		var opts []session.Option
		if f.longTermMem != nil && f.config.LongTermMemory.UserMergeEnabled {
			resolved := dinoMem.ResolveUserID("", f.config.LongTermMemory.DefaultUserID)
			opts = append(opts, session.WithUserID(resolved))
		}
		s, cerr = f.CreateSession(ctx, sessionID, opts...)
		if cerr != nil {
			return cerr
		}
	}
	return session.ApplySnapshotToSession(ctx, s, snap)
}

// wrapSessionTool applies the full tool wrapper stack for a session tool, from
// innermost to outermost: approval (path + explicit) -> result limiter ->
// nonFatal -> loop detection.
//
// Ordering rationale:
//  1. approval is innermost so a rejection surfaces as a real error (veto) and
//     is NOT turned into a recoverable {ok:false} by nonFatal (BLOCKER-2);
//  2. limiter bounds the execution result (approval input is never bounded);
//     its oversized-result {ok:false} map is a normal result nonFatal returns;
//  3. nonFatal converts execution errors into structured results fed back to
//     the model, but passes through loop and approval-rejection errors;
//  4. loopDetection is outermost so it observes every real call and its
//     LoopDetectedError pierces nonFatal to stop the loop.
func (f *dinoFactory) wrapSessionTool(t types.Tool, sessionID string, senderAdapter *toolEventSenderAdapter, needApproval map[string]bool) types.Tool {
	wrapped := wrapWorkspacePathTools(t, f.config.WorkspaceRoot, sessionID, f.approvalStore)
	if needApproval[t.Name()] {
		wrapped = NewApprovalTool(wrapped, sessionID, f.approvalStore, needApproval)
	}
	wrapped = dinoTools.WrapToolResultLimiter(wrapped, f.config.Tools.ResultLimiterMaxBytes, f.config.Tools.ResultLimiterMaxStringBytes)
	wrapped = dinoTools.WrapNonFatalTool(wrapped)
	wrapped = dinoTools.WrapLoopDetection(wrapped, sessionID, f.loopDetector, senderAdapter)
	return wrapped
}

func wrapWorkspacePathTools(t types.Tool, workspaceRoot, sessionID string, store *ApprovalStore) types.Tool {
	switch t.Name() {
	case "read_file", "write_file", "edit_file", "list_directory", "glob", "grep":
		return NewExternalPathApprovalTool(t, workspaceRoot, sessionID, store)
	default:
		return t
	}
}

const shutdownTimeout = 30 * time.Second

func (f *dinoFactory) Shutdown(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		f.mu.Lock()
		defer f.mu.Unlock()

		for _, sess := range f.sessions {
			sess.Close()
		}
		f.sessions = make(map[string]*session.Session)

		if resettable, ok := f.budget.(interface{ ResetAll() }); ok {
			resettable.ResetAll()
		}

		if f.subagentManager != nil {
			f.subagentManager.Close()
		}

		if f.mcpManager != nil {
			f.mcpManager.Close()
		}

		if f.longTermMem != nil {
			f.longTermMem.Stop()
			f.longTermMem = nil
		}

		done <- nil
	}()

	select {
	case <-shutdownCtx.Done():
		return fmt.Errorf("shutdown timed out after %v", shutdownTimeout)
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func loadBuiltinTools(reg *dinoTools.Registry, workspace string) error {
	builtinTools := []types.Tool{
		agentTools.NewReadTool(workspace),
		agentTools.NewWriteTool(workspace),
		agentTools.NewEditTool(workspace),
		dinoTools.NewBashTool(workspace),
		dinoTools.NewGlobTool(workspace),
		dinoTools.NewGrepTool(workspace),
		dinoTools.NewListDirectoryTool(workspace),
		dinoTools.NewQuestionTool(),
		dinoTools.NewJobKillTool(),
		dinoTools.NewJobOutputTool(),
		dinoTools.NewWebFetchTool(),
		dinoTools.NewWebSearchTool(),
		dinoTools.NewTodoTool(),
		dinoTools.NewMCPClientTool(),
	}
	for _, t := range builtinTools {
		if err := reg.Register(t); err != nil {
			return err
		}
	}
	return nil
}
