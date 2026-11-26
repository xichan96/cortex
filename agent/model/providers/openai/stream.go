package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/model"
)

// ---- Wire stream types ----------------------------------------------------

type wireChunk struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Choices []wireChoice    `json:"choices"`
	Usage   *wireUsage      `json:"usage"`
	Error   json.RawMessage `json:"error"`
}

type wireChoice struct {
	Index        int       `json:"index"`
	Delta        wireDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason"`
}

type wireDelta struct {
	Role             string              `json:"role"`
	Content          *string             `json:"content"`
	ReasoningContent string              `json:"reasoning_content"`
	Reasoning        string              `json:"reasoning"`
	ToolCalls        []wireToolCallDelta `json:"tool_calls"`
}

type wireToolCallDelta struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireUsage struct {
	PromptTokens          int  `json:"prompt_tokens"`
	CompletionTokens      int  `json:"completion_tokens"`
	TotalTokens           int  `json:"total_tokens"`
	PromptCacheHitTokens  *int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens *int `json:"prompt_cache_miss_tokens"`
	PromptTokensDetails   *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type wireErrorPayload struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

// ---- Stream state ---------------------------------------------------------

type toolState struct {
	wireIndex  int
	blockIndex int
	id         string
	name       string
	parser     *model.JSONArgsParser
	closed     bool
}

type stream struct {
	ctx      context.Context
	cancel   context.CancelFunc
	client   *http.Client
	url      string
	body     []byte
	header   map[string]string
	req      *model.Request
	provider string
	retry    model.TransportRetryPolicy

	events chan model.StreamEvent
	done   chan struct{}

	mu      sync.Mutex
	result  *model.AssistantMessage
	resErr  error
	content []model.ContentBlock

	// Open content blocks, or -1 when closed.
	textIndex     int
	thinkingIndex int
	tools         map[int]*toolState
	toolOrder     []int

	// Response metadata and usage, updated while reading SSE.
	responseID     string
	responseModel  string
	promptTokens   int
	completionTok  int
	totalTokens    int
	cachedTokens   int
	reasoningToken int
	stopReason     model.StopReason
	finishError    string

	terminal bool
	sawTool  bool
}

func newStream(ctx context.Context, cancel context.CancelFunc, p *Provider, req *model.Request, body []byte) *stream {
	return &stream{
		ctx:           ctx,
		cancel:        cancel,
		client:        p.client,
		url:           p.endpoint(),
		body:          body,
		header:        p.requestHeaders(req),
		req:           req,
		provider:      p.id,
		retry:         p.retry,
		events:        make(chan model.StreamEvent, 64),
		done:          make(chan struct{}),
		content:       make([]model.ContentBlock, 0, 4),
		textIndex:     -1,
		thinkingIndex: -1,
		tools:         make(map[int]*toolState),
	}
}

// Events implements model.Stream.
func (s *stream) Events() <-chan model.StreamEvent { return s.events }

// Result implements model.Stream. It blocks until the event stream is closed.
// A failed stream still yields an authoritative message whose StopReason is
// error/aborted and whose ErrorMessage explains the failure.
func (s *stream) Result() (*model.AssistantMessage, error) {
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result, s.resErr
}

// Close implements model.Stream. It is idempotent and interrupts an in-flight
// request.
func (s *stream) Close() error {
	s.cancel()
	return nil
}

// emit delivers one event. Sending stops when the stream context is canceled so
// a caller that closed the stream without draining cannot leak the goroutine.
func (s *stream) emit(event model.StreamEvent) bool {
	select {
	case s.events <- event:
		return true
	case <-s.ctx.Done():
		return false
	}
}

func (s *stream) run() {
	defer close(s.done)
	defer close(s.events)

	if !s.emit(model.StreamEvent{Type: model.StreamStart}) {
		s.setResult(s.errorMessage())
		return
	}

	baseReq, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.url, nil)
	if err != nil {
		s.fail(&model.StreamErrorInfo{Code: "request_error", Message: err.Error(), Cause: err})
		return
	}
	for name, value := range s.header {
		baseReq.Header.Set(name, value)
	}

	// Retry only until a response body is available. Once readSSE starts,
	// content may already have reached the caller, so the request is no longer
	// replayable.
	var resp *http.Response
	err = model.TransportRetry(s.ctx, func(ctx context.Context) error {
		req := baseReq.Clone(ctx)
		req.Body = io.NopCloser(bytes.NewReader(s.body))
		req.ContentLength = int64(len(s.body))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(s.body)), nil
		}
		r, err := s.client.Do(req)
		if err != nil {
			return s.transportError(err)
		}
		if r.StatusCode < 200 || r.StatusCode >= 300 {
			info := parseHTTPError(r)
			_ = r.Body.Close()
			return info
		}
		resp = r
		return nil
	}, s.retry)
	if err != nil {
		s.failStreamError(err)
		return
	}
	defer resp.Body.Close()

	s.readSSE(resp.Body)
}

