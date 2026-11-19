package dino

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/xichan96/cortex/agent/home"
	agentskills "github.com/xichan96/cortex/agent/skills"
	"github.com/xichan96/cortex/pkg/logger"
)

// homeEnv is the resolved agent-home state a factory keeps for the lifetime of
// its sessions. It is intentionally read-only after construction: sessions
// share the same context files and skill roots.
type homeEnv struct {
	paths    home.Paths
	resolved *home.Resolved
	context  []home.ContextFile

	systemPrompt       string
	appendSystemPrompt string
	trusted            bool

	// Resources is the discovered prompts/agents/plugins/models/auth/memory
	// snapshot. Discovery is additive: no session wiring consumes it yet.
	Resources HomeResources
}

// loadHomeEnv resolves the ~/.cortex + <cwd>/.cortex layout and loads the
// project layers. A nil result means HomeConfig.Enabled is false.
//
// Project-provided executable resources (settings, skills, SYSTEM.md) are only
// loaded when the trust decision is TrustAllowed. The decision follows the
// agent/home order — CLI override > TrustHandler > trust.json > ProjectTrust —
// evaluated in non-interactive mode, so "ask" and "never" both deny unless an
// override or handler says otherwise. Trust resolution failures are
// fail-closed: they deny project resources but never fail factory construction.
// Context files are content, not executable resources, and load regardless of
// trust (design §2.3).
func loadHomeEnv(cfg HomeConfig) (*homeEnv, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	paths, err := home.Resolve(home.Options{
		Home:       cfg.Root,
		CWD:        cfg.CWD,
		SessionDir: cfg.SessionDir,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve agent home: %w", err)
	}

	trusted := resolveProjectTrust(cfg, paths)
	resolved, err := home.LoadPaths(paths, home.LoadOptions{ProjectUntrusted: !trusted})
	if err != nil {
		return nil, fmt.Errorf("load agent settings: %w", err)
	}
	for _, d := range resolved.Diagnostics {
		logger.Warn("[DinoFactory] agent home setting", slog.String("diagnostic", d.Error()))
	}

	contextFiles, diags, err := home.LoadContextFiles(context.Background(), paths)
	if err != nil {
		return nil, fmt.Errorf("load agent context: %w", err)
	}
	for _, d := range diags {
		logger.Warn("[DinoFactory] agent home context", slog.String("diagnostic", d.Error()))
	}

	systemPrompt, appendPrompt := loadHomeSystemPrompts(paths, trusted)
	resources := loadHomeResources(paths, trusted)
	for _, d := range resources.Diagnostics {
		if d.Severity >= home.SeverityWarning {
			logger.Warn("[DinoFactory] agent home resource", slog.String("diagnostic", d.Error()))
		}
	}
	return &homeEnv{
		paths:              paths,
		resolved:           resolved,
		context:            contextFiles,
		systemPrompt:       systemPrompt,
		appendSystemPrompt: appendPrompt,
		trusted:            trusted,
		Resources:          resources,
	}, nil
}

// resolveProjectTrust evaluates the trust decision chain for the resolved
// workspace. Any error or unresolved (TrustUnknown) decision fails closed to
// untrusted: project resources are skipped and a warning is logged, but the
// factory is still constructed with user-level resources.
func resolveProjectTrust(cfg HomeConfig, paths home.Paths) bool {
	decision, err := home.ResolveTrust(context.Background(), home.TrustRequest{
		CWD:     paths.CWD,
		Reasons: projectTrustReasons(paths),
		Mode:    home.TrustModeNonInteractive,
	}, home.NewFileTrustStore(paths.TrustPath), home.TrustOptions{
		CLIOverride:   cfg.TrustOverride,
		Handler:       cfg.TrustHandler,
		DefaultPolicy: home.ParseTrustPolicy(cfg.ProjectTrust),
	})
	if err != nil {
		logger.Warn("[DinoFactory] project trust resolution failed; loading user resources only",
			slog.String("cwd", paths.CWD), slog.String("error", err.Error()))
		return false
	}
	switch decision {
	case home.TrustAllowed:
		return true
	case home.TrustDenied:
		return false
	default:
		logger.Warn("[DinoFactory] project trust unresolved; loading user resources only",
			slog.String("cwd", paths.CWD))
		return false
	}
}

// projectTrustReasons lists the project-provided executable resources that
// actually exist for this workspace. The list is informational (handlers and
// prompts show it) and does not influence the default decision.
func projectTrustReasons(paths home.Paths) []string {
	candidates := []string{
		paths.ProjectSettingsPath,
		paths.ProjectMCPPath,
		paths.ProjectSkillsDir,
		paths.ProjectPromptsDir,
		paths.ProjectAgentsDir,
		paths.ProjectPluginsDir,
		paths.ProjectSystemPromptPath,
		paths.ProjectAppendSystemPromptPath,
	}
	reasons := make([]string, 0, len(candidates))
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			reasons = append(reasons, p)
		}
	}
	return reasons
}

