package xcron

import (
	"context"
	"time"

	"gorm.io/gorm"
)

type GormJobStore struct {
	db *gorm.DB
}

func NewGormJobStore(db *gorm.DB) *GormJobStore {
	return &GormJobStore{db: db}
}

func (s *GormJobStore) Save(ctx context.Context, job *Job) error {
	return s.db.WithContext(ctx).Save(job).Error
}

func (s *GormJobStore) Get(ctx context.Context, id string) (*Job, error) {
	var job Job
	err := s.db.WithContext(ctx).First(&job, "id = ?", id).Error
	return &job, err
}

func (s *GormJobStore) Delete(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Delete(&Job{}, "id = ?", id).Error
}

func (s *GormJobStore) List(ctx context.Context, offset, limit int) ([]*Job, int64, error) {
	return s.ListWithOptions(ctx, ListOptions{Offset: offset, Limit: limit})
}

func (s *GormJobStore) ListWithOptions(ctx context.Context, opts ListOptions) ([]*Job, int64, error) {
	var jobs []*Job
	q := s.db.WithContext(ctx).Model(&Job{})
	if len(opts.Status) > 0 {
		q = q.Where("status IN ?", opts.Status)
	}
	if len(opts.Type) > 0 {
		q = q.Where("type IN ?", opts.Type)
	}
	if opts.SessionID != "" {
		q = q.Where("session_id = ?", opts.SessionID)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return nil, 0, err
	}
	order := "created_at desc"
	if opts.OrderBy == "next_run_at" {
		order = "next_run_at asc"
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	err := q.Offset(opts.Offset).Limit(limit).Order(order).Find(&jobs).Error
	return jobs, count, err
}

func (s *GormJobStore) GetPendingJobs(ctx context.Context) ([]*Job, error) {
	var jobs []*Job
	// Find jobs that are pending or running (in case of crash) or periodic/cron that need to be re-scheduled
	// Actually, for Cron/Periodic, we just need to load all active ones.
	// For OneShot, we need those that are pending.
	// We can just load all non-completed/stopped/failed jobs.
	err := s.db.WithContext(ctx).
		Where("status NOT IN ?", []JobStatus{JobStatusCompleted, JobStatusStopped, JobStatusFailed}).
		Find(&jobs).Error
	return jobs, err
}

func (s *GormJobStore) UpdateStatus(ctx context.Context, id string, status JobStatus, lastRun *time.Time, nextRun time.Time, lastDuration time.Duration, lastError string) error {
	updates := map[string]interface{}{
		"status":      status,
		"updated_at":  time.Now(),
		"next_run_at": nextRun,
	}
	if lastRun != nil {
		updates["last_run_at"] = lastRun
	}
	if lastDuration > 0 {
		updates["last_duration"] = lastDuration
	}
	if lastError != "" {
		updates["last_error"] = lastError
	}
	if status == JobStatusRunning {
		now := time.Now()
		updates["running_at"] = &now
	} else {
		updates["running_at"] = nil
	}
	return s.db.WithContext(ctx).Model(&Job{}).Where("id = ?", id).Updates(updates).Error
}

func (s *GormJobStore) ResetStuckJobs(ctx context.Context, timeout time.Duration) error {
	threshold := time.Now().Add(-timeout)
	return s.db.WithContext(ctx).Model(&Job{}).
		Where("status = ? AND running_at < ?", JobStatusRunning, threshold).
		Updates(map[string]interface{}{
			"status":     JobStatusFailed,
			"running_at": nil,
			"last_error": "Job execution stuck/timed out",
			"updated_at": time.Now(),
		}).Error
}