// failStreamError turns an error from the retry loop into a terminal stream
// error. The loop returns the same *StreamErrorInfo values as the old
// single-attempt path, so the event shape is unchanged.
func (s *stream) failStreamError(err error) {
	var info *model.StreamErrorInfo
	if errors.As(err, &info) {
		s.fail(info)
		return
	}
	s.fail(s.transportError(err))
}

// readSSE consumes the chunk stream and emits mapped events. It always leaves
// the stream in a terminal state (done or error).
func (s *stream) readSSE(body io.Reader) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), maxSSELineBytes)

	var data strings.Builder

	dispatch := func() bool {
		if data.Len() == 0 {
			return true
		}
		payload := strings.TrimSpace(data.String())
		data.Reset()
		return s.handlePayload(payload)
	}

	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if !dispatch() {
				return
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment / keep-alive
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			value = strings.TrimPrefix(value, " ")
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
		}
		// event:, id: and retry: fields carry no chat-completions payload and
		// are ignored.
	}

	if s.terminal {
		return
	}
	if !dispatch() {
		return
	}
	if s.terminal {
		return
	}

	if err := scanner.Err(); err != nil {
		if s.ctx.Err() != nil {
			s.fail(s.contextError())
			return
		}
		s.fail(&model.StreamErrorInfo{
			Code:      "stream_read_error",
			Message:   err.Error(),
			Retryable: true,
			Cause:     err,
		})
		return
	}
	if s.ctx.Err() != nil {
		s.fail(s.contextError())
		return
	}

	// Some gateways close the connection instead of sending data: [DONE].
	// Treat an observed finish_reason as a successful completion.
	if s.stopReason != "" && s.stopReason != model.StopReasonPending {
		if s.finishError != "" {
			s.fail(&model.StreamErrorInfo{Code: "finish_reason", Message: s.finishError})
			return
		}
		s.finish()
		return
	}
	s.fail(&model.StreamErrorInfo{
		Code:    "stream_incomplete",
		Message: "OpenAI stream ended before a finish_reason or [DONE]",
	})
}

// handlePayload decodes one SSE data payload and returns false when the stream
// reached a terminal state.
func (s *stream) handlePayload(payload string) bool {
	if payload == "" {
		return true
	}
	if payload == "[DONE]" {
		s.finish()
		return false
	}

	var chunk wireChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		s.fail(&model.StreamErrorInfo{
			Code:    "stream_decode_error",
			Message: fmt.Sprintf("invalid SSE payload: %v", err),
		})
		return false
	}

	if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
		s.fail(errorFromRaw(chunk.Error))
		return false
	}

	if chunk.ID != "" && s.responseID == "" {
		s.responseID = chunk.ID
	}
	if chunk.Model != "" {
		s.responseModel = chunk.Model
	}
	if chunk.Usage != nil {
		s.applyUsage(*chunk.Usage)
	}
	for _, choice := range chunk.Choices {
		s.handleChoice(choice)
	}
	return !s.terminal
}

func (s *stream) handleChoice(choice wireChoice) {
	if choice.FinishReason != nil && *choice.FinishReason != "" {
		reason, message := mapFinishReason(*choice.FinishReason)
		s.stopReason = reason
		if message != "" {
			s.finishError = message
		}
	}

	// reasoning_content is DeepSeek's field; some compatible providers use
	// reasoning or reasoning_text. Process it before text so a chunk carrying
	// both produces thinking followed by text.
	reasoning := choice.Delta.ReasoningContent
	if reasoning == "" {
		reasoning = choice.Delta.Reasoning
	}
	if reasoning != "" {
		s.appendThinking(reasoning)
	}

	if choice.Delta.Content != nil && *choice.Delta.Content != "" {
		s.appendText(*choice.Delta.Content)
	}

	for _, toolCall := range choice.Delta.ToolCalls {
		s.appendToolCall(toolCall)
	}
}

