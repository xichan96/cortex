# xcron

`xcron` is a high-reliability scheduled task system for Agents, built on top of `robfig/cron/v3`. It supports single delayed triggers, periodic triggers, and standard cron expressions, with persistence, concurrency safety, and retry mechanisms.

## Features

- **Trigger Modes**:
  - **OneShot**: Execute once after a specified delay (ms precision).
  - **Periodic**: Execute at fixed intervals (e.g., `@every 10s`).
  - **Cron**: Standard Cron syntax (e.g., `0 0 * * * *`).
- **Persistence**: Auto-resume tasks after restart using `JobStore` (GORM supported).
- **Reliability**:
  - Automatic retries with configurable count.
  - Distributed lock support via `Locker` interface.
  - Failure callbacks for monitoring.
- **Concurrency**: Thread-safe scheduler.

## Installation

```bash
go get github.com/xichan96/cortex/xcron
```

## Usage

### 1. Initialize Scheduler

```go
import (
    "github.com/xichan96/cortex/xcron"
    "gorm.io/driver/sqlite"
    "gorm.io/gorm"
)

// Setup DB
db, _ := gorm.Open(sqlite.Open("cron.db"), &gorm.Config{})
db.AutoMigrate(&xcron.Job{})

// Create Store and Scheduler
store := xcron.NewGormJobStore(db)
scheduler := xcron.NewScheduler(store)

// Optional: Set Failure Handler
scheduler.SetFailureHandler(func(job *xcron.Job, err error) {
    fmt.Printf("Job %s failed: %v\n", job.ID, err)
})

// Start
scheduler.Start()
defer scheduler.Stop()
```

### 2. Register Task Handlers

Define business logic for different task types.

```go
scheduler.RegisterHandler(xcron.TaskTypeGreet, func(ctx context.Context, payload string) error {
    fmt.Println("Hello user:", payload)
    return nil
})
```

### 3. Add Jobs

**One-Shot (Delayed):**
```go
// Run once after 500ms
id, err := scheduler.AddJob(ctx, "greet-once", xcron.JobTypeOneShot, "500ms", xcron.TaskTypeGreet, "UserA", 3)
```

**Periodic:**
```go
// Run every 10 seconds
id, err := scheduler.AddJob(ctx, "poll-status", xcron.JobTypePeriodic, "10s", xcron.TaskTypeFunction, nil, 0)
```

**Cron:**
```go
// Run at top of every hour
id, err := scheduler.AddJob(ctx, "hourly-report", xcron.JobTypeCron, "0 0 * * * *", xcron.TaskTypeWorkflow, nil, 1)
```

### 4. Manage Jobs

```go
// List jobs
jobs, count, err := scheduler.ListJobs(ctx, 0, 10)

// Stop a job
err := scheduler.StopJob(ctx, jobID)

// Remove a job (delete from DB)
err := scheduler.RemoveJob(ctx, jobID)
```

## Distributed Locking

To enable distributed locking, implement the `xcron.Locker` interface (e.g., using Redis) and set it on the scheduler.

```go
scheduler.SetLocker(myRedisLocker)
```

## Testing

Run unit tests:
```bash
go test -v ./xcron/...
```
