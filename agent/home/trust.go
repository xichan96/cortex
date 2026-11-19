package home

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TrustDecision is the answer to "may project-provided executable resources be
// loaded?". It gates resources only; it is not a sandbox and it does not
// restrict tool execution.
type TrustDecision int

const (
	TrustUnknown TrustDecision = iota
	TrustAllowed
	TrustDenied
)

func (d TrustDecision) String() string {
	switch d {
	case TrustAllowed:
		return "allowed"
	case TrustDenied:
		return "denied"
	default:
		return "unknown"
	}
}

// TrustMode selects how a missing stored decision is resolved.
type TrustMode int

const (
	// TrustModeInteractive may show a prompt, so "ask" resolves to
	// TrustUnknown and the caller asks the user.
	TrustModeInteractive TrustMode = iota
	// TrustModeNonInteractive cannot prompt: anything but an explicit "always"
	// policy is denied.
	TrustModeNonInteractive
)

func (m TrustMode) String() string {
	if m == TrustModeNonInteractive {
		return "non_interactive"
	}
	return "interactive"
}

// TrustPolicy is the configured default_project_trust value.
type TrustPolicy string

const (
	TrustPolicyAsk    TrustPolicy = "ask"
	TrustPolicyAlways TrustPolicy = "always"
	TrustPolicyNever  TrustPolicy = "never"
)

// ParseTrustPolicy normalizes a policy string. Unknown values fall back to ask.
func ParseTrustPolicy(value string) TrustPolicy {
	switch TrustPolicy(strings.ToLower(strings.TrimSpace(value))) {
	case TrustPolicyAlways:
		return TrustPolicyAlways
	case TrustPolicyNever:
		return TrustPolicyNever
	default:
		return TrustPolicyAsk
	}
}

// TrustRequest describes a trust evaluation for one workspace.
type TrustRequest struct {
	CWD     string
	Reasons []string
	Mode    TrustMode
}

// TrustStore persists decisions keyed by canonical path.
type TrustStore interface {
	Lookup(ctx context.Context, canonicalPath string) (TrustDecision, bool, error)
	Save(ctx context.Context, canonicalPath string, d TrustDecision) error
}

// ProjectTrustHandler is an extension hook evaluated before the store. It
// reports handled=false when it has no opinion. The first handler returning a
// decision owns the outcome.
type ProjectTrustHandler func(ctx context.Context, req TrustRequest) (decision TrustDecision, handled bool, err error)

// TrustOptions controls ResolveTrust.
type TrustOptions struct {
	// CLIOverride is TrustUnknown when no --approve/--no-approve flag was
	// given. Any other value wins immediately.
	CLIOverride TrustDecision
	// Handler is an optional extension hook (project_trust).
	Handler ProjectTrustHandler
	// DefaultPolicy is default_project_trust. Empty means ask.
	DefaultPolicy TrustPolicy
}

// ResolveTrust applies the trust decision order:
//
//	CLI override > project_trust handler > nearest ancestor in trust.json >
//	default_project_trust (ask by default)
//
// In non-interactive mode "ask" cannot prompt and therefore denies.
func ResolveTrust(ctx context.Context, req TrustRequest, store TrustStore, opts TrustOptions) (TrustDecision, error) {
	if err := ctx.Err(); err != nil {
		return TrustUnknown, err
	}

	switch opts.CLIOverride {
	case TrustAllowed, TrustDenied:
		return opts.CLIOverride, nil
	}

	if opts.Handler != nil {
		decision, handled, err := opts.Handler(ctx, req)
		if err != nil {
			return TrustUnknown, fmt.Errorf("home: project trust handler: %w", err)
		}
		if handled {
			switch decision {
			case TrustAllowed, TrustDenied:
				return decision, nil
			}
		}
	}

	if store != nil && strings.TrimSpace(req.CWD) != "" {
		canonical := CanonicalPath(req.CWD)
		decision, found, err := store.Lookup(ctx, canonical)
		if err != nil {
			return TrustUnknown, fmt.Errorf("home: trust store lookup: %w", err)
		}
		if found {
			switch decision {
			case TrustAllowed, TrustDenied:
				return decision, nil
			}
		}
	}

	switch ParseTrustPolicy(string(opts.DefaultPolicy)) {
	case TrustPolicyAlways:
		return TrustAllowed, nil
	case TrustPolicyNever:
		return TrustDenied, nil
	default:
		if req.Mode == TrustModeNonInteractive {
			return TrustDenied, nil
		}
		return TrustUnknown, nil
	}
}

// CanonicalPath returns an absolute, symlink-resolved path suitable for trust
// keys. Non-existent paths resolve their existing parent and append the base.
func CanonicalPath(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved)
	}
	parent := filepath.Dir(abs)
	if resolved, err := filepath.EvalSymlinks(parent); err == nil {
		return filepath.Join(resolved, filepath.Base(abs))
	}
	return filepath.Clean(abs)
}