func (s *stream) appendText(delta string) {
	if s.textIndex < 0 {
		s.closeThinking()
		s.textIndex = s.appendContent(model.ContentBlock{Type: model.ContentText})
		if !s.emit(model.StreamEvent{Type: model.StreamTextStart, Index: s.textIndex}) {
			return
		}
	}
	s.content[s.textIndex].Text += delta
	s.emit(model.StreamEvent{
		Type:  model.StreamTextDelta,
		Index: s.textIndex,
		Delta: delta,
	})
}

func (s *stream) appendThinking(delta string) {
	if s.thinkingIndex < 0 {
		s.closeText()
		s.thinkingIndex = s.appendContent(model.ContentBlock{Type: model.ContentThinking})
		if !s.emit(model.StreamEvent{Type: model.StreamThinkingStart, Index: s.thinkingIndex}) {
			return
		}
	}
	s.content[s.thinkingIndex].Thinking += delta
	s.emit(model.StreamEvent{
		Type:  model.StreamThinkingDelta,
		Index: s.thinkingIndex,
		Delta: delta,
	})
}

func (s *stream) appendToolCall(delta wireToolCallDelta) {
	wireIndex := 0
	if delta.Index != nil {
		wireIndex = *delta.Index
	}

	state, ok := s.tools[wireIndex]
	if !ok {
		s.sawTool = true
		s.closeText()
		s.closeThinking()
		state = &toolState{
			wireIndex: wireIndex,
			blockIndex: s.appendContent(model.ContentBlock{
				Type:     model.ContentToolCall,
				ToolCall: &model.ToolCall{Arguments: map[string]any{}},
			}),
			id:     delta.ID,
			name:   delta.Function.Name,
			parser: model.NewJSONArgsParser(),
		}
		s.content[state.blockIndex].ToolCall.ID = state.id
		s.content[state.blockIndex].ToolCall.Name = state.name
		s.tools[wireIndex] = state
		s.toolOrder = append(s.toolOrder, wireIndex)
		s.emit(model.StreamEvent{
			Type:  model.StreamToolStart,
			Index: state.blockIndex,
			ToolCall: &model.ToolCallDelta{
				Index: state.blockIndex,
				ID:    state.id,
				Name:  state.name,
			},
		})
	} else {
		if state.closed {
			return
		}
		// Later chunks may repeat or fill in the id/name.
		if state.id == "" && delta.ID != "" {
			state.id = delta.ID
			s.content[state.blockIndex].ToolCall.ID = delta.ID
		}
		if state.name == "" && delta.Function.Name != "" {
			state.name = delta.Function.Name
			s.content[state.blockIndex].ToolCall.Name = delta.Function.Name
		}
	}

	if delta.Function.Arguments == "" {
		return
	}
	state.parser.Append(delta.Function.Arguments)
	s.emit(model.StreamEvent{
		Type:  model.StreamToolDelta,
		Index: state.blockIndex,
		ToolCall: &model.ToolCallDelta{
			Index:          state.blockIndex,
			ID:             state.id,
			Name:           state.name,
			ArgumentsDelta: delta.Function.Arguments,
		},
	})
}

func (s *stream) closeText() {
	if s.textIndex < 0 {
		return
	}
	index := s.textIndex
	s.textIndex = -1
	s.emit(model.StreamEvent{
		Type:  model.StreamTextEnd,
		Index: index,
		Delta: s.content[index].Text,
	})
}

func (s *stream) closeThinking() {
	if s.thinkingIndex < 0 {
		return
	}
	index := s.thinkingIndex
	s.thinkingIndex = -1
	s.emit(model.StreamEvent{
		Type:  model.StreamThinkingEnd,
		Index: index,
		Delta: s.content[index].Thinking,
	})
}

func (s *stream) closeToolCalls() {
	for _, wireIndex := range s.toolOrder {
		state, ok := s.tools[wireIndex]
		if !ok || state.closed {
			continue
		}
		state.closed = true
		arguments, argErr := state.parser.Finish()
		call := s.content[state.blockIndex].ToolCall
		if call == nil {
			call = &model.ToolCall{}
			s.content[state.blockIndex].ToolCall = call
		}
		call.ID = state.id
		call.Name = state.name
		call.Arguments = arguments
		call.ArgumentsError = argErr
		s.emit(model.StreamEvent{
			Type:  model.StreamToolEnd,
			Index: state.blockIndex,
			ToolCall: &model.ToolCallDelta{
				Index:     state.blockIndex,
				ID:        state.id,
				Name:      state.name,
				Arguments: arguments,
			},
		})
	}
}

