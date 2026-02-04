package xcron

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// JobType defines the trigger mode of the job
type JobType string

const (
	JobTypeOneShot  JobType = "one_shot"
	JobTypePeriodic JobType = "periodic"
	JobTypeCron     JobType = "cron"
)

// JobStatus defines the current state of the job
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
	JobStatusStopped   JobStatus = "stopped"
)

// TaskType defines the business logic to execute
type TaskType string

const (
	TaskTypeGreet    TaskType = "greet"
	TaskTypeReply    TaskType = "reply"
	TaskTypeWorkflow TaskType = "workflow"
	TaskTypeFunction TaskType = "function"
)

// Job represents a scheduled task
type Job struct {
	ID         string     `gorm:"primaryKey" json:"id"`
	Name       string     `json:"name"`
	Type       JobType    `json:"type"`
	Schedule   string     `json:"schedule"` // Cron expr, duration string, or timestamp
	Status     JobStatus  `json:"status"`
	Payload    string     `json:"payload"` // JSON encoded data
	TaskType   TaskType   `json:"task_type"`
	Retries    int        `json:"retries"`
	MaxRetries int        `json:"max_retries"`
	LastRunAt  *time.Time `json:"last_run_at"`
	NextRunAt  time.Time  `json:"next_run_at" gorm:"index"`
	// Execution stats
	RunningAt    *time.Time    `json:"running_at,omitempty"`
	LastDuration time.Duration `json:"last_duration,omitempty"`
	LastError    string        `json:"last_error,omitempty"`
	Enabled      bool          `json:"enabled" gorm:"default:true"`

	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
	CronEntryID int            `gorm:"-" json:"-"` // Internal cron entry ID
}

type JobStore interface {
	Save(ctx context.Context, job *Job) error
	Get(ctx context.Context, id string) (*Job, error)
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, offset, limit int) ([]*Job, int64, error)
	GetPendingJobs(ctx context.Context) ([]*Job, error)
	UpdateStatus(ctx context.Context, id string, status JobStatus, lastRun *time.Time, nextRun time.Time, lastDuration time.Duration, lastError string) error
	ResetStuckJobs(ctx context.Context, timeout time.Duration) error
}

// JobPayload is the generic structure for job data
type JobPayload struct {
	TargetID string            `json:"target_id"` // UserID or AgentID
	Data     map[string]string `json:"data"`
}

// AgentPayload defines the structure for agent-driven tasks (Reference: OpenClaw)
type AgentPayload struct {
	Message        string `json:"message"`
	Model          string `json:"model,omitempty"`
	Thinking       string `json:"thinking,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// SystemPayload defines the structure for system events (Reference: OpenClaw)
type SystemPayload struct {
	Event string `json:"event"`
	Text  string `json:"text"`
}
