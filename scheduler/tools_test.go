package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/agent/skills"
	"github.com/xichan96/cortex/agent/tools"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/xcron"
)

// MockJobStore implements xcron.JobStore
type MockJobStore struct {
	jobs              map[string]*xcron.Job
	lastDeletedStatus xcron.JobStatus
	FailUpdateStatus  bool
	FailDelete        bool
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
	if m.FailDelete {
		return fmt.Errorf("delete failed")
	}
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

func (m *MockJobStore) ListWithOptions(ctx context.Context, opts xcron.ListOptions) ([]*xcron.Job, int64, error) {
	var jobs []*xcron.Job
	for _, job := range m.jobs {
		if len(opts.Status) > 0 {
			var ok bool
			for _, st := range opts.Status {
				if job.Status == st {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		if len(opts.Type) > 0 {
			var ok bool
			for _, ty := range opts.Type {
				if job.Type == ty {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		if opts.SessionID != "" && job.SessionID != opts.SessionID {
			continue
		}
		j := *job
		jobs = append(jobs, &j)
	}
	if opts.OrderBy == "next_run_at" {
		sort.Slice(jobs, func(i, j int) bool {
			return jobs[i].NextRunAt.Before(jobs[j].NextRunAt)
		})
	} else {
		sort.Slice(jobs, func(i, j int) bool {
			return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
		})
	}
	count := int64(len(jobs))
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if opts.Offset >= len(jobs) {
		return []*xcron.Job{}, count, nil
	}
	end := opts.Offset + limit
	if end > len(jobs) {
		end = len(jobs)
	}
	return jobs[opts.Offset:end], count, nil
}

func (m *MockJobStore) GetPendingJobs(ctx context.Context) ([]*xcron.Job, error) {
	var out []*xcron.Job
	for _, j := range m.jobs {
		out = append(out, j)
	}
	return out, nil
}

func (m *MockJobStore) UpdateStatus(ctx context.Context, id string, status xcron.JobStatus, lastRun *time.Time, nextRun time.Time, lastDuration time.Duration, lastError string) error {
	if m.FailUpdateStatus {
		return fmt.Errorf("update status failed")
	}
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

type dummyTool struct{ name string }

func (d dummyTool) Name() string                                        { return d.name }
func (d dummyTool) Description() string                                 { return "d" }
func (d dummyTool) Schema() map[string]interface{}                       { return nil }
func (d dummyTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) { return nil, nil }
func (d dummyTool) Metadata() types.ToolMetadata                        { return types.ToolMetadata{} }

func TestService_Handler_SkillNotFoundAndDisallowedTools(t *testing.T) {
	store := NewMockJobStore()
	sched := xcron.NewScheduler(store)
	svc := NewService(sched)
	skillReg := skills.NewRegistry()
	skillReg.Register(&skills.Skill{Name: "a", Description: "a", Triggers: []skills.Trigger{{Type: "keyword", Pattern: "a"}}, Content: "a"})
	toolReg := tools.NewRegistry()
	toolReg.Register(dummyTool{name: "file"})
	toolReg.Register(dummyTool{name: "command"})
	toolReg.Register(dummyTool{name: "search"})
	svc.ConfigureAgent(mockLLMProvider{}, types.NewAgentConfig(), nil, toolReg, skillReg)
	_, err := sched.AddJobWithOptions(context.Background(), "job1", xcron.JobTypeOneShot, "30ms", xcron.TaskTypeAgent, map[string]interface{}{"message": "run", "skills": []string{"unknown-skill"}}, "", 0, "")
	assert.NoError(t, err)
	_, err = sched.AddJobWithOptions(context.Background(), "job2", xcron.JobTypeOneShot, "50ms", xcron.TaskTypeAgent, map[string]interface{}{"message": "run", "tools": []string{"schedule_job", "search"}}, "", 0, "")
	assert.NoError(t, err)
	_, err = sched.AddJobWithOptions(context.Background(), "job3", xcron.JobTypeOneShot, "70ms", xcron.TaskTypeAgent, map[string]interface{}{"message": "run", "tools": []string{"ghost"}}, "", 0, "")
	assert.NoError(t, err)
	sched.Start()
	defer sched.Stop()
	time.Sleep(200 * time.Millisecond)
}

func TestService_Handler_MemoryFactory(t *testing.T) {
	store := NewMockJobStore()
	sched := xcron.NewScheduler(store)
	svc := NewService(sched)
	var gotSessionID string
	svc.ConfigureAgentWithMemoryFactory(mockLLMProvider{}, types.NewAgentConfig(), func(sessionID string) types.MemoryProvider {
		gotSessionID = sessionID
		return providers.NewSimpleMemoryProvider()
	}, nil, nil)
	_, err := svc.ScheduleJob(context.Background(), ScheduleJobInput{
		Name: "mf", Type: "oneshot", Schedule: "25ms", Payload: "hi", SessionID: "session-1",
	})
	assert.NoError(t, err)
	sched.Start()
	defer sched.Stop()
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, "session-1", gotSessionID)
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
	assert.Contains(t, msg, "use macos-alert")
	assert.Contains(t, msg, "Drink water")
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

func TestParseAgentInstruction(t *testing.T) {
	msg, out := ParseAgentInstruction(`{"message":"hello"}`)
	assert.Equal(t, "hello", msg)
	assert.Equal(t, "hello", out.Message)

	msg, _ = ParseAgentInstruction(`"plain"`)
	assert.Equal(t, "plain", msg)

	msg, out = ParseAgentInstruction(`{"message":"nested","skills":["a"],"tools":["b"],"max_iterations":5}`)
	assert.Equal(t, "nested", msg)
	assert.Equal(t, []string{"a"}, out.Skills)
	assert.Equal(t, []string{"b"}, out.Tools)
	assert.Equal(t, 5, out.MaxIterations)

	msg, _ = ParseAgentInstruction(`  {"message":"trimmed"}  `)
	assert.Equal(t, "trimmed", msg)
	msg, _ = ParseAgentInstruction(`"{\"message\":\"inner\"}"`)
	assert.Equal(t, "inner", msg)

	msg, _ = ParseAgentInstruction(`not json`)
	assert.Equal(t, "not json", msg)
}

func TestParsePositiveInt(t *testing.T) {
	n, err := parsePositiveInt(float64(3))
	assert.NoError(t, err)
	assert.Equal(t, 3, n)
	_, err = parsePositiveInt(float64(0))
	assert.Error(t, err)
	_, err = parsePositiveInt(float64(-1))
	assert.Error(t, err)
	_, err = parsePositiveInt(float64(3.5))
	assert.Error(t, err)

	n, err = parsePositiveInt(float32(2))
	assert.NoError(t, err)
	assert.Equal(t, 2, n)
	n, err = parsePositiveInt(int(1))
	assert.NoError(t, err)
	assert.Equal(t, 1, n)
	n, _ = parsePositiveInt(int64(4))
	assert.Equal(t, 4, n)
	n, _ = parsePositiveInt(int32(5))
	assert.Equal(t, 5, n)
	_, err = parsePositiveInt("x")
	assert.Error(t, err)
}

func TestScheduleJobTool_DescriptionSchemaMetadata(t *testing.T) {
	tool := &ScheduleJobTool{}
	assert.NotEmpty(t, tool.Description())
	assert.NotNil(t, tool.Schema())
	assert.Equal(t, "schedule_job", tool.Metadata().SourceNodeName)
}

func TestListJobsTool_DescriptionSchemaMetadata(t *testing.T) {
	tool := &ListJobsTool{}
	assert.NotEmpty(t, tool.Description())
	assert.NotNil(t, tool.Schema())
	assert.Equal(t, "list_jobs", tool.Metadata().SourceNodeName)
}

func TestDeleteJobTool_DescriptionSchemaMetadata(t *testing.T) {
	tool := &DeleteJobTool{}
	assert.NotEmpty(t, tool.Description())
	assert.NotNil(t, tool.Schema())
	assert.Equal(t, "delete_job", tool.Metadata().SourceNodeName)
}

func TestStopJobTool_DescriptionSchemaMetadata(t *testing.T) {
	tool := &StopJobTool{}
	assert.NotEmpty(t, tool.Description())
	assert.NotNil(t, tool.Schema())
	assert.Equal(t, "stop_job", tool.Metadata().SourceNodeName)
}

func TestScheduleJobTool_Execute_MessageRequired(t *testing.T) {
	svc := NewService(xcron.NewScheduler(NewMockJobStore()))
	tool := getTool(NewTools(svc), "schedule_job")
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"name": "x", "type": "oneshot", "schedule": "1h",
		"payload": map[string]interface{}{},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "message")
}

func TestScheduleJobTool_Execute_MaxIterations(t *testing.T) {
	store := NewMockJobStore()
	svc := NewService(xcron.NewScheduler(store))
	tool := getTool(NewTools(svc), "schedule_job")
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"name": "x", "type": "oneshot", "schedule": "1h",
		"payload": map[string]interface{}{
			"message": "do it", "max_iterations": 5,
		},
	})
	assert.NoError(t, err)
	_, err = tool.Execute(context.Background(), map[string]interface{}{
		"name": "y", "type": "oneshot", "schedule": "1h",
		"payload": map[string]interface{}{
			"message": "do it", "max_iterations": -1,
		},
	})
	assert.Error(t, err)
	_, err = tool.Execute(context.Background(), map[string]interface{}{
		"name": "z", "type": "oneshot", "schedule": "1h",
		"payload": map[string]interface{}{
			"message": "do it", "max_iterations": "bad",
		},
	})
	assert.Error(t, err)
}

func TestScheduleJobTool_Execute_SkillsWithReadFile(t *testing.T) {
	store := NewMockJobStore()
	svc := NewService(xcron.NewScheduler(store))
	tool := getTool(NewTools(svc), "schedule_job")
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"name": "sk", "type": "oneshot", "schedule": "1h",
		"payload": map[string]interface{}{
			"message": "do it", "skills": []string{"a"}, "tools": []string{"read_file"},
		},
	})
	assert.NoError(t, err)
	var j *xcron.Job
	for _, job := range store.jobs {
		if job.Name == "sk" {
			j = job
			break
		}
	}
	assert.NotNil(t, j)
	var pl map[string]interface{}
	assert.NoError(t, json.Unmarshal([]byte(j.Payload), &pl))
	tools, _ := pl["tools"].([]interface{})
	var names []string
	for _, t := range tools {
		names = append(names, t.(string))
	}
	assert.Contains(t, names, "file")
	assert.NotContains(t, names, "read_file")
}

func TestScheduleJobTool_Execute_NonAgentTaskRejected(t *testing.T) {
	svc := NewService(xcron.NewScheduler(NewMockJobStore()))
	tool := getTool(NewTools(svc), "schedule_job")
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"name": "x", "type": "oneshot", "schedule": "1h", "payload": "x", "task_type": "other",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "only 'agent_task'")
}

func TestScheduleJobTool_Execute_ToolsInvalidType(t *testing.T) {
	svc := NewService(xcron.NewScheduler(NewMockJobStore()))
	tool := getTool(NewTools(svc), "schedule_job")
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"name": "x", "type": "oneshot", "schedule": "1h",
		"payload": map[string]interface{}{"message": "x", "tools": map[string]int{}},
		"task_type": "agent_task",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tools must be")
}

func TestDeleteJobTool_Execute_InvalidJobID(t *testing.T) {
	svc := NewService(xcron.NewScheduler(NewMockJobStore()))
	del := getTool(NewTools(svc), "delete_job")
	_, err := del.Execute(context.Background(), map[string]interface{}{"job_id": 123})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "job_id")
}

func TestDeleteJobTool_Execute_DeleteFails(t *testing.T) {
	store := NewMockJobStore()
	store.FailDelete = true
	store.jobs["j1"] = &xcron.Job{ID: "j1", Name: "x"}
	sched := xcron.NewScheduler(store)
	svc := NewService(sched)
	del := getTool(NewTools(svc), "delete_job")
	_, err := del.Execute(context.Background(), map[string]interface{}{"job_id": "j1"})
	assert.Error(t, err)
}

func TestListJobsTool_Execute_WithFilters(t *testing.T) {
	store := NewMockJobStore()
	store.jobs["1"] = &xcron.Job{ID: "1", Name: "A", Status: xcron.JobStatusPending, Type: xcron.JobTypePeriodic}
	store.jobs["2"] = &xcron.Job{ID: "2", Name: "B", Status: xcron.JobStatusCompleted, Type: xcron.JobTypeOneShot}
	svc := NewService(xcron.NewScheduler(store))
	tool := getTool(NewTools(svc), "list_jobs")
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"limit": 1.0, "offset": 0.0, "order_by": "next_run_at",
		"status": []interface{}{"pending"}, "type": []interface{}{"periodic"}, "session_id": "",
	})
	assert.NoError(t, err)
	assert.NotNil(t, res)
}