// closeOpenBlocks emits the end event for any block still open when the stream
// terminates, keeping the event sequence well-formed.
func (s *stream) closeOpenBlocks() {
	s.closeText()
	s.closeThinking()
	s.closeToolCalls()
}

func (s *stream) appendContent(block model.ContentBlock) int {
	s.content = append(s.content, block)
	return len(s.content) - 1
}

// applyUsage merges one usage snapshot. OpenAI reports prompt_tokens including
// cached tokens; DeepSeek additionally reports prompt_cache_hit_tokens.
func (s *stream) applyUsage(usage wireUsage) {
	s.promptTokens = usage.PromptTokens
	s.completionTok = usage.CompletionTokens
	s.totalTokens = usage.TotalTokens
	if usage.PromptTokensDetails != nil {
		s.cachedTokens = usage.PromptTokensDetails.CachedTokens
	} else if usage.PromptCacheHitTokens != nil {
		s.cachedTokens = *usage.PromptCacheHitTokens
	}
	if usage.CompletionTokensDetails != nil {
		s.reasoningToken = usage.CompletionTokensDetails.ReasoningTokens
	}
}

// finish emits the authoritative done event and records the result.
func (s *stream) finish() {
	if s.terminal {
		return
	}
	if s.finishError != "" {
		s.fail(&model.StreamErrorInfo{Code: "finish_reason", Message: s.finishError})
		return
	}
	s.terminal = true
	s.closeOpenBlocks()

	if s.stopReason == "" || s.stopReason == model.StopReasonPending {
		if s.sawTool {
			s.stopReason = model.StopReasonToolUse
		} else {
			s.stopReason = model.StopReasonStop
		}
	}

	message := s.message()
	s.setResult(message)
	s.emit(model.DoneEvent(message))
}

// fail emits an authoritative error event and records the result.
func (s *stream) fail(info *model.StreamErrorInfo) {
	if s.terminal {
		return
	}
	s.terminal = true
	s.closeOpenBlocks()

	reason := model.StopReasonError
	if info != nil && info.Cause != nil &&
		(errors.Is(info.Cause, context.Canceled) || errors.Is(info.Cause, context.DeadlineExceeded)) {
		reason = model.StopReasonAborted
	}

	event := model.ErrorEvent(reason, info)
	if event.Message != nil {
		event.Message.Provider = s.providerID()
		event.Message.API = model.APIOpenAICompletions
		if s.req.Model != nil {
			event.Message.Model = s.req.Model.ID
		}
		event.Message.ResponseID = s.responseID
		event.Message.ResponseModel = s.responseModel
		event.Message.ThinkingLevel = s.req.Thinking
		event.Message.Usage = s.usage()
		event.Message.Content = append([]model.ContentBlock(nil), s.content...)
	}
	s.setResult(event.Message)
	s.emit(event)
}

func (s *stream) message() *model.AssistantMessage {
	modelID := ""
	if s.req.Model != nil {
		modelID = s.req.Model.ID
	}
	content := make([]model.ContentBlock, len(s.content))
	copy(content, s.content)
	return &model.AssistantMessage{
		Role:          model.RoleAssistant,
		Content:       content,
		API:           model.APIOpenAICompletions,
		Provider:      s.providerID(),
		Model:         modelID,
		ResponseModel: s.responseModel,
		ResponseID:    s.responseID,
		ThinkingLevel: s.req.Thinking,
		Usage:         s.usage(),
		StopReason:    s.stopReason,
		Timestamp:     time.Now().UnixMilli(),
	}
}

// providerID is the provider identity captured when the stream was created.
func (s *stream) providerID() string {
	return s.provider
}

// errorMessage returns a placeholder message for a stream canceled before it
// could even attempt the request.
func (s *stream) errorMessage() *model.AssistantMessage {
	message := &model.AssistantMessage{
		Role:         model.RoleAssistant,
		Content:      []model.ContentBlock{},
		API:          model.APIOpenAICompletions,
		Provider:     s.providerID(),
		StopReason:   model.StopReasonAborted,
		ErrorMessage: "stream closed before start",
		Timestamp:    time.Now().UnixMilli(),
	}
	if s.req != nil && s.req.Model != nil {
		message.Model = s.req.Model.ID
	}
	return message
}

