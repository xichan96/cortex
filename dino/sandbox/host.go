package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultMaxOutputBytes is the per-stream capture limit used when neither
	// ExecSpec.MaxOutputBytes nor HostOptions.MaxOutputBytes is set.
	DefaultMaxOutputBytes = 1 << 20
	// DefaultFileMode is used by WriteFile when mode is zero.
	DefaultFileMode fs.FileMode = 0o644
	// DefaultDirMode is used for directories created by WriteFile.
	DefaultDirMode fs.FileMode = 0o755
)

// HostOptions configures a HostSandbox.
type HostOptions struct {
	// Root is the sandbox root. It must exist and be a directory.
	Root string
	// NoInheritEnv starts the child environment empty instead of inheriting
	// the host environment. The default (false) inherits.
	NoInheritEnv bool
	// EnvAllowlist, when non-empty, restricts the child environment to the
	// listed variables. Entries are names, optionally ending in '*' for a
	// prefix match ("GIT_*"). It filters both the inherited environment and
	// ExecSpec.Env.
	EnvAllowlist []string
	// EnvDeny removes variables after the allowlist is applied and wins over
	// both inheritance and ExecSpec.Env. Entries follow the same syntax as
	// EnvAllowlist.
	EnvDeny []string
	// DefaultTimeout applies when ExecSpec.Timeout is zero. Zero means no
	// timeout. Must not be negative.
	DefaultTimeout time.Duration
	// MaxOutputBytes is the per-stream capture limit when
	// ExecSpec.MaxOutputBytes is zero. Zero uses DefaultMaxOutputBytes;
	// negative means unlimited.
	MaxOutputBytes int
	// AllowSymlinks disables the symlink escape check for file operations.
	// The check is enabled by default: a symlink inside the root that points
	// outside makes the operation fail with ErrPathEscape.
	AllowSymlinks bool
}

// HostSandbox runs commands and accesses files on the host machine.
//
// It is deliberately NOT an isolation boundary. Commands run with the same
// user, filesystem and network access as the cortex process: they can read
// /etc, write outside Root, or open sockets. Root only confines this package's
// file helpers (ReadFile/WriteFile/Stat) and the default working directory,
// and symlink checks are best-effort (TOCTOU applies). Use a container or
// remote backend when a real security boundary is required.
//
// Command and Args are passed directly to exec; no shell is involved.
type HostSandbox struct {
	root           string
	realRoot       string
	noInheritEnv   bool
	envAllowlist   []string
	envDeny        []string
	defaultTimeout time.Duration
	maxOutputBytes int
	allowSymlinks  bool
}

var _ Sandbox = (*HostSandbox)(nil)

// NewHostSandbox validates opts and returns a host backend rooted at
// opts.Root.
func NewHostSandbox(opts HostOptions) (*HostSandbox, error) {
	if strings.TrimSpace(opts.Root) == "" {
		return nil, fmt.Errorf("%w: root is required", ErrInvalidSpec)
	}
	if opts.DefaultTimeout < 0 {
		return nil, fmt.Errorf("%w: default timeout must not be negative", ErrInvalidSpec)
	}
	abs, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("sandbox: resolve root %q: %w", opts.Root, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("sandbox: root %q: %w", opts.Root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: root %q is not a directory", ErrInvalidSpec, opts.Root)
	}
	realRoot, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("sandbox: resolve root %q: %w", opts.Root, err)
	}
	if opts.MaxOutputBytes == 0 {
		opts.MaxOutputBytes = DefaultMaxOutputBytes
	}
	return &HostSandbox{
		root:           filepath.Clean(abs),
		realRoot:       filepath.Clean(realRoot),
		noInheritEnv:   opts.NoInheritEnv,
		envAllowlist:   append([]string(nil), opts.EnvAllowlist...),
		envDeny:        append([]string(nil), opts.EnvDeny...),
		defaultTimeout: opts.DefaultTimeout,
		maxOutputBytes: opts.MaxOutputBytes,
		allowSymlinks:  opts.AllowSymlinks,
	}, nil
}

// Name returns "host".
func (s *HostSandbox) Name() string { return "host" }

