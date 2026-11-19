package host

import (
	"encoding/json"
	"os"
	"testing"
)

// TestHelperPlugin is not a real test: when CORTEX_PLUGIN_HELPER=1 the test
// binary acts as a fake plugin speaking the stdio frame protocol. Host tests
// re-exec os.Executable with -test.run=TestHelperPlugin.
func TestHelperPlugin(t *testing.T) {
	if os.Getenv("CORTEX_PLUGIN_HELPER") != "1" {
		return
	}
	runHelperPlugin()
	os.Exit(0)
}

func runHelperPlugin() {
	mode := os.Getenv("CORTEX_PLUGIN_MODE")
	if mode == "exit-immediately" {
		os.Exit(4)
	}
	for {
		frame, err := ReadFrame(os.Stdin)
		if err != nil {
			return
		}
		if frame.Type != FrameRequest {
			continue
		}
		var req requestMessage
		if err := json.Unmarshal(frame.JSON, &req); err != nil {
			helperError(frame.ID, CodeInvalidParams, err.Error())
			continue
		}
		switch req.Method {
		case MethodInitialize:
			version := ProtocolVersion
			if mode == "bad-version" {
				version = 99
			}
			helperResult(frame.ID, PluginInfo{
				ProtocolVersion: version,
				Name:            "fake-plugin",
				Version:         "0.1.0",
				Capabilities:    Capabilities{Tools: true, Hooks: true, Resources: true},
			})
		case MethodToolsList:
			helperResult(frame.ID, toolsListResult{Tools: []ToolSpec{
				{
					Name:        "echo",
					Description: "echo the arguments",
					InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}}}`),
				},
				{Name: "boom", Description: "a second tool"},
			}})
		case MethodToolsCall:
			switch mode {
			case "hang":
				continue
			case "crash-always":
				os.Exit(3)
			case "crash-once":
				marker := os.Getenv("CORTEX_PLUGIN_MARKER")
				if _, err := os.Stat(marker); err != nil {
					_ = os.WriteFile(marker, []byte("crashed"), 0o600)
					os.Exit(3)
				}
			}
			var params callToolParams
			if len(req.Params) > 0 {
				_ = json.Unmarshal(req.Params, &params)
			}
			if params.Stream {
				helperEvent(frame.ID, "tools/update", map[string]any{"step": 1})
				helperEvent(frame.ID, "tools/update", map[string]any{"step": 2})
			}
			helperResult(frame.ID, map[string]any{
				"tool":      params.Name,
				"arguments": params.Arguments,
			})
		case MethodHooksEmit:
			var params emitParams
			if len(req.Params) > 0 {
				_ = json.Unmarshal(req.Params, &params)
			}
			var payload struct {
				Block bool `json:"block"`
			}
			if len(params.Payload) > 0 {
				_ = json.Unmarshal(params.Payload, &payload)
			}
			if payload.Block {
				helperResult(frame.ID, EmitResult{Decision: DecisionBlock, Reason: "blocked by helper"})
			} else {
				helperResult(frame.ID, EmitResult{Decision: DecisionNone})
			}
		case MethodResourcesList:
			helperResult(frame.ID, resourcesListResult{Resources: []ResourceSpec{
				{Name: "greeting", Kind: "prompt", Location: "file:///prompts/greeting.md"},
			}})
		case MethodShutdown:
			helperResult(frame.ID, struct{}{})
			return
		default:
			helperError(frame.ID, CodeMethodNotFound, "unknown method "+req.Method)
		}
	}
}

func helperResult(id uint32, result any) {
	body, err := json.Marshal(map[string]any{"result": result})
	if err != nil {
		return
	}
	_ = WriteFrame(os.Stdout, Frame{Type: FrameResponse, ID: id, JSON: body})
}

func helperError(id uint32, code int, message string) {
	body, err := json.Marshal(RPCError{Code: code, Message: message})
	if err != nil {
		return
	}
	_ = WriteFrame(os.Stdout, Frame{Type: FrameError, ID: id, JSON: body})
}

func helperEvent(id uint32, name string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	body, err := json.Marshal(eventMessage{Event: name, ID: id, Data: raw})
	if err != nil {
		return
	}
	_ = WriteFrame(os.Stdout, Frame{Type: FrameEvent, ID: id, JSON: body})
}
