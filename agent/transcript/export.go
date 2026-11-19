package transcript

import (
	"fmt"
	"io"
)

// ExportBranch writes the current branch as JSONL: the session header followed
// by root→leaf entries. The output can be fed to Import.
func (l *Log) ExportBranch(w io.Writer) error {
	return l.ExportBranchFrom(w, "")
}

// ExportBranchFrom writes the root→entryID branch. An empty entryID means the
// current leaf.
func (l *Log) ExportBranchFrom(w io.Writer, entryID string) error {
	if w == nil {
		return fmt.Errorf("transcript: export writer is nil")
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return fmt.Errorf("transcript: log is closed")
	}
	path, err := l.pathToLocked(entryID)
	if err != nil {
		return err
	}
	headerLine, err := marshalLine(l.header)
	if err != nil {
		return err
	}
	if err := writeAll(w, headerLine); err != nil {
		return err
	}
	for _, entry := range path {
		line, merr := marshalLine(entry)
		if merr != nil {
			return merr
		}
		if werr := writeAll(w, line); werr != nil {
			return werr
		}
	}
	return nil
}
