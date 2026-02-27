package skills

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xichan96/cortex/pkg/logger"
	"gopkg.in/yaml.v3"
)

// LoadSkillsFromDirs scans the provided directories for SKILL.md files and loads them.
func LoadSkillsFromDirs(ctx context.Context, l *logger.Logger, dirs []string) ([]Skill, error) {
	var skills []Skill

	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		absDir, err := filepath.Abs(dir)
		if err != nil {
			if l != nil {
				l.Warn(fmt.Sprintf("could not get absolute path for %s: %v", dir, err))
			}
			absDir = dir
		}

		err = filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil {
				// Log error but continue walking
				if l != nil {
					l.Warn(fmt.Sprintf("error accessing path %q: %v", path, err))
				}
				return nil
			}

			if !info.IsDir() && strings.EqualFold(info.Name(), "SKILL.MD") {
				skill, err := loadSkillFromFile(path)
				if err != nil {
					// Log error but continue
					if l != nil {
						l.Warn(fmt.Sprintf("skipping invalid skill file %s: %v", path, err))
					}
					return nil
				}
				skills = append(skills, skill)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("error walking directory %s: %w", dir, err)
		}
	}

	return skills, nil
}

// loadSkillFromFile reads a SKILL.md file and extracts metadata from the frontmatter.
func loadSkillFromFile(path string) (Skill, error) {
	file, err := os.Open(path)
	if err != nil {
		return Skill{}, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var frontmatterLines []string
	var contentLines []string
	inFrontmatter := false
	lineCount := 0
	frontmatterDone := false

	// Safety limit
	const maxLines = 20000

	for scanner.Scan() {
		line := scanner.Text()
		lineCount++
		if lineCount > maxLines {
			break
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			if !inFrontmatter && !frontmatterDone {
				if lineCount == 1 {
					inFrontmatter = true
					continue
				}
			} else if inFrontmatter {
				inFrontmatter = false
				frontmatterDone = true
				continue
			}
		}

		if inFrontmatter {
			frontmatterLines = append(frontmatterLines, line)
		} else {
			contentLines = append(contentLines, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return Skill{}, err
	}

	if inFrontmatter {
		return Skill{}, fmt.Errorf("frontmatter was not closed with '---' in %s", path)
	}

	// Parse Frontmatter
	var meta map[string]interface{}
	if len(frontmatterLines) > 0 {
		if err := yaml.Unmarshal([]byte(strings.Join(frontmatterLines, "\n")), &meta); err != nil {
			return Skill{}, fmt.Errorf("failed to parse frontmatter in %s: %w", path, err)
		}
	}

	skill := Skill{
		Path:     path,
		Priority: 100,
		Timeout:  60 * time.Second,
		Content:  strings.TrimSpace(strings.Join(contentLines, "\n")),
		Metadata: meta,
	}

	if name, ok := meta["name"].(string); ok {
		skill.Name = name
	} else {
		skill.Name = filepath.Base(filepath.Dir(path))
	}

	if desc, ok := meta["description"].(string); ok {
		skill.Description = desc
	}

	// Use helper functions to parse metadata into struct fields
	parseTriggers(meta, &skill)
	parseMeta(meta, &skill)

	return skill, nil
}

func parseTriggers(meta map[string]interface{}, spec *Skill) {
	raw, ok := meta["triggers"]
	if !ok {
		return
	}
	list, ok := raw.([]interface{})
	if !ok {
		return
	}
	for _, item := range list {
		var t Trigger
		// Handle map[string]interface{} (common in yaml.v3)
		if m, ok := item.(map[string]interface{}); ok {
			if v, ok := m["type"].(string); ok {
				t.Type = v
			}
			if v, ok := m["pattern"].(string); ok {
				t.Pattern = v
			}
		} else if m, ok := item.(map[interface{}]interface{}); ok {
			// Handle map[interface{}]interface{} (possible in other yaml parsers)
			if v, ok := m["type"].(string); ok {
				t.Type = v
			}
			if v, ok := m["pattern"].(string); ok {
				t.Pattern = v
			}
		}

		if t.Type != "" && t.Pattern != "" {
			spec.Triggers = append(spec.Triggers, t)
		}
	}
}

func parseMeta(meta map[string]interface{}, spec *Skill) {
	// Helper to safely get int from int or float64
	getInt := func(key string) int {
		if v, ok := meta[key]; ok {
			if i, ok := v.(int); ok {
				return i
			}
			if f, ok := v.(float64); ok {
				return int(f)
			}
		}
		return 0
	}

	if v := getInt("priority"); v != 0 {
		spec.Priority = v
	}
	if v := getInt("retries"); v != 0 {
		spec.Retries = v
	}

	if v, ok := meta["fallback"].(string); ok {
		spec.Fallback = v
	}
	if v, ok := meta["timeout"].(string); ok {
		if d, err := time.ParseDuration(v); err == nil {
			spec.Timeout = d
		}
	}
	if v, ok := meta["tags"].([]interface{}); ok {
		for _, t := range v {
			if s, ok := t.(string); ok {
				spec.Tags = append(spec.Tags, s)
			}
		}
	}
	if v, ok := meta["dependencies"].([]interface{}); ok {
		for _, d := range v {
			if s, ok := d.(string); ok {
				spec.Dependencies = append(spec.Dependencies, s)
			}
		}
	}
}
