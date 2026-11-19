package home

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveHomePrecedence(t *testing.T) {
	envHome := t.TempDir()
	optionHome := t.TempDir()
	cwd := t.TempDir()
	t.Setenv(EnvCortexHome, envHome)

	p, err := Resolve(Options{CWD: cwd})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wantEnv, _ := filepath.Abs(envHome)
	if p.Root != wantEnv || p.AgentDir != wantEnv {
		t.Fatalf("CORTEX_HOME not used: root=%q agentDir=%q want %q", p.Root, p.AgentDir, wantEnv)
	}

	p, err = Resolve(Options{Home: optionHome, CWD: cwd})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wantOption, _ := filepath.Abs(optionHome)
	if p.Root != wantOption {
		t.Fatalf("Options.Home should win: root=%q want %q", p.Root, wantOption)
	}
}

func TestResolveDefaultHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvCortexHome, "")
	cwd := t.TempDir()

	p, err := Resolve(Options{CWD: cwd})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := filepath.Join(home, ".cortex")
	if p.Root != want {
		t.Fatalf("default home = %q, want %q", p.Root, want)
	}
}

func TestResolveLayout(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	p, err := Resolve(Options{Home: home, CWD: cwd})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if p.ProjectDir != filepath.Join(cwd, ".cortex") {
		t.Fatalf("ProjectDir = %q", p.ProjectDir)
	}
	if p.SessionsDir != filepath.Join(p.Root, "sessions") {
		t.Fatalf("SessionsDir = %q", p.SessionsDir)
	}
	if p.SkillsDir != filepath.Join(p.Root, "skills") {
		t.Fatalf("SkillsDir = %q", p.SkillsDir)
	}
	if p.SessionDir() != filepath.Join(p.SessionsDir, "--"+EncodeCWD(cwd)+"--") {
		t.Fatalf("SessionDir() = %q", p.SessionDir())
	}
}

func TestResolveSessionDirOverride(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	envSessions := t.TempDir()
	optionSessions := t.TempDir()

	t.Setenv(EnvSessionDir, envSessions)
	p, err := Resolve(Options{Home: home, CWD: cwd})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wantEnv, _ := filepath.Abs(envSessions)
	if p.SessionsDir != wantEnv {
		t.Fatalf("CORTEX_SESSION_DIR not used: %q", p.SessionsDir)
	}

	p, err = Resolve(Options{Home: home, CWD: cwd, SessionDir: optionSessions})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wantOption, _ := filepath.Abs(optionSessions)
	if p.SessionsDir != wantOption {
		t.Fatalf("Options.SessionDir should win: %q", p.SessionsDir)
	}
}

func TestProjectDirDoesNotSearchUpward(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(filepath.Join(root, ".cortex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	p, err := Resolve(Options{Home: home, CWD: nested})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.ProjectDir != filepath.Join(nested, ".cortex") {
		t.Fatalf("ProjectDir = %q; must only use cwd", p.ProjectDir)
	}
	if p.ProjectDir == filepath.Join(root, ".cortex") {
		t.Fatal("ProjectDir searched upward")
	}
}

func TestEncodeCWD(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"/Users/me/project", "Users-me-project"},
		{"/", ""},
		{"relative/path", "relative-path"},
		{`C:\work\proj`, "C--work-proj"},
		{"/tmp/a b", "tmp-a b"},
	}
	for _, tt := range tests {
		if got := EncodeCWD(tt.in); got != tt.want {
			t.Errorf("EncodeCWD(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSessionPath(t *testing.T) {
	got := SessionPath("/Users/me/project", "2026-11-19T10:00:00.000Z", "abc-123")
	want := filepath.Join("sessions", "--Users-me-project--", "2026-11-19T10-00-00-000Z_abc-123.jsonl")
	if got != want {
		t.Fatalf("SessionPath = %q, want %q", got, want)
	}

	// A session id with separators must never escape the session directory.
	got = SessionPath("/w", "ts", "../evil")
	if filepath.Dir(got) != filepath.Join("sessions", "--w--") {
		t.Fatalf("SessionPath escaped its directory: %q", got)
	}
	if filepath.Base(got) != "ts_..-evil.jsonl" {
		t.Fatalf("SessionPath sanitization = %q", got)
	}
}

func TestPathsSessionFilePathHonorsCustomSessionsDir(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	sessions := t.TempDir()
	p, err := Resolve(Options{Home: home, CWD: cwd, SessionDir: sessions})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wantSessions, _ := filepath.Abs(sessions)
	want := filepath.Join(wantSessions, "--"+EncodeCWD(cwd)+"--", "2026-01-02T03-04-05Z_s1.jsonl")
	if got := p.SessionFilePath("2026-01-02T03:04:05Z", "s1"); got != want {
		t.Fatalf("SessionFilePath = %q, want %q", got, want)
	}
}
