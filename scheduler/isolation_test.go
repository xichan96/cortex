package scheduler_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"

	"github.com/xichan96/cortex/pkg/xcron"
	"github.com/xichan96/cortex/scheduler"
)

// newIsolatedService builds a Service backed by its own private database.
func newIsolatedService(t *testing.T) (*scheduler.Service, *xcron.Scheduler, *gorm.DB) {
	t.Helper()
	db := newTestDB(t)
	store := xcron.NewGormJobStore(db)
	sched := xcron.NewScheduler(store)
	return scheduler.NewService(sched), sched, db
}

// TestNewTestDB_IsolatesDatabases is the direct regression guard for the old
// `file::memory:?cache=shared` DSN: two test databases must not see each
// other's rows.
func TestNewTestDB_IsolatesDatabases(t *testing.T) {
	db1 := newTestDB(t)
	db2 := newTestDB(t)

	job := &xcron.Job{
		ID:        "only-in-db1",
		Name:      "isolated",
		Type:      xcron.JobTypeOneShot,
		Status:    xcron.JobStatusPending,
		TaskType:  xcron.TaskTypeAgent,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := db1.Create(job).Error; err != nil {
		t.Fatalf("create job in db1: %v", err)
	}

	var got xcron.Job
	err := db2.First(&got, "id = ?", job.ID).Error
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "job from db1 must not be visible in db2")
}

// TestService_InstancesDoNotShareJobs reproduces the original flake shape:
// a job pending in one service's DB must never be picked up by another
// service/scheduler instance started against its own DB.
func TestService_InstancesDoNotShareJobs(t *testing.T) {
	svc1, sched1, db1 := newIsolatedService(t)
	svc2, sched2, db2 := newIsolatedService(t)

	var mu sync.Mutex
	runs := map[string][]string{}
	record := func(name string) scheduler.AgentTaskExecutor {
		return func(ctx context.Context, job *xcron.Job, instruction string, payload xcron.AgentPayload) error {
			mu.Lock()
			runs[name] = append(runs[name], instruction)
			mu.Unlock()
			return nil
		}
	}
	svc1.SetAgentTaskExecutor(record("svc1"))
	svc2.SetAgentTaskExecutor(record("svc2"))

	// Create svc1's job while neither scheduler is running, so svc2.Start
	// would resume it if both services shared a database.
	id1, err := svc1.ScheduleJob(context.Background(), scheduler.ScheduleJobInput{
		Name: "job-one", Type: "oneshot", Schedule: "10ms", Payload: "only-for-svc1",
	})
	if err != nil {
		t.Fatalf("schedule job: %v", err)
	}

	sched1.Start()
	defer sched1.Stop()
	sched2.Start()
	defer sched2.Stop()

	id2, err := svc2.ScheduleJob(context.Background(), scheduler.ScheduleJobInput{
		Name: "job-two", Type: "oneshot", Schedule: "10ms", Payload: "only-for-svc2",
	})
	if err != nil {
		t.Fatalf("schedule job: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n1, n2 := len(runs["svc1"]), len(runs["svc2"])
		mu.Unlock()
		if n1 >= 1 && n2 >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	got1 := append([]string(nil), runs["svc1"]...)
	got2 := append([]string(nil), runs["svc2"]...)
	mu.Unlock()

	assert.Equal(t, []string{"only-for-svc1"}, got1, "svc1 executor must only run its own job")
	assert.Equal(t, []string{"only-for-svc2"}, got2, "svc2 executor must not run svc1's job")

	// Each database holds exactly its own job.
	assert.NoError(t, db1.First(&xcron.Job{}, "id = ?", id1).Error)
	assert.NoError(t, db2.First(&xcron.Job{}, "id = ?", id2).Error)

	var leaked int64
	if err := db1.Model(&xcron.Job{}).Where("id = ?", id2).Count(&leaked).Error; err != nil {
		t.Fatalf("count db1: %v", err)
	}
	assert.Zero(t, leaked, "svc2's job must not be stored in svc1's database")
	if err := db2.Model(&xcron.Job{}).Where("id = ?", id1).Count(&leaked).Error; err != nil {
		t.Fatalf("count db2: %v", err)
	}
	assert.Zero(t, leaked, "svc1's job must not be stored in svc2's database")
}
