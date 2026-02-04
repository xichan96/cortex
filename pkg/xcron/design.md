# xcron Design Document

## 1. Overview
`xcron` is a high-reliability scheduled task system designed for Agent scenarios. It supports millisecond-level delayed execution, periodic execution, and standard Cron expressions. It ensures task persistence, concurrency safety, and failure recovery.

## 2. Architecture

### 2.1 Core Components

*   **Scheduler**: The central controller. Wraps `robfig/cron/v3` engine. Manages task lifecycle, storage synchronization, and execution flow.
*   **JobStore**: Interface for persistence. Default implementation uses GORM (SQLite/MySQL).
*   **Executor**: Internal logic that wraps business handlers with retry policies and status updates.
*   **Locker**: Interface for distributed locking (e.g., Redis).

### 2.2 Data Model

**Job Struct:**
*   `ID`: UUID
*   `Type`: OneShot, Periodic, Cron
*   `Schedule`: Expression string (Cron expr, `@every`, duration)
*   `Status`: Pending, Running, Completed, Failed, Stopped
*   `TaskType`: Business logic identifier (Greet, Reply, etc.)
*   `Payload`: JSON data for the handler (Supports `AgentPayload`, `SystemPayload`)
*   `ExecutionStats`: `RunningAt`, `LastDuration`, `LastError`

### 2.3 Execution Flow

1.  **AddJob**:
    *   Save `Job` to `Store` (Status: Pending).
    *   Parse schedule.
    *   Register with `cron` engine.
2.  **Trigger**:
    *   `cron` engine fires callback.
    *   **Wrapper**:
        *   Check `Store` for current status (skip if Stopped/Completed).
        *   Acquire `Lock` (if distributed).
        *   Update Status -> Running, Set `RunningAt`.
        *   Execute `Handler` (with Retries).
        *   On Success: Status -> Completed (OneShot) or Pending (Periodic). Update NextRun, `LastDuration`.
        *   On Failure: Status -> Failed, `LastError`, `LastDuration`. Trigger Callback.
3.  **Recovery**:
    *   **Restart**: `Scheduler.Start()` loads pending jobs.
    *   **Stuck Job**: Background routine resets jobs stuck in `Running` state for > 2 hours.

## 3. Key Features Implementation

*   **OneShot**: Implemented using a custom `cron.Schedule` or `AddFunc` with self-removal logic upon completion.
*   **Persistence**: Jobs are stored in DB. On restart, only active jobs are loaded.
*   **Concurrency**: `sync.RWMutex` protects internal maps. `Locker` interface prevents duplicate execution across nodes.
*   **Retry**: In-process retry loop with simple backoff.
*   **Observability**: Detailed execution stats (Duration, Error) recorded in DB.

## 4. Interfaces

```go
type JobStore interface {
    Save(ctx context.Context, job *Job) error
    Get(ctx context.Context, id string) (*Job, error)
    Delete(ctx context.Context, id string) error
    List(ctx context.Context, offset, limit int) ([]*Job, int64, error)
    GetPendingJobs(ctx context.Context) ([]*Job, error)
    UpdateStatus(ctx context.Context, id string, status JobStatus, lastRun *time.Time, nextRun time.Time) error
}

type Locker interface {
    Lock(ctx context.Context, key string, ttl time.Duration) (bool, error)
    Unlock(ctx context.Context, key string) error
}
```
