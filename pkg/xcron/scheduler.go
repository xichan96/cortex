package xcron

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"github.com/xichan96/cortex/pkg/logger"
)

type TaskHandler func(ctx context.Context, payload string) error

type Scheduler struct {
	cron      *cron.Cron
	store     JobStore
	entries   map[string]cron.EntryID
	mu        sync.RWMutex
	handlers  map[TaskType]TaskHandler
	stopCh    chan struct{}
	locker    Locker
	onFailure func(job *Job, err error)
}

type Locker interface {
	Lock(ctx context.Context, key string, ttl time.Duration) (bool, error)
	Unlock(ctx context.Context, key string) error
}

func NewScheduler(store JobStore) *Scheduler {
	// Use WithSeconds for higher precision (though still second-level for cron)
	// For millisecond precision OneShot, we might need a separate mechanism or accept second precision for cron engine,
	// but the requirement says "Precise to millisecond".
	// Standard cron is 1s.
	// However, we can use a custom runner or just accept 1s precision for cron/periodic,
	// and use time.AfterFunc for immediate OneShot if it's very short?
	// But for persistence, we rely on the loop.
	// Let's stick to cron with seconds for now, and see if we can optimize.

	c := cron.New(cron.WithSeconds())

	return &Scheduler{
		cron:     c,
		store:    store,
		entries:  make(map[string]cron.EntryID),
		handlers: make(map[TaskType]TaskHandler),
		stopCh:   make(chan struct{}),
	}
}

func (s *Scheduler) SetLocker(l Locker) {
	s.locker = l
}

func (s *Scheduler) SetFailureHandler(h func(job *Job, err error)) {
	s.onFailure = h
}

func (s *Scheduler) RegisterHandler(taskType TaskType, handler TaskHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[taskType] = handler
}

func (s *Scheduler) Start() {
	s.cron.Start()
	// Load pending jobs from store
	ctx := context.Background()
	jobs, err := s.store.GetPendingJobs(ctx)
	if err != nil {
		logger.Error("Failed to load pending jobs", slog.String("error", err.Error()))
		return
	}

	go s.checkStuckJobs()

	for _, job := range jobs {
		if err := s.scheduleJob(job); err != nil {
			logger.Error("Failed to reschedule job", slog.String("error", err.Error()), slog.String("job_id", job.ID))
		}
	}
}

func (s *Scheduler) checkStuckJobs() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			ctx := context.Background()
			if err := s.store.ResetStuckJobs(ctx, 2*time.Hour); err != nil {
				logger.Error("Failed to reset stuck jobs", slog.String("error", err.Error()))
			}
		}
	}
}

func (s *Scheduler) Stop() {
	s.cron.Stop()
	close(s.stopCh)
}

func (s *Scheduler) AddJob(ctx context.Context, name string, jobType JobType, schedule string, taskType TaskType, payload interface{}, maxRetries int) (string, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	job := &Job{
		ID:         uuid.New().String(),
		Name:       name,
		Type:       jobType,
		Schedule:   schedule,
		Status:     JobStatusPending,
		Payload:    string(payloadBytes),
		TaskType:   taskType,
		MaxRetries: maxRetries,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	// Pre-calculate NextRunAt for OneShot to ensure consistency across restarts
	if job.Type == JobTypeOneShot {
		if d, err := time.ParseDuration(schedule); err == nil {
			job.NextRunAt = time.Now().Add(d)
		} else {
			return "", fmt.Errorf("invalid one-shot schedule: %w", err)
		}
	}

	if err := s.store.Save(ctx, job); err != nil {
		return "", err
	}

	if err := s.scheduleJob(job); err != nil {
		return "", err
	}

	return job.ID, nil
}

func (s *Scheduler) RemoveJob(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entryID, ok := s.entries[id]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, id)
	}

	// Update status to stopped/removed in DB
	// Or physically delete? Requirement says "Remove" -> "Force stop or thoroughly delete".
	// Let's assume Delete removes it.
	return s.store.Delete(ctx, id)
}

func (s *Scheduler) StopJob(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entryID, ok := s.entries[id]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, id)
	}

	return s.store.UpdateStatus(ctx, id, JobStatusStopped, nil, time.Time{}, 0, "")
}

func (s *Scheduler) ListJobs(ctx context.Context, offset, limit int) ([]*Job, int64, error) {
	return s.store.List(ctx, offset, limit)
}

