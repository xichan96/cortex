package task

import (
	"context"
	"fmt"
	"time"

	agenttypes "github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino"
)

type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
)

type Plan struct {
	Goal      string     `json:"goal,omitempty"`
	Steps     []PlanStep `json:"steps,omitempty"`
	Reasoning string     `json:"reasoning,omitempty"`
	Approved  bool       `json:"approved,omitempty"`
}

type PlanStep struct {
	Index     int                    `json:"index"`
	Tool      string                 `json:"tool"`
	Input     map[string]interface{} `json:"input"`
	Reasoning string                 `json:"reasoning,omitempty"`
}

type Task struct {
	ID          string
	SessionID   string
	Description string
	Plan        *Plan
	Status      TaskStatus
	Result      *TaskResult

	Config   *TaskConfig
	Progress *TaskProgress

	PendingInput agenttypes.AgentInput

	CreatedAt time.Time
	UpdatedAt time.Time
}

type TaskConfig struct {
	MaxBudgetTokens        int
	MaxTurns               int
	MaxOuterIterations     int
	Timeout                time.Duration
	RetryLimit             int
	StallWindow            int
	CheckpointKeepVersions int
	CompactEveryNTurns     int
	ContextWindow          int
	ModelID                string
	Isolated               bool
	PreferContextReset     bool
	CompletionMarker       string
	ArtifactDir            string
	VerifyCommand          string
	VerifyTextMustContain  string
	VerifyLLMSystemPrompt  string
}

type TaskProgress struct {
	CurrentTurn      int
	OuterIteration   int
	VerifyRetryCount int
	ConsumedTokens   int
	LastCheckpoint   time.Time
	LastCompactTurn  int
	CompactCount     int
	CompactHistory   []string
}

type TaskResult struct {
	FinalSnapshot *TurnSnapshot
}

type StopReason int

const (
	StopReasonCompleted StopReason = iota
	StopReasonAgentIdle
	StopReasonMaxTurnsReached
	StopReasonMaxOuterIterations
	StopReasonMaxBudgetReached
	StopReasonContextOverflow
	StopReasonTimeout
	StopReasonFailed
	StopReasonUserCancelled
	StopReasonVerificationFailed
)

type TurnSnapshot struct {
	AssistantText       string
	Usage               *agenttypes.Usage
	HadError            bool
	ErrorMessage        string
	ArtifactFingerprint string
	VerifyExitCode      *int
	EngineStopCause     string
}

type InnerTurnDriver interface {
	RunOneUserTurn(ctx context.Context, sess *dino.Session, in agenttypes.AgentInput) (*TurnSnapshot, StopReason, error)
}

type Verifier interface {
	Verify(ctx context.Context, t *Task, snap *TurnSnapshot) (ok bool, vreason string)
}

type SessionStore interface {
	Save(ctx context.Context, session *TaskSession) error
	Load(ctx context.Context, taskID string) (*TaskSession, error)
	Delete(ctx context.Context, taskID string) error
	List(ctx context.Context, sessionID string) ([]*TaskSession, error)
}

type TaskState string

type TaskSession struct {
	TaskID       string
	SessionID    string
	State        TaskState
	Messages     []string
	InputTokens  int
	OutputTokens int
	UpdatedAt    time.Time
}

func NewIsolatedTaskSessionID(prefix string) string {
	return fmt.Sprintf("%s-i-%d", prefix, time.Now().UnixNano())
}
