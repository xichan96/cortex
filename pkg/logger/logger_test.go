package logger

import (
	"os"
	"strings"
	"testing"
)

func TestLogger(t *testing.T) {
	// Test File Output
	tmpFile, err := os.CreateTemp("", "test_log_*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	cfg := &LoggerConfig{
		FilePath: tmpFile.Name(),
	}
	l := NewLoggerWithConfig(cfg)
	l.Info("test message")

	// Read file content
	content, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "test message") {
		t.Errorf("Expected log file to contain 'test message', got %s", string(content))
	}

	// Test Silent
	cfgSilent := &LoggerConfig{
		Silent: true,
	}
	lSilent := NewLoggerWithConfig(cfgSilent)

	// Capture stdout/stderr? Silent writes to io.Discard, so we can't capture it easily unless we mock.
	// But we can check internal logger handler if we exposed it, or just rely on no panic.
	// Here we just ensure it doesn't panic.
	lSilent.Info("should be silent")
}

func TestDefaultLogger(t *testing.T) {
	l := NewLogger()
	if l == nil {
		t.Fatal("NewLogger() returned nil")
	}
}