func (s *Scheduler) scheduleJob(job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// If already scheduled, remove first (re-schedule)
	if entryID, ok := s.entries[job.ID]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, job.ID)
	}

	var entryID cron.EntryID
	var err error

	wrapper := s.createJobWrapper(job)

	switch job.Type {
	case JobTypeCron:
		entryID, err = s.cron.AddFunc(job.Schedule, wrapper)
	case JobTypePeriodic:
		// Schedule is duration string e.g. "10s"
		// Convert to @every
		// If it's a simple duration, AddFunc supports "@every X"
		// If user passed "10s", we prepend @every.
		// If user passed "@every 10s", we use it as is.
		spec := job.Schedule
		if len(spec) > 0 && spec[0] != '@' {
			spec = "@every " + spec
		}
		entryID, err = s.cron.AddFunc(spec, wrapper)
	case JobTypeOneShot:
		// OneShot needs special handling.
		var targetTime time.Time
		if !job.NextRunAt.IsZero() {
			targetTime = job.NextRunAt
		} else {
			// Fallback: parse schedule (for old jobs or immediate fallback)
			if d, err := time.ParseDuration(job.Schedule); err == nil {
				targetTime = time.Now().Add(d)
			} else {
				return fmt.Errorf("invalid one-shot schedule: %w", err)
			}
		}

		// If the target time is in the past, execute immediately directly without cron engine
		if targetTime.Before(time.Now()) {
			go wrapper()
			return nil
		}

		entryID = s.cron.Schedule(&OneShotSchedule{TargetTime: targetTime}, cron.FuncJob(wrapper))
	}

	if err != nil {
		return err
	}

	s.entries[job.ID] = entryID
	job.CronEntryID = int(entryID)
	return nil
}

func (s *Scheduler) createJobWrapper(job *Job) func() {
	return func() {
		startTime := time.Now()
		ctx := context.Background()

		// Reload job to get latest status/retries
		currentJob, err := s.store.Get(ctx, job.ID)
		if err != nil {
			logger.Error("Failed to load job for execution", slog.String("error", err.Error()), slog.String("job_id", job.ID))
			return
		}

		if currentJob.Status == JobStatusStopped || currentJob.Status == JobStatusCompleted {
			return
		}

		// Distributed lock
		if s.locker != nil {
			locked, err := s.locker.Lock(ctx, "job_lock:"+currentJob.ID, 1*time.Minute)
			if err != nil || !locked {
				logger.Info("Failed to acquire lock, skipping", slog.String("job_id", currentJob.ID))
				return
			}
			defer s.locker.Unlock(ctx, "job_lock:"+currentJob.ID)
		}

		// Update status to Running
		s.store.UpdateStatus(ctx, currentJob.ID, JobStatusRunning, nil, time.Time{}, 0, "")

		handler, ok := s.handlers[currentJob.TaskType]
		if !ok {
			logger.Error("No handler registered for task type", slog.String("type", string(currentJob.TaskType)))
			s.handleFailure(ctx, currentJob, fmt.Errorf("no handler for type %s", currentJob.TaskType), time.Since(startTime))
			return
		}

		// Execute with retry logic
		err = s.executeWithRetry(ctx, handler, currentJob)

		now := time.Now()
		duration := time.Since(startTime)

		if err != nil {
			s.handleFailure(ctx, currentJob, err, duration)
		} else {
			// Success
			nextRun := time.Time{} // For one-shot, no next run
			status := JobStatusCompleted

			if currentJob.Type != JobTypeOneShot {
				status = JobStatusPending // Back to pending for next run
				// Calculate next run? Cron does it for us, but for DB visibility we might want to know.
				// We can ask the Cron entry?
				s.mu.RLock()
				entryID, ok := s.entries[currentJob.ID]
				s.mu.RUnlock()

				if ok {
					entry := s.cron.Entry(entryID)
					nextRun = entry.Next
				}
			} else {
				// OneShot done, remove from scheduler
				s.mu.Lock()
				if entryID, ok := s.entries[currentJob.ID]; ok {
					s.cron.Remove(entryID)
					delete(s.entries, currentJob.ID)
				}
				s.mu.Unlock()
			}

			s.store.UpdateStatus(ctx, currentJob.ID, status, &now, nextRun, duration, "")
		}
	}
}

func (s *Scheduler) executeWithRetry(ctx context.Context, handler TaskHandler, job *Job) error {
	var lastErr error
	for i := 0; i <= job.MaxRetries; i++ {
		if i > 0 {
			time.Sleep(time.Duration(i) * 100 * time.Millisecond) // Simple backoff
		}
		if err := handler(ctx, job.Payload); err == nil {
			return nil
		} else {
			lastErr = err
			logger.Warn("Job execution failed, retrying", slog.String("error", err.Error()), slog.String("job_id", job.ID), slog.Int("retry", i))
		}
	}
	return lastErr
}

func (s *Scheduler) handleFailure(ctx context.Context, job *Job, err error, duration time.Duration) {
	logger.Error("Job failed after retries", slog.String("error", err.Error()), slog.String("job_id", job.ID))
	s.store.UpdateStatus(ctx, job.ID, JobStatusFailed, nil, time.Time{}, duration, err.Error())
	if s.onFailure != nil {
		s.onFailure(job, err)
	}
}

// Custom Schedule for OneShot
type OneShotSchedule struct {
	TargetTime time.Time
}

func (s *OneShotSchedule) Next(t time.Time) time.Time {
	if t.After(s.TargetTime) {
		return time.Time{} // Zero time means stop
	}
	return s.TargetTime
}
