package transcript

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

// FormatVersion is the session file format version understood by this package.
const FormatVersion = 1

// HeaderType is the value of Header.Type.
const HeaderType = "session"

var (
	// ErrReadOnly is returned when appending to a read-only log.
	ErrReadOnly = errors.New("transcript: log is read-only")
	// ErrLocked is returned when another writer holds the session lock.
	ErrLocked = errors.New("transcript: session file is locked by another writer")
	// ErrExists is returned when a destination file already exists.
	ErrExists = errors.New("transcript: destination file already exists")
	// ErrEntryNotFound is returned for unknown entry ids.
	ErrEntryNotFound = errors.New("transcript: entry not found")
	// ErrMalformedHeader is returned for an invalid or missing session header.
	ErrMalformedHeader = errors.New("transcript: malformed session header")
)

// VersionError reports a session file whose format version is not supported.
type VersionError struct {
	Path    string
	Version int
	Want    int
}

func (e *VersionError) Error() string {
	if e.Version == 0 {
		return fmt.Sprintf("transcript: %s: session header has no version (want %d); migrate the session first", e.Path, e.Want)
	}
	return fmt.Sprintf("transcript: %s: unsupported session version %d (want %d); migrate the session first", e.Path, e.Version, e.Want)
}

// Diagnostic reports a line that could not be decoded. Corruption is never
// silently discarded: callers can inspect the line number, the entry id (when
// known) and a short raw snippet.
type Diagnostic struct {
	Line    int    `json:"line,omitempty"`
	EntryID string `json:"entryId,omitempty"`
	Message string `json:"message"`
	Raw     string `json:"raw,omitempty"`
}

func (d Diagnostic) String() string {
	if d.Line > 0 {
		return fmt.Sprintf("line %d: %s", d.Line, d.Message)
	}
	return d.Message
}

const maxDiagnosticRaw = 200

func newLineDiagnostic(line int, err error, raw string) Diagnostic {
	if len(raw) > maxDiagnosticRaw {
		raw = raw[:maxDiagnosticRaw] + "…"
	}
	return Diagnostic{Line: line, Message: err.Error(), Raw: raw}
}

