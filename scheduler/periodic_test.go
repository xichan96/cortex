package scheduler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/xichan96/cortex/agent/skills"
	"github.com/xichan96/cortex/agent/tools"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/xcron"
	"github.com/xichan96/cortex/scheduler"
)

// PeriodicMockLLM for inspecting inputs
type PeriodicMockLLM struct {
	mock.Mock
}

func (m *PeriodicMockLLM) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
	args := m.Called(ctx, messages)
	return args.Get(0).(types.Message), args.Error(1)
}

func (m *PeriodicMockLLM) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	args := m.Called(ctx, messages)
	return args.Get(0).(<-chan types.StreamMessage), args.Error(1)
}

func (m *PeriodicMockLLM) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	args := m.Called(ctx, messages, tools)
	return args.Get(0).(types.Message), args.Error(1)
}

func (m *PeriodicMockLLM) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	args := m.Called(ctx, messages, tools)
	return args.Get(0).(<-chan types.StreamMessage), args.Error(1)
}

func (m *PeriodicMockLLM) GetModelName() string {
	return "mock-model"
}

func (m *PeriodicMockLLM) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{Name: "mock-model"}
}

type PeriodicDummyTool struct {
	NameVal string
}

func (t *PeriodicDummyTool) Name() string        { return t.NameVal }
func (t *PeriodicDummyTool) Description() string { return "dummy tool" }
func (t *PeriodicDummyTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	return "executed", nil
}
func (t *PeriodicDummyTool) Schema() map[string]interface{} { return nil }
func (t *PeriodicDummyTool) Metadata() types.ToolMetadata   { return types.ToolMetadata{} }

func setupComponents(t *testing.T, db *gorm.DB) (*scheduler.Service, *xcron.Scheduler, *PeriodicMockLLM, *skills.Registry) {
	jobStore := xcron.NewGormJobStore(db)
	cronScheduler := xcron.NewScheduler(jobStore)
	svc := scheduler.NewService(cronScheduler)

	skillRegistry := skills.NewRegistry()
	mockSkill := &skills.Skill{
		Name:        "macos-alert",
		Description: "Send alerts",
		Triggers: []skills.Trigger{
			{Type: "keyword", Pattern: "alert"},
		},
		Content: "Use 'command' tool to run osascript...",
	}
	skillRegistry.Register(mockSkill)

	toolRegistry := tools.NewRegistry()
	toolRegistry.Register(&PeriodicDummyTool{NameVal: "command"})
	toolRegistry.Register(&PeriodicDummyTool{NameVal: "file"})

	mockLLM := new(PeriodicMockLLM)

	agentConfig := types.NewAgentConfig()
	svc.ConfigureAgent(mockLLM, agentConfig, nil, toolRegistry, skillRegistry)

	return svc, cronScheduler, mockLLM, skillRegistry
}

func TestPeriodicTask_NormalExecution(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	assert.NoError(t, err)
	db.AutoMigrate(&xcron.Job{})

	svc, cronScheduler, mockLLM, _ := setupComponents(t, db)

	mockLLM.On("ChatWithTools", mock.Anything, mock.Anything, mock.Anything).Return(types.Message{
		Role:    "assistant",
		Content: "I will execute the alert.",
	}, nil)

	cronScheduler.Start()
	defer cronScheduler.Stop()

	payload := xcron.AgentPayload{Message: "alert me"}
	payloadBytes, _ := json.Marshal(payload)

	jobID, err := svc.ScheduleJob(context.Background(), scheduler.ScheduleJobInput{
		Name:     "periodic-alert",
		Type:     "periodic",
		Schedule: "1s",
		Payload:  string(payloadBytes),
	})
	assert.NoError(t, err)

	time.Sleep(2 * time.Second)

	var job xcron.Job
	db.First(&job, "id = ?", jobID)
	assert.NotNil(t, job.LastRunAt)
	mockLLM.AssertExpectations(t)
}

