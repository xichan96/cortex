// Package home resolves the cortex agent home directory layout and loads the
// layered configuration, trust decisions, and context files that live in it.
//
// The package depends only on the standard library (plus yaml for settings
// compatibility) so that every higher layer — engine, runtime, dino, CLI — can
// import it without creating cycles.
package home

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvCortexHome overrides the default agent home (~/.cortex).
const EnvCortexHome = "CORTEX_HOME"

// EnvSessionDir overrides the default session directory.
const EnvSessionDir = "CORTEX_SESSION_DIR"

// Options controls home resolution. Every field is optional; empty values fall
// through to the environment and then to built-in defaults.
type Options struct {
	// Home overrides the agent home root. Expanded via ExpandPath.
	Home string
	// CWD is the workspace directory. Defaults to the process working directory.
	CWD string
	// SessionDir overrides the session directory. Defaults to
	// CORTEX_SESSION_DIR, then <Root>/sessions.
	SessionDir string
}

// Paths is the resolved agent home layout for one workspace.
//
// Root and AgentDir both name the agent home root; AgentDir is kept as the
// canonical spelling used by the design document ("<agent-dir>"), while Root is
// the short name used by callers that treat it as a filesystem root.
type Paths struct {
	Root     string
	AgentDir string
	CWD      string

	// User-level configuration files.
	SettingsPath           string
	ModelsPath             string
	ModelsStorePath        string
	MCPPath                string
	MCPAuthPath            string
	AuthPath               string
	TrustPath              string
	PermissionsPath        string
	SystemPromptPath       string
	AppendSystemPromptPath string

	// User-level resource directories.
	SkillsDir      string
	PromptsDir     string
	AgentsDir      string
	PluginsDir     string
	MemoryDir      string
	SessionsDir    string
	CacheDir       string
	TracesDir      string
	LogsDir        string
	BackupsDir     string
	FileHistoryDir string
	TmpDir         string
	BinDir         string

	// Project-level files and directories inside <cwd>/.cortex. Project
	// configuration is only read from the current working directory; there is
	// deliberately no upward search.
	ProjectDir                    string
	ProjectSettingsPath           string
	ProjectMCPPath                string
	ProjectSystemPromptPath       string
	ProjectAppendSystemPromptPath string
	ProjectSkillsDir              string
	ProjectPromptsDir             string
	ProjectAgentsDir              string
	ProjectPluginsDir             string
	ProjectMemoryDir              string
}

// Resolve builds the agent home layout. Resolution order for the root is
// Options.Home, then CORTEX_HOME, then ~/.cortex.
func Resolve(opts Options) (Paths, error) {
	root := strings.TrimSpace(opts.Home)
	if root == "" {
		root = strings.TrimSpace(os.Getenv(EnvCortexHome))
	}
	if root == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, fmt.Errorf("home: resolve user home: %w", err)
		}
		root = filepath.Join(homeDir, ".cortex")
	}
	root, err := ExpandPath(root)
	if err != nil {
		return Paths{}, fmt.Errorf("home: resolve agent home %q: %w", opts.Home, err)
	}

	cwd := strings.TrimSpace(opts.CWD)
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return Paths{}, fmt.Errorf("home: resolve working directory: %w", err)
		}
	}
	cwd, err = ExpandPath(cwd)
	if err != nil {
		return Paths{}, fmt.Errorf("home: resolve working directory %q: %w", cwd, err)
	}

	sessionDir := strings.TrimSpace(opts.SessionDir)
	if sessionDir == "" {
		sessionDir = strings.TrimSpace(os.Getenv(EnvSessionDir))
	}
	if sessionDir == "" {
		sessionDir = filepath.Join(root, "sessions")
	} else {
		sessionDir, err = ExpandPath(sessionDir)
		if err != nil {
			return Paths{}, fmt.Errorf("home: resolve session directory: %w", err)
		}
	}

	projectDir := filepath.Join(cwd, ".cortex")
	join := filepath.Join

	p := Paths{
		Root:     root,
		AgentDir: root,
		CWD:      cwd,

		SettingsPath:           join(root, "settings.json"),
		ModelsPath:             join(root, "models.json"),
		ModelsStorePath:        join(root, "models-store.json"),
		MCPPath:                join(root, "mcp.json"),
		MCPAuthPath:            join(root, "mcp-auth.json"),
		AuthPath:               join(root, "auth.json"),
		TrustPath:              join(root, "trust.json"),
		PermissionsPath:        join(root, "permissions.json"),
		SystemPromptPath:       join(root, "SYSTEM.md"),
		AppendSystemPromptPath: join(root, "APPEND_SYSTEM.md"),

		SkillsDir:      join(root, "skills"),
		PromptsDir:     join(root, "prompts"),
		AgentsDir:      join(root, "agents"),
		PluginsDir:     join(root, "plugins"),
		MemoryDir:      join(root, "memory"),
		SessionsDir:    sessionDir,
		CacheDir:       join(root, "cache"),
		TracesDir:      join(root, "traces"),
		LogsDir:        join(root, "logs"),
		BackupsDir:     join(root, "backups"),
		FileHistoryDir: join(root, "file-history"),
		TmpDir:         join(root, "tmp"),
		BinDir:         join(root, "bin"),

		ProjectDir:                    projectDir,
		ProjectSettingsPath:           join(projectDir, "settings.json"),
		ProjectMCPPath:                join(projectDir, "mcp.json"),
		ProjectSystemPromptPath:       join(projectDir, "SYSTEM.md"),
		ProjectAppendSystemPromptPath: join(projectDir, "APPEND_SYSTEM.md"),
		ProjectSkillsDir:              join(projectDir, "skills"),
		ProjectPromptsDir:             join(projectDir, "prompts"),
		ProjectAgentsDir:              join(projectDir, "agents"),
		ProjectPluginsDir:             join(projectDir, "plugins"),
		ProjectMemoryDir:              join(projectDir, "memory"),
	}
	return p, nil
}

