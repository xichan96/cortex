package dino

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/session"
)

type ToolDefinition struct {
	ID          string
	Name        string
	Description string
	Parameters  map[string]interface{}
	Metadata    ToolMetadataExt
}

type ToolMetadataExt struct {
	Title       string                 `json:"title,omitempty"`
	Snapshot    string                 `json:"snapshot,omitempty"`
	Truncated   bool                   `json:"truncated,omitempty"`
	OutputPath  string                 `json:"output_path,omitempty"`
	Attachments []FileAttachment       `json:"attachments,omitempty"`
	Extra       map[string]interface{} `json:"extra,omitempty"`
}

type ToolContext struct {
	SessionID string
	MessageID string
	Agent     string
	Abort     <-chan struct{}
	CallID    string
	Extra     map[string]interface{}
	Messages  []MessageInfo
	Metadata  func(title string, metadata map[string]interface{})
	Ask       func(request PermissionRequest) error
}

type MessageInfo struct {
	Role    string
	Content string
	Parts   []PartInfo
}

type PartInfo struct {
	Type   string
	Text   string
	Tool   string
	CallID string
	State  ToolState
	Input  map[string]interface{}
	Output string
	Error  string
}

type ToolState struct {
	Status   string                 `json:"status"`
	Input    map[string]interface{} `json:"input,omitempty"`
	Output   string                 `json:"output,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	Title    string                 `json:"title,omitempty"`
	Time     *ToolTime              `json:"time,omitempty"`
}

type ToolTime struct {
	Start int64 `json:"start"`
	End   int64 `json:"end,omitempty"`
}

type ToolResult struct {
	Title       string                 `json:"title"`
	Metadata    map[string]interface{} `json:"metadata"`
	Output      string                 `json:"output"`
	Attachments []FileAttachment       `json:"attachments,omitempty"`
}

type ToolExecutor func(args map[string]interface{}, ctx ToolContext) (*ToolResult, error)

type Tool struct {
	definition  *ToolDefinition
	executor    ToolExecutor
	paramSchema map[string]interface{}
}

func NewTool(id, name, description string, executor ToolExecutor) *Tool {
	return &Tool{
		definition: &ToolDefinition{
			ID:          id,
			Name:        name,
			Description: description,
		},
		executor: executor,
	}
}

func (t *Tool) WithParameters(params map[string]interface{}) *Tool {
	t.definition.Parameters = params
	return t
}

func (t *Tool) Name() string {
	return t.definition.Name
}

func (t *Tool) Description() string {
	return t.definition.Description
}

func (t *Tool) Schema() map[string]interface{} {
	if t.definition.Parameters != nil {
		return t.definition.Parameters
	}
	return map[string]interface{}{}
}

func (t *Tool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		ToolType: "builtin",
	}
}

func (t *Tool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if t.executor == nil {
		return nil, fmt.Errorf("tool %s: no executor (e.g. from ToolFromJSON)", t.definition.Name)
	}
	var abortChan <-chan struct{}
	if ctx != nil {
		abortChan = ctx.Done()
	}

	var localMeta ToolMetadataExt
	toolCtx := ToolContext{
		Abort: abortChan,
		Metadata: func(title string, metadata map[string]interface{}) {
			localMeta.Title = title
			localMeta.Extra = metadata
		},
		Ask: func(request PermissionRequest) error {
			return nil
		},
	}

	result, err := t.executor(input, toolCtx)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"title":       result.Title,
		"metadata":    result.Metadata,
		"output":      result.Output,
		"attachments": result.Attachments,
	}, nil
}

func (t *Tool) ToAgentTool() types.Tool {
	return &toolAdapter{
		tool: t,
	}
}

type toolAdapter struct {
	tool *Tool
}

func (a *toolAdapter) Name() string                   { return a.tool.Name() }
func (a *toolAdapter) Description() string            { return a.tool.Description() }
func (a *toolAdapter) Schema() map[string]interface{} { return a.tool.Schema() }
func (a *toolAdapter) Metadata() types.ToolMetadata   { return a.tool.Metadata() }

func (a *toolAdapter) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	return a.tool.Execute(ctx, input)
}

type PermissionRequest struct {
	Patterns  []string
	SessionID string
	Metadata  map[string]interface{}
	Always    []string
	Ruleset   *session.Permission
}

func ToolFromAgent(agentTool types.Tool) *Tool {
	id := agentTool.Name()
	if meta := agentTool.Metadata(); meta.SourceNodeName != "" {
		id = meta.SourceNodeName
	}
	return &Tool{
		definition: &ToolDefinition{
			ID:          id,
			Name:        agentTool.Name(),
			Description: agentTool.Description(),
			Parameters:  agentTool.Schema(),
		},
		paramSchema: agentTool.Schema(),
	}
}

func (t *Tool) ToJSON() (string, error) {
	data, err := json.Marshal(t.definition)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func ToolFromJSON(jsonStr string) (*Tool, error) {
	var def ToolDefinition
	if err := json.Unmarshal([]byte(jsonStr), &def); err != nil {
		return nil, err
	}
	return &Tool{
		definition: &def,
	}, nil
}

type ApprovalStore struct {
	mu      sync.Mutex
	pending map[string]chan bool
	sender  ApprovalSender
	timeout time.Duration
}

type ApprovalSender interface {
	SendToolApprovalRequest(sessionID, requestID, toolName, arguments string)
	SendToolApprovalResponse(sessionID, requestID, toolName string, approved bool)
}

func NewApprovalStore(timeout time.Duration) *ApprovalStore {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &ApprovalStore{
		pending: make(map[string]chan bool),
		timeout: timeout,
	}
}

func (s *ApprovalStore) SetSender(sender ApprovalSender) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sender = sender
}

func (s *ApprovalStore) RequestApproval(
	ctx context.Context,
	sessionID,
	toolName,
	arguments string,
) (approved bool, err error) {
	requestID := generateRequestID()
	ch := make(chan bool, 1)

	s.mu.Lock()
	s.pending[requestID] = ch
	sender := s.sender
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.pending, requestID)
		s.mu.Unlock()
	}()

	if sender != nil {
		sender.SendToolApprovalRequest(sessionID, requestID, toolName, arguments)
	}

	timer := time.NewTimer(s.timeout)
	defer timer.Stop()

	select {
	case approved = <-ch:
		if sender != nil {
			sender.SendToolApprovalResponse(sessionID, requestID, toolName, approved)
		}
		return approved, nil
	case <-ctx.Done():
		return false, ctx.Err()
	case <-timer.C:
		return false, ErrApprovalTimeout
	}
}

func (s *ApprovalStore) Respond(requestID string, approved bool) {
	s.mu.Lock()
	ch := s.pending[requestID]
	s.mu.Unlock()

	if ch != nil {
		select {
		case ch <- approved:
		default:
		}
	}
}

func (s *ApprovalStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.pending {
		select {
		case ch <- false:
		default:
		}
	}
	s.pending = make(map[string]chan bool)
}

type ApprovalTool struct {
	inner     types.Tool
	sessionID string
	store     *ApprovalStore
	dangerous map[string]bool
}

func NewApprovalTool(inner types.Tool, sessionID string, store *ApprovalStore, dangerous map[string]bool) *ApprovalTool {
	return &ApprovalTool{
		inner:     inner,
		sessionID: sessionID,
		store:     store,
		dangerous: dangerous,
	}
}

func (a *ApprovalTool) Name() string {
	return a.inner.Name()
}

func (a *ApprovalTool) Description() string {
	return a.inner.Description()
}

func (a *ApprovalTool) Schema() map[string]interface{} {
	return a.inner.Schema()
}

func (a *ApprovalTool) Metadata() types.ToolMetadata {
	return a.inner.Metadata()
}

func (a *ApprovalTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	toolName := a.inner.Name()

	if !a.dangerous[toolName] {
		return a.inner.Execute(ctx, input)
	}

	argsJSON, errMarshal := marshalJSON(input)
	if errMarshal != nil {
		argsJSON = fmt.Sprintf("%v", input)
	}
	approved, err := a.store.RequestApproval(ctx, a.sessionID, toolName, argsJSON)

	if err != nil {
		return nil, fmt.Errorf("approval request failed for tool '%s': %w", toolName, err)
	}

	if !approved {
		return nil, fmt.Errorf("tool '%s' was rejected by user", toolName)
	}

	return a.inner.Execute(ctx, input)
}

func (a *ApprovalTool) RequiresApproval() bool {
	return true
}

var approvalRequestIDCounter uint64

func generateRequestID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddUint64(&approvalRequestIDCounter, 1))
}

func marshalJSON(v interface{}) (string, error) {
	if v == nil {
		return "", nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type ApprovalError string

func (e ApprovalError) Error() string {
	return string(e)
}

const ErrApprovalTimeout ApprovalError = "approval timeout"
