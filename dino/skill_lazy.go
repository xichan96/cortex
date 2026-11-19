package dino

import (
	"context"
	"fmt"
	"log/slog"

	agentskills "github.com/xichan96/cortex/agent/skills"
	dinoTools "github.com/xichan96/cortex/dino/tools"
	"github.com/xichan96/cortex/pkg/logger"
)

// skillIndexPrompt renders the metadata-only skill index injected into the
// system prompt. Bodies are never included; the model loads them through the
// "skill" tool (or by reading the advertised location).
//
// The index lives in dino/skill_lazy.go and is appended by CreateSession
// rather than by composeSystemPrompt (dino/home.go): the latter determines the
// ordering of the static prompt layers, while the skill index refreshes with
// ReloadSkills.
func (f *dinoFactory) skillIndexPrompt() string {
	if f.skillIndex == nil {
		return ""
	}
	return agentskills.FormatAvailableSkillsDetailed(f.skillIndex.Advertisable())
}

// SkillIndex returns the metadata-only skill index (nil when skills are not
// loaded). Hosts use it to list skills and their constraints (allowed-tools,
// paths) without reading bodies.
func (f *dinoFactory) SkillIndex() *agentskills.Index {
	return f.skillIndex
}

// ReloadSkills re-scans the configured roots and replaces the skill index.
// Newly added skills become visible, deleted ones disappear, and changed
// bodies are picked up by LoadBody on the next call. It is a no-op when
// skills were not loaded.
func (f *dinoFactory) ReloadSkills(ctx context.Context) error {
	if f.skillIndex == nil {
		return nil
	}
	diags, err := f.skillIndex.Reload(ctx)
	logSkillDiagnostics(diags)
	if err != nil {
		return fmt.Errorf("reload skills: %w", err)
	}
	if f.tools != nil {
		warnUnknownSkillTools(f.tools, f.skillIndex.All())
	}
	return nil
}

// SkillAllowedTools returns the allowed-tools declared by one skill. The
// second result is false when the skill is unknown or unrestricted. This is
// the read side of the round-scoped allowed_tools enforcement; see
// RestrictToolNames for the filtering half.
func (f *dinoFactory) SkillAllowedTools(name string) ([]string, bool) {
	if f.skillIndex == nil {
		return nil, false
	}
	entry, ok := f.skillIndex.Get(name)
	if !ok {
		return nil, false
	}
	tools := entry.AllowedTools()
	return tools, len(tools) > 0
}

// RestrictToolNames returns the subset of available tool names permitted by a
// skill's allowed_tools declaration.
//
// An empty allowed list means "no restriction" and every available name is
// returned (as a copy). Names are matched exactly; allowed names that do not
// exist in available are ignored — they are reported at startup by
// warnUnknownSkillTools instead. The order of available is preserved.
//
// Round-level enforcement (limiting the tool set of the turn that invoked a
// skill) lives in the engine and is out of scope for P2-A; this helper is the
// building block for it.
func RestrictToolNames(available, allowed []string) []string {
	if len(allowed) == 0 {
		return append([]string(nil), available...)
	}
	permitted := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		permitted[name] = struct{}{}
	}
	out := make([]string, 0, len(available))
	for _, name := range available {
		if _, ok := permitted[name]; ok {
			out = append(out, name)
		}
	}
	return out
}

// logSkillDiagnostics surfaces discovery diagnostics (name collisions,
// missing descriptions, unreadable files) at warning level.
func logSkillDiagnostics(diags []agentskills.Diagnostic) {
	for _, d := range diags {
		logger.Warn("skill diagnostic",
			slog.String("kind", d.Kind),
			slog.String("path", d.Path),
			slog.String("message", d.Message))
	}
}

// warnUnknownSkillTools warns about allowed-tools entries that name a tool the
// factory does not know. Design 6.5 requires an explicit warning rather than
// silently ignoring unknown declarations.
func warnUnknownSkillTools(registry *dinoTools.Registry, entries []*agentskills.Entry) {
	if registry == nil {
		return
	}
	for _, entry := range entries {
		for _, name := range entry.AllowedTools() {
			if _, err := registry.Get(name); err != nil {
				logger.Warn("skill declares unknown tool in allowed-tools",
					slog.String("skill", entry.Name),
					slog.String("tool", name))
			}
		}
	}
}
