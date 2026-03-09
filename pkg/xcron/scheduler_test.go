package xcron

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestDB() *gorm.DB {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		panic(err)
	}
	db.AutoMigrate(&Job{})
	db.Exec("DELETE FROM jobs") // Clear table
	return db
}

func TestScheduler_OneShot(t *testing.T) {
	db := setupTestDB()
	store := NewGormJobStore(db)
	scheduler := NewScheduler(store)

	var wg sync.WaitGroup
	wg.Add(1)

	scheduler.RegisterHandler(TaskTypeGreet, func(ctx context.Context, job *Job) error {
		wg.Done()
		return nil
	})

	scheduler.Start()
	defer scheduler.Stop()

	id, err := scheduler.AddJob(context.Background(), "test-oneshot", JobTypeOneShot, "100ms", TaskTypeGreet, nil, "", 0)
	assert.NoError(t, err)

	// Wait for execution
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
		time.Sleep(100 * time.Millisecond) // Allow UpdateStatus to finish
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for job execution")
	}

	// Verify job status
	job, err := store.Get(context.Background(), id)
	assert.NoError(t, err)
	assert.Equal(t, JobStatusCompleted, job.Status)
}

func TestScheduler_Retry(t *testing.T) {
	db := setupTestDB()
	store := NewGormJobStore(db)
	scheduler := NewScheduler(store)

	var wg sync.WaitGroup
	wg.Add(1) // Should eventually succeed

	attempts := 0
	scheduler.RegisterHandler(TaskTypeFunction, func(ctx context.Context, job *Job) error {
		attempts++
		if attempts <= 2 {
			return errors.New("fail")
		}
		wg.Done()
		return nil
	})

	scheduler.Start()
	defer scheduler.Stop()

	// Max retries 2, so it should run 3 times total (1 initial + 2 retries)
	id, err := scheduler.AddJob(context.Background(), "test-retry", JobTypeOneShot, "50ms", TaskTypeFunction, nil, "", 2)
	assert.NoError(t, err)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
		time.Sleep(100 * time.Millisecond) // Allow UpdateStatus to finish
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for job execution")
	}

	assert.Equal(t, 3, attempts)
	job, err := store.Get(context.Background(), id)
	assert.NoError(t, err)
	assert.Equal(t, JobStatusCompleted, job.Status)
}

func TestScheduler_NoHandler(t *testing.T) {
	db := setupTestDB()
	store := NewGormJobStore(db)
	scheduler := NewScheduler(store)
	scheduler.Start()
	defer scheduler.Stop()

	// Add job with unknown task type
	id, err := scheduler.AddJob(context.Background(), "no-handler", JobTypeOneShot, "10ms", "unknown_type", nil, "", 0)
	assert.NoError(t, err)

	// Wait enough time for it to fail
	time.Sleep(200 * time.Millisecond)

	job, err := store.Get(context.Background(), id)
	assert.NoError(t, err)
	assert.Equal(t, JobStatusFailed, job.Status)
}

func TestScheduler_Persistence(t *testing.T) {
	db := setupTestDB()
	store := NewGormJobStore(db)

	// Create a job but don't start scheduler yet
	job := &Job{
		ID:        "persist-test",
		Name:      "persist",
		Type:      JobTypeOneShot,
		Schedule:  "100ms",
		Status:    JobStatusPending,
		TaskType:  TaskTypeReply,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	store.Save(context.Background(), job)

	scheduler := NewScheduler(store)
	var wg sync.WaitGroup
	wg.Add(1)

	scheduler.RegisterHandler(TaskTypeReply, func(ctx context.Context, job *Job) error {
		wg.Done()
		return nil
	})

	scheduler.Start()
	defer scheduler.Stop()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for job execution")
	}
}

