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

type TaskHandler func(ctx context.Context, job *Job) error

type Scheduler struct {
	cron               *cron.Cron
	store              JobStore
	entries            map[string]cron.EntryID
	mu                 sync.RWMutex
	handlers           map[TaskType]TaskHandler
	stopCh             chan struct{}
	locker             Locker
	onFailure          func(job *Job, err error)
	sem                chan struct{}
	serialSem          chan struct{}
	stuckCheckInterval time.Duration
	stuckTimeout       time.Duration
	metrics            MetricsRecorder
}

type Locker interface {
	Lock(ctx context.Context, key string, ttl time.Duration) (bool, error)
	Unlock(ctx context.Context, key string) error
}

func NewScheduler(store JobStore) *Scheduler {
	c := cron.New(cron.WithSeconds())

	serialSem := make(chan struct{}, 1)
	serialSem <- struct{}{}
	return &Scheduler{
		cron:               c,
		store:              store,
		entries:            make(map[string]cron.EntryID),
		handlers:           make(map[TaskType]TaskHandler),
		stopCh:             make(chan struct{}),
		serialSem:          serialSem,
		stuckCheckInterval: 10 * time.Minute,
		stuckTimeout:       2 * time.Hour,
	}
}

func (s *Scheduler) SetLocker(l Locker) {
	s.locker = l
}

func (s *Scheduler) SetFailureHandler(h func(job *Job, err error)) {
	s.onFailure = h
}

func (s *Scheduler) SetMaxConcurrent(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 {
		s.sem = nil
		return
	}
	s.sem = make(chan struct{}, n)
	for i := 0; i < n; i++ {
		s.sem <- struct{}{}
	}
}

func (s *Scheduler) SetStuckCheck(interval, timeout time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if interval > 0 {
		s.stuckCheckInterval = interval
	}
	if timeout > 0 {
		s.stuckTimeout = timeout
	}
}

func (s *Scheduler) SetMetricsRecorder(rec MetricsRecorder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics = rec
}

func (s *Scheduler) RegisterHandler(taskType TaskType, handler TaskHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[taskType] = handler
}

func (s *Scheduler) Start() {
	ctx := context.Background()
	jobs, err := s.store.GetPendingJobs(ctx)
	if err != nil {
		logger.Error("Failed to load pending jobs", slog.String("error", err.Error()))
		return
	}
	s.cron.Start()
	go s.runStuckCheck()
	for _, job := range jobs {
		if err := s.scheduleJob(job); err != nil {
			logger.Error("Failed to reschedule job", slog.String("error", err.Error()), slog.String("job_id", job.ID))
		}
	}
}

func (s *Scheduler) runStuckCheck() {
	s.mu.RLock()
	interval := s.stuckCheckInterval
	timeout := s.stuckTimeout
	s.mu.RUnlock()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.mu.RLock()
			timeout = s.stuckTimeout
			s.mu.RUnlock()
			ctx := context.Background()
			if err := s.store.ResetStuckJobs(ctx, timeout); err != nil {
				logger.Error("Failed to reset stuck jobs", slog.String("error", err.Error()))
			}
		}
	}
}

func (s *Scheduler) Stop() {
	s.cron.Stop()
	close(s.stopCh)
}

func (s *Scheduler) AddJob(ctx context.Context, name string, jobType JobType, schedule string, taskType TaskType, payload interface{}, sessionID string, maxRetries int) (string, error) {
	return s.AddJobWithOptions(ctx, name, jobType, schedule, taskType, payload, sessionID, maxRetries, "")
}

func (s *Scheduler) AddJobWithOptions(ctx context.Context, name string, jobType JobType, schedule string, taskType TaskType, payload interface{}, sessionID string, maxRetries int, executionMode string) (string, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	job := &Job{
		ID:              uuid.New().String(),
		Name:            name,
		Type:            jobType,
		SessionID:       sessionID,
		Schedule:        schedule,
		Status:          JobStatusPending,
		Payload:         string(payloadBytes),
		TaskType:        taskType,
		MaxRetries:      maxRetries,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
		ExecutionMode:   executionMode,
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

func (s *Scheduler) ListJobsWithOptions(ctx context.Context, opts ListOptions) ([]*Job, int64, error) {
	return s.store.ListWithOptions(ctx, opts)
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
		s.mu.RLock()
		sem := s.sem
		serialSem := s.serialSem
		mode := job.ExecutionMode
		s.mu.RUnlock()
		if mode == "serial" && serialSem != nil {
			<-serialSem
			defer func() { serialSem <- struct{}{} }()
		}
		if sem != nil {
			<-sem
			defer func() { sem <- struct{}{} }()
		}

		startTime := time.Now()
		ctx := context.Background()

		currentJob, err := s.store.Get(ctx, job.ID)
		if err != nil {
			logger.Error("Failed to load job for execution", slog.String("error", err.Error()), slog.String("job_id", job.ID))
			return
		}
		if currentJob == nil {
			return
		}

		if currentJob.Status == JobStatusStopped || currentJob.Status == JobStatusCompleted || currentJob.Status == JobStatusFailed {
			s.mu.Lock()
			if entryID, ok := s.entries[currentJob.ID]; ok {
				s.cron.Remove(entryID)
				delete(s.entries, currentJob.ID)
			}
			s.mu.Unlock()
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

		s.store.UpdateStatus(ctx, currentJob.ID, JobStatusRunning, nil, time.Time{}, 0, "")
		s.mu.RLock()
		rec := s.metrics
		s.mu.RUnlock()
		if rec != nil {
			rec.JobStarted(currentJob.ID, string(currentJob.TaskType))
		}

		handler, ok := s.handlers[currentJob.TaskType]
		if !ok {
			if rec != nil {
				rec.JobFailed(currentJob.ID, string(currentJob.TaskType))
			}
			logger.Error("No handler registered for task type", slog.String("type", string(currentJob.TaskType)))
			s.handleFailure(ctx, currentJob, fmt.Errorf("no handler for type %s", currentJob.TaskType), time.Since(startTime))
			return
		}

		// Execute with retry logic
		logger.Info("🔄 Executing job", slog.String("job_id", currentJob.ID), slog.String("type", string(currentJob.Type)), slog.String("task_type", string(currentJob.TaskType)))
		err = s.executeWithRetry(ctx, handler, currentJob)

		now := time.Now()
		duration := time.Since(startTime)

		if err != nil {
			if rec != nil {
				rec.JobFailed(currentJob.ID, string(currentJob.TaskType))
			}
			s.handleFailure(ctx, currentJob, err, duration)
			return
		}
		if rec != nil {
			rec.JobCompleted(currentJob.ID, string(currentJob.TaskType), duration)
		}
		nextRun := time.Time{}
		status := JobStatusCompleted
		if currentJob.Type != JobTypeOneShot {
			status = JobStatusPending
			s.mu.RLock()
			entryID, ok := s.entries[currentJob.ID]
			s.mu.RUnlock()
			if ok {
				nextRun = s.cron.Entry(entryID).Next
			}
		} else {
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

func (s *Scheduler) executeWithRetry(ctx context.Context, handler TaskHandler, job *Job) error {
	var lastErr error
	for i := 0; i <= job.MaxRetries; i++ {
		if i > 0 {
			backoff := 100 * time.Millisecond * time.Duration(1<<uint(i-1))
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			time.Sleep(backoff)
		}
		err := handler(ctx, job)
		if err == nil {
			return nil
		}
		lastErr = err
		logger.Warn("Job execution failed, retrying", slog.String("error", err.Error()), slog.String("job_id", job.ID), slog.Int("retry", i))
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
