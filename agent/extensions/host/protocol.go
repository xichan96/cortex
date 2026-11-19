// Package host implements the out-of-process plugin host of the general agent
// framework (design §7.5): a child process speaking length-prefixed JSON
// frames over stdin/stdout.
//
// The package has three layers:
//
//   - protocol.go: the frame codec and the wire message types.
//   - host.go: process lifecycle (spawn, handshake, timeout, cancel, death
//     detection, bounded restart) and the method calls.
//   - extension.go: adapters that expose a plugin's tools as an in-process
//     extensions.Extension (design §7.2/§7.4).
//
// Package discovery (cortex-plugin.json) lives in discover.go. Sandboxing,
// network transports, and package installation are out of scope for this
// layer.
package host

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ProtocolVersion is the wire protocol revision this host speaks. It is
// exchanged during the initialize handshake; a mismatch disables the plugin.
const ProtocolVersion = 1

// MaxFrameSize bounds the payload (type + id + JSON) of a single frame. It
// protects the host from a misbehaving plugin claiming a huge length.
const MaxFrameSize = 16 << 20

// frameHeaderSize is the number of bytes following the length prefix: one byte
// of type plus a u32 id. The JSON body is the remainder.
const frameHeaderSize = 5

// FrameType identifies a wire frame. The numeric values are part of the
// protocol.
type FrameType uint8

const (
	// FrameRequest asks the peer to execute a method.
	FrameRequest FrameType = 1
	// FrameResponse carries a successful method result.
	FrameResponse FrameType = 2
	// FrameError carries a failed method result (RPCError).
	FrameError FrameType = 3
	// FrameEvent is a notification correlated with a pending request id. Plugin
	// tool calls use it to stream progress updates.
	FrameEvent FrameType = 4
	// FrameCancel asks the peer to abandon the request with the matching id.
	FrameCancel FrameType = 5
	// FramePing is a liveness probe. Peers ignore it when idle.
	FramePing FrameType = 6
)

// String returns the frame type's protocol name.
func (t FrameType) String() string {
	switch t {
	case FrameRequest:
		return "request"
	case FrameResponse:
		return "response"
	case FrameError:
		return "error"
	case FrameEvent:
		return "event"
	case FrameCancel:
		return "cancel"
	case FramePing:
		return "ping"
	default:
		return fmt.Sprintf("frame(%d)", byte(t))
	}
}

// Frame is one wire message:
//
//	u32 length | u8 type | u32 id | json
//
// length counts the bytes after the prefix itself (5 + len(JSON)).
type Frame struct {
	Type FrameType
	ID   uint32
	JSON []byte
}

// Protocol errors.
var (
	ErrMalformedFrame      = errors.New("pluginhost: malformed frame")
	ErrFrameTooLarge       = errors.New("pluginhost: frame exceeds limit")
	ErrUnsupportedProtocol = errors.New("pluginhost: unsupported protocol version")
)

// WriteFrame encodes f and writes it to w in a single logical frame.
func WriteFrame(w io.Writer, f Frame) error {
	if len(f.JSON) > MaxFrameSize-frameHeaderSize {
		return fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, len(f.JSON))
	}
	buf := make([]byte, 4+frameHeaderSize+len(f.JSON))
	binary.BigEndian.PutUint32(buf[0:4], uint32(frameHeaderSize+len(f.JSON)))
	buf[4] = byte(f.Type)
	binary.BigEndian.PutUint32(buf[5:9], f.ID)
	copy(buf[9:], f.JSON)
	for len(buf) > 0 {
		n, err := w.Write(buf)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		buf = buf[n:]
	}
	return nil
}

// ReadFrame reads one frame using the default size limit.
func ReadFrame(r io.Reader) (Frame, error) { return ReadFrameLimit(r, MaxFrameSize) }

// ReadFrameLimit reads one frame, rejecting payloads larger than limit. A
// limit <= 0 means MaxFrameSize.
func ReadFrameLimit(r io.Reader, limit int) (Frame, error) {
	if limit <= 0 {
		limit = MaxFrameSize
	}
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Frame{}, err
	}
	length := int(binary.BigEndian.Uint32(header[:]))
	if length < frameHeaderSize {
		return Frame{}, fmt.Errorf("%w: length %d", ErrMalformedFrame, length)
	}
	if length > limit {
		return Frame{}, fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, length, limit)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return Frame{}, err
	}
	return Frame{
		Type: FrameType(body[0]),
		ID:   binary.BigEndian.Uint32(body[1:5]),
		JSON: body[5:],
	}, nil
}

