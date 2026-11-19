package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xichan96/cortex/agent/skills"
	"github.com/xichan96/cortex/agent/types"
)

// LazySkillTool is the lazy-loading variant of SkillTool. It holds only the
// skill index (metadata) and reads a skill body on demand from disk, so
// startup never pays for bodies of skills that are never used.
//
// The tool keeps the "skill" name and the skill_name/input parameters of
// SkillTool, so existing prompts and hosts keep working unchanged.
type LazySkillTool struct {
	index *skills.Index
}

// NewLazySkillTool builds the skill tool backed by a metadata-only index.
// A nil index is tolerated: execution then reports that skills are
// unavailable instead of panicking.
func NewLazySkillTool(index *skills.Index) types.Tool {
	return &LazySkillTool{index: index}
}

func (t *LazySkillTool) Name() string {
	return "skill"
}

func (t *LazySkillTool) Description() string {
	return "Load a skill's instructions. Each skill contains specialized instructions; their bodies are read on demand. " +
		"Call this with the skill name before doing work the skill covers, then follow the returned instructions."
}

func (t *LazySkillTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"skill_name": map[string]interface{}{
				"type":        "string",
				"description": "The name of the skill to use",
			},
			"input": map[string]interface{}{
				"type":        "string",
				"description": "Input for the skill task",
			},
		},
		"required": []string{"skill_name"},
	}
}

// Execute resolves the skill, loads its body from disk, and renders the
// expansion block. Unknown skills and unreadable/invalid files return a
// recoverable error so the model can correct itself instead of aborting the
// turn.
func (t *LazySkillTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.index == nil {
		return nil, fmt.Errorf("skills are not available in this session")
	}

	skillName := firstString(input, "skill_name", "skillName", "name")
	if skillName == "" {
		return nil, fmt.Errorf("skill_name is required")
	}

	entry, ok := t.lookup(skillName)
	if !ok {
		return nil, fmt.Errorf("skill not found: %s (call the skill tool with a name advertised in <available_skills>)", skillName)
	}

	body, err := t.index.LoadBody(entry.Name)
	if err != nil {
		// Recoverable: the file may have been moved, deleted, or edited into an
		// invalid state. The model can retry after the host reloads skills.
		return nil, fmt.Errorf("skill %q could not be loaded: %w (the skill file may have changed; retry after skills are reloaded)", entry.Name, err)
	}

	return FormatSkillExpansion(entry, body, firstString(input, "input", "task", "query")), nil
}

func (t *LazySkillTool) lookup(name string) (*skills.Entry, bool) {
	if entry, ok := t.index.Get(name); ok {
		return entry, true
	}
	// Skill names are case-insensitive for model convenience; discovery keeps
	// the canonical spelling.
	for _, entry := range t.index.All() {
		if strings.EqualFold(entry.Name, name) {
			return entry, true
		}
	}
	return nil, false
}

func (t *LazySkillTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		ToolType: "builtin",
		Priority: 5,
	}
}

// FormatSkillExpansion renders a skill body the way the model should see it:
// the skill name, the absolute location, the directory relative references
// resolve against, the declared constraints (both human-readable lines and a
// machine-readable Policy line), and finally the body (plus the caller's input
// appended as <args> when present).
func FormatSkillExpansion(entry *skills.Entry, body, input string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Skill: %s\n", entry.Name)
	fmt.Fprintf(&b, "Location: %s\n", entry.Path)
	fmt.Fprintf(&b, "Skill directory (resolve relative paths from here): %s\n", entry.BaseDir)
	if tools := entry.AllowedTools(); len(tools) > 0 {
		fmt.Fprintf(&b, "Allowed tools: %s\n", strings.Join(tools, ", "))
	}
	if paths := entry.Paths(); len(paths) > 0 {
		fmt.Fprintf(&b, "Paths: %s\n", strings.Join(paths, ", "))
	}
	// The structured policy lets hosts and the model apply the constraints
	// without parsing the prose lines. It is additive: the body format below
	// and the skill_name/input parameters are unchanged.
	if policy := skills.FromEntry(entry); !policy.Empty() {
		if encoded, err := json.Marshal(policy); err == nil {
			fmt.Fprintf(&b, "Policy: %s\n", encoded)
		}
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "<skill name=%q location=%q>\n", entry.Name, entry.Path)
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("</skill>")
	if input != "" {
		b.WriteString("\n<args>")
		b.WriteString(input)
		b.WriteString("</args>")
	}
	return b.String()
}
