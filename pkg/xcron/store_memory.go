package xcron

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryJobStore implements JobStore using in-memory map
type MemoryJobStore struct {
	jobs map[string]*Job
	mu   sync.RWMutex
}

// NewMemoryJobStore creates a new in-memory job store
func NewMemoryJobStore() *MemoryJobStore {
	return &MemoryJobStore{
		jobs: make(map[string]*Job),
	}
}

func (s *MemoryJobStore) Save(ctx context.Context, job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	// Create a copy to store
	storedJob := *job
	s.jobs[job.ID] = &storedJob
	return nil
}

func (s *MemoryJobStore) Get(ctx context.Context, id string) (*Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	job, ok := s.jobs[id]
	if !ok {
		return nil, fmt.Errorf("job not found: %s", id)
	}
	
	// Return a copy
	retJob := *job
	return &retJob, nil
}

func (s *MemoryJobStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	if _, ok := s.jobs[id]; !ok {
		return fmt.Errorf("job not found: %s", id)
	}
	
	delete(s.jobs, id)
	return nil
}

func (s *MemoryJobStore) List(ctx context.Context, offset, limit int) ([]*Job, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	
	var jobs []*Job
	for _, job := range s.jobs {
		// Return copies
		j := *job
		jobs = append(jobs, &j)
	}
	
	// Sort by CreatedAt desc
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	
	count := int64(len(jobs))
	if limit <= 0 {
		limit = 50
	}
	if offset >= len(jobs) {
		return []*Job{}, count, nil
	}
	end := offset + limit
	if end > len(jobs) {
		end = len(jobs)
	}
	
	return jobs[offset:end], count, nil
}

func (s *MemoryJobStore) ListWithOptions(ctx context.Context, opts ListOptions) ([]*Job, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var jobs []*Job
	for _, job := range s.jobs {
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
		return []*Job{}, count, nil
	}
	end := opts.Offset + limit
	if end > len(jobs) {
		end = len(jobs)
	}
	return jobs[opts.Offset:end], count, nil
}

func (s *MemoryJobStore) GetPendingJobs(ctx context.Context) ([]*Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	
	var jobs []*Job
	for _, job := range s.jobs {
		if job.Status != JobStatusCompleted && job.Status != JobStatusStopped && job.Status != JobStatusFailed {
			j := *job
			jobs = append(jobs, &j)
		}
	}
	return jobs, nil
}

func (s *MemoryJobStore) UpdateStatus(ctx context.Context, id string, status JobStatus, lastRun *time.Time, nextRun time.Time, lastDuration time.Duration, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	job, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %s", id)
	}
	
	job.Status = status
	job.UpdatedAt = time.Now()
	job.NextRunAt = nextRun
	
	if lastRun != nil {
		job.LastRunAt = lastRun
	}
	if lastDuration > 0 {
		job.LastDuration = lastDuration
	}
	if lastError != "" {
		job.LastError = lastError
	}
	
	if status == JobStatusRunning {
		now := time.Now()
		job.RunningAt = &now
	} else {
		job.RunningAt = nil
	}
	
	return nil
}

func (s *MemoryJobStore) ResetStuckJobs(ctx context.Context, timeout time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	threshold := time.Now().Add(-timeout)
	
	for _, job := range s.jobs {
		if job.Status == JobStatusRunning && job.RunningAt != nil && job.RunningAt.Before(threshold) {
			job.Status = JobStatusFailed
			job.RunningAt = nil
			job.LastError = "Job execution stuck/timed out"
			job.UpdatedAt = time.Now()
		}
	}
	return nil
}
