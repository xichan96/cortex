package types

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func TruncateToolResult(content string, maxLen int, writeDir string) (display string, truncated bool, filePath string) {
	if maxLen <= 0 {
		maxLen = MaxTruncationLength
	}
	if len(content) <= maxLen {
		return content, false, ""
	}
	if writeDir == "" {
		return TruncateString(content, maxLen), true, ""
	}
	if err := os.MkdirAll(writeDir, 0750); err != nil {
		return TruncateString(content, maxLen), true, ""
	}
	name := fmt.Sprintf("tool_%d_%d.txt", time.Now().UnixNano(), len(content)%10000)
	path := filepath.Join(writeDir, name)
	if err := os.WriteFile(path, []byte(content), 0640); err != nil {
		return TruncateString(content, maxLen), true, ""
	}
	display = content[:maxLen] + "\n…(truncated). Full output saved to: " + path + " — use read/grep to view."
	return display, true, path
}