// OpenOptions controls Open.
type OpenOptions struct {
	// Create creates the file when it does not exist and writes Header.
	Create bool
	// ReadOnly opens without a lock and rejects appends. Cross-process readers
	// are allowed; the writer lock only excludes other writers.
	ReadOnly bool
	// NoLock disables the exclusive writer lock (not recommended).
	NoLock bool
	// NoSync skips fsync after each append. Close still syncs.
	NoSync bool
	// Header is used when creating a new file. Missing fields are filled in.
	Header Header
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Log is an append-only session tree backed by a JSONL file.
//
// A writer holds an exclusive flock on "<path>.lock"; readers may open the
// file concurrently. Appends update the in-memory index only after the line was
// written (and synced), so memory never runs ahead of disk.
type Log struct {
	mu     sync.Mutex
	path   string
	f      *os.File
	lock   *os.File
	header Header

	entries []Entry
	byID    map[string]Entry
	leafID  string
	size    int64

	readOnly bool
	noSync   bool
	closed   bool
	now      func() time.Time

	diagnostics []Diagnostic
}

// ParseJSONL decodes a session stream (header + entries). Corrupt entry lines
// are reported as diagnostics and skipped; header and I/O errors are returned.
func ParseJSONL(r io.Reader) (Header, []Entry, []Diagnostic, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var (
		header     Header
		headerSeen bool
		entries    []Entry
		diags      []Diagnostic
		seenIDs    = make(map[string]bool)
	)
	lineNo := 0
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			lineNo++
			text := strings.TrimSuffix(line, "\n")
			text = strings.TrimSuffix(text, "\r")
			if strings.TrimSpace(text) == "" {
				// Blank lines are not data; skip them.
			} else if !headerSeen {
				if uerr := json.Unmarshal([]byte(text), &header); uerr != nil {
					return Header{}, nil, diags, fmt.Errorf("%w: line %d: %v", ErrMalformedHeader, lineNo, uerr)
				}
				if verr := validateHeader(header); verr != nil {
					return Header{}, nil, diags, verr
				}
				headerSeen = true
			} else {
				entry, derr := UnmarshalEntry([]byte(text))
				if derr != nil {
					diags = append(diags, newLineDiagnostic(lineNo, derr, text))
				} else if seenIDs[entry.Base().ID] {
					diags = append(diags, newLineDiagnostic(lineNo, fmt.Errorf("duplicate entry id %q", entry.Base().ID), text))
				} else {
					seenIDs[entry.Base().ID] = true
					entries = append(entries, entry)
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return Header{}, nil, diags, err
		}
	}
	if !headerSeen {
		return Header{}, nil, diags, fmt.Errorf("%w: empty session", ErrMalformedHeader)
	}
	diags = append(diags, validateTree(entries)...)
	return header, entries, diags, nil
}

func validateHeader(h Header) error {
	if h.Type == "" {
		return fmt.Errorf("%w: header type is missing", ErrMalformedHeader)
	}
	if h.Type != HeaderType {
		return fmt.Errorf("%w: header type %q, want %q", ErrMalformedHeader, h.Type, HeaderType)
	}
	if h.Version != FormatVersion {
		return &VersionError{Version: h.Version, Want: FormatVersion}
	}
	if h.ID == "" {
		return fmt.Errorf("%w: header id is missing", ErrMalformedHeader)
	}
	return nil
}

// validateTree reports dangling parents and parent cycles.
func validateTree(entries []Entry) []Diagnostic {
	index := make(map[string]Entry, len(entries))
	for _, e := range entries {
		index[e.Base().ID] = e
	}

	var out []Diagnostic
	for _, e := range entries {
		if p := e.Base().ParentID; p != nil {
			if _, ok := index[*p]; !ok {
				out = append(out, Diagnostic{
					EntryID: e.Base().ID,
					Message: fmt.Sprintf("parent entry %q is missing", *p),
				})
			}
		}
	}

	for _, e := range entries {
		seen := map[string]bool{}
		cur := e.Base().ID
		for {
			if seen[cur] {
				out = append(out, Diagnostic{
					EntryID: e.Base().ID,
					Message: fmt.Sprintf("parent cycle detected at entry %q", cur),
				})
				break
			}
			seen[cur] = true
			entry, ok := index[cur]
			if !ok {
				break
			}
			if entry.Base().ParentID == nil {
				break
			}
			cur = *entry.Base().ParentID
		}
	}
	return out
}

// Open opens or creates a session file. Existing files are validated against
// FormatVersion; older/unknown versions return *VersionError instead of being
// rewritten.
func Open(path string, opts OpenOptions) (*Log, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	existed := true
	if _, statErr := os.Stat(path); statErr != nil {
		if !os.IsNotExist(statErr) {
			return nil, statErr
		}
		existed = false
	}

	var (
		f   *os.File
		err error
	)
	switch {
	case opts.ReadOnly:
		f, err = os.Open(path)
	case opts.Create:
		f, err = os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	default:
		f, err = os.OpenFile(path, os.O_RDWR, 0o600)
	}
	if err != nil {
		return nil, err
	}

	var lock *os.File
	if !opts.ReadOnly && !opts.NoLock {
		lock, err = acquireLock(path)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
	}

	closeAll := func() {
		if lock != nil {
			_ = releaseLock(lock)
			_ = lock.Close()
		}
		_ = f.Close()
	}

	info, err := f.Stat()
	if err != nil {
		closeAll()
		return nil, err
	}
	size := info.Size()

	log := &Log{
		path:     path,
		f:        f,
		lock:     lock,
		readOnly: opts.ReadOnly,
		noSync:   opts.NoSync,
		now:      now,
		byID:     make(map[string]Entry),
		size:     size,
	}

	if size == 0 {
		if opts.ReadOnly || !opts.Create {
			closeAll()
			return nil, fmt.Errorf("%w: %s is empty", ErrMalformedHeader, path)
		}
		failCreate := func() {
			closeAll()
			if !existed {
				_ = os.Remove(path)
			}
		}
		header, herr := normalizeNewHeader(opts.Header, now)
		if herr != nil {
			failCreate()
			return nil, herr
		}
		line, merr := marshalLine(header)
		if merr != nil {
			failCreate()
			return nil, merr
		}
		if werr := writeAll(f, line); werr != nil {
			failCreate()
			return nil, werr
		}
		if !opts.NoSync {
			if serr := f.Sync(); serr != nil {
				failCreate()
				return nil, serr
			}
		}
		log.header = header
		log.size = int64(len(line))
		log.installEntries(nil)
		return log, nil
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		closeAll()
		return nil, err
	}
	header, entries, diags, err := ParseJSONL(f)
	if err != nil {
		if verr, ok := err.(*VersionError); ok {
			verr.Path = path
		}
		closeAll()
		return nil, err
	}
	log.header = header
	log.diagnostics = append(log.diagnostics, diags...)
	log.installEntries(entries)

	// A torn tail without a trailing newline would be glued to the next
	// append. Repair the separator once, leaving the partial line intact as its
	// own (diagnosed) line.
	if !opts.ReadOnly {
		needsNewline, nerr := lastByteIsNewline(f, size)
		if nerr != nil {
			closeAll()
			return nil, nerr
		}
		if !needsNewline {
			if _, serr := f.Seek(size, io.SeekStart); serr != nil {
				closeAll()
				return nil, serr
			}
			if werr := writeAll(f, []byte{'\n'}); werr != nil {
				closeAll()
				return nil, werr
			}
			if !opts.NoSync {
				if serr := f.Sync(); serr != nil {
					closeAll()
					return nil, serr
				}
			}
			size++
			log.size = size
		}
	}

	if _, err := f.Seek(log.size, io.SeekStart); err != nil {
		closeAll()
		return nil, err
	}
	return log, nil
}

func normalizeNewHeader(h Header, now func() time.Time) (Header, error) {
	if h.Type == "" {
		h.Type = HeaderType
	}
	if h.Type != HeaderType {
		return Header{}, fmt.Errorf("%w: header type %q, want %q", ErrMalformedHeader, h.Type, HeaderType)
	}
	if h.Version == 0 {
		h.Version = FormatVersion
	}
	if h.Version != FormatVersion {
		return Header{}, &VersionError{Version: h.Version, Want: FormatVersion}
	}
	if h.ID == "" {
		h.ID = NewID()
	}
	if h.Timestamp.IsZero() {
		h.Timestamp = now().UTC()
	}
	return h, nil
}

func (l *Log) installEntries(entries []Entry) {
	for _, e := range entries {
		l.entries = append(l.entries, e)
		l.byID[e.Base().ID] = e
		l.leafID = e.Base().ID
	}
}

func lastByteIsNewline(f *os.File, size int64) (bool, error) {
	if size == 0 {
		return true, nil
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], size-1); err != nil {
		return false, err
	}
	return b[0] == '\n', nil
}