// Method names exposed by the plugin protocol (design §7.5).
const (
	MethodInitialize    = "initialize"
	MethodToolsList     = "tools/list"
	MethodToolsCall     = "tools/call"
	MethodHooksEmit     = "hooks/emit"
	MethodResourcesList = "resources/list"
	MethodShutdown      = "shutdown"
)

// JSON-RPC style error codes used on the wire. They are advisory; plugins may
// return any code.
const (
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
)

// RPCError is the body of a FrameError.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error implements error.
func (e *RPCError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Code == 0 {
		return "pluginhost: " + e.Message
	}
	return fmt.Sprintf("pluginhost: rpc error %d: %s", e.Code, e.Message)
}

// requestMessage is the JSON body of a FrameRequest.
type requestMessage struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// responseMessage is the JSON body of a FrameResponse.
type responseMessage struct {
	Result json.RawMessage `json:"result,omitempty"`
}

// eventMessage is the JSON body of a FrameEvent. ID correlates the event with
// the pending request that produced it; Event names the update kind.
type eventMessage struct {
	Event string          `json:"event"`
	ID    uint32          `json:"id"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Capabilities declares which method groups a peer supports. The bool fields
// keep the wire form explicit and forward compatible: absent fields are false.
type Capabilities struct {
	Tools     bool `json:"tools,omitempty"`
	Hooks     bool `json:"hooks,omitempty"`
	Resources bool `json:"resources,omitempty"`
	UI        bool `json:"ui,omitempty"`
}

// HostCapabilities is the capability set the host advertises to plugins.
func HostCapabilities() Capabilities {
	return Capabilities{Tools: true, Hooks: true, Resources: true}
}

// InitializeParams is the params object of the initialize handshake.
type InitializeParams struct {
	ProtocolVersion int             `json:"protocol_version"`
	Capabilities    Capabilities    `json:"capabilities"`
	Config          json.RawMessage `json:"config,omitempty"`
}

// PluginInfo is the result of a successful initialize handshake.
type PluginInfo struct {
	ProtocolVersion int          `json:"protocol_version"`
	Name            string       `json:"name"`
	Version         string       `json:"version"`
	Capabilities    Capabilities `json:"capabilities"`
}

// ToolSpec is a tool declaration returned by tools/list.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// Schema decodes the declared input schema. It falls back to a permissive
// object schema when the plugin supplied none or supplied invalid JSON.
func (t ToolSpec) Schema() map[string]any {
	return schemaOf(t.InputSchema)
}

func schemaOf(raw json.RawMessage) map[string]any {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{"type": "object"}
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil || schema == nil {
		return map[string]any{"type": "object"}
	}
	return schema
}

// toolsListResult is the tools/list result body.
type toolsListResult struct {
	Tools []ToolSpec `json:"tools"`
}

// ResourceSpec is a resource declaration returned by resources/list.
type ResourceSpec struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Location string `json:"location,omitempty"`
}

// resourcesListResult is the resources/list result body.
type resourcesListResult struct {
	Resources []ResourceSpec `json:"resources"`
}

// callToolParams is the tools/call params body.
type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// Stream requests per-call FrameEvent updates. A plugin may ignore it.
	Stream bool `json:"stream,omitempty"`
}

// ToolUpdate is one streamed progress event correlated with a tools/call.
type ToolUpdate struct {
	// Event is the plugin-defined update kind (for example "tools/update").
	Event string
	Data  json.RawMessage
}

// EmitKind selects hook semantics for hooks/emit. A gate may veto the action;
// none is a pure notification.
type EmitKind string

const (
	// EmitNone is an observational notification; any decision is advisory.
	EmitNone EmitKind = "none"
	// EmitGate is a veto point; DecisionBlock stops the action.
	EmitGate EmitKind = "gate"
)

// Decision is a hook verdict.
type Decision string

const (
	DecisionNone     Decision = ""
	DecisionContinue Decision = "continue"
	DecisionBlock    Decision = "block"
	DecisionEnd      Decision = "end"
)

// emitParams is the hooks/emit params body.
type emitParams struct {
	Event   string          `json:"event"`
	Kind    EmitKind        `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// EmitResult is the merged hook result.
type EmitResult struct {
	Decision Decision        `json:"decision,omitempty"`
	Reason   string          `json:"reason,omitempty"`
	Value    json.RawMessage `json:"value,omitempty"`
}

// Blocked reports whether the hook vetoed the action.
func (r EmitResult) Blocked() bool { return r.Decision == DecisionBlock }
