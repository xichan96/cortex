package sandbox

import (
	"context"
	"os"
)

// DirLister is an optional Sandbox extension implemented by backends that can
// enumerate directory entries. The core Sandbox interface only exposes Stat, so
// tools that need directory entries discover this capability with a
// type assertion and report a clear error when a backend does not support it.
//
// Paths follow the same confinement rules as ReadFile/Stat: relative paths are
// resolved against Root and escapes are rejected with ErrPathEscape.
type DirLister interface {
	ReadDir(ctx context.Context, path string) ([]FileInfo, error)
}

var _ DirLister = (*HostSandbox)(nil)

// ReadDir lists path inside the root. Entries whose metadata cannot be read are
// skipped, matching the legacy directory-listing behavior.
func (s *HostSandbox) ReadDir(ctx context.Context, path string) ([]FileInfo, error) {
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
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, fileInfoFromOS(info))
	}
	return out, nil
}
