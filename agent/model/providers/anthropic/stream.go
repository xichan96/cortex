package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/model"
)

// ---- Wire stream types ----------------------------------------------------

type wireUsage struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheCreation            *struct {
		Ephemeral1hInputTokens *int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	OutputTokensDetails *struct {
		ThinkingTokens *int `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

type wireMessageStart struct {
	Type    string `json:"type"`
	Message struct {
		ID    string    `json:"id"`
		Model string    `json:"model"`
		Usage wireUsage `json:"usage"`
	} `json:"message"`
}

type wireContentBlockStart struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		Thinking  string          `json:"thinking"`
		Signature string          `json:"signature"`
		Data      string          `json:"data"`
	} `json:"content_block"`
}

type wireContentBlockDelta struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		Signature   string `json:"signature"`
	} `json:"delta"`
}

type wireContentBlockStop struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

type wireMessageDelta struct {
	Type  string `json:"type"`
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage wireUsage `json:"usage"`
}

type wireStreamError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// sseEnvelope is used to peek at the event type before decoding the payload.
type sseEnvelope struct {
	Type string `json:"type"`
}

// ---- Stream state ---------------------------------------------------------

type blockKind int

const (
	blockText blockKind = iota
	blockThinking
	blockTool
)

type blockState struct {
	kind         blockKind
	contentIndex int
	parser       *model.JSONArgsParser
	initialInput json.RawMessage
	toolID       string
	toolName     string
}

type stream struct {
	ctx    context.Context
	cancel context.CancelFunc
	client *http.Client
	url    string
	body   []byte
	header map[string]string
	req    *model.Request

	events chan model.StreamEvent
	done   chan struct{}

	mu      sync.Mutex
	result  *model.AssistantMessage
	resErr  error
	content []model.ContentBlock
	blocks  map[int]*blockState

	// Response metadata and usage, updated while reading SSE.
	responseID    string
	responseModel string
	inputTokens   int
	outputTokens  int
	cacheRead     int
	cacheWrite    int
	cacheWrite1h  int
	reasoning     int
	stopReason    model.StopReason

	terminal bool
	sawTool  bool
}

func newStream(ctx context.Context, cancel context.CancelFunc, p *Provider, req *model.Request, body []byte) *stream {
	return &stream{
		ctx:     ctx,
		cancel:  cancel,
		client:  p.client,
		url:     p.endpoint(),
		body:    body,
		header:  p.requestHeaders(req),
		req:     req,
		events:  make(chan model.StreamEvent, 64),
		done:    make(chan struct{}),
		content: make([]model.ContentBlock, 0, 4),
		blocks:  make(map[int]*blockState),
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

	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.url, bytes.NewReader(s.body))
	if err != nil {
		s.fail(&model.StreamErrorInfo{Code: "request_error", Message: err.Error(), Cause: err})
		return
	}
	for name, value := range s.header {
		req.Header.Set(name, value)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.fail(s.transportError(err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.fail(parseHTTPError(resp))
		return
	}

	s.readSSE(resp.Body)
}

// readSSE consumes the event stream and emits mapped events. It always leaves
// the stream in a terminal state (done or error).
func (s *stream) readSSE(body io.Reader) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), maxSSELineBytes)

	var eventName string
	var data strings.Builder

	dispatch := func() bool {
		if data.Len() == 0 {
			eventName = ""
			return true
		}
		payload := data.String()
		data.Reset()
		name := eventName
		eventName = ""
		return s.handleEvent(name, payload)
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
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			value = strings.TrimPrefix(value, " ")
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
		}
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

	// Some gateways close the stream after message_delta without sending
	// message_stop. Treat a known stop reason as a successful completion.
	if s.stopReason != "" && s.stopReason != model.StopReasonPending {
		if s.stopReason.IsError() {
			s.fail(&model.StreamErrorInfo{Code: "stream_error", Message: "model stopped with an error"})
			return
		}
		s.finish()
		return
	}
	s.fail(&model.StreamErrorInfo{
		Code:    "stream_incomplete",
		Message: "Anthropic stream ended before message_stop",
	})
}

// handleEvent decodes one SSE payload and returns false when the stream reached
// a terminal state.
func (s *stream) handleEvent(eventName, payload string) bool {
	var envelope sseEnvelope
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		s.fail(&model.StreamErrorInfo{
			Code:    "stream_decode_error",
			Message: fmt.Sprintf("invalid SSE payload: %v", err),
		})
		return false
	}
	kind := envelope.Type
	if kind == "" {
		kind = eventName
	}

	switch kind {
	case "message_start":
		var event wireMessageStart
		if json.Unmarshal([]byte(payload), &event) == nil {
			s.responseID = event.Message.ID
			s.responseModel = event.Message.Model
			s.applyUsage(event.Message.Usage)
		}
	case "content_block_start":
		var event wireContentBlockStart
		if json.Unmarshal([]byte(payload), &event) != nil {
			return true
		}
		s.startBlock(event)
	case "content_block_delta":
		var event wireContentBlockDelta
		if json.Unmarshal([]byte(payload), &event) != nil {
			return true
		}
		s.deltaBlock(event)
	case "content_block_stop":
		var event wireContentBlockStop
		if json.Unmarshal([]byte(payload), &event) != nil {
			return true
		}
		s.stopBlock(event.Index)
	case "message_delta":
		var event wireMessageDelta
		if json.Unmarshal([]byte(payload), &event) == nil {
			if event.Delta.StopReason != "" {
				s.stopReason = mapStopReason(event.Delta.StopReason)
			}
			s.applyUsage(event.Usage)
		}
	case "message_stop":
		if s.stopReason == model.StopReasonError {
			s.fail(&model.StreamErrorInfo{Code: "stop_reason", Message: "model stopped with a refusal"})
			return false
		}
		s.finish()
		return false
	case "error":
		var event wireStreamError
		_ = json.Unmarshal([]byte(payload), &event)
		message := event.Error.Message
		if message == "" {
			message = payload
		}
		code := event.Error.Type
		if code == "" {
			code = "stream_error"
		}
		s.fail(&model.StreamErrorInfo{
			Code:      code,
			Message:   message,
			Retryable: code == "overloaded_error" || code == "rate_limit_error" || code == "api_error",
		})
		return false
	// ping and any future event types are ignored.
	default:
	}
	return true
}

func (s *stream) startBlock(event wireContentBlockStart) {
	block := event.ContentBlock
	index := event.Index

	switch block.Type {
	case "text":
		contentIndex := s.appendContent(model.ContentBlock{Type: model.ContentText, Text: block.Text})
		s.blocks[index] = &blockState{kind: blockText, contentIndex: contentIndex}
		s.emit(model.StreamEvent{Type: model.StreamTextStart, Index: index})

	case "thinking":
		contentIndex := s.appendContent(model.ContentBlock{
			Type:              model.ContentThinking,
			Thinking:          block.Thinking,
			ThinkingSignature: block.Signature,
		})
		s.blocks[index] = &blockState{kind: blockThinking, contentIndex: contentIndex}
		s.emit(model.StreamEvent{Type: model.StreamThinkingStart, Index: index})

	case "redacted_thinking":
		contentIndex := s.appendContent(model.ContentBlock{
			Type:              model.ContentThinking,
			Thinking:          "[Reasoning redacted]",
			ThinkingSignature: block.Data,
			Redacted:          true,
		})
		s.blocks[index] = &blockState{kind: blockThinking, contentIndex: contentIndex}
		s.emit(model.StreamEvent{Type: model.StreamThinkingStart, Index: index})

	case "tool_use":
		s.sawTool = true
		call := &model.ToolCall{
			ID:        block.ID,
			Name:      block.Name,
			Arguments: map[string]any{},
		}
		contentIndex := s.appendContent(model.ContentBlock{Type: model.ContentToolCall, ToolCall: call})
		s.blocks[index] = &blockState{
			kind:         blockTool,
			contentIndex: contentIndex,
			parser:       model.NewJSONArgsParser(),
			initialInput: block.Input,
			toolID:       block.ID,
			toolName:     block.Name,
		}
		s.emit(model.StreamEvent{
			Type:  model.StreamToolStart,
			Index: index,
			ToolCall: &model.ToolCallDelta{
				Index: index,
				ID:    block.ID,
				Name:  block.Name,
			},
		})
	}
}

func (s *stream) deltaBlock(event wireContentBlockDelta) {
	state, ok := s.blocks[event.Index]
	if !ok {
		return
	}
	content := &s.content[state.contentIndex]

	switch event.Delta.Type {
	case "text_delta":
		if state.kind != blockText || event.Delta.Text == "" {
			return
		}
		content.Text += event.Delta.Text
		s.emit(model.StreamEvent{
			Type:  model.StreamTextDelta,
			Index: event.Index,
			Delta: event.Delta.Text,
		})

	case "thinking_delta":
		if state.kind != blockThinking || event.Delta.Thinking == "" {
			return
		}
		content.Thinking += event.Delta.Thinking
		s.emit(model.StreamEvent{
			Type:  model.StreamThinkingDelta,
			Index: event.Index,
			Delta: event.Delta.Thinking,
		})

	case "signature_delta":
		if state.kind == blockThinking {
			content.ThinkingSignature += event.Delta.Signature
		}

	case "input_json_delta":
		if state.kind != blockTool || state.parser == nil || event.Delta.PartialJSON == "" {
			return
		}
		state.parser.Append(event.Delta.PartialJSON)
		s.emit(model.StreamEvent{
			Type:  model.StreamToolDelta,
			Index: event.Index,
			ToolCall: &model.ToolCallDelta{
				Index:          event.Index,
				ID:             state.toolID,
				Name:           state.toolName,
				ArgumentsDelta: event.Delta.PartialJSON,
			},
		})
	}
}

func (s *stream) stopBlock(index int) {
	state, ok := s.blocks[index]
	if !ok {
		return
	}
	delete(s.blocks, index)
	content := &s.content[state.contentIndex]

	switch state.kind {
	case blockText:
		s.emit(model.StreamEvent{
			Type:  model.StreamTextEnd,
			Index: index,
			Delta: content.Text,
		})
	case blockThinking:
		s.emit(model.StreamEvent{
			Type:  model.StreamThinkingEnd,
			Index: index,
			Delta: content.Thinking,
		})
	case blockTool:
		arguments, argErr := s.finishToolArguments(state)
		if content.ToolCall == nil {
			content.ToolCall = &model.ToolCall{ID: state.toolID, Name: state.toolName}
		}
		content.ToolCall.Arguments = arguments
		content.ToolCall.ArgumentsError = argErr
		s.emit(model.StreamEvent{
			Type:  model.StreamToolEnd,
			Index: index,
			ToolCall: &model.ToolCallDelta{
				Index:     index,
				ID:        state.toolID,
				Name:      state.toolName,
				Arguments: arguments,
			},
		})
	}
}

func (s *stream) finishToolArguments(state *blockState) (map[string]any, *model.ToolArgumentError) {
	if state.parser != nil && state.parser.Len() > 0 {
		return state.parser.Finish()
	}
	if len(state.initialInput) > 0 {
		return model.ParseToolArguments(string(state.initialInput))
	}
	return map[string]any{}, nil
}

func (s *stream) appendContent(block model.ContentBlock) int {
	s.content = append(s.content, block)
	return len(s.content) - 1
}

// closeOpenBlocks emits the end event for any block still open when the stream
// terminates, keeping the event sequence well-formed even if the provider
// omitted a content_block_stop.
func (s *stream) closeOpenBlocks() {
	if len(s.blocks) == 0 {
		return
	}
	indices := make([]int, 0, len(s.blocks))
	for index := range s.blocks {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		s.stopBlock(index)
	}
}

// applyUsage merges a usage snapshot. Anthropic's message_start carries the
// input/cache counts and message_delta carries the cumulative output count;
// pointer fields distinguish "absent" from a real zero.
func (s *stream) applyUsage(usage wireUsage) {
	if usage.InputTokens != nil {
		s.inputTokens = *usage.InputTokens
	}
	if usage.OutputTokens != nil {
		s.outputTokens = *usage.OutputTokens
	}
	if usage.CacheReadInputTokens != nil {
		s.cacheRead = *usage.CacheReadInputTokens
	}
	if usage.CacheCreationInputTokens != nil {
		s.cacheWrite = *usage.CacheCreationInputTokens
	}
	if usage.CacheCreation != nil && usage.CacheCreation.Ephemeral1hInputTokens != nil {
		s.cacheWrite1h = *usage.CacheCreation.Ephemeral1hInputTokens
	}
	if usage.OutputTokensDetails != nil && usage.OutputTokensDetails.ThinkingTokens != nil {
		s.reasoning = *usage.OutputTokensDetails.ThinkingTokens
	}
}

// finish emits the authoritative done event and records the result.
func (s *stream) finish() {
	if s.terminal {
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
		event.Message.Provider = "anthropic"
		event.Message.API = model.APIAnthropicMessages
		event.Message.Model = s.req.Model.ID
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
	provider := "anthropic"
	api := model.APIAnthropicMessages
	modelID := ""
	thinking := model.ThinkingLevel("")
	if s.req != nil {
		thinking = s.req.Thinking
		if s.req.Model != nil {
			modelID = s.req.Model.ID
		}
	}
	content := make([]model.ContentBlock, len(s.content))
	copy(content, s.content)
	return &model.AssistantMessage{
		Role:          model.RoleAssistant,
		Content:       content,
		API:           api,
		Provider:      provider,
		Model:         modelID,
		ResponseModel: s.responseModel,
		ResponseID:    s.responseID,
		ThinkingLevel: thinking,
		Usage:         s.usage(),
		StopReason:    s.stopReason,
		Timestamp:     time.Now().UnixMilli(),
	}
}

// errorMessage returns a placeholder message for a stream canceled before it
// could even attempt the request.
func (s *stream) errorMessage() *model.AssistantMessage {
	message := &model.AssistantMessage{
		Role:         model.RoleAssistant,
		Content:      []model.ContentBlock{},
		API:          model.APIAnthropicMessages,
		Provider:     "anthropic",
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
		Input:        s.inputTokens + s.cacheRead + s.cacheWrite,
		Output:       s.outputTokens,
		CacheRead:    s.cacheRead,
		CacheWrite:   s.cacheWrite,
		CacheWrite1h: s.cacheWrite1h,
		Reasoning:    s.reasoning,
	}
	usage.TotalTokens = usage.Input + usage.Output
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
	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	message := strings.TrimSpace(string(body))
	code := ""
	if json.Unmarshal(body, &payload) == nil && payload.Error.Message != "" {
		message = payload.Error.Message
		code = payload.Error.Type
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
	}
}

// mapStopReason maps Anthropic's stop_reason values onto the provider-neutral
// set. Unknown values degrade to "stop" rather than dropping the terminal event.
func mapStopReason(reason string) model.StopReason {
	switch reason {
	case "end_turn", "stop_sequence", "pause_turn":
		return model.StopReasonStop
	case "max_tokens":
		return model.StopReasonLength
	case "tool_use":
		return model.StopReasonToolUse
	case "refusal", "sensitive":
		return model.StopReasonError
	default:
		return model.StopReasonStop
	}
}
