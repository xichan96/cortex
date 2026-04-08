package gateway

import (
	"encoding/json"

	agenttypes "github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino"
	dinopkg "github.com/xichan96/cortex/dino/pkg"
)

const (
	TypeUserMessage = "user_message"
	TypeHeartbeat   = "heartbeat"

	MethodConnect       = "connect"
	MethodSubscribe     = "subscribe"
	MethodSend          = "send"
	MethodToolApproval  = "tool_approval"
	MethodRecreateAgent = "recreate_agent"
	MethodHeartbeat     = TypeHeartbeat

	MsgFromUserMessage = "user.message"

	JSONRPCResType   = "res"
	MsgFromServer    = "server"
	MsgFromWSErr     = "error"
	HeartbeatAckType = "heartbeat_ack"
)

type UserMessageContent struct {
	Content     string                   `json:"content"`
	Text        string                   `json:"text,omitempty"`
	Parts       []agenttypes.MessagePart `json:"parts,omitempty"`
	Attachments []dinopkg.UserAttachment `json:"attachments,omitempty"`
}

type UserMessageBroadcast struct {
	Type      string             `json:"type"`
	Event     string             `json:"event"`
	Role      string             `json:"role"`
	SessionID string             `json:"session_id"`
	Payload   UserMessageContent `json:"payload"`
}

type JSONRPCWSReq struct {
	Type   string          `json:"type"`
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type ConnectParams struct {
	Auth    map[string]string `json:"auth"`
	Client  map[string]any    `json:"client"`
	AgentID string            `json:"agent_id"`
}

type SubscribeParams struct {
	SessionID string `json:"session_id"`
}

type ToolApprovalParams struct {
	RequestID string `json:"request_id"`
	Approved  bool   `json:"approved"`
}

type ClientWSMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type UserMessageInput struct {
	Content string                   `json:"content,omitempty"`
	Text    string                   `json:"text,omitempty"`
	Parts   []agenttypes.MessagePart `json:"parts,omitempty"`
}

type CreateSessionBody struct {
	SessionID string       `json:"session_id"`
	Config    *dino.Config `json:"config"`
}

type ChatBody struct {
	SessionID   string                   `json:"session_id"`
	Content     string                   `json:"content"`
	Text        string                   `json:"text"`
	Parts       []agenttypes.MessagePart `json:"parts,omitempty"`
	Attachments []dinopkg.UserAttachment `json:"attachments,omitempty"`
}

type JSONRPCWSResBody struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Ok    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type HeartbeatAckBody struct {
	Type string `json:"type"`
}

func MustJSONRPCWSResMsg(id string, ok bool, err string) string {
	b, e := json.Marshal(JSONRPCWSResBody{Type: JSONRPCResType, ID: id, Ok: ok, Error: err})
	if e != nil {
		return ""
	}
	return string(b)
}

func MustHeartbeatAckMsg() string {
	b, e := json.Marshal(HeartbeatAckBody{Type: HeartbeatAckType})
	if e != nil {
		return ""
	}
	return string(b)
}
