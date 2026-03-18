package dino

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/engine"
	agentTools "github.com/xichan96/cortex/agent/tools/builtin/fs"
	"github.com/xichan96/cortex/agent/types"

	dinoLoop "github.com/xichan96/cortex/dino/loop"
	"github.com/xichan96/cortex/dino/memory"
	"github.com/xichan96/cortex/dino/session"
	dinoSkills "github.com/xichan96/cortex/dino/skills"
	dinoTools "github.com/xichan96/cortex/dino/tools"
)

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
		inputJSON, _ := json.Marshal(input)
		if err := m.provider.AddMessage(ctx, memory.Message{
			Role:      role,
			Content:   fmt.Sprintf("Input: %s", string(inputJSON)),
			Timestamp: time.Now(),
		}); err != nil {
			return err
		}
	}

	if output != nil {
		outputJSON, _ := json.Marshal(output)
		if err := m.provider.AddMessage(ctx, memory.Message{
			Role:      "assistant",
			Content:   fmt.Sprintf("Output: %s", string(outputJSON)),
			Timestamp: time.Now(),
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
}

type dinoFactory struct {
	config        *Config
	llmProvider   types.LLMProvider
	tools         *dinoTools.Registry
	loopDetector  dinoLoop.Detector
	budget        Budget
	skills        []*Skill
	approvalStore *ApprovalStore
	sessions      map[string]*session.Session
	mu            sync.RWMutex
	streamSender  StreamEventSender
}

func (f *dinoFactory) LoopDetector() dinoLoop.Detector {
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

func (f *dinoFactory) RecordLoop(sessionID string, action dinoLoop.Action) {
	f.loopDetector.Record(sessionID, action)
}

func (f *dinoFactory) RecordTokens(ctx context.Context, sessionID string, tokens int) {
	f.budget.RecordTokens(ctx, sessionID, tokens)
}

func (f *dinoFactory) Detect(ctx context.Context, sessionID string, action dinoLoop.Action) *dinoLoop.Result {
	return f.loopDetector.Detect(ctx, sessionID, action)
}

type FactoryOption func(*dinoFactory)

func WithStreamEventSender(sender StreamEventSender) FactoryOption {
	return func(f *dinoFactory) {
		f.streamSender = sender
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
	var cortexSkills []*dinoSkills.Skill
	if cfg.Skills.Path != "" {
		loader := dinoSkills.NewLoader()
		if err := loader.LoadFromDirs(context.Background(), []string{cfg.Skills.Path}); err != nil {
			log.Printf("failed to load skills from %s: %v", cfg.Skills.Path, err)
		} else {
			cortexSkills = loader.All()
			log.Printf("loaded %d skills from %s", len(cortexSkills), cfg.Skills.Path)
			for _, s := range cortexSkills {
				log.Printf("  - skill: %s", s.Name)
				loadedSkills = append(loadedSkills, &Skill{
					Name:        s.Name,
					Description: s.Description,
					Prompt:      s.Content,
				})
			}
		}
	}

	if len(cortexSkills) > 0 {
		if err := toolRegistry.Register(dinoTools.NewSkillTool(cortexSkills)); err != nil {
			log.Printf("failed to register skill tool: %v", err)
		}
	}

	approvalStore := NewApprovalStore(5 * time.Minute)

	f := &dinoFactory{
		config:      cfg,
		llmProvider: llmProvider,
		tools:       toolRegistry,
		loopDetector: dinoLoop.NewDetector(&dinoLoop.Config{
			Enabled:             cfg.LoopDetection.Enabled,
			MaxRepeats:          cfg.LoopDetection.MaxRepeats,
			SimilarityThreshold: cfg.LoopDetection.SimilarityThreshold,
		}),
		budget:        NewBudget(&cfg.Budget),
		skills:        loadedSkills,
		approvalStore: approvalStore,
		sessions:      make(map[string]*session.Session),
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
	log.Printf("[DinoFactory] Total tools in registry: %d", len(sessionTools))
	for _, t := range sessionTools {
		log.Printf("[DinoFactory] Available tool: %s", t.Name())
	}
	var wrappedTools []types.Tool
	allowed := make(map[string]bool)
	denied := make(map[string]bool)
	dangerous := make(map[string]bool)
	if f.config != nil {
		log.Printf("[DinoFactory] Tools config - Allowed: %v, Denied: %v, ApprovalRequired: %v",
			f.config.Tools.Allowed, f.config.Tools.Denied, f.config.Tools.ApprovalRequired)
		for _, name := range f.config.Tools.Allowed {
			allowed[name] = true
		}
		for _, name := range f.config.Tools.Denied {
			denied[name] = true
		}
		for _, name := range f.config.Tools.ApprovalRequired {
			dangerous[name] = true
		}
	}

	senderAdapter := &toolEventSenderAdapter{sender: f.streamSender}

	for _, t := range sessionTools {
		name := t.Name()
		if len(denied) > 0 && denied[name] {
			log.Printf("[DinoFactory] Tool %s denied by denied list", name)
			continue
		}
		if len(allowed) > 0 && !allowed[name] {
			log.Printf("[DinoFactory] Tool %s not in allowed list, skipping", name)
			continue
		}
		log.Printf("[DinoFactory] Adding tool: %s", name)
		wrapped := t
		if len(dangerous) > 0 && dangerous[name] {
			log.Printf("[DinoFactory] Tool %s requires approval", name)
			wrapped = NewApprovalTool(wrapped, sessionID, f.approvalStore, dangerous)
		}
		wrapped = dinoTools.WrapLoopDetection(wrapped, sessionID, f.loopDetector, senderAdapter)
		wrappedTools = append(wrappedTools, wrapped)
	}

	log.Printf("[DinoFactory] Total tools added to agent: %d", len(wrappedTools))

	agent.AddTools(ctx, wrappedTools)

	if f.config.Memory.MaxHistoryMessages > 0 || f.config.Memory.EnableCompress {
		memConfig := &memory.Config{
			MaxHistoryMessages:      f.config.Memory.MaxHistoryMessages,
			EnableMemoryCompress:    f.config.Memory.EnableCompress,
			MemoryCompressThreshold: f.config.Memory.CompressThreshold,
			KeepRecentCount:         f.config.Memory.KeepRecentCount,
			PersistDirectory:        f.config.Memory.PersistDirectory,
		}

		var memProvider memory.Provider
		var err error

		if f.config.Memory.Type == "sqlite" || f.config.Memory.PersistEnabled {
			memProvider, err = memory.NewSQLite(sessionID, memConfig)
			if err != nil {
				log.Printf("[DinoFactory] Failed to create SQLite memory, falling back to in-memory: %v", err)
				memProvider = memory.NewInMemory(sessionID, memConfig)
			} else {
				log.Printf("[DinoFactory] SQLite memory enabled for session %s", sessionID)
			}
		} else {
			memProvider = memory.NewInMemory(sessionID, memConfig)
		}

		agent.SetMemory(ctx, &memoryAdapter{provider: memProvider})
		log.Printf("[DinoFactory] Memory enabled for session %s (max history: %d, type: %s)", sessionID, f.config.Memory.MaxHistoryMessages, f.config.Memory.Type)
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

	sess := session.NewSession(sessionID, agent, f, ctx, cfg, plannerHelper)
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

func (f *dinoFactory) Shutdown(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, sess := range f.sessions {
		sess.Close()
	}

	f.sessions = make(map[string]*session.Session)
	if resettable, ok := f.budget.(interface{ ResetAll() }); ok {
		resettable.ResetAll()
	}

	return nil
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
	}
	for _, t := range builtinTools {
		if err := reg.Register(t); err != nil {
			return err
		}
	}
	return nil
}
