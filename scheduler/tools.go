package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

// NewTools creates a set of tools for interacting with the scheduler service
func NewTools(s *Service) []types.Tool {
	return []types.Tool{
		&ScheduleJobTool{service: s},
		&ListJobsTool{service: s},
		&DeleteJobTool{service: s},
		&StopJobTool{service: s},
	}
}

// =================================================================================
// ScheduleJobTool
// =================================================================================

type ScheduleJobTool struct {
	service *Service
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
			"session_id": map[string]interface{}{
				"type":        "string",
				"description": "Optional session ID for memory context persistence.",
			},
			"schedule": map[string]interface{}{
				"type":        "string",
				"description": "Schedule expression. For oneshot/periodic: duration string (e.g. '10s', '1h'). For cron: cron expression (e.g. '* * * * * *').",
			},
			"payload": map[string]interface{}{
				"type":        "string",
				"description": "The instruction or payload to execute. If task_type is 'agent_task', this can be a string (instruction) or JSON object (with message, model, thinking, timeout_seconds, max_iterations, tools, skills).",
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

	jobType, ok := input["type"].(string)
	if !ok || jobType == "" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("type is required"))
	}

	sessionID, _ := input["session_id"].(string)

	schedule, ok := input["schedule"].(string)
	if !ok || schedule == "" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("schedule is required"))
	}

	payload, ok := input["payload"]
	if !ok || payload == nil {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("payload is required"))
	}

	taskType, _ := input["task_type"].(string)
	if taskType == "" {
		taskType = "agent_task"
	}

	// Validate agent_task payload
	if taskType == "agent_task" {
		if payloadStr, ok := payload.(string); ok {
			trimmed := strings.TrimSpace(payloadStr)
			if strings.HasPrefix(trimmed, "{") {
				var parsed map[string]interface{}
				if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
					payload = parsed
				}
			}
		}

		if payloadMap, ok := payload.(map[string]interface{}); ok {
			if msg, ok := payloadMap["message"].(string); ok && msg != "" {
				payloadMap["message"] = sanitizeMessage(schedule, msg)
			} else {
				return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("payload must include non-empty 'message' describing the specific action"))
			}
			if maxIt, ok := payloadMap["max_iterations"]; ok && maxIt != nil {
				switch v := maxIt.(type) {
				case float64:
					if v <= 0 || v != float64(int(v)) {
						return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("max_iterations must be a positive integer"))
					}
					payloadMap["max_iterations"] = int(v)
				case float32:
					if v <= 0 || v != float32(int(v)) {
						return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("max_iterations must be a positive integer"))
					}
					payloadMap["max_iterations"] = int(v)
				case int:
					if v <= 0 {
						return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("max_iterations must be a positive integer"))
					}
				case int64:
					if v <= 0 {
						return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("max_iterations must be a positive integer"))
					}
					payloadMap["max_iterations"] = int(v)
				case int32:
					if v <= 0 {
						return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("max_iterations must be a positive integer"))
					}
					payloadMap["max_iterations"] = int(v)
				default:
					return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("max_iterations must be a positive integer"))
				}
			}
			// Ensure required tools when skills are present
			if skillsVal, hasSkills := payloadMap["skills"]; hasSkills && skillsVal != nil {
				var skills []string
				switch v := skillsVal.(type) {
				case []string:
					skills = v
				case []interface{}:
					for _, it := range v {
						if s, ok := it.(string); ok {
							skills = append(skills, s)
						}
					}
				}
				// Collect tools if provided
				toolsList := map[string]struct{}{}
				if toolsVal, ok := payloadMap["tools"]; ok && toolsVal != nil {
					switch v := toolsVal.(type) {
					case []string:
						for _, tname := range v {
							toolsList[tname] = struct{}{}
						}
					case []interface{}:
						for _, it := range v {
							if s, ok := it.(string); ok {
								toolsList[s] = struct{}{}
							}
						}
					}
				}
				// Normalize and auto-inject safe defaults:
				// - 'file' tool (for read_file operation to read SKILL.md)
				// - 'command' tool (to run shell commands required by skills)
				if len(skills) > 0 {
					// normalize 'read_file' -> 'file'
					if _, hasReadFile := toolsList["read_file"]; hasReadFile {
						delete(toolsList, "read_file")
						toolsList["file"] = struct{}{}
					}
					// ensure 'file' and 'command'
					toolsList["file"] = struct{}{}
					toolsList["command"] = struct{}{}

					// write back normalized tools
					norm := make([]string, 0, len(toolsList))
					for name := range toolsList {
						norm = append(norm, name)
					}
					payloadMap["tools"] = norm
				}
			}
			toolsVal, hasTools := payloadMap["tools"]
			if hasTools && toolsVal != nil {
				switch v := toolsVal.(type) {
				case []string:
					if len(v) == 0 {
						return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("tools must be a non-empty array of strings"))
					}
				case []interface{}:
					if len(v) == 0 {
						return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("tools must be a non-empty array of strings"))
					}
				default:
					return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("tools must be an array of strings"))
				}
			} else {
				// tools optional: if omitted, agent runs with default registry or none
				// no-op
			}
		} else {
			// allow string payload (raw instruction); will be wrapped later in service
			// no-op
		}
	}

	// Restrict task_type to agent_task only
	if taskType != "" && taskType != "agent_task" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("only 'agent_task' is supported for scheduling"))
	}

	jobInput := ScheduleJobInput{
		Name:      name,
		Type:      jobType,
		SessionID: sessionID,
		Schedule:  schedule,
		Payload:   payload,
		TaskType:  taskType,
	}

	id, err := t.service.ScheduleJob(ctx, jobInput)
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

