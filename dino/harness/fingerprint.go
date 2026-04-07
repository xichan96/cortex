package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func DirTrackedFilesFingerprint(dir string, baseNames []string) (string, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", err
	}
	dirKey := filepath.Clean(dir)
	want := make(map[string]struct{})
	for _, n := range baseNames {
		n = strings.TrimSpace(n)
		if n != "" {
			want[n] = struct{}{}
		}
	}
	if len(want) == 0 {
		h := sha256.Sum256([]byte(dirKey))
		return hex.EncodeToString(h[:8]), nil
	}
	var paths []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		base := filepath.Base(path)
		if _, ok := want[base]; ok {
			paths = append(paths, path)
		}
		return nil
	})
	if len(paths) == 0 {
		h := sha256.Sum256([]byte(dirKey))
		return hex.EncodeToString(h[:8]), nil
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		_, _ = fmt.Fprintf(h, "%s:%d:%d\n", p, st.Size(), st.ModTime().UnixNano())
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16]), nil
}