// Root returns the absolute sandbox root directory.
func (s *HostSandbox) Root() string { return s.root }

// Run executes spec.Command without a shell. A non-zero exit status is
// returned in ExecResult.ExitCode with a nil error. A nil error therefore only
// means the process ran to completion; callers must inspect ExitCode.
//
// Timeout and context cancellation kill the whole process group on Unix and
// return ErrTimeout or ErrCanceled. Partial output is still populated.
func (s *HostSandbox) Run(ctx context.Context, spec ExecSpec) (ExecResult, error) {
	res := ExecResult{ExitCode: -1}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return res, errors.Join(ErrCanceled, err)
	}
	if spec.Command == "" {
		return res, fmt.Errorf("%w: command is required", ErrInvalidSpec)
	}
	if strings.IndexByte(spec.Command, 0) >= 0 {
		return res, fmt.Errorf("%w: command contains NUL", ErrInvalidSpec)
	}
	if spec.Timeout < 0 {
		return res, fmt.Errorf("%w: timeout must not be negative", ErrInvalidSpec)
	}
	timeout := spec.Timeout
	if timeout == 0 {
		timeout = s.defaultTimeout
	}
	limit := spec.MaxOutputBytes
	if limit == 0 {
		limit = s.maxOutputBytes
	}

	dir, err := s.resolveDir(spec.Dir)
	if err != nil {
		return res, err
	}
	env, err := s.buildEnv(spec.Env)
	if err != nil {
		return res, err
	}

	stdout := newLimitedBuffer(limit)
	stderr := newLimitedBuffer(limit)
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if len(spec.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	setProcessGroup(cmd)

	started := time.Now()
	if err := cmd.Start(); err != nil {
		res.Duration = time.Since(started)
		return res, fmt.Errorf("sandbox: start %q: %w", spec.Command, err)
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	var timer *time.Timer
	var timeoutCh <-chan time.Time
	if timeout > 0 {
		timer = time.NewTimer(timeout)
		timeoutCh = timer.C
		defer timer.Stop()
	}

	var waitErr error
	timedOut := false
	canceled := false
	select {
	case waitErr = <-waitCh:
	case <-timeoutCh:
		timedOut = true
		_ = killProcessGroup(cmd)
		waitErr = <-waitCh
	case <-ctx.Done():
		canceled = true
		_ = killProcessGroup(cmd)
		waitErr = <-waitCh
	}

	res.Duration = time.Since(started)
	res.Stdout = stdout.Bytes()
	res.Stderr = stderr.Bytes()
	res.Truncated = stdout.Truncated() || stderr.Truncated()
	res.ExitCode = exitCodeOf(waitErr)

	switch {
	case timedOut:
		return res, fmt.Errorf("%w: command %q timed out after %s", ErrTimeout, spec.Command, timeout)
	case canceled:
		return res, errors.Join(ErrCanceled, ctx.Err())
	case waitErr == nil:
		res.ExitCode = 0
		return res, nil
	default:
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return res, nil
		}
		return res, fmt.Errorf("sandbox: wait %q: %w", spec.Command, waitErr)
	}
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// ReadFile reads path inside the root.
func (s *HostSandbox) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	abs, err := s.resolve(path)
	if err != nil {
		return nil, err
	}
	if err := s.checkReal(abs); err != nil {
		return nil, err
	}
	return os.ReadFile(abs)
}

