package scheduler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/xcron"
)

// MockJobStore implements xcron.JobStore
type MockJobStore struct {
	jobs              map[string]*xcron.Job
	lastDeletedStatus xcron.JobStatus
}

func NewMockJobStore() *MockJobStore {
	return &MockJobStore{
		jobs: make(map[string]*xcron.Job),
	}
}

func (m *MockJobStore) Save(ctx context.Context, job *xcron.Job) error {
	m.jobs[job.ID] = job
	return nil
}

func (m *MockJobStore) Get(ctx context.Context, id string) (*xcron.Job, error) {
	if job, ok := m.jobs[id]; ok {
		return job, nil
	}
	return nil, nil
}

func (m *MockJobStore) Delete(ctx context.Context, id string) error {
	if job, ok := m.jobs[id]; ok {
		m.lastDeletedStatus = job.Status
		delete(m.jobs, id)
	}
	return nil
}

func (m *MockJobStore) List(ctx context.Context, offset, limit int) ([]*xcron.Job, int64, error) {
	var jobs []*xcron.Job
	for _, job := range m.jobs {
		jobs = append(jobs, job)
	}
	if offset >= len(jobs) {
		return []*xcron.Job{}, int64(len(m.jobs)), nil
	}
	end := offset + limit
	if end > len(jobs) {
		end = len(jobs)
	}
	return jobs[offset:end], int64(len(m.jobs)), nil
}

func (m *MockJobStore) GetPendingJobs(ctx context.Context) ([]*xcron.Job, error) {
	return []*xcron.Job{}, nil
}

func (m *MockJobStore) UpdateStatus(ctx context.Context, id string, status xcron.JobStatus, lastRun *time.Time, nextRun time.Time, lastDuration time.Duration, lastError string) error {
	if job, ok := m.jobs[id]; ok {
		job.Status = status
		job.LastRunAt = lastRun
		job.NextRunAt = nextRun
		job.LastDuration = lastDuration
		job.LastError = lastError
	}
	return nil
}

func (m *MockJobStore) ResetStuckJobs(ctx context.Context, timeout time.Duration) error {
	return nil
}

func getTool(tools []types.Tool, name string) types.Tool {
	for _, t := range tools {
		if t.Name() == name {
			return t
		}
	}
	return nil
}

