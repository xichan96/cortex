package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/xichan96/cortex/pkg/xcron"
)

// MockJobStore implements xcron.JobStore
type MockJobStore struct {
	jobs map[string]*xcron.Job
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
	return nil, nil // Return error if needed, but nil for now
}

func (m *MockJobStore) Delete(ctx context.Context, id string) error {
	delete(m.jobs, id)
	return nil
}

func (m *MockJobStore) List(ctx context.Context, offset, limit int) ([]*xcron.Job, int64, error) {
	var jobs []*xcron.Job
	for _, job := range m.jobs {
		jobs = append(jobs, job)
	}
	// Simple slice logic (not efficient but fine for small tests)
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

func TestScheduleJobTool_Execute(t *testing.T) {
	store := NewMockJobStore()
	scheduler := xcron.NewScheduler(store)
	tool := &ScheduleJobTool{scheduler: scheduler}

	// Test case 1: Missing required fields
	_, err := tool.Execute(map[string]interface{}{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")

	_, err = tool.Execute(map[string]interface{}{
		"name": "test-job",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "type is required")

	// Test case 2: Valid input (Oneshot)
	result, err := tool.Execute(map[string]interface{}{
		"name":      "test-job",
		"type":      "oneshot",
		"schedule":  "1h",
		"payload":   "do something",
		"task_type": "agent_task",
	})
	assert.NoError(t, err)
	assert.NotNil(t, result)
	
	// Verify job is stored
	resMap := result.(map[string]interface{})
	jobID := resMap["job_id"].(string)
	assert.NotEmpty(t, jobID)
	
	assert.Len(t, store.jobs, 1)
	assert.Equal(t, "test-job", store.jobs[jobID].Name)
}

func TestListJobsTool_Execute(t *testing.T) {
	store := NewMockJobStore()
	scheduler := xcron.NewScheduler(store)
	tool := &ListJobsTool{scheduler: scheduler}

	// Add dummy jobs
	store.jobs["1"] = &xcron.Job{ID: "1", Name: "Job1"}
	store.jobs["2"] = &xcron.Job{ID: "2", Name: "Job2"}
	store.jobs["3"] = &xcron.Job{ID: "3", Name: "Job3"}

	// Test case 1: List all (default limit 50)
	res, err := tool.Execute(map[string]interface{}{})
	assert.NoError(t, err)
	jobs := res.([]map[string]interface{})
	assert.Len(t, jobs, 3)

	// Test case 2: Pagination (Limit 1)
	res, err = tool.Execute(map[string]interface{}{
		"limit": 1.0, // JSON numbers are float64
	})
	assert.NoError(t, err)
	jobs = res.([]map[string]interface{})
	assert.Len(t, jobs, 1)

	// Test case 3: Pagination (Offset 3)
	res, err = tool.Execute(map[string]interface{}{
		"offset": 3.0,
	})
	assert.NoError(t, err)
	jobs = res.([]map[string]interface{})
	assert.Len(t, jobs, 0)
}
