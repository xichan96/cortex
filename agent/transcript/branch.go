package transcript

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// ForkOptions configures ForkWithOptions.
type ForkOptions struct {
	// Path is the destination session file. It must not exist.
	Path string
	// EntryID is the fork point; empty means the current leaf.
	EntryID string
	// Header overrides the new header. Empty fields are filled from the source
	// session (CWD), from EntryID's path (ParentSession) and from the clock.
	Header Header
}

// Fork copies the root→entryID path into a new session file and opens it.
// The new header records the source file as parentSession.
func (l *Log) Fork(entryID, path string) (*Log, error) {
	return l.ForkWithOptions(ForkOptions{Path: path, EntryID: entryID})
}

// Clone copies the current branch (root→leaf) into a new session file.
func (l *Log) Clone(path string) (*Log, error) {
	return l.ForkWithOptions(ForkOptions{Path: path})
}

// ForkWithOptions implements Fork/Clone with header overrides.
func (l *Log) ForkWithOptions(opts ForkOptions) (*Log, error) {
	if opts.Path == "" {
		return nil, fmt.Errorf("transcript: fork destination path is required")
	}

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, fmt.Errorf("transcript: log is closed")
	}
	path, err := l.pathToLocked(opts.EntryID)
	if err != nil {
		l.mu.Unlock()
		return nil, err
	}
	if len(path) == 0 {
		l.mu.Unlock()
		return nil, fmt.Errorf("%w: empty branch", ErrEntryNotFound)
	}

	header := opts.Header
	if header.Type == "" {
		header.Type = HeaderType
	}
	if header.Type != HeaderType {
		l.mu.Unlock()
		return nil, fmt.Errorf("%w: header type %q, want %q", ErrMalformedHeader, header.Type, HeaderType)
	}
	if header.Version == 0 {
		header.Version = FormatVersion
	}
	if header.Version != FormatVersion {
		l.mu.Unlock()
		return nil, &VersionError{Path: opts.Path, Version: header.Version, Want: FormatVersion}
	}
	if header.ID == "" {
		header.ID = NewID()
	}
	if header.Timestamp.IsZero() {
		header.Timestamp = l.now().UTC()
	}
	if header.CWD == "" {
		header.CWD = l.header.CWD
	}
	if header.ParentSession == "" {
		header.ParentSession = l.path
	}
	l.mu.Unlock()

	// Write with O_EXCL so an existing session is never overwritten.
	f, err := os.OpenFile(opts.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%w: %s", ErrExists, opts.Path)
		}
		return nil, err
	}
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(opts.Path)
	}
	lines := make([][]byte, 0, len(path)+1)
	headerLine, err := marshalLine(header)
	if err != nil {
		cleanup()
		return nil, err
	}
	lines = append(lines, headerLine)
	for _, entry := range path {
		line, merr := marshalLine(entry)
		if merr != nil {
			cleanup()
			return nil, merr
		}
		lines = append(lines, line)
	}
	for _, line := range lines {
		if werr := writeAll(f, line); werr != nil {
			cleanup()
			return nil, werr
		}
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(opts.Path)
		return nil, err
	}

	// Re-open through the normal path so indexes/locks/invariants are rebuilt.
	return Open(opts.Path, OpenOptions{})
}

// Import validates a JSONL stream and writes it to a new session file. Existing
// files are never overwritten and any corrupt line or broken parent link
// rejects the import.
func Import(r io.Reader, path string) (*Log, error) {
	header, entries, diags, err := ParseJSONL(r)
	if err != nil {
		return nil, fmt.Errorf("transcript: import: %w", err)
	}
	if len(diags) > 0 {
		return nil, fmt.Errorf("transcript: import rejected: %d corrupt or inconsistent line(s): %s", len(diags), diags[0])
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%w: %s", ErrExists, path)
		}
		return nil, err
	}
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(path)
	}
	headerLine, err := marshalLine(header)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := writeAll(f, headerLine); err != nil {
		cleanup()
		return nil, err
	}
	for _, entry := range entries {
		line, merr := marshalLine(entry)
		if merr != nil {
			cleanup()
			return nil, merr
		}
		if werr := writeAll(f, line); werr != nil {
			cleanup()
			return nil, werr
		}
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return Open(path, OpenOptions{})
}
