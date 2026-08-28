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
	"sync/atomic"
	"time"

	agentfs "github.com/xichan96/cortex/agent/tools/builtin/fs"
	"github.com/xichan96/cortex/agent/types"
	dinoTools "github.com/xichan96/cortex/dino/tools"
	"github.com/xichan96/cortex/pkg/logger"
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

type DefinedTool struct {
	definition  *ToolDefinition
	executor    ToolExecutor
	paramSchema map[string]interface{}
}

func NewDefinedTool(id, name, description string, executor ToolExecutor) *DefinedTool {
	return &DefinedTool{
		definition: &ToolDefinition{
			ID:          id,
			Name:        name,
			Description: description,
		},
		executor: executor,
	}
}

func (t *DefinedTool) WithParameters(params map[string]interface{}) *DefinedTool {
	t.definition.Parameters = params
	return t
}

func (t *DefinedTool) Name() string {
	return t.definition.Name
}

func (t *DefinedTool) Description() string {
	return t.definition.Description
}

func (t *DefinedTool) Schema() map[string]interface{} {
	if t.definition.Parameters != nil {
		return t.definition.Parameters
	}
	return map[string]interface{}{}
}

func (t *DefinedTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		ToolType: "builtin",
	}
}

func (t *DefinedTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if t.executor == nil {
		return nil, fmt.Errorf("tool %s: no executor (e.g. from DefinedToolFromJSON)", t.definition.Name)
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
			return ErrApprovalNotAvailable
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

func (t *DefinedTool) ToAgentTool() types.Tool {
	return &definedToolAdapter{
		tool: t,
	}
}

type definedToolAdapter struct {
	tool *DefinedTool
}

func (a *definedToolAdapter) Name() string                   { return a.tool.Name() }
func (a *definedToolAdapter) Description() string            { return a.tool.Description() }
func (a *definedToolAdapter) Schema() map[string]interface{} { return a.tool.Schema() }
func (a *definedToolAdapter) Metadata() types.ToolMetadata   { return a.tool.Metadata() }

func (a *definedToolAdapter) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	return a.tool.Execute(ctx, input)
}

type PermissionRequest struct {
	Permission string
	SessionID  string
	Input      map[string]interface{}
	Metadata   map[string]interface{}
}

func DefinedToolFromAgent(agentTool types.Tool) *DefinedTool {
	id := agentTool.Name()
	if meta := agentTool.Metadata(); meta.SourceNodeName != "" {
		id = meta.SourceNodeName
	}
	return &DefinedTool{
		definition: &ToolDefinition{
			ID:          id,
			Name:        agentTool.Name(),
			Description: agentTool.Description(),
			Parameters:  agentTool.Schema(),
		},
		paramSchema: agentTool.Schema(),
	}
}

func (t *DefinedTool) ToJSON() (string, error) {
	data, err := json.Marshal(t.definition)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func DefinedToolFromJSON(jsonStr string) (*DefinedTool, error) {
	var def ToolDefinition
	if err := json.Unmarshal([]byte(jsonStr), &def); err != nil {
		return nil, err
	}
	return &DefinedTool{
		definition: &def,
	}, nil
}

// ApprovalStore gates tool execution on user approval via ApprovalSender.
// Call SetSender before any tool that uses ActionAsk runs; if sender is nil,
// RequestApproval returns ErrApprovalNotAvailable immediately.
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
	s.mu.Lock()
	sender := s.sender
	s.mu.Unlock()
	if sender == nil {
		return false, ErrApprovalNotAvailable
	}

	requestID := generateRequestID()
	ch := make(chan bool, 1)

	s.mu.Lock()
	s.pending[requestID] = ch
	s.mu.Unlock()

	sendRequest := func() {
		sender.SendToolApprovalRequest(sessionID, requestID, toolName, arguments)
	}

	cleanup := func() {
		s.mu.Lock()
		delete(s.pending, requestID)
		s.mu.Unlock()
	}

	timer := time.NewTimer(s.timeout)
	defer timer.Stop()

	sendRequest()

	select {
	case approved = <-ch:
		cleanup()
		sender.SendToolApprovalResponse(sessionID, requestID, toolName, approved)
		return approved, nil
	case <-ctx.Done():
		cleanup()
		return false, ctx.Err()
	case <-timer.C:
		cleanup()
		return false, ErrApprovalTimeout
	}
}

// approvalRespondTimeout bounds how long Respond blocks when the approval
// channel is full (the previous response has not been consumed yet, e.g. a
// double response). After the timeout the response is dropped rather than
// hanging the caller (HTTP/MCP gateway goroutine) forever. A var (not const)
// so tests can shorten it.
var approvalRespondTimeout = 5 * time.Second

// Respond delivers an approval decision to the pending RequestApproval waiter.
// It returns true if the decision was delivered, false if the requestID is
// unknown/cleaned up or the delivery timed out. Unlike the previous
// select-default drop, it blocks (up to approvalRespondTimeout) so a response
// is never silently lost while the waiter is still alive.
func (s *ApprovalStore) Respond(requestID string, approved bool) bool {
	s.mu.Lock()
	ch := s.pending[requestID]
	s.mu.Unlock()

	if ch == nil {
		return false // unknown / already cleaned up
	}
	select {
	case ch <- approved:
		return true
	case <-time.After(approvalRespondTimeout):
		logger.Warn("[ApprovalStore] approval response timed out",
			slog.String("request_id", requestID),
			slog.Bool("approved", approved))
		return false
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
		return nil, fmt.Errorf("failed to marshal tool arguments: %w", errMarshal)
	}
	approved, err := a.store.RequestApproval(ctx, a.sessionID, toolName, argsJSON)

	if err != nil {
		return nil, fmt.Errorf("approval request failed for tool '%s': %w", toolName, err)
	}

	if !approved {
		// Unrecoverable user veto — must surface as a real error, never be
		// swallowed by nonFatalTool and fed back to the model.
		return nil, &ApprovalRejectedError{ToolName: toolName}
	}

	return a.inner.Execute(ctx, input)
}

func (a *ApprovalTool) RequiresApproval() bool {
	return true
}

func collectOutsideAbsPaths(workspace, toolName string, input map[string]interface{}) ([]string, error) {
	if workspace == "*" || strings.TrimSpace(workspace) == "" {
		return nil, nil
	}
	var raw []string
	add := func(p string) error {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil
		}
		absW, absR, err := agentfs.ResolveAbsRequested(workspace, p)
		if err != nil {
			return err
		}
		if !agentfs.IsUnderWorkspaceRoot(absW, absR) {
			raw = append(raw, absR)
		}
		return nil
	}
	switch toolName {
	case "read_file", "write_file", "edit_file", "list_directory":
		if p, ok := input["path"].(string); ok {
			if err := add(p); err != nil {
				return nil, err
			}
		}
	case "glob":
		if p, ok := input["pattern"].(string); ok {
			if err := add(p); err != nil {
				return nil, err
			}
		}
	case "grep":
		if p, ok := input["path"].(string); ok && p != "" {
			if err := add(p); err != nil {
				return nil, err
			}
		}
	case "file":
		if p, ok := input["path"].(string); ok {
			if err := add(p); err != nil {
				return nil, err
			}
		}
		if tp, ok := input["target_path"].(string); ok && tp != "" {
			if err := add(tp); err != nil {
				return nil, err
			}
		}
	}
	seen := make(map[string]struct{}, len(raw))
	var out []string
	for _, p := range raw {
		c := filepath.Clean(p)
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out, nil
}

type ExternalPathApprovalTool struct {
	inner     types.Tool
	workspace string
	sessionID string
	store     *ApprovalStore
}

func NewExternalPathApprovalTool(inner types.Tool, workspace, sessionID string, store *ApprovalStore) *ExternalPathApprovalTool {
	return &ExternalPathApprovalTool{inner: inner, workspace: workspace, sessionID: sessionID, store: store}
}

func (e *ExternalPathApprovalTool) Name() string {
	return e.inner.Name()
}

func (e *ExternalPathApprovalTool) Description() string {
	return e.inner.Description()
}

func (e *ExternalPathApprovalTool) Schema() map[string]interface{} {
	return e.inner.Schema()
}

func (e *ExternalPathApprovalTool) Metadata() types.ToolMetadata {
	return e.inner.Metadata()
}

func (e *ExternalPathApprovalTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if e.workspace == "*" {
		return e.inner.Execute(ctx, input)
	}
	outside, err := collectOutsideAbsPaths(e.workspace, e.inner.Name(), input)
	if err != nil {
		return nil, err
	}
	if len(outside) == 0 {
		return e.inner.Execute(ctx, input)
	}
	payload := map[string]interface{}{
		"reason":             "external_workspace_paths",
		"absolute_paths":     outside,
		"workspace_root":     e.workspace,
		"original_arguments": input,
	}
	argsJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal approval payload: %w", err)
	}
	approved, err := e.store.RequestApproval(ctx, e.sessionID, e.inner.Name(), string(argsJSON))
	if err != nil {
		return nil, fmt.Errorf("workspace path approval: %w", err)
	}
	if !approved {
		// Unrecoverable user veto — pass through, never recoverable.
		return nil, &ApprovalRejectedError{ToolName: e.inner.Name()}
	}
	ctx2 := ctx
	for _, abs := range outside {
		ctx2 = agentfs.ContextWithApprovedPath(ctx2, abs)
		if strings.ContainsAny(abs, "*?[") {
			ctx2 = agentfs.ContextWithApprovedPrefix(ctx2, filepath.Dir(abs))
			continue
		}
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			ctx2 = agentfs.ContextWithApprovedPrefix(ctx2, abs)
		}
	}
	return e.inner.Execute(ctx2, input)
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

const (
	ErrApprovalTimeout      ApprovalError = "approval timeout"
	ErrApprovalNotAvailable ApprovalError = "approval not available in this context"
)

// ApprovalRejectedError marks a user denial of a tool approval. It is an
// unrecoverable user veto: feeding it back to the model as a recoverable
// {ok:false} result would make the model retry the very tool the user just
// refused, looping until the doom detector fires. Wrappers (nonFatalTool) must
// pass it through as a real error so the engine surfaces the veto instead of
// resuming the loop. Defined in dino/tools so both the approval wrappers here
// and nonFatalTool (in dino/tools) can share it without an import cycle.
type ApprovalRejectedError = dinoTools.ApprovalRejectedError
