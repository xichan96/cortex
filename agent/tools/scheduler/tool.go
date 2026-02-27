package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
	"github.com/xichan96/cortex/pkg/xcron"
)

// Constant for the default task type used by the agent
const AgentTaskType = "agent_task"

// NewSchedulerTools creates a set of tools for interacting with the xcron scheduler
func NewSchedulerTools(s *xcron.Scheduler) []types.Tool {
	return []types.Tool{
		&ScheduleJobTool{scheduler: s},
		&ListJobsTool{scheduler: s},
		&DeleteJobTool{scheduler: s},
	}
}

// RegisterAgentTaskHandler registers a handler that executes agent tasks
// agentExecutor is a function that takes the payload (instruction) and executes it using the agent
func RegisterAgentTaskHandler(s *xcron.Scheduler, agentExecutor func(input string) error) {
	s.RegisterHandler(AgentTaskType, func(ctx context.Context, payload string) error {
		// xcron stores payload as JSON. If the payload was a simple string, it is stored as "string".
		// We try to unmarshal it back to a raw string to be friendly to the Agent.
		var decoded string
		if err := json.Unmarshal([]byte(payload), &decoded); err == nil {
			return agentExecutor(decoded)
		}
		// If it's not a simple JSON string (e.g. object or raw text), pass it as is.
		return agentExecutor(payload)
	})
}

// =================================================================================
// ScheduleJobTool
// =================================================================================

type ScheduleJobTool struct {
	scheduler *xcron.Scheduler
}

func (t *ScheduleJobTool) Name() string {
	return "schedule_job"
}

func (t *ScheduleJobTool) Description() string {
	return "Schedule a new job. Supports 'oneshot' (run once after delay), 'periodic' (run every interval), and 'cron' (run at specific times)."
}

func (t *ScheduleJobTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Unique name for the job",
			},
			"type": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"oneshot", "periodic", "cron"},
				"description": "Type of the job",
			},
			"schedule": map[string]interface{}{
				"type":        "string",
				"description": "Schedule expression. For oneshot/periodic: duration string (e.g. '10s', '1h'). For cron: cron expression (e.g. '* * * * * *').",
			},
			"payload": map[string]interface{}{
				"type":        "string",
				"description": "The instruction or payload to execute. If task_type is 'agent_task', this is the prompt for the agent.",
			},
			"task_type": map[string]interface{}{
				"type":        "string",
				"description": "The type of task handler to use. Defaults to 'agent_task' if not specified.",
			},
		},
		"required": []string{"name", "type", "schedule", "payload"},
	}
}

func (t *ScheduleJobTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	name, ok := input["name"].(string)
	if !ok || name == "" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("name is required"))
	}

	jobTypeStr, ok := input["type"].(string)
	if !ok || jobTypeStr == "" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("type is required"))
	}

	schedule, ok := input["schedule"].(string)
	if !ok || schedule == "" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("schedule is required"))
	}

	payload, ok := input["payload"].(string)
	if !ok || payload == "" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("payload is required"))
	}

	taskType, _ := input["task_type"].(string)

	if taskType == "" {
		taskType = AgentTaskType
	}

	var jobType xcron.JobType
	switch jobTypeStr {
	case "oneshot":
		jobType = xcron.JobTypeOneShot
	case "periodic":
		jobType = xcron.JobTypePeriodic
	case "cron":
		jobType = xcron.JobTypeCron
	default:
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid job type: %s", jobTypeStr))
	}

	// Use the provided context, but ensure we have a timeout for the scheduling operation itself
	// The job execution will happen asynchronously, so this timeout is just for adding the job to the scheduler
	scheduleCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	id, err := t.scheduler.AddJob(scheduleCtx, name, jobType, schedule, xcron.TaskType(taskType), payload, 0)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status": "success",
		"job_id": id,
		"msg":    fmt.Sprintf("Job '%s' scheduled successfully", name),
	}, nil
}

func (t *ScheduleJobTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "schedule_job",
		ToolType:       "extension",
	}
}

// =================================================================================
// ListJobsTool
// =================================================================================

type ListJobsTool struct {
	scheduler *xcron.Scheduler
}

func (t *ListJobsTool) Name() string {
	return "list_jobs"
}

func (t *ListJobsTool) Description() string {
	return "List all currently scheduled jobs."
}

func (t *ListJobsTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Number of jobs to return (default 50)",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "Offset to start listing from (default 0)",
			},
		},
	}
}

func (t *ListJobsTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	limit := 50
	if l, ok := input["limit"].(float64); ok {
		limit = int(l)
	}

	offset := 0
	if o, ok := input["offset"].(float64); ok {
		offset = int(o)
	}

	// Default to listing first 50 jobs
	jobs, _, err := t.scheduler.ListJobs(listCtx, offset, limit)
	if err != nil {
		return nil, err
	}

	// Simplify output for LLM
	var result []map[string]interface{}
	for _, j := range jobs {
		result = append(result, map[string]interface{}{
			"id":        j.ID,
			"name":      j.Name,
			"type":      j.Type,
			"schedule":  j.Schedule,
			"status":    j.Status,
			"next_run":  j.NextRunAt,
			"task_type": j.TaskType,
		})
	}

	return result, nil
}

func (t *ListJobsTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "list_jobs",
		ToolType:       "extension",
	}
}

// =================================================================================
// DeleteJobTool
// =================================================================================

type DeleteJobTool struct {
	scheduler *xcron.Scheduler
}

func (t *DeleteJobTool) Name() string {
	return "delete_job"
}

func (t *DeleteJobTool) Description() string {
	return "Delete a scheduled job by its ID."
}

func (t *DeleteJobTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"job_id": map[string]interface{}{
				"type":        "string",
				"description": "The ID of the job to delete",
			},
		},
		"required": []string{"job_id"},
	}
}

func (t *DeleteJobTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	jobID, ok := input["job_id"].(string)
	if !ok || jobID == "" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("job_id is required"))
	}

	deleteCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := t.scheduler.RemoveJob(deleteCtx, jobID); err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status": "success",
		"msg":    fmt.Sprintf("Job %s deleted successfully", jobID),
	}, nil
}

func (t *DeleteJobTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "delete_job",
		ToolType:       "extension",
	}
}