// loadHomeSystemPrompts reads SYSTEM.md / APPEND_SYSTEM.md. A trusted project
// file replaces the user-level file of the same name; files are never merged.
func loadHomeSystemPrompts(paths home.Paths, trusted bool) (systemPrompt, appendPrompt string) {
	systemPrompt = readOptionalFile(paths.SystemPromptPath)
	appendPrompt = readOptionalFile(paths.AppendSystemPromptPath)

	if trusted {
		if p := readOptionalFile(paths.ProjectSystemPromptPath); p != "" {
			systemPrompt = p
		}
		if p := readOptionalFile(paths.ProjectAppendSystemPromptPath); p != "" {
			appendPrompt = p
		}
	}
	return systemPrompt, appendPrompt
}

func readOptionalFile(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("[DinoFactory] read agent home file",
				slog.String("path", path), slog.String("error", err.Error()))
		}
		return ""
	}
	return strings.TrimSpace(string(data))
}

// skillRoots returns the home-provided skill roots in priority order. The
// caller appends them after explicitly configured roots, so config still wins
// on name collisions (LoadSkillsFromDirs is first-wins).
func (h *homeEnv) skillRoots() []string {
	if h == nil {
		return nil
	}
	roots := []string{
		h.paths.SkillsDir,
		filepath.Join(h.paths.CWD, ".agents", "skills"),
	}
	if userHome, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(userHome, ".agents", "skills"))
	}
	if h.trusted {
		roots = append(roots, h.paths.ProjectSkillsDir)
	}

	// Only existing directories: the skill loader warns for every missing root,
	// and most users will not have every conventional location populated.
	existing := make([]string, 0, len(roots))
	for _, root := range roots {
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			existing = append(existing, root)
		}
	}
	return existing
}

// composeSystemPrompt is the single place that assembles the system prompt for
// a new session. Keeping it separate from CreateSession makes the ordering
// testable without constructing a full engine.
//
// Order: base (or SYSTEM.md) → APPEND_SYSTEM.md → context files → session
// extra → skill index. Context files already present verbatim in the base are
// skipped, so a host that also injected AGENTS.md via hostconfig does not get
// duplicates.
func (f *dinoFactory) composeSystemPrompt(extra string) string {
	base := f.config.SystemPrompt
	appendPrompt := ""
	if f.home != nil {
		if f.home.systemPrompt != "" {
			base = f.home.systemPrompt
		}
		appendPrompt = f.home.appendSystemPrompt
	}

	var b strings.Builder
	b.WriteString(base)
	if appendPrompt != "" {
		b.WriteString("\n\n")
		b.WriteString(appendPrompt)
	}
	if f.home != nil {
		b.WriteString(renderHomeContext(f.home.context, b.String()))
	}
	if extra = strings.TrimSpace(extra); extra != "" {
		b.WriteString("\n\n# 本次会话的额外要求\n")
		b.WriteString(extra)
	}
	if len(f.cortexSkills) > 0 {
		b.WriteString(agentskills.BuildSystemPromptInjectionWithTriggers(f.cortexSkills))
	}
	return b.String()
}

func renderHomeContext(files []home.ContextFile, existing string) string {
	var b strings.Builder
	for _, cf := range files {
		content := strings.TrimSpace(cf.Content)
		if content == "" || strings.Contains(existing, content) {
			continue
		}
		b.WriteString("\n\n<project_instructions path=\"")
		b.WriteString(html.EscapeString(cf.Path))
		b.WriteString("\">\n")
		b.WriteString(content)
		b.WriteString("\n</project_instructions>")
	}
	return b.String()
}

func appendUniqueStrings(dst []string, values ...string) []string {
	seen := make(map[string]struct{}, len(dst)+len(values))
	for _, v := range dst {
		seen[v] = struct{}{}
	}
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		dst = append(dst, v)
	}
	return dst
}
