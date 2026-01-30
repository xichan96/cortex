package skills

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadSkillsFromDirs scans the provided directories for SKILL.md files and loads them.
func LoadSkillsFromDirs(dirs []string) ([]Skill, error) {
	var skills []Skill

	for _, dir := range dirs {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			log.Printf("Warning: could not get absolute path for %s: %v", dir, err)
			absDir = dir
		}

		err = filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				// Log error but continue walking
				log.Printf("Warning: error accessing path %q: %v", path, err)
				return nil
			}

			if !info.IsDir() && strings.EqualFold(info.Name(), "SKILL.MD") {
				skill, err := loadSkillFromFile(path)
				if err != nil {
					// Log error but continue
					log.Printf("Warning: skipping invalid skill file %s: %v", path, err)
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
	inFrontmatter := false
	lineCount := 0

	// Safety limit: don't read too many lines looking for frontmatter
	const maxFrontmatterLines = 500

	for scanner.Scan() {
		line := scanner.Text()
		lineCount++

		if lineCount > maxFrontmatterLines {
			break
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			if !inFrontmatter {
				if lineCount == 1 {
					inFrontmatter = true
					continue
				}
			} else {
				// End of frontmatter
				inFrontmatter = false
				break
			}
		}

		if inFrontmatter {
			frontmatterLines = append(frontmatterLines, line)
		} else if lineCount == 1 {
			// First line is not ---, so no frontmatter
			return Skill{}, fmt.Errorf("missing frontmatter delimiter '---' on first line")
		}
	}

	if err := scanner.Err(); err != nil {
		return Skill{}, err
	}

	// Ensure frontmatter was closed
	if inFrontmatter {
		return Skill{}, fmt.Errorf("frontmatter was not closed with '---' in %s", path)
	}

	if len(frontmatterLines) == 0 {
		return Skill{}, fmt.Errorf("no valid frontmatter found in %s", path)
	}

	var skill Skill
	err = yaml.Unmarshal([]byte(strings.Join(frontmatterLines, "\n")), &skill)
	if err != nil {
		return Skill{}, fmt.Errorf("failed to parse frontmatter in %s: %w", path, err)
	}

	skill.Path = path
	// If name is missing, default to directory name
	if skill.Name == "" {
		skill.Name = filepath.Base(filepath.Dir(path))
	}

	return skill, nil
}
