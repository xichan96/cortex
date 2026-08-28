package utils

import (
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		redacted  bool // 期望是否发生脱敏
		leakToken string // 若非空，断言该 token 不再出现
	}{
		{"api_key eq", "api_key=sk-abc123", true, "sk-abc123"},
		{"api_key colon", "API_KEY: 12345", true, "12345"},
		{"token eq", "token=abcdef", true, "abcdef"},
		{"password eq", "password = hunter2", true, "hunter2"},
		{"secret eq", "secret=xyz", true, "xyz"},
		{"authorization bearer", "Authorization: Bearer eyJhbGciOi", true, "eyJhbGciOi"},
		{"client_secret", "client_secret: foobar", true, "foobar"},
		{"plain Chinese", "用户喜欢用 Go 语言写程序", false, ""},
		{"token as word", "the token economy is growing", false, ""},
		{"postgres word", "project uses postgres", false, ""},
		{"mixed keeps context", "config: api_key=abc123, port=8080", true, "abc123"},
	}
	for _, c := range cases {
		got := RedactSecrets(c.in)
		if c.redacted {
			if !strings.Contains(got, "[REDACTED_SECRET]") {
				t.Errorf("%s: RedactSecrets(%q) = %q, want redaction", c.name, c.in, got)
			}
		} else {
			if got != c.in {
				t.Errorf("%s: RedactSecrets(%q) = %q, want unchanged", c.name, c.in, got)
			}
		}
		if c.leakToken != "" && strings.Contains(got, c.leakToken) {
			t.Errorf("%s: secret leaked in %q", c.name, got)
		}
	}
}

func TestRedactSecretsIdempotent(t *testing.T) {
	in := "api_key=abc123 token=def456 Authorization: Bearer eyJhbGciOi"
	once := RedactSecrets(in)
	twice := RedactSecrets(once)
	if once != twice {
		t.Fatalf("redact should be idempotent:\n once=%q\n twice=%q", once, twice)
	}
}