func TestScheduler_Management(t *testing.T) {
	db := setupTestDB()
	store := NewGormJobStore(db)
	scheduler := NewScheduler(store)
	scheduler.Start()
	defer scheduler.Stop()

	// Add jobs
	id1, _ := scheduler.AddJob(context.Background(), "j1", JobTypePeriodic, "1h", TaskTypeGreet, nil, "", 0)
	id2, _ := scheduler.AddJob(context.Background(), "j2", JobTypePeriodic, "1h", TaskTypeGreet, nil, "", 0)

	// List
	jobs, count, err := scheduler.ListJobs(context.Background(), 0, 10)
	assert.NoError(t, err)
	assert.Equal(t, int64(2), count)
	assert.Len(t, jobs, 2)

	// Stop
	err = scheduler.StopJob(context.Background(), id1)
	assert.NoError(t, err)
	j1, _ := store.Get(context.Background(), id1)
	assert.Equal(t, JobStatusStopped, j1.Status)

	// Remove
	err = scheduler.RemoveJob(context.Background(), id2)
	assert.NoError(t, err)
	_, err = store.Get(context.Background(), id2)
	assert.Error(t, err) // Should be not found or deleted
}

func TestScheduler_PeriodicAndCron(t *testing.T) {
	db := setupTestDB()
	store := NewGormJobStore(db)
	scheduler := NewScheduler(store)

	var counter int32
	scheduler.RegisterHandler(TaskTypeWorkflow, func(ctx context.Context, job *Job) error {
		atomic.AddInt32(&counter, 1)
		return nil
	})

	scheduler.Start()
	defer scheduler.Stop()

	scheduler.AddJob(context.Background(), "periodic", JobTypePeriodic, "1s", TaskTypeWorkflow, nil, "", 0)

	time.Sleep(1500 * time.Millisecond)
	assert.GreaterOrEqual(t, atomic.LoadInt32(&counter), int32(1))
}

type MockLocker struct {
	locked bool
}

func (m *MockLocker) Lock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if m.locked {
		return false, nil
	}
	m.locked = true
	return true, nil
}

func (m *MockLocker) Unlock(ctx context.Context, key string) error {
	m.locked = false
	return nil
}

func TestScheduler_Locking(t *testing.T) {
	db := setupTestDB()
	store := NewGormJobStore(db)
	scheduler := NewScheduler(store)
	locker := &MockLocker{}
	scheduler.SetLocker(locker)

	var wg sync.WaitGroup
	wg.Add(1)

	scheduler.RegisterHandler(TaskTypeGreet, func(ctx context.Context, job *Job) error {
		wg.Done()
		return nil
	})

	scheduler.Start()
	defer scheduler.Stop()

	scheduler.AddJob(context.Background(), "locked-job", JobTypeOneShot, "50ms", TaskTypeGreet, nil, "", 0)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
		time.Sleep(100 * time.Millisecond)
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout")
	}

	// Should be unlocked after execution
	assert.False(t, locker.locked)
}

func TestScheduler_FailureCallback(t *testing.T) {
	db := setupTestDB()
	store := NewGormJobStore(db)
	scheduler := NewScheduler(store)

	var failedJobID string
	var wg sync.WaitGroup
	wg.Add(1)

	scheduler.SetFailureHandler(func(job *Job, err error) {
		failedJobID = job.ID
		wg.Done()
	})

	scheduler.RegisterHandler(TaskTypeGreet, func(ctx context.Context, job *Job) error {
		return errors.New("permanent fail")
	})

	scheduler.Start()
	defer scheduler.Stop()

	id, _ := scheduler.AddJob(context.Background(), "fail-job", JobTypeOneShot, "50ms", TaskTypeGreet, nil, "", 0)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		assert.Equal(t, id, failedJobID)
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for failure callback")
	}
}

func TestScheduler_Errors(t *testing.T) {
	db := setupTestDB()
	store := NewGormJobStore(db)
	scheduler := NewScheduler(store)

	// Invalid schedule
	_, err := scheduler.AddJob(context.Background(), "invalid", JobTypeOneShot, "not-a-duration", TaskTypeGreet, nil, "", 0)
	assert.Error(t, err)

	// Invalid payload (channel cannot be marshaled)
	_, err = scheduler.AddJob(context.Background(), "invalid-json", JobTypeOneShot, "1s", TaskTypeGreet, make(chan int), "", 0)
	assert.Error(t, err)
}

