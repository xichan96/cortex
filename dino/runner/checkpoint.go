package runner

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/xichan96/cortex/agent/types"
	dinotask "github.com/xichan96/cortex/dino/task"
)

type taskCheckpointV1 struct {
	Version     int                   `json:"v"`
	Progress    dinotask.TaskProgress `json:"progress"`
	PendingText string                `json:"pending_text"`
	Description string                `json:"description,omitempty"`
}

func marshalTaskCheckpoint(tk *dinotask.Task) ([]byte, error) {
	if tk == nil {
		return nil, fmt.Errorf("nil task")
	}
	p := dinotask.TaskProgress{}
	if tk.Progress != nil {
		p = *tk.Progress
	}
	cp := taskCheckpointV1{
		Version:     1,
		Progress:    p,
		PendingText: tk.PendingInput.String(),
		Description: tk.Description,
	}
	return json.Marshal(cp)
}

func applyTaskCheckpoint(tk *dinotask.Task, raw []byte) error {
	if tk == nil {
		return fmt.Errorf("nil task")
	}
	var cp taskCheckpointV1
	if err := json.Unmarshal(raw, &cp); err != nil {
		return err
	}
	if cp.Version != 1 {
		return fmt.Errorf("unsupported checkpoint version %d", cp.Version)
	}
	tk.Progress = &cp.Progress
	tk.PendingInput = types.NewAgentInput(cp.PendingText)
	if cp.Description != "" {
		tk.Description = cp.Description
	}
	return nil
}

func taskSessionFromCheckpoint(tk *dinotask.Task, raw []byte) (*dinotask.TaskSession, error) {
	if tk == nil {
		return nil, fmt.Errorf("nil task")
	}
	return &dinotask.TaskSession{
		TaskID:    tk.ID,
		SessionID: tk.SessionID,
		Messages:  []string{string(raw)},
		UpdatedAt: time.Now().UTC(),
	}, nil
}

func checkpointFromTaskSession(ts *dinotask.TaskSession) ([]byte, error) {
	if ts == nil || len(ts.Messages) == 0 {
		return nil, fmt.Errorf("empty task session")
	}
	return []byte(ts.Messages[0]), nil
}
