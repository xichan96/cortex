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
	agentproviders "github.com/xichan96/cortex/agent/providers"
	agentTools "github.com/xichan96/cortex/agent/tools/builtin/fs"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/logger"

	agentskills "github.com/xichan96/cortex/agent/skills"
	agentutils "github.com/xichan96/cortex/agent/utils"
	dinoAgent "github.com/xichan96/cortex/dino/agent"
	"github.com/xichan96/cortex/dino/chatstore"
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
	skills               []*Skill
	approvalStore        *ApprovalStore
	sessions             map[string]*session.Session
	mu                   sync.RWMutex
	streamSender         StreamEventSender
	subagentManager      *dinoAgent.SubagentManager
	mcpManager           *dinoTools.MCPManager
	sessionToolsProvider func(sessionID string) []types.Tool
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

func NewDinoFactory(cfg *Config, opts ...FactoryOption) (DinoFactory, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	llmProvider, err := createLLMProvider(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create LLM provider: %w", err)
	}

	toolRegistry := dinoTools.NewRegistry()
	if err := loadBuiltinTools(toolRegistry, cfg.WorkspaceRoot); err != nil {
		return nil, fmt.Errorf("load builtin tools: %w", err)
	}

	var loadedSkills []*Skill
	skillRegistry := agentskills.NewRegistry()
	if cfg.Skills.Path != "" && cfg.Skills.AutoLoad {
		if err := skillRegistry.LoadFromDirs(context.Background(), logger.GetLogger(), []string{cfg.Skills.Path}); err != nil {
			logger.Warn("failed to load skills", slog.String("path", cfg.Skills.Path), slog.String("error", err.Error()))
		} else {
			cortexSkills := skillRegistry.All()
			logger.Info("loaded skills", slog.Int("count", len(cortexSkills)), slog.String("path", cfg.Skills.Path))
			for _, s := range cortexSkills {
				logger.Info("skill loaded", slog.String("name", s.Name))
				loadedSkills = append(loadedSkills, &Skill{
					Name:        s.Name,
					Description: s.Description,
					Prompt:      s.Content,
				})
			}
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
		budget:        NewBudget(&cfg.Budget),
		skills:        loadedSkills,
		approvalStore: approvalStore,
		sessions:      make(map[string]*session.Session),
	}

	f.subagentManager = dinoAgent.NewSubagentManager(&cfg.Subagent, f)

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
	if len(f.skills) > 0 {
		systemPrompt += "\n\nAvailable Skills:\n"
		for _, s := range f.skills {
			systemPrompt += fmt.Sprintf("- %s: %s\n", s.Name, s.Description)
		}
	}
	agentConfig.SystemMessage = systemPrompt
	agentConfig.MaxIterations = f.config.MaxIterations
	agentConfig.Timeout = f.config.Timeout
	agentConfig.ToolExecutionTimeout = f.config.ToolExecutionTimeout
	agentConfig.ToolTimeouts = f.config.ToolTimeouts
	agentConfig.ToolTimeoutCalculator = f.config.ToolTimeoutCalculator
	agentConfig.DoomLoopThreshold = f.config.LoopDetection.MaxRepeats
	agentConfig.Temperature = f.config.Temperature
	agentConfig.MaxCompletionTokens = f.config.MaxTokens
	agentConfig.TopP = f.config.TopP
	agentConfig.MaxBudgetTokens = f.config.Memory.MaxBudgetTokens
	agentConfig.CompactAfterTurns = f.config.Memory.CompactAfterTurns
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

	sessionTools := f.tools.GetAll()
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
		wrapped := wrapWorkspacePathTools(t, f.config.WorkspaceRoot, sessionID, f.approvalStore)
		if needApproval[name] {
			wrapped = NewApprovalTool(wrapped, sessionID, f.approvalStore, needApproval)
		}
		wrapped = dinoTools.WrapLoopDetection(wrapped, sessionID, f.loopDetector, senderAdapter)
		wrappedTools = append(wrappedTools, wrapped)
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
			wrapped := wrapWorkspacePathTools(t, f.config.WorkspaceRoot, sessionID, f.approvalStore)
			if needApproval[name] {
				wrapped = NewApprovalTool(wrapped, sessionID, f.approvalStore, needApproval)
			}
			wrapped = dinoTools.WrapLoopDetection(wrapped, sessionID, f.loopDetector, senderAdapter)
			wrappedTools = append(wrappedTools, wrapped)
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
		wrapped := dinoTools.WrapLoopDetection(registerDelegate, sessionID, f.loopDetector, senderAdapter)
		wrappedTools = append(wrappedTools, wrapped)
		logger.Info("[DinoFactory] Adding tool", slog.String("tool", registerDelegate.Name()))
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

	sess := session.NewSession(sessionID, agent, f, ctx, cfg, plannerHelper, f.budget)
	f.sessions[sessionID] = sess

	sess.Start()

	return sess, nil
}

func (f *dinoFactory) GetSession(sessionID string) *session.Session {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.sessions[sessionID]
}

func (f *dinoFactory) CloseSession(sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if sess, exists := f.sessions[sessionID]; exists {
		sess.Close()
		delete(f.sessions, sessionID)
	}
}

func (f *dinoFactory) CloseAll() {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, sess := range f.sessions {
		sess.Close()
	}
	f.sessions = make(map[string]*session.Session)
}

func (f *dinoFactory) GetTools() []types.Tool {
	return f.tools.GetAll()
}

func (f *dinoFactory) GetSkills() []*Skill {
	return f.skills
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
		s, cerr = f.CreateSession(ctx, sessionID)
		if cerr != nil {
			return cerr
		}
	}
	return session.ApplySnapshotToSession(ctx, s, snap)
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