func TestScheduler_Stats(t *testing.T) {
	db := setupTestDB()
	store := NewGormJobStore(db)
	scheduler := NewScheduler(store)
	scheduler.Start()
	defer scheduler.Stop()

	var wg sync.WaitGroup
	wg.Add(1)

	scheduler.RegisterHandler(TaskTypeGreet, func(ctx context.Context, job *Job) error {
		time.Sleep(50 * time.Millisecond) // Simulate work
		wg.Done()
		return nil
	})

	id, err := scheduler.AddJob(context.Background(), "stats", JobTypeOneShot, "10ms", TaskTypeGreet, nil, "", 0)
	assert.NoError(t, err)

	wg.Wait()
	time.Sleep(100 * time.Millisecond) // Wait for update

	job, err := store.Get(context.Background(), id)
	assert.NoError(t, err)
	assert.Equal(t, JobStatusCompleted, job.Status)
	assert.True(t, job.LastDuration >= 50*time.Millisecond, "Duration should be recorded")
	assert.NotNil(t, job.LastRunAt)
}

func TestScheduler_StuckJobs(t *testing.T) {
	db := setupTestDB()
	store := NewGormJobStore(db)

	// Create stuck job
	stuckTime := time.Now().Add(-3 * time.Hour)
	job := &Job{
		ID:        "stuck-job",
		Name:      "stuck",
		Type:      JobTypePeriodic,
		Schedule:  "@every 1h",
		Status:    JobStatusRunning,
		RunningAt: &stuckTime,
		TaskType:  TaskTypeGreet,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	store.Save(context.Background(), job)

	err := store.ResetStuckJobs(context.Background(), 2*time.Hour)
	assert.NoError(t, err)

	updatedJob, err := store.Get(context.Background(), "stuck-job")
	assert.NoError(t, err)
	assert.Equal(t, JobStatusFailed, updatedJob.Status)
	assert.Nil(t, updatedJob.RunningAt)
	assert.Contains(t, updatedJob.LastError, "stuck")
}

func TestScheduler_OneShot_Restart_Delay_Fix(t *testing.T) {
	db := setupTestDB()
	store := NewGormJobStore(db)
	scheduler := NewScheduler(store)

	var wg sync.WaitGroup
	wg.Add(1)

	// Capture the execution time
	var execTime time.Time
	scheduler.RegisterHandler(TaskTypeGreet, func(ctx context.Context, job *Job) error {
		execTime = time.Now()
		wg.Done()
		return nil
	})

	scheduler.Start()

	// 1. Add a OneShot job with 2s delay
	// We expect it to run at T0 + 2s
	_, err := scheduler.AddJob(context.Background(), "test-restart", JobTypeOneShot, "2s", TaskTypeGreet, nil, "", 0)
	assert.NoError(t, err)

	// 2. Stop scheduler immediately
	scheduler.Stop()
	t.Log("Scheduler stopped")

	// 3. Wait for 3 seconds. The job should have expired by now.
	time.Sleep(3 * time.Second)

	// 4. Restart scheduler
	// If logic is flawed (re-calculating delay), it might schedule for Now + 2s.
	// If logic is fixed, it should see it's past due and execute immediately.
	scheduler = NewScheduler(store)
	scheduler.RegisterHandler(TaskTypeGreet, func(ctx context.Context, job *Job) error {
		execTime = time.Now()
		wg.Done()
		return nil
	})

	t.Log("Scheduler restarting")
	scheduler.Start()
	defer scheduler.Stop()

	// 5. Wait for execution
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success: Job executed immediately (or fast enough)
		t.Logf("Job executed at %v", execTime)
	case <-time.After(1500 * time.Millisecond):
		// Failure: If the bug exists, it will reschedule for +2s, so 1.5s is not enough.
		t.Fatal("Timeout waiting for job execution - likely rescheduled with full delay instead of immediate execution")
	}
}
