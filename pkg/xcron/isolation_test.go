package xcron

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// TestSetupTestDB_IsolatesDatabases guards the per-test DB helper against a
// regression to a process-wide shared database (e.g. `file::memory:?cache=shared`).
func TestSetupTestDB_IsolatesDatabases(t *testing.T) {
	db1 := setupTestDB(t)
	db2 := setupTestDB(t)

	job := &Job{
		ID:        "only-in-db1",
		Name:      "isolated",
		Type:      JobTypeOneShot,
		Status:    JobStatusPending,
		TaskType:  TaskTypeGreet,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := NewGormJobStore(db1).Save(context.Background(), job); err != nil {
		t.Fatalf("save job in db1: %v", err)
	}

	_, err := NewGormJobStore(db2).Get(context.Background(), job.ID)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "job from db1 must not be visible in db2")
}

// TestSchedulers_DoNotShareJobState runs two schedulers concurrently against
// separate databases. Each handler must only ever see jobs from its own store;
// this is the regression guard for cross-test / cross-instance execution.
func TestSchedulers_DoNotShareJobState(t *testing.T) {
	sched1 := NewScheduler(NewGormJobStore(setupTestDB(t)))
	sched2 := NewScheduler(NewGormJobStore(setupTestDB(t)))

	var mu sync.Mutex
	seen := map[string][]string{}
	record := func(name string) TaskHandler {
		return func(ctx context.Context, job *Job) error {
			mu.Lock()
			seen[name] = append(seen[name], job.ID)
			mu.Unlock()
			return nil
		}
	}
	sched1.RegisterHandler(TaskTypeGreet, record("sched1"))
	sched2.RegisterHandler(TaskTypeGreet, record("sched2"))

	// Create sched1's pending job before either scheduler starts, so sched2.Start
	// would resume and execute it if the stores were shared.
	id1, err := sched1.AddJob(context.Background(), "one", JobTypeOneShot, "10ms", TaskTypeGreet, nil, "", 0)
	if err != nil {
		t.Fatalf("add job: %v", err)
	}

	sched1.Start()
	defer sched1.Stop()
	sched2.Start()
	defer sched2.Stop()

	id2, err := sched2.AddJob(context.Background(), "two", JobTypeOneShot, "10ms", TaskTypeGreet, nil, "", 0)
	if err != nil {
		t.Fatalf("add job: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n1, n2 := len(seen["sched1"]), len(seen["sched2"])
		mu.Unlock()
		if n1 >= 1 && n2 >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	got1 := append([]string(nil), seen["sched1"]...)
	got2 := append([]string(nil), seen["sched2"]...)
	mu.Unlock()

	assert.Equal(t, []string{id1}, got1, "sched1 must only execute its own job")
	assert.Equal(t, []string{id2}, got2, "sched2 must only execute its own job")
}