// WriteFile writes data to path inside the root, creating parent directories.
func (s *HostSandbox) WriteFile(ctx context.Context, path string, data []byte, mode fs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if mode == 0 {
		mode = DefaultFileMode
	}
	abs, err := s.resolve(path)
	if err != nil {
		return err
	}
	if err := s.checkReal(abs); err != nil {
		return err
	}
	if !s.allowSymlinks {
		// A dangling symlink resolves to nothing, so checkReal cannot tell
		// where os.WriteFile would land. Refuse rather than risk writing
		// through it.
		if info, statErr := os.Lstat(abs); statErr == nil && info.Mode()&fs.ModeSymlink != 0 {
			if _, resolveErr := filepath.EvalSymlinks(abs); resolveErr != nil {
				return fmt.Errorf("%w: dangling symlink %q", ErrPathEscape, path)
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(abs), DefaultDirMode); err != nil {
		return fmt.Errorf("sandbox: create parent of %q: %w", path, err)
	}
	return os.WriteFile(abs, data, mode)
}

// Stat returns metadata for path inside the root.
func (s *HostSandbox) Stat(ctx context.Context, path string) (FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return FileInfo{}, err
	}
	abs, err := s.resolve(path)
	if err != nil {
		return FileInfo{}, err
	}
	if err := s.checkReal(abs); err != nil {
		return FileInfo{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return FileInfo{}, err
	}
	return fileInfoFromOS(info), nil
}

func (s *HostSandbox) resolveDir(dir string) (string, error) {
	if dir == "" {
		return s.root, nil
	}
	abs, err := s.resolve(dir)
	if err != nil {
		return "", err
	}
	if err := s.checkReal(abs); err != nil {
		return "", err
	}
	return abs, nil
}

// resolve maps a sandbox path to a cleaned absolute host path and performs the
// lexical containment check. The symlink check is done separately.
func (s *HostSandbox) resolve(path string) (string, error) {
	if strings.IndexByte(path, 0) >= 0 {
		return "", fmt.Errorf("%w: path contains NUL", ErrInvalidSpec)
	}
	if path == "" {
		return s.root, nil
	}
	var abs string
	if filepath.IsAbs(path) {
		abs = filepath.Clean(path)
	} else {
		abs = filepath.Join(s.root, path)
	}
	if !within(s.root, abs) && !within(s.realRoot, abs) {
		return "", fmt.Errorf("%w: %q", ErrPathEscape, path)
	}
	return abs, nil
}

// checkReal rejects paths whose deepest existing ancestor resolves through a
// symlink to somewhere outside the root. It is best-effort: callers still
// share a filesystem with unrelated processes, so this is not a hard boundary.
func (s *HostSandbox) checkReal(abs string) error {
	if s.allowSymlinks {
		return nil
	}
	real, err := evalExistingAncestor(abs)
	if err != nil {
		return fmt.Errorf("sandbox: resolve %q: %w", abs, err)
	}
	if !within(s.realRoot, real) {
		return fmt.Errorf("%w: %q", ErrPathEscape, abs)
	}
	return nil
}

// evalExistingAncestor resolves symlinks for the deepest existing ancestor of
// path and re-appends the components that do not exist yet. Newly created
// components cannot themselves be symlinks, so the result represents where the
// path would really live.
func evalExistingAncestor(path string) (string, error) {
	cur := path
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		missing = append(missing, filepath.Base(cur))
		cur = parent
	}
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s *HostSandbox) buildEnv(explicit map[string]string) ([]string, error) {
	merged := make(map[string]string)
	if !s.noInheritEnv {
		for _, kv := range os.Environ() {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			merged[k] = v
		}
	}
	for k, v := range explicit {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.IndexByte(v, 0) >= 0 {
			return nil, fmt.Errorf("%w: invalid environment variable %q", ErrInvalidSpec, k)
		}
		merged[k] = v
	}
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		if matchesAny(s.envDeny, k) {
			continue
		}
		if len(s.envAllowlist) > 0 && !matchesAny(s.envAllowlist, k) {
			continue
		}
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out, nil
}

// matchesAny reports whether name matches one of the patterns. Patterns are
// exact names, or prefixes ending in '*'.
func matchesAny(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if pattern == name {
			return true
		}
		if prefix, ok := strings.CutSuffix(pattern, "*"); ok && strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// limitedBuffer captures at most limit bytes (negative means unlimited) and
// records whether more data arrived. Write always reports the full length so a
// child process is never blocked or sees a write error because of the cap.
type limitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit >= 0 {
		remaining := b.limit - b.buf.Len()
		if remaining <= 0 {
			b.truncated = true
			return len(p), nil
		}
		if len(p) > remaining {
			_, _ = b.buf.Write(p[:remaining])
			b.truncated = true
			return len(p), nil
		}
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *limitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func (b *limitedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}