func (s *stream) usage() model.Usage {
	usage := model.Usage{
		Input:       s.promptTokens,
		Output:      s.completionTok,
		CacheRead:   s.cachedTokens,
		Reasoning:   s.reasoningToken,
		TotalTokens: s.totalTokens,
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.Input + usage.Output
	}
	if s.req != nil && s.req.Model != nil {
		usage.Cost = s.req.Model.Cost.EstimateUsage(usage)
	}
	return usage
}

func (s *stream) setResult(message *model.AssistantMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result = message
	s.resErr = nil
}

// transportError converts a client.Do error into a structured stream error.
func (s *stream) transportError(err error) *model.StreamErrorInfo {
	if s.ctx.Err() != nil {
		return s.contextError()
	}
	transport := model.NewTransportError(0, nil, err.Error(), err)
	return &model.StreamErrorInfo{
		Code:      "transport_error",
		Message:   err.Error(),
		Retryable: model.IsRetryableTransportError(transport),
		Cause:     err,
	}
}

func (s *stream) contextError() *model.StreamErrorInfo {
	return &model.StreamErrorInfo{
		Code:    "aborted",
		Message: "stream canceled",
		Cause:   s.ctx.Err(),
	}
}

// parseHTTPError turns a non-2xx response into a structured stream error.
func parseHTTPError(resp *http.Response) *model.StreamErrorInfo {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	message := strings.TrimSpace(string(body))
	code := ""

	var payload struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && len(payload.Error) > 0 {
		info := errorFromRaw(payload.Error)
		if info.Message != "" {
			message = info.Message
		}
		code = info.Code
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	if code == "" {
		code = fmt.Sprintf("http_%d", resp.StatusCode)
	}
	transport := model.NewTransportError(resp.StatusCode, resp.Header, message, nil)
	return &model.StreamErrorInfo{
		Code:       code,
		Message:    message,
		StatusCode: resp.StatusCode,
		Retryable:  model.IsRetryableTransportError(transport),
		Cause:      transport,
	}
}

// errorFromRaw decodes the several error shapes OpenAI-compatible endpoints
// emit: {"message","type","code"} objects, plain strings and bare payloads.
func errorFromRaw(raw json.RawMessage) *model.StreamErrorInfo {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return &model.StreamErrorInfo{Code: "stream_error", Message: "provider returned an error"}
	}

	var text string
	if json.Unmarshal(raw, &text) == nil {
		return &model.StreamErrorInfo{Code: "stream_error", Message: text, Retryable: model.IsRetryableSemanticError(text)}
	}

	var payload wireErrorPayload
	message := trimmed
	code := ""
	retryable := false
	if json.Unmarshal(raw, &payload) == nil && (payload.Message != "" || payload.Type != "") {
		if payload.Message != "" {
			message = payload.Message
		}
		code = payload.CodeString()
		if code == "" {
			code = payload.Type
		}
		retryable = model.IsRetryableSemanticError(message + " " + payload.Type + " " + code)
	}
	if code == "" {
		code = "stream_error"
	}
	return &model.StreamErrorInfo{Code: code, Message: message, Retryable: retryable}
}

// CodeString renders the optional code field, which some endpoints send as a
// number and others as a string.
func (p wireErrorPayload) CodeString() string {
	switch value := p.Code.(type) {
	case nil:
		return ""
	case string:
		return value
	case float64:
		return fmt.Sprintf("%.0f", value)
	case bool:
		return fmt.Sprintf("%t", value)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return strings.Trim(string(encoded), `"`)
	}
}

// mapFinishReason maps OpenAI's finish_reason values onto the provider-neutral
// set. Unknown values degrade to "stop" rather than dropping the terminal
// event.
func mapFinishReason(reason string) (model.StopReason, string) {
	switch reason {
	case "stop", "end":
		return model.StopReasonStop, ""
	case "length":
		return model.StopReasonLength, ""
	case "tool_calls", "function_call":
		return model.StopReasonToolUse, ""
	case "content_filter":
		return model.StopReasonError, "provider finish_reason: content_filter"
	case "network_error":
		return model.StopReasonError, "provider finish_reason: network_error"
	default:
		return model.StopReasonStop, ""
	}
}