// ExpandPath expands a leading "~" and makes the path absolute and clean.
func ExpandPath(path string) (string, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", nil
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if p == "~" {
			p = homeDir
		} else {
			p = filepath.Join(homeDir, p[2:])
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// EncodeCWD encodes a working directory into a single safe path segment.
// A leading slash is dropped and every "/", "\" and ":" is replaced by "-".
//
//	/Users/me/project => Users-me-project
func EncodeCWD(cwd string) string {
	c := filepath.ToSlash(filepath.Clean(cwd))
	c = strings.TrimPrefix(c, "/")
	replacer := strings.NewReplacer("/", "-", `\`, "-", ":", "-")
	return replacer.Replace(c)
}

// SessionDirName returns the per-workspace session directory name:
// --<encoded-cwd>--.
func SessionDirName(cwd string) string {
	return "--" + EncodeCWD(cwd) + "--"
}

// SessionDirForCWD returns the session directory for cwd relative to an agent
// home, i.e. sessions/--<encoded-cwd>--. Empty cwd falls back to the process
// working directory.
func SessionDirForCWD(cwd string) string {
	if strings.TrimSpace(cwd) == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}
	return filepath.Join("sessions", SessionDirName(cwd))
}

// SessionPath returns the session file path relative to the agent home:
// sessions/--<encoded-cwd>--/<timestamp>_<session-id>.jsonl.
//
// Characters that are not portable in file names (":" and "." in ISO
// timestamps, path separators) are replaced by "-". This mirrors pi's on-disk
// naming so existing session directories stay readable.
func SessionPath(cwd, timestamp, sessionID string) string {
	return filepath.Join(SessionDirForCWD(cwd), sessionFileName(timestamp, sessionID))
}

// SessionFilePath returns the absolute session file path for this layout,
// honoring a custom session directory.
func (p Paths) SessionFilePath(timestamp, sessionID string) string {
	return filepath.Join(p.SessionsDir, SessionDirName(p.CWD), sessionFileName(timestamp, sessionID))
}

// SessionDir returns the absolute per-workspace session directory.
func (p Paths) SessionDir() string {
	return filepath.Join(p.SessionsDir, SessionDirName(p.CWD))
}

func sessionFileName(timestamp, sessionID string) string {
	return sanitizeTimestamp(timestamp) + "_" + sanitizeSessionID(sessionID) + ".jsonl"
}

func sanitizeTimestamp(timestamp string) string {
	ts := strings.TrimSpace(timestamp)
	replacer := strings.NewReplacer(":", "-", ".", "-", "/", "-", `\`, "-")
	return replacer.Replace(ts)
}

func sanitizeSessionID(id string) string {
	s := strings.TrimSpace(id)
	if s == "" {
		return "session"
	}
	replacer := strings.NewReplacer("/", "-", `\`, "-", ":", "-")
	return replacer.Replace(s)
}