func sanitizeMessage(schedule string, msg string) string {
	s := msg
	pats := []string{
		`(?U)(每隔\s*[一二三四五六七八九十0-9]+\s*(秒|分钟|分|小时|天))`,
		`(?U)(每\s*[一二三四五六七八九十0-9]+\s*(秒|分钟|分|小时|天))`,
		`(?U)((在)?\s*[一二三四五六七八九十0-9]+\s*(秒|分钟|分|小时|天)后)`,
	}
	for _, p := range pats {
		re := regexp.MustCompile(p)
		s = re.ReplaceAllString(s, "")
	}
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "，。,.、;；")
	return s
}

// =================================================================================
// ListJobsTool
// =================================================================================

type ListJobsTool struct {
	service *Service
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
	limit := 50
	if l, ok := input["limit"].(float64); ok {
		limit = int(l)
	}

	offset := 0
	if o, ok := input["offset"].(float64); ok {
		offset = int(o)
	}

	jobs, _, err := t.service.ListJobs(ctx, limit, offset)
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
	service *Service
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

	if err := t.service.DeleteJob(ctx, jobID); err != nil {
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

// =================================================================================
// StopJobTool
// =================================================================================

type StopJobTool struct {
	service *Service
}

func (t *StopJobTool) Name() string {
	return "stop_job"
}

func (t *StopJobTool) Description() string {
	return "Stop a scheduled job by its ID or name without deleting it."
}

func (t *StopJobTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"job_id": map[string]interface{}{
				"type":        "string",
				"description": "The ID of the job to stop",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "The name of the job to stop (if job_id is not provided)",
			},
		},
	}
}

func (t *StopJobTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	jobID, _ := input["job_id"].(string)
	name, _ := input["name"].(string)

	if jobID == "" && name == "" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("either job_id or name must be provided"))
	}

	if jobID == "" && name != "" {
		jobs, _, err := t.service.ListJobs(ctx, 1000, 0)
		if err != nil {
			return nil, err
		}
		for _, j := range jobs {
			if j.Name == name {
				jobID = j.ID
				break
			}
		}
		if jobID == "" {
			return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("no job found with name: %s", name))
		}
	}

	if err := t.service.StopJob(ctx, jobID); err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status": "success",
		"msg":    fmt.Sprintf("Job %s stopped successfully", jobID),
	}, nil
}

func (t *StopJobTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "stop_job",
		ToolType:       "extension",
	}
}