// FileTrustStore is a TrustStore backed by trust.json: a JSON object mapping
// canonical paths to booleans. Writes are atomic (temp file + rename) and
// serialized across processes with a sibling <path>.lock file.
type FileTrustStore struct {
	Path string
	// LockTimeout bounds how long Save/Lookup wait for the lock. Zero means 5s.
	LockTimeout time.Duration
	// StaleLockAfter is how old a lock file must be before it is considered
	// abandoned. Zero means 30s.
	StaleLockAfter time.Duration
	// Now is injectable for tests. Nil uses time.Now.
	Now func() time.Time
}

// NewFileTrustStore returns a store backed by path (usually <agent-dir>/trust.json).
func NewFileTrustStore(path string) *FileTrustStore {
	return &FileTrustStore{Path: path}
}

// Lookup returns the nearest decision at or above canonicalPath.
func (s *FileTrustStore) Lookup(ctx context.Context, canonicalPath string) (TrustDecision, bool, error) {
	if canonicalPath == "" {
		return TrustUnknown, false, nil
	}
	if _, err := os.Stat(s.Path); err != nil {
		if os.IsNotExist(err) {
			return TrustUnknown, false, nil
		}
		return TrustUnknown, false, fmt.Errorf("home: stat trust store %s: %w", s.Path, err)
	}
	unlock, err := s.lock(ctx, false)
	if err != nil {
		return TrustUnknown, false, err
	}
	entries, err := s.read()
	if err != nil {
		unlock()
		return TrustUnknown, false, err
	}
	unlock()

	current := CanonicalPath(canonicalPath)
	if current == "" {
		return TrustUnknown, false, nil
	}
	for {
		if decision, ok := entries[current]; ok {
			if decision {
				return TrustAllowed, true, nil
			}
			return TrustDenied, true, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return TrustUnknown, false, nil
		}
		current = parent
	}
}

// Save records a decision for canonicalPath. TrustUnknown removes the entry,
// letting a parent decision apply again.
func (s *FileTrustStore) Save(ctx context.Context, canonicalPath string, d TrustDecision) error {
	if canonicalPath == "" {
		return errors.New("home: trust store: empty path")
	}
	unlock, err := s.lock(ctx, true)
	if err != nil {
		return err
	}
	defer unlock()

	entries, err := s.read()
	if err != nil {
		return err
	}

	key := CanonicalPath(canonicalPath)
	delete(entries, key)
	switch d {
	case TrustAllowed:
		entries[key] = true
	case TrustDenied:
		entries[key] = false
	case TrustUnknown:
		// Deleted above.
	default:
		return fmt.Errorf("home: trust store: invalid decision %d", int(d))
	}
	return s.write(entries)
}

func (s *FileTrustStore) read() (map[string]bool, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("home: read trust store %s: %w", s.Path, err)
	}
	data = stripBOM(data)
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]bool{}, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("home: parse trust store %s: %w", s.Path, err)
	}
	entries := make(map[string]bool, len(raw))
	for key, value := range raw {
		switch v := value.(type) {
		case bool:
			entries[key] = v
		case nil:
			// A null entry means "no decision"; hide it from lookup.
		default:
			return nil, fmt.Errorf("home: trust store %s: value for %q must be true or false", s.Path, key)
		}
	}
	return entries, nil
}

func (s *FileTrustStore) write(entries map[string]bool) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return fmt.Errorf("home: create trust store directory: %w", err)
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("home: encode trust store: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".trust-*.tmp")
	if err != nil {
		return fmt.Errorf("home: create trust store temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("home: write trust store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("home: sync trust store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("home: close trust store temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("home: chmod trust store: %w", err)
	}
	if err := os.Rename(tmpName, s.Path); err != nil {
		return fmt.Errorf("home: replace trust store: %w", err)
	}
	return nil
}

func (s *FileTrustStore) lock(ctx context.Context, create bool) (func(), error) {
	lockPath := s.Path + ".lock"
	if create {
		if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
			return nil, fmt.Errorf("home: create trust store directory: %w", err)
		}
	}

	timeout := s.LockTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	staleAfter := s.StaleLockAfter
	if staleAfter <= 0 {
		staleAfter = 30 * time.Second
	}
	now := s.Now
	if now == nil {
		now = time.Now
	}

	deadline := now().Add(timeout)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("home: acquire trust store lock %s: %w", lockPath, err)
		}

		if info, statErr := os.Stat(lockPath); statErr == nil && now().Sub(info.ModTime()) > staleAfter {
			// Abandoned lock from a crashed process.
			_ = os.Remove(lockPath)
			continue
		}

		if now().After(deadline) {
			return nil, fmt.Errorf("home: trust store lock %s: timed out after %s", lockPath, timeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