func TestScheduleJobTool_Execute(t *testing.T) {
	store := NewMockJobStore()
	xcronScheduler := xcron.NewScheduler(store)
	service := NewService(xcronScheduler)
	tools := NewTools(service)

	tool := getTool(tools, "schedule_job")
	assert.NotNil(t, tool)

	// Test case 1: Missing required fields
	_, err := tool.Execute(context.Background(), map[string]interface{}{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")

	_, err = tool.Execute(context.Background(), map[string]interface{}{
		"name": "test-job",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "type is required")

	// Test case 2: Valid input (Oneshot)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"name":      "test-job",
		"type":      "oneshot",
		"schedule":  "1h",
		"payload":   "do something",
		"task_type": "agent_task",
	})
	assert.NoError(t, err)

	// Test case 3: Valid input (Map payload without tools)
	resultNoTools, err := tool.Execute(context.Background(), map[string]interface{}{
		"name":     "test-job-invalid",
		"type":     "oneshot",
		"schedule": "1h",
		"payload": map[string]interface{}{
			"message": "do something",
		},
		"task_type": "agent_task",
	})
	assert.NoError(t, err)
	assert.NotNil(t, resultNoTools)

	// Test case 4: Valid input (With tools)
	result2, err := tool.Execute(context.Background(), map[string]interface{}{
		"name":     "test-job-valid",
		"type":     "oneshot",
		"schedule": "1h",
		"payload": map[string]interface{}{
			"message": "do something",
			"tools":   []string{"search"},
		},
		"task_type": "agent_task",
	})
	assert.NoError(t, err)
	assert.NotNil(t, result2)

	// Verify job 1 (test-job)
	resMap := result.(map[string]interface{})
	jobID := resMap["job_id"].(string)
	assert.NotEmpty(t, jobID)
	assert.Equal(t, "test-job", store.jobs[jobID].Name)

	// Verify job 2 (test-job-valid)
	resMap2 := result2.(map[string]interface{})
	jobID2 := resMap2["job_id"].(string)
	assert.NotEmpty(t, jobID2)
	assert.Equal(t, "test-job-valid", store.jobs[jobID2].Name)

	// Test case 5: Valid input (With session_id)
	result3, err := tool.Execute(context.Background(), map[string]interface{}{
		"name":       "test-job-session",
		"type":       "oneshot",
		"session_id": "session-123",
		"schedule":   "1h",
		"payload": map[string]interface{}{
			"message": "do something",
			"tools":   []string{"search"},
		},
		"task_type": "agent_task",
	})
	assert.NoError(t, err)
	assert.NotNil(t, result3)

	// Verify job 3 (test-job-session)
	resMap3 := result3.(map[string]interface{})
	jobID3 := resMap3["job_id"].(string)
	assert.NotEmpty(t, jobID3)
	assert.Equal(t, "test-job-session", store.jobs[jobID3].Name)
	assert.Equal(t, "session-123", store.jobs[jobID3].SessionID)

	// Test case 6: Valid input (JSON string payload without tools)
	_, err = tool.Execute(context.Background(), map[string]interface{}{
		"name":      "test-job-json-str-invalid",
		"type":      "oneshot",
		"schedule":  "1h",
		"payload":   `{"message":"do something"}`,
		"task_type": "agent_task",
	})
	assert.NoError(t, err)

	// Test case 7: Valid input (JSON string payload with tools)
	_, err = tool.Execute(context.Background(), map[string]interface{}{
		"name":      "test-job-json-str-valid",
		"type":      "oneshot",
		"schedule":  "1h",
		"payload":   `{"message":"do something","tools":["search"]}`,
		"task_type": "agent_task",
	})
	assert.NoError(t, err)

	// Test case 8: Invalid input (Empty tools)
	_, err = tool.Execute(context.Background(), map[string]interface{}{
		"name":     "test-job-empty-tools",
		"type":     "oneshot",
		"schedule": "1h",
		"payload": map[string]interface{}{
			"message": "do something",
			"tools":   []interface{}{},
		},
		"task_type": "agent_task",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tools must be a non-empty array of strings")

	assert.Len(t, store.jobs, 6)
}

func TestScheduleJobTool_SanitizeMessage(t *testing.T) {
	store := NewMockJobStore()
	x := xcron.NewScheduler(store)
	svc := NewService(x)
	tool := getTool(NewTools(svc), "schedule_job")
	assert.NotNil(t, tool)

	// Periodic 1m, message contains time phrase
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"name":     "drink-water",
		"type":     "periodic",
		"schedule": "1m",
		"payload": map[string]interface{}{
			"message": "use macos-alert，every minute pop up a notification：Drink water！Keep healthy. Title is “Drink Water Reminder”，sound is “Ping”。",
		},
		"task_type": "agent_task",
	})
	assert.NoError(t, err)

	// Find job and verify payload message sanitized
	var got *xcron.Job
	for _, j := range store.jobs {
		if j.Name == "drink-water" {
			got = j
			break
		}
	}
	assert.NotNil(t, got)
	var payload map[string]interface{}
	err = json.Unmarshal([]byte(got.Payload), &payload)
	assert.NoError(t, err)
	msg := payload["message"].(string)
	assert.NotContains(t, msg, "every minute")
	assert.Contains(t, msg, "use macos-alert，every minute pop up a notification：Drink water！Keep healthy. Title is “Drink Water Reminder”，sound is “Ping”。")
}
func TestListJobsTool_Execute(t *testing.T) {
	store := NewMockJobStore()
	xcronScheduler := xcron.NewScheduler(store)
	service := NewService(xcronScheduler)
	tools := NewTools(service)

	tool := getTool(tools, "list_jobs")
	assert.NotNil(t, tool)

	// Add dummy jobs
	store.jobs["1"] = &xcron.Job{ID: "1", Name: "Job1"}
	store.jobs["2"] = &xcron.Job{ID: "2", Name: "Job2"}
	store.jobs["3"] = &xcron.Job{ID: "3", Name: "Job3"}

	// Test case 1: List all (default limit 50)
	res, err := tool.Execute(context.Background(), map[string]interface{}{})
	assert.NoError(t, err)
	jobs := res.([]map[string]interface{})
	assert.Len(t, jobs, 3)

	// Test case 2: Pagination (Limit 1)
	res, err = tool.Execute(context.Background(), map[string]interface{}{
		"limit": 1.0,
	})
	assert.NoError(t, err)
	jobs = res.([]map[string]interface{})
	assert.Len(t, jobs, 1)
}

func TestStopJobTool_Execute(t *testing.T) {
	store := NewMockJobStore()
	x := xcron.NewScheduler(store)
	svc := NewService(x)
	tools := NewTools(svc)
	stop := getTool(tools, "stop_job")
	assert.NotNil(t, stop)

	jobID, err := svc.ScheduleJob(context.Background(), ScheduleJobInput{
		Name:     "stop-me",
		Type:     "periodic",
		Schedule: "1m",
		Payload: map[string]interface{}{
			"message": "noop",
		},
		TaskType: "agent_task",
	})
	assert.NoError(t, err)
	assert.NotEmpty(t, jobID)

	_, err = stop.Execute(context.Background(), map[string]interface{}{
		"job_id": jobID,
	})
	assert.NoError(t, err)

	job, _ := store.Get(context.Background(), jobID)
	assert.Equal(t, xcron.JobStatusStopped, job.Status)
}

func TestDeleteJobTool_StopThenDelete(t *testing.T) {
	store := NewMockJobStore()
	x := xcron.NewScheduler(store)
	svc := NewService(x)
	tools := NewTools(svc)
	del := getTool(tools, "delete_job")
	assert.NotNil(t, del)

	jobID, err := svc.ScheduleJob(context.Background(), ScheduleJobInput{
		Name:     "delete-me",
		Type:     "periodic",
		Schedule: "1m",
		Payload: map[string]interface{}{
			"message": "noop",
			"tools":   []string{"search"},
		},
		TaskType: "agent_task",
	})
	assert.NoError(t, err)
	assert.NotEmpty(t, jobID)

	_, err = del.Execute(context.Background(), map[string]interface{}{
		"job_id": jobID,
	})
	assert.NoError(t, err)

	// Ensure job was removed
	job, _ := store.Get(context.Background(), jobID)
	assert.Nil(t, job)
	// Ensure status recorded as stopped before deletion
	assert.Equal(t, xcron.JobStatusStopped, store.lastDeletedStatus)
}

type mockLLMProvider struct{}

func (m mockLLMProvider) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
	return types.Message{Role: "assistant", Content: "ok"}, nil
}

