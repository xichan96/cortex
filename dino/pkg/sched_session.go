package pkg

import (
	"context"
	"strings"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/scheduler"
)

func cloneInputMap(in map[string]interface{}) map[string]interface{} {
	if in == nil {
		return map[string]interface{}{}
	}
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func SchedulerToolsForSession(svc *scheduler.Service, chatSessionID string) []types.Tool {
	if svc == nil {
		return nil
	}
	bind := strings.TrimSpace(chatSessionID)
	raw := scheduler.NewTools(svc)
	out := make([]types.Tool, 0, len(raw))
	for _, t := range raw {
		switch t.Name() {
		case "schedule_job":
			out = append(out, &scheduleJobSessionTool{inner: t, bind: bind})
		case "list_jobs":
			out = append(out, &listJobsSessionTool{inner: t, bind: bind})
		default:
			out = append(out, t)
		}
	}
	return out
}

type scheduleJobSessionTool struct {
	inner types.Tool
	bind  string
}

func (w *scheduleJobSessionTool) Name() string                   { return w.inner.Name() }
func (w *scheduleJobSessionTool) Description() string            { return w.inner.Description() }
func (w *scheduleJobSessionTool) Schema() map[string]interface{} { return w.inner.Schema() }
func (w *scheduleJobSessionTool) Metadata() types.ToolMetadata   { return w.inner.Metadata() }
func (w *scheduleJobSessionTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	in := input
	if w.bind != "" {
		in = cloneInputMap(input)
		in["session_id"] = w.bind
	}
	return w.inner.Execute(ctx, in)
}

type listJobsSessionTool struct {
	inner types.Tool
	bind  string
}

func (w *listJobsSessionTool) Name() string                   { return w.inner.Name() }
func (w *listJobsSessionTool) Description() string            { return w.inner.Description() }
func (w *listJobsSessionTool) Schema() map[string]interface{} { return w.inner.Schema() }
func (w *listJobsSessionTool) Metadata() types.ToolMetadata   { return w.inner.Metadata() }
func (w *listJobsSessionTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	in := input
	if w.bind != "" {
		in = cloneInputMap(input)
		in["session_id"] = w.bind
	}
	return w.inner.Execute(ctx, in)
}
