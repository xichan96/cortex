package dino

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/engine"
	agentTools "github.com/xichan96/cortex/agent/tools/builtin/fs"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/logger"

	agentskills "github.com/xichan96/cortex/agent/skills"
	agentutils "github.com/xichan96/cortex/agent/utils"
	dinoAgent "github.com/xichan96/cortex/dino/agent"
	"github.com/xichan96/cortex/dino/memory"
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
	provider memory.Provider
}

func (m *memoryAdapter) LoadMemoryVariables(ctx context.Context) (map[string]interface{}, error) {
	return nil, nil
}

func (m *memoryAdapter) SaveContext(ctx context.Context, input, output map[string]interface{}) error {
	if input != nil {
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
		if err := m.provider.AddMessage(ctx, memory.Message{
			Role:    role,
			Content: fmt.Sprintf("Input: %s", string(inputJSON)),
		}); err != nil {
			return err
		}
	}

	if output != nil {
		outputJSON, err := json.Marshal(output)
		if err != nil {
			logger.Warn("[memoryAdapter] failed to marshal output", slog.String("error", err.Error()))
			outputJSON = []byte(fmt.Sprintf("%v", output))
		}
		if err := m.provider.AddMessage(ctx, memory.Message{
			Role:    "assistant",
			Content: fmt.Sprintf("Output: %s", string(outputJSON)),
		}); err != nil {
			return err
		}
	}

	return nil
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
			Role:    msg.Role,
			Content: msg.Content,
		}
	}
	return result, nil
}

func (m *memoryAdapter) CompressMemory(ctx context.Context, llm types.LLMProvider, maxMessages int) error {
	return m.provider.Compress(ctx)
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
}

type dinoFactory struct {
	config          *Config
	llmProvider     types.LLMProvider
	tools           *dinoTools.Registry
	loopDetector    agentutils.LoopDetector
	budget          Budget
	skills          []*Skill
	approvalStore   *ApprovalStore
	sessions        map[string]*session.Session
	mu              sync.RWMutex
	streamSender    StreamEventSender
	subagentManager *dinoAgent.SubagentManager
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
	agentConfig.MaxTokens = f.config.MaxTokens
	agentConfig.TopP = f.config.TopP
	agentConfig.EnableMemoryCompress = true
	agentConfig.MemoryCompressThreshold = f.config.Budget.MaxToolCalls / 2

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
		wrapped := dinoTools.WrapLoopDetection(delegateTool, sessionID, f.loopDetector, senderAdapter)
		wrappedTools = append(wrappedTools, wrapped)
		logger.Info("[DinoFactory] Adding tool", slog.String("tool", delegateTool.Name()))
	}

	logger.Info("[DinoFactory] Total tools added to agent", slog.Int("count", len(wrappedTools)))

	agent.AddTools(ctx, wrappedTools)

	if f.config.Memory.MaxHistoryMessages > 0 || f.config.Memory.EnableCompress {
		memConfig := &memory.Config{
			MaxHistoryMessages:      f.config.Memory.MaxHistoryMessages,
			EnableMemoryCompress:    f.config.Memory.EnableCompress,
			MemoryCompressThreshold: f.config.Memory.CompressThreshold,
			KeepRecentCount:         f.config.Memory.KeepRecentCount,
			PersistDirectory:        f.config.Memory.PersistDirectory,
			SQLiteFile:              f.config.Memory.PersistFileName,
		}

		var memProvider memory.Provider
		var err error

		if f.config.Memory.Type == "sqlite" || f.config.Memory.PersistEnabled {
			memProvider, err = memory.NewSQLite(sessionID, memConfig)
			if err != nil {
				logger.Warn("[DinoFactory] Failed to create SQLite memory, falling back to in-memory", slog.String("error", err.Error()))
				memProvider = memory.NewInMemory(sessionID, memConfig)
			} else {
				logger.Info("[DinoFactory] SQLite memory enabled", slog.String("session_id", sessionID))
			}
		} else {
			memProvider = memory.NewInMemory(sessionID, memConfig)
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
