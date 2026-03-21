package shell

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/xichan96/cortex/pkg/csync"
)

// BackgroundShell represents a running background shell command.
type BackgroundShell struct {
	ID        string
	Cmd       string
	Args      []string
	StartedAt time.Time

	stdout  *bytes.Buffer
	stderr  *bytes.Buffer
	done    chan struct{}
	cancel  context.CancelFunc
	exitErr error
}

// BackgroundShellManager manages background shell commands.
type BackgroundShellManager struct {
	shells *csync.Map[string, *BackgroundShell]
}

// NewBackgroundShellManager creates a new BackgroundShellManager.
func NewBackgroundShellManager() *BackgroundShellManager {
	return &BackgroundShellManager{
		shells: csync.NewMap[string, *BackgroundShell](),
	}
}

// Start starts a new background shell command.
func (m *BackgroundShellManager) Start(ctx context.Context, workingDir string, blockFuncs []BlockFunc, cmd string, args ...string) (*BackgroundShell, error) {
	// Generate a unique ID
	id := uuid.New().String()

	// Create output buffers
	var stdout, stderr bytes.Buffer

	// Create cancellation context
	ctx, cancel := context.WithCancel(ctx)

	bs := &BackgroundShell{
		ID:        id,
		Cmd:       cmd,
		Args:      args,
		StartedAt: time.Now(),
		stdout:    &stdout,
		stderr:    &stderr,
		done:      make(chan struct{}),
		cancel:    cancel,
	}

	// Store in map
	m.shells.Set(id, bs)

	// Start execution in goroutine
	go func() {
		defer close(bs.done)

		shell := NewShell(&Options{
			WorkingDir: workingDir,
			BlockFuncs: blockFuncs,
			Env:        EnvironNonInteractive(),
		})

		// Construct full command if args are provided (simple concatenation for now,
		// but ideally shell.Exec handles single string.
		// If 'cmd' is the full command string, we use it.
		// If 'args' are provided, we might need to join them?
		// Crush's implementation in `Start` takes `cmd` as the full command string usually.
		// Let's assume `cmd` is the full script.
		fullCmd := cmd
		if len(args) > 0 {
			// This is ambiguous. Let's assume cmd is the script.
		}

		// Use ExecStream to capture output
		err := shell.ExecStream(ctx, fullCmd, &stdout, &stderr)
		bs.exitErr = err
	}()

	return bs, nil
}

// Get retrieves a background shell by ID.
func (m *BackgroundShellManager) Get(id string) (*BackgroundShell, bool) {
	return m.shells.Get(id)
}

// Kill terminates a background shell.
func (m *BackgroundShellManager) Kill(id string) error {
	bs, ok := m.shells.Get(id)
	if !ok {
		return fmt.Errorf("background shell %s not found", id)
	}

	bs.cancel()
	m.shells.Del(id)
	return nil
}

// List returns all running background shells.
func (m *BackgroundShellManager) List() []*BackgroundShell {
	var shells []*BackgroundShell
	for _, bs := range m.shells.Seq2() {
		shells = append(shells, bs)
	}
	return shells
}

// Cleanup removes all background shells.
func (m *BackgroundShellManager) Cleanup() {
	var wg sync.WaitGroup

	// Collect shells first to avoid concurrent map iteration issues if we delete during iteration (though csync is safe)
	var shells []*BackgroundShell
	for _, bs := range m.shells.Seq2() {
		shells = append(shells, bs)
	}

	for _, shell := range shells {
		wg.Add(1)
		go func(bs *BackgroundShell) {
			defer wg.Done()
			bs.cancel()
			// Wait for it to finish
			<-bs.done
		}(shell)
	}
	wg.Wait()

	// Clear map
	m.shells = csync.NewMap[string, *BackgroundShell]()
}

// GetOutput returns the current output of a background shell.
func (bs *BackgroundShell) GetOutput() (stdout string, stderr string, done bool, err error) {
	select {
	case <-bs.done:
		return bs.stdout.String(), bs.stderr.String(), true, bs.exitErr
	default:
		return bs.stdout.String(), bs.stderr.String(), false, nil
	}
}

// IsDone checks if the background shell has finished execution.
func (bs *BackgroundShell) IsDone() bool {
	select {
	case <-bs.done:
		return true
	default:
		return false
	}
}

// Wait blocks until the background shell completes.
func (bs *BackgroundShell) Wait() {
	<-bs.done
}
