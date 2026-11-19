// Package sandbox defines a backend-neutral process and file boundary for
// agent tools.
//
// A Sandbox separates "what may this tool reach" from approval and trust:
// approvals decide whether a call is allowed, a sandbox decides what the call
// can touch once it runs. Backends may be local (HostSandbox), containerized
// (DockerSandbox, future) or remote (SSHSandbox, future).
//
// HostSandbox does NOT provide isolation: commands run with the full
// privileges of the host process. See HostSandbox for the exact guarantees.
package sandbox

import (
	"context"
	"errors"
	"io/fs"
	"time"
)

// Sandbox is a process and file capability boundary. Implementations must be
// safe for concurrent use unless documented otherwise.
type Sandbox interface {
	// Name identifies the backend, e.g. "host".
	Name() string
	// Root returns the sandbox's root directory. For the host backend this is
	// the only directory the file helpers and default working directory are
	// confined to. For remote backends it is the logical workspace root.
	Root() string
	// Run executes a command. Command and Args are passed to the backend
	// directly; implementations must not perform shell concatenation.
	Run(ctx context.Context, spec ExecSpec) (ExecResult, error)
	// ReadFile reads a file inside the sandbox root.
	ReadFile(ctx context.Context, path string) ([]byte, error)
	// WriteFile writes a file inside the sandbox root, creating parent
	// directories as needed.
	WriteFile(ctx context.Context, path string, data []byte, mode fs.FileMode) error
	// Stat returns metadata for a path inside the sandbox root.
	Stat(ctx context.Context, path string) (FileInfo, error)
}

// ExecSpec describes a single process execution.
type ExecSpec struct {
	// Command is the executable to run. It is resolved by the backend; it is
	// never interpreted by a shell.
	Command string
	// Args are passed verbatim to Command.
	Args []string
	// Dir is the working directory. Relative paths are resolved against the
	// sandbox root and must stay inside it. Empty means the root.
	Dir string
	// Env contains additional environment variables for the process. On the
	// host backend these are still subject to EnvAllowlist/EnvDeny.
	Env map[string]string
	// Stdin is fed to the process on standard input.
	Stdin []byte
	// Timeout bounds the process lifetime. Zero uses the backend default;
	// negative values are rejected.
	Timeout time.Duration
	// MaxOutputBytes caps each captured stream independently. Zero uses the
	// backend default; negative means unlimited.
	MaxOutputBytes int
}

// ExecResult is the outcome of Run.
type ExecResult struct {
	// ExitCode is the process exit status, or -1 if it did not exit normally
	// (failed to start, killed by a signal, timed out or was canceled).
	ExitCode int
	// Stdout is the captured standard output, possibly truncated.
	Stdout []byte
	// Stderr is the captured standard error, possibly truncated.
	Stderr []byte
	// Duration is the wall-clock time from start to process exit.
	Duration time.Duration
	// Truncated reports whether Stdout or Stderr hit MaxOutputBytes.
	Truncated bool
}

// FileInfo is a backend-neutral subset of os.FileInfo.
type FileInfo struct {
	Name    string
	Size    int64
	Mode    fs.FileMode
	ModTime time.Time
	IsDir   bool
}

func fileInfoFromOS(info fs.FileInfo) FileInfo {
	return FileInfo{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}
}

// Sentinel errors. Use errors.Is to test them.
var (
	// ErrPathEscape reports a path (or working directory) outside the sandbox
	// root.
	ErrPathEscape = errors.New("sandbox: path escapes root")
	// ErrTimeout reports that the process exceeded its timeout and was killed.
	ErrTimeout = errors.New("sandbox: execution timed out")
	// ErrCanceled reports that the caller's context was canceled or expired.
	ErrCanceled = errors.New("sandbox: execution canceled")
	// ErrInvalidSpec reports an invalid ExecSpec or option.
	ErrInvalidSpec = errors.New("sandbox: invalid spec")
)