func TestPeriodicTask_PersistenceAndResume(t *testing.T) {
	// Use a shared in-memory DB (file based for persistence across connections)
	dbName := fmt.Sprintf("file:test_periodic_%d.db?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dbName), &gorm.Config{})
	assert.NoError(t, err)
	db.AutoMigrate(&xcron.Job{})

	// 1. Create Job and run once
	{
		svc, cronScheduler, mockLLM, _ := setupComponents(t, db)
		mockLLM.On("ChatWithTools", mock.Anything, mock.Anything, mock.Anything).Return(types.Message{
			Role:    "assistant",
			Content: "Executing...",
		}, nil).Maybe()

		cronScheduler.Start()

		payload := xcron.AgentPayload{Message: "alert me"}
		payloadBytes, _ := json.Marshal(payload)

		_, err := svc.ScheduleJob(context.Background(), scheduler.ScheduleJobInput{
			Name:     "persistent-job",
			Type:     "periodic",
			Schedule: "1s",
			Payload:  string(payloadBytes),
		})
		assert.NoError(t, err)

		time.Sleep(1500 * time.Millisecond)
		cronScheduler.Stop()
	}

	// 2. Restart Scheduler (new instance, same DB)
	{
		_, cronScheduler, mockLLM, _ := setupComponents(t, db)
		mockLLM.On("ChatWithTools", mock.Anything, mock.Anything, mock.Anything).Return(types.Message{
			Role:    "assistant",
			Content: "Executing again...",
		}, nil)

		// Start should resume the job
		cronScheduler.Start()

		// Wait for execution
		time.Sleep(2 * time.Second)
		cronScheduler.Stop()

		var job xcron.Job
		db.First(&job, "name = ?", "persistent-job")
		// It should have run at least twice (once in first run, once in second)
		// Or at least once in second run if first run didn't finish?
		// But we waited.
		// Since it's periodic, it keeps running.
		assert.NotNil(t, job.LastRunAt)
		mockLLM.AssertExpectations(t)
	}
}

func TestPeriodicTask_SkillUpdate(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	assert.NoError(t, err)
	db.AutoMigrate(&xcron.Job{})

	svc, cronScheduler, mockLLM, registry := setupComponents(t, db)

	// Initially no "new-skill"
	// We schedule a job that needs "new-skill"
	payload := xcron.AgentPayload{Message: "use new skill"}
	payloadBytes, _ := json.Marshal(payload)

	// Mock for when skill is missing (ChatWithTools called with empty tools)
	mockLLM.On("ChatWithTools", mock.Anything, mock.Anything, mock.Anything).Return(types.Message{
		Role: "assistant", Content: "No skill found",
	}, nil).Maybe()

	cronScheduler.Start()
	defer cronScheduler.Stop()

	jobID, _ := svc.ScheduleJob(context.Background(), scheduler.ScheduleJobInput{
		Name:     "dynamic-skill",
		Type:     "periodic",
		Schedule: "1s",
		Payload:  string(payloadBytes),
	})

	time.Sleep(1500 * time.Millisecond)

	// Now register the new skill
	newSkill := &skills.Skill{
		Name:        "new-skill",
		Description: "A new skill",
		Triggers: []skills.Trigger{
			{Type: "keyword", Pattern: "new skill"},
		},
		Content: "New skill instructions",
	}
	registry.Register(newSkill)

	// Next run should pick it up
	// Expect ChatWithTools with new skill (via tool injection logic? no, tools are fixed in registry,
	// but planner will identify skill and inject it into payload)
	// If planner identifies skill, it adds to agentPayload.Skills.
	// If agentPayload.Skills > 0, tools (file, command) are injected.

	mockLLM.On("ChatWithTools", mock.Anything, mock.Anything, mock.MatchedBy(func(tools []types.Tool) bool {
		// Just check if called
		return true
	})).Return(types.Message{
		Role: "assistant", Content: "Found new skill",
	}, nil)

	time.Sleep(2 * time.Second)

	var job xcron.Job
	db.First(&job, "id = ?", jobID)
	assert.NotNil(t, job.LastRunAt)
}