func (m mockLLMProvider) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	ch := make(chan types.StreamMessage)
	close(ch)
	return ch, nil
}

func (m mockLLMProvider) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return types.Message{Role: "assistant", Content: "ok"}, nil
}

func (m mockLLMProvider) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	ch := make(chan types.StreamMessage)
	close(ch)
	return ch, nil
}

func (m mockLLMProvider) GetModelName() string {
	return "mock"
}

func (m mockLLMProvider) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{Name: "mock"}
}

func TestAgentEngine_SaveContextUsesChatMessageRole(t *testing.T) {
	cfg := types.NewAgentConfig()
	cfg.ChatMessageRole = "system"

	ae := engine.NewAgentEngine(mockLLMProvider{}, cfg)
	mem := providers.NewSimpleMemoryProvider()
	ae.SetMemory(context.Background(), mem)

	result, err := ae.Execute(context.Background(), "hello", nil)
	assert.NoError(t, err)
	assert.Equal(t, "ok", result.Output)

	history, err := mem.GetChatHistory(context.Background())
	assert.NoError(t, err)
	assert.Len(t, history, 2)
	assert.Equal(t, "system", history[0].Role)
	assert.Equal(t, "hello", history[0].Content)
	assert.Equal(t, "assistant", history[1].Role)
	assert.Equal(t, "ok", history[1].Content)
}
