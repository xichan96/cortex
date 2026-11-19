package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExpandSkillCommand expands an explicit skill invocation
// (/skill:name args) by reading the skill body from baseDir. baseDir may be
// either the directory holding SKILL.md or the skill file itself.
//
// The result is a <skill> block followed by the supplied arguments (when
// non-empty). disable-model-invocation does not affect explicit invocation.
func ExpandSkillCommand(name, args, baseDir string) (string, error) {
	skillPath, err := resolveSkillFilePath(baseDir)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return "", fmt.Errorf("read skill %q: %w", name, err)
	}
	doc, _, err := ParseDocument(data, skillPath)
	if err != nil {
		return "", fmt.Errorf("parse skill %q: %w", name, err)
	}
	return FormatSkillBlock(name, skillPath, filepath.Dir(skillPath), doc.Body, args), nil
}

// ExpandSkillCommand expands an explicit skill invocation using the index's
// lazily-loaded body. The skill must exist in the index; it may have
// disable-model-invocation set.
func (ix *Index) ExpandSkillCommand(name, args string) (string, error) {
	entry, ok := ix.Get(name)
	if !ok {
		return "", fmt.Errorf("skill %q not found", name)
	}
	body, err := ix.LoadBody(name)
	if err != nil {
		return "", err
	}
	return FormatSkillBlock(entry.Name, entry.Path, entry.BaseDir, body, args), nil
}

// FormatSkillBlock renders the wrapper used to inject a skill body into a
// conversation. Arguments are appended after the block, separated by a blank
// line, so the model can see the explicit user request.
func FormatSkillBlock(name, location, baseDir, body, args string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<skill name=\"%s\" location=\"%s\">\n",
		escapeSkillXML(name), escapeSkillXML(location))
	b.WriteString("References are relative to ")
	b.WriteString(baseDir)
	b.WriteString(".\n\n")
	if trimmed := strings.TrimSpace(body); trimmed != "" {
		b.WriteString(trimmed)
		b.WriteString("\n")
	}
	b.WriteString("</skill>")
	if trimmedArgs := strings.TrimSpace(args); trimmedArgs != "" {
		b.WriteString("\n\n")
		b.WriteString(trimmedArgs)
	}
	return b.String()
}

// resolveSkillFilePath accepts either a skill file or a directory containing
// SKILL.md (case-insensitively, for compatibility with older layouts).
func resolveSkillFilePath(baseDir string) (string, error) {
	info, err := os.Stat(baseDir)
	if err == nil && !info.IsDir() {
		return baseDir, nil
	}

	direct := filepath.Join(baseDir, SkillFileName)
	if info, err := os.Stat(direct); err == nil && !info.IsDir() {
		return direct, nil
	}

	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return "", fmt.Errorf("skill directory %q: %w", baseDir, err)
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), SkillFileName) && !entry.IsDir() {
			return filepath.Join(baseDir, entry.Name()), nil
		}
	}
	return "", fmt.Errorf("no %s found in %q", SkillFileName, baseDir)
}

func escapeSkillXML(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(s)
}
