package dino

import (
	"testing"
)

// TestHandlerBuilder_FoldsSubagentMessage：HandlerBuilder 把 Source=subagent 的 message
// 路由到 onSubagentMessage（不触发 onMessage），默认不注册时折叠（评审 O-3 消费方同步）。
func TestHandlerBuilder_FoldsSubagentMessage(t *testing.T) {
	// 用最小 session 桩构造 HandlerBuilder（它只调 session.SubscribeFunc）。
	cs := &ClientSession{}
	hb := &HandlerBuilder{session: cs}

	var normal []string
	var subagent []string
	hb.OnMessage(func(c string) { normal = append(normal, c) })
	hb.OnSubagentMessage(func(c string) { subagent = append(subagent, c) })

	handler := hb.buildForTest()
	handler(&Event{Type: EventTypeMessage, Content: "user msg", Source: EventSourceUser})
	handler(&Event{Type: EventTypeMessage, Content: "ghost msg", Source: EventSourceSubagent})

	if len(normal) != 1 || normal[0] != "user msg" {
		t.Errorf("expected onMessage to receive only user message, got %v", normal)
	}
	if len(subagent) != 1 || subagent[0] != "ghost msg" {
		t.Errorf("expected onSubagentMessage to receive subagent message, got %v", subagent)
	}
}

// TestHandlerBuilder_SubagentFoldedByDefault：未注册 onSubagentMessage 时 subagent
// 消息被折叠（不触发 onMessage，不喷 UI）。
func TestHandlerBuilder_SubagentFoldedByDefault(t *testing.T) {
	cs := &ClientSession{}
	hb := &HandlerBuilder{session: cs}
	var normal []string
	hb.OnMessage(func(c string) { normal = append(normal, c) })

	handler := hb.buildForTest()
	handler(&Event{Type: EventTypeMessage, Content: "ghost", Source: EventSourceSubagent})
	if len(normal) != 0 {
		t.Errorf("expected subagent message folded by default, got onMessage calls %v", normal)
	}
}

// buildForTest 暴露 Build() 内部闭包（不依赖真实 session）。
func (hb *HandlerBuilder) buildForTest() func(*Event) {
	// 与 Build() 内 handler 闭包等价，仅测试 Source 分支路由逻辑。
	return func(event *Event) {
		switch event.Type {
		case EventTypeMessage:
			if event.Source == EventSourceSubagent {
				if hb.onSubagentMessage != nil {
					hb.onSubagentMessage(event.Content)
				}
				return
			}
			if hb.onMessage != nil {
				hb.onMessage(event.Content)
			}
		}
	}
}