func TestStopJobTool_Execute_ByName(t *testing.T) {
	store := NewMockJobStore()
	x := xcron.NewScheduler(store)
	svc := NewService(x)
	jobID, _ := svc.ScheduleJob(context.Background(), ScheduleJobInput{
		Name: "by-name-job", Type: "periodic", Schedule: "1m", Payload: map[string]interface{}{"message": "x"}, TaskType: "agent_task",
	})
	assert.NotEmpty(t, jobID)
	stop := getTool(NewTools(svc), "stop_job")
	_, err := stop.Execute(context.Background(), map[string]interface{}{"name": "by-name-job"})
	assert.NoError(t, err)
	job, _ := store.Get(context.Background(), jobID)
	assert.Equal(t, xcron.JobStatusStopped, job.Status)
}

func TestStopJobTool_Execute_ByNameNotFound(t *testing.T) {
	svc := NewService(xcron.NewScheduler(NewMockJobStore()))
	stop := getTool(NewTools(svc), "stop_job")
	_, err := stop.Execute(context.Background(), map[string]interface{}{"name": "nonexistent"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no job found")
}

func TestService_ListJobs(t *testing.T) {
	store := NewMockJobStore()
	store.jobs["1"] = &xcron.Job{ID: "1", Name: "J1"}
	svc := NewService(xcron.NewScheduler(store))
	jobs, total, err := svc.ListJobs(context.Background(), 10, 0)
	assert.NoError(t, err)
	assert.Len(t, jobs, 1)
	assert.Equal(t, int64(1), total)
}

func TestService_ListJobsWithOptions_DefaultLimit(t *testing.T) {
	store := NewMockJobStore()
	svc := NewService(xcron.NewScheduler(store))
	_, _, err := svc.ListJobsWithOptions(context.Background(), ListJobsOptions{Limit: 0, Offset: 0})
	assert.NoError(t, err)
}

func TestService_ScheduleJob_InvalidType(t *testing.T) {
	svc := NewService(xcron.NewScheduler(NewMockJobStore()))
	_, err := svc.ScheduleJob(context.Background(), ScheduleJobInput{
		Name: "x", Type: "invalid", Schedule: "1h", Payload: "x",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid job type")
}

func TestService_ScheduleJob_CronAndMapPayload(t *testing.T) {
	store := NewMockJobStore()
	svc := NewService(xcron.NewScheduler(store))
	id, err := svc.ScheduleJob(context.Background(), ScheduleJobInput{
		Name: "cron-job", Type: "cron", Schedule: "0 * * * * *", Payload: map[string]interface{}{"message": "run"}, TaskType: "agent_task",
	})
	assert.NoError(t, err)
	assert.NotEmpty(t, id)
}

func TestService_ConfigureAgentWithMemoryFactory(t *testing.T) {
	store := NewMockJobStore()
	sched := xcron.NewScheduler(store)
	svc := NewService(sched)
	called := false
	svc.ConfigureAgentWithMemoryFactory(mockLLMProvider{}, types.NewAgentConfig(), func(sessionID string) types.MemoryProvider {
		called = true
		return providers.NewSimpleMemoryProvider()
	}, nil, nil)
	assert.NotNil(t, svc)
	_ = called
}

func TestService_EnsureAgentConfig(t *testing.T) {
	store := NewMockJobStore()
	sched := xcron.NewScheduler(store)
	svc := NewService(sched)
	svc.ConfigureAgent(mockLLMProvider{}, nil, nil, nil, nil)
	svc.ConfigureAgent(mockLLMProvider{}, &types.AgentConfig{}, nil, nil, nil)
	_, err := svc.ScheduleJob(context.Background(), ScheduleJobInput{
		Name: "e", Type: "oneshot", Schedule: "1h", Payload: "x",
	})
	assert.NoError(t, err)
}

func TestRegisterAgentTaskHandler(t *testing.T) {
	store := NewMockJobStore()
	sched := xcron.NewScheduler(store)
	var gotMsg string
	RegisterAgentTaskHandler(sched, func(input string) error {
		gotMsg = input
		return nil
	})
	_, err := sched.AddJobWithOptions(context.Background(), "t", xcron.JobTypeOneShot, "20ms", xcron.TaskTypeAgent, `{"message":"agent-message"}`, "", 0, "")
	assert.NoError(t, err)
	sched.Start()
	defer sched.Stop()
	time.Sleep(100 * time.Millisecond)
	assert.Contains(t, gotMsg, "agent-message")
}

func TestRegisterAgentTaskHandler_RawPayload(t *testing.T) {
	store := NewMockJobStore()
	sched := xcron.NewScheduler(store)
	var gotMsg string
	RegisterAgentTaskHandler(sched, func(input string) error {
		gotMsg = input
		return nil
	})
	_, err := sched.AddJobWithOptions(context.Background(), "raw", xcron.JobTypeOneShot, "20ms", xcron.TaskTypeAgent, 123, "", 0, "")
	assert.NoError(t, err)
	sched.Start()
	defer sched.Stop()
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, "123", gotMsg)
}

func TestService_DeleteJob_StopFails(t *testing.T) {
	store := NewMockJobStore()
	store.FailUpdateStatus = true
	store.jobs["j1"] = &xcron.Job{ID: "j1", Name: "x", Type: xcron.JobTypePeriodic, Schedule: "1h"}
	sched := xcron.NewScheduler(store)
	svc := NewService(sched)
	err := svc.DeleteJob(context.Background(), "j1")
	assert.NoError(t, err)
	_, ok := store.jobs["j1"]
	assert.False(t, ok)
}

func TestAgentEngine_SaveContextUsesChatMessageRole(t *testing.T) {
	cfg := types.NewAgentConfig()
	cfg.ChatMessageRole = "system"

	ae := engine.NewAgentEngine(mockLLMProvider{}, cfg)
	mem := providers.NewSimpleMemoryProvider()
	ae.SetMemory(context.Background(), mem)

	result, err := ae.Execute(context.Background(), types.NewAgentInput("hello"), nil)
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