func marshalLine(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

// NewID returns a short random entry id (8 hex characters, matching pi).
func NewID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%08x", uint32(time.Now().UnixNano()))
}

func (l *Log) newID() string {
	for i := 0; i < 1000; i++ {
		id := NewID()
		if _, exists := l.byID[id]; !exists {
			return id
		}
	}
	return fmt.Sprintf("%s-%d", NewID(), len(l.entries))
}

// Append writes one entry, assigning a missing id, timestamp and parent (the
// current leaf). The in-memory index is updated only after a successful write.
func (l *Log) Append(e Entry) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return "", fmt.Errorf("transcript: log is closed")
	}
	if l.readOnly {
		return "", ErrReadOnly
	}
	if e == nil || e.Base() == nil {
		return "", fmt.Errorf("transcript: cannot append a nil entry")
	}

	base := e.Base()
	origID := base.ID
	origParent := base.ParentID
	origTimestamp := base.Timestamp
	origType := base.Type
	rollbackEntry := func() {
		base.ID = origID
		base.ParentID = origParent
		base.Timestamp = origTimestamp
		base.Type = origType
	}

	base.Type = e.EntryType()
	if base.ParentID != nil && *base.ParentID == "" {
		base.ParentID = nil
	}
	if base.ID == "" {
		base.ID = l.newID()
	}
	if _, exists := l.byID[base.ID]; exists {
		rollbackEntry()
		return "", fmt.Errorf("transcript: duplicate entry id %q", base.ID)
	}
	if base.Timestamp.IsZero() {
		base.Timestamp = l.now().UTC()
	}
	if base.ParentID == nil && l.leafID != "" {
		parent := l.leafID
		base.ParentID = &parent
	}
	if base.ParentID != nil {
		if _, ok := l.byID[*base.ParentID]; !ok {
			rollbackEntry()
			return "", fmt.Errorf("%w: %s", ErrEntryNotFound, *base.ParentID)
		}
	}

	line, err := marshalLine(e)
	if err != nil {
		rollbackEntry()
		return "", err
	}
	start := l.size
	if err := writeAll(l.f, line); err != nil {
		l.rollbackWrite(start)
		rollbackEntry()
		return "", err
	}
	if !l.noSync {
		if err := l.f.Sync(); err != nil {
			l.rollbackWrite(start)
			rollbackEntry()
			return "", err
		}
	}
	l.size = start + int64(len(line))
	l.entries = append(l.entries, e)
	l.byID[base.ID] = e
	l.leafID = base.ID
	return base.ID, nil
}

