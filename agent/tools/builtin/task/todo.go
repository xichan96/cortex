package task

import (
	"context"
	_ "embed"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type TaskState string

const (
	TaskStatePending    TaskState = "pending"
	TaskStateInProgress TaskState = "in_progress"
	TaskStateCompleted  TaskState = "completed"
	TaskStateCancelled  TaskState = "cancelled"
)

type todoTask struct {
	ID         string    `json:"id"`
	Content    string    `json:"content"`
	ActiveForm string    `json:"active_form"`
	State      TaskState `json:"state"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type TodoTool struct {
	mu    sync.RWMutex
	tasks map[string]*todoTask
	seq   atomic.Int64
}

func NewTodoTool() types.Tool {
	return &TodoTool{tasks: make(map[string]*todoTask)}
}

func (t *TodoTool) Name() string {
	return "todo"
}

//go:embed todo.txt
var todoDescription string

func (t *TodoTool) Description() string {
	return todoDescription
}

func (t *TodoTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"operation": map[string]interface{}{
				"type":        "string",
				"description": "add | remove | update | list",
				"enum":        []string{"add", "remove", "update", "list"},
			},
			"task_id": map[string]interface{}{
				"type":        "string",
				"description": "Task id for remove or update.",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Imperative description (e.g. Run tests). Required for add; optional for update.",
			},
			"active_form": map[string]interface{}{
				"type":        "string",
				"description": "Present-continuous form (e.g. Running tests). Optional on add (derived if omitted).",
			},
			"state": map[string]interface{}{
				"type":        "string",
				"description": "For update: pending | in_progress | completed | cancelled",
				"enum":        []string{"pending", "in_progress", "completed", "cancelled"},
			},
		},
		"required": []string{"operation"},
	}
}

func (t *TodoTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	op, _ := input["operation"].(string)
	switch op {
	case "add":
		return t.opAdd(input)
	case "remove":
		return t.opRemove(input)
	case "update":
		return t.opUpdate(input)
	case "list":
		return t.opList()
	default:
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("operation must be add, remove, update, or list"))
	}
}

func (t *TodoTool) opAdd(input map[string]interface{}) (interface{}, error) {
	content, _ := input["content"].(string)
	if content == "" {
		if v, ok := input["task"].(string); ok {
			content = v
		}
	}
	if content == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("content is required for add"))
	}
	activeForm, _ := input["active_form"].(string)
	if activeForm == "" {
		activeForm = content
	}
	id := "t-" + strconv.FormatInt(t.seq.Add(1), 10)
	now := time.Now()
	nt := &todoTask{ID: id, Content: content, ActiveForm: activeForm, State: TaskStatePending, CreatedAt: now, UpdatedAt: now}
	t.mu.Lock()
	t.tasks[id] = nt
	t.mu.Unlock()
	return map[string]interface{}{"task": nt}, nil
}

func (t *TodoTool) opRemove(input map[string]interface{}) (interface{}, error) {
	id, _ := input["task_id"].(string)
	if id == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("task_id is required for remove"))
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.tasks[id]; !ok {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("unknown task_id"))
	}
	delete(t.tasks, id)
	return map[string]interface{}{"removed": id}, nil
}

func parseState(s string) (TaskState, error) {
	switch TaskState(s) {
	case TaskStatePending, TaskStateInProgress, TaskStateCompleted, TaskStateCancelled:
		return TaskState(s), nil
	default:
		return "", fmt.Errorf("invalid state")
	}
}

func (t *TodoTool) opUpdate(input map[string]interface{}) (interface{}, error) {
	id, _ := input["task_id"].(string)
	if id == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("task_id is required for update"))
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	task, ok := t.tasks[id]
	if !ok {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("unknown task_id"))
	}
	if v, ok := input["content"].(string); ok && v != "" {
		task.Content = v
	} else if v, ok := input["task"].(string); ok && v != "" {
		task.Content = v
	}
	if v, ok := input["active_form"].(string); ok && v != "" {
		task.ActiveForm = v
	}
	if v, ok := input["state"].(string); ok && v != "" {
		st, err := parseState(v)
		if err != nil {
			return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(err)
		}
		if st == TaskStateInProgress {
			for tid, o := range t.tasks {
				if tid != id && o.State == TaskStateInProgress {
					o.State = TaskStatePending
					o.UpdatedAt = time.Now()
				}
			}
		}
		task.State = st
	}
	task.UpdatedAt = time.Now()
	out := *task
	return map[string]interface{}{"task": out}, nil
}

func (t *TodoTool) opList() (interface{}, error) {
	t.mu.RLock()
	list := make([]*todoTask, 0, len(t.tasks))
	for _, v := range t.tasks {
		p := new(todoTask)
		*p = *v
		list = append(list, p)
	}
	t.mu.RUnlock()
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	return map[string]interface{}{"tasks": list, "count": len(list)}, nil
}

func (t *TodoTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "todo",
		IsFromToolkit:  false,
		ToolType:       "task",
	}
}
