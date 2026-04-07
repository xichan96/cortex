package scheduler_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/xcron"
	"github.com/xichan96/cortex/scheduler"
)

// MockLLM for inspecting inputs in service tests
type MockLLM struct {
	mock.Mock
}

func (m *MockLLM) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
	args := m.Called(ctx, messages)
	// Simulate a successful LLM response
	return types.Message{Role: "assistant", Content: "Action completed"}, args.Error(1)
}

func (m *MockLLM) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	args := m.Called(ctx, messages)
	return args.Get(0).(<-chan types.StreamMessage), args.Error(1)
}

func (m *MockLLM) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	args := m.Called(ctx, messages, tools)
	// Simulate a successful LLM response
	return types.Message{Role: "assistant", Content: "Action completed"}, args.Error(1)
}

func (m *MockLLM) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	args := m.Called(ctx, messages, tools)
	return args.Get(0).(<-chan types.StreamMessage), args.Error(1)
}

func (m *MockLLM) GetModelName() string {
	return "mock-model"
}

func (m *MockLLM) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{Name: "mock-model"}
}

func setupServiceTest(t *testing.T) (*scheduler.Service, *xcron.Scheduler, *MockLLM, *gorm.DB) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	assert.NoError(t, err)
	db.AutoMigrate(&xcron.Job{})

	jobStore := xcron.NewGormJobStore(db)
	cronScheduler := xcron.NewScheduler(jobStore)
	svc := scheduler.NewService(cronScheduler)

	mockLLM := new(MockLLM)
	agentConfig := types.NewAgentConfig()

	// Configure the service with the mock LLM and other necessary components
	svc.ConfigureAgent(mockLLM, agentConfig, nil, nil, nil)

	return svc, cronScheduler, mockLLM, db
}

func TestService_Integration_SerialAndConcurrent(t *testing.T) {
	svc, cronScheduler, mockLLM, db := setupServiceTest(t)

	// Set a concurrency limit for the underlying xcron scheduler
	cronScheduler.SetMaxConcurrent(1) // To test both serial and parallel behavior

	var executionCounter int32
	var wg sync.WaitGroup
	wg.Add(2)

	// Mock the LLM behavior. Since the agent will be executed, the mock needs to be told what to expect.
	// We expect two calls to ChatWithTools.
	mockLLM.On("ChatWithTools", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		atomic.AddInt32(&executionCounter, 1)
		time.Sleep(100 * time.Millisecond) // Simulate work
		atomic.AddInt32(&executionCounter, -1)
		wg.Done()
	}).Return(types.Message{Role: "assistant", Content: "done"}, nil).Times(2)

	cronScheduler.Start()
	defer cronScheduler.Stop()

	// Schedule two jobs to run at the same time
	// One is serial, the other is not. Given MaxConcurrent(1), they should run sequentially.
	input1 := scheduler.ScheduleJobInput{
		Name:          "serial-job",
		Type:          "oneshot",
		Schedule:      "10ms",
		Payload:       "do something serially",
		ExecutionMode: "serial",
	}
	jobID1, err := svc.ScheduleJob(context.Background(), input1)
	assert.NoError(t, err)

	input2 := scheduler.ScheduleJobInput{
		Name:     "another-job",
		Type:     "oneshot",
		Schedule: "10ms",
		Payload:  "do something else",
	}
	jobID2, err := svc.ScheduleJob(context.Background(), input2)
	assert.NoError(t, err)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for jobs to execute")
	}

	mockLLM.AssertExpectations(t)

	var job1, job2 xcron.Job
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		if db.First(&job1, "id = ?", jobID1).Error == nil && db.First(&job2, "id = ?", jobID2).Error == nil &&
			job1.Status == xcron.JobStatusCompleted && job2.Status == xcron.JobStatusCompleted {
			return
		}
	}
	db.First(&job1, "id = ?", jobID1)
	db.First(&job2, "id = ?", jobID2)
	assert.Equal(t, xcron.JobStatusCompleted, job1.Status, "job1")
	assert.Equal(t, xcron.JobStatusCompleted, job2.Status, "job2")
}

func TestService_AgentTaskExecutorBypassesAgentEngine(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	assert.NoError(t, err)
	assert.NoError(t, db.AutoMigrate(&xcron.Job{}))

	jobStore := xcron.NewGormJobStore(db)
	cronScheduler := xcron.NewScheduler(jobStore)
	svc := scheduler.NewService(cronScheduler)

	var calls int32
	svc.SetAgentTaskExecutor(func(ctx context.Context, job *xcron.Job, instruction string, payload xcron.AgentPayload) error {
		assert.Equal(t, "hello from executor", instruction)
		assert.Equal(t, "s-1", job.SessionID)
		atomic.AddInt32(&calls, 1)
		return nil
	})

	cronScheduler.Start()
	defer cronScheduler.Stop()

	_, err = svc.ScheduleJob(context.Background(), scheduler.ScheduleJobInput{
		Name:      "exec-test",
		Type:      "oneshot",
		Schedule:  "10ms",
		SessionID: "s-1",
		Payload:   "hello from executor",
	})
	assert.NoError(t, err)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&calls) >= 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("executor not invoked, calls=%d", atomic.LoadInt32(&calls))
}