// AppendMessage appends a message entry and returns its id.
func (l *Log) AppendMessage(m types.Message) (string, error) {
	return l.Append(&MessageEntry{Message: m})
}

func (l *Log) rollbackWrite(start int64) {
	_ = l.f.Truncate(start)
	_, _ = l.f.Seek(start, io.SeekStart)
}

// Sync flushes pending writes to disk.
func (l *Log) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.readOnly {
		return nil
	}
	return l.f.Sync()
}

// Close closes the log and releases the writer lock.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true

	var errs []error
	if l.f != nil {
		if !l.readOnly {
			if err := l.f.Sync(); err != nil {
				errs = append(errs, err)
			}
		}
		if err := l.f.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if l.lock != nil {
		if err := releaseLock(l.lock); err != nil {
			errs = append(errs, err)
		}
		if err := l.lock.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Header returns the session header.
func (l *Log) Header() Header {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.header
}

// Path returns the session file path.
func (l *Log) Path() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.path
}

// ReadOnly reports whether the log rejects appends.
func (l *Log) ReadOnly() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.readOnly
}

// Leaf returns the current leaf id ("" before the first entry).
func (l *Log) Leaf() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.leafID
}

// Len returns the number of entries currently indexed.
func (l *Log) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// Entries returns a copy of all entries in file order.
func (l *Log) Entries() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Entry, len(l.entries))
	copy(out, l.entries)
	return out
}

// Get looks up an entry by id.
func (l *Log) Get(id string) (Entry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.byID[id]
	return e, ok
}

// Diagnostics returns a copy of load-time diagnostics.
func (l *Log) Diagnostics() []Diagnostic {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Diagnostic, len(l.diagnostics))
	copy(out, l.diagnostics)
	return out
}

// Branch moves the in-memory leaf to id. Nothing is written until the next
// append, which becomes a child of id.
func (l *Log) Branch(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.byID[id]; !ok {
		return fmt.Errorf("%w: %s", ErrEntryNotFound, id)
	}
	l.leafID = id
	return nil
}

// ResetLeaf clears the leaf so the next append becomes a new root.
func (l *Log) ResetLeaf() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.leafID = ""
}

// PathTo returns the root→id path. An empty id means the current leaf.
func (l *Log) PathTo(id string) ([]Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pathToLocked(id)
}

func (l *Log) pathToLocked(id string) ([]Entry, error) {
	if id == "" {
		id = l.leafID
	}
	if id == "" {
		return nil, nil
	}
	var reversed []Entry
	seen := make(map[string]bool)
	for id != "" {
		if seen[id] {
			return nil, fmt.Errorf("transcript: parent cycle detected at entry %q", id)
		}
		seen[id] = true
		e, ok := l.byID[id]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrEntryNotFound, id)
		}
		reversed = append(reversed, e)
		if e.Base().ParentID == nil {
			break
		}
		id = *e.Base().ParentID
	}
	path := make([]Entry, len(reversed))
	for i := range reversed {
		path[len(reversed)-1-i] = reversed[i]
	}
	return path, nil
}
