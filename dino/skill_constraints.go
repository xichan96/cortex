package dino

import (
	"strings"

	"github.com/xichan96/cortex/agent/skills"
	"github.com/xichan96/cortex/agent/types"
)

// PolicyForSkill resolves the constraint policy declared by a skill. The second
// result reports whether the skill exists; a nil index or an unknown name
// yields (zero policy, false). Lookup is exact first, then case-insensitive to
// match the lazy "skill" tool's name handling.
//
// PolicyForSkill is the read side of the round-scoped enforcement described in
// design 6.5. Wiring the resulting policy into the engine's tool list for the
// activation turn is intentionally outside this package's scope; hosts can use
// ApplySkillPolicy for that.
func PolicyForSkill(index *skills.Index, name string) (skills.SkillPolicy, bool) {
	if index == nil || strings.TrimSpace(name) == "" {
		return skills.SkillPolicy{}, false
	}
	if entry, ok := index.Get(name); ok {
		return skills.FromEntry(entry), true
	}
	for _, entry := range index.All() {
		if strings.EqualFold(entry.Name, name) {
			return skills.FromEntry(entry), true
		}
	}
	return skills.SkillPolicy{}, false
}

// ApplySkillPolicy narrows a tool list to the names permitted by a skill's
// allowed_tools declaration, preserving input order.
//
// An empty AllowedTools list means "no restriction": tools is returned as-is
// (the same slice, not a copy). Declared names that do not exist in tools are
// ignored; they are reported separately at startup by warnUnknownSkillTools so
// they are not silently dropped (design 6.5).
//
// The policy's Paths field does not affect the tool list; it governs which
// skills are advertisable (see skills.AdvertisableForPaths).
func ApplySkillPolicy(tools []types.Tool, policy skills.SkillPolicy) []types.Tool {
	if len(policy.AllowedTools) == 0 {
		return tools
	}

	permitted := make(map[string]struct{}, len(policy.AllowedTools))
	for _, name := range policy.AllowedTools {
		permitted[name] = struct{}{}
	}

	out := make([]types.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		if _, ok := permitted[tool.Name()]; ok {
			out = append(out, tool)
		}
	}
	return out
}
