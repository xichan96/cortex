package skills

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	SkillFileName   = "SKILL.md"
	ExternalDirName = ".claude"
	AgentsDirName   = ".agents"
	OpenCodeDirName = ".opencode"
)

type Trigger struct {
	Type    string `yaml:"type"`
	Pattern string `yaml:"pattern"`
}

type Skill struct {
	Name        string                 `yaml:"name"`
	Description string                 `yaml:"description"`
	Path        string                 `yaml:"-"`
	Content     string                 `yaml:"-"`
	Triggers    []Trigger              `yaml:"triggers"`
	Tags        []string               `yaml:"tags"`
	Priority    int                    `yaml:"priority"`
	Timeout     time.Duration          `yaml:"timeout"`
	Retries     int                    `yaml:"retries"`
	Metadata    map[string]interface{} `yaml:"metadata,omitempty"`
}

type Loader struct {
	mu           sync.RWMutex
	skills       map[string]*Skill
	dirs         []string
	commandIndex map[string]*Skill
	keywordIndex map[string]*Skill
	regexIndex   []*regexEntry
}

type regexEntry struct {
	re    *regexp.Regexp
	skill *Skill
}

func NewLoader() *Loader {
	return &Loader{
		skills:       make(map[string]*Skill),
		commandIndex: make(map[string]*Skill),
		keywordIndex: make(map[string]*Skill),
	}
}

func (l *Loader) LoadFromDirs(ctx context.Context, dirs []string) error {
	l.mu.Lock()
	l.dirs = dirs
	l.mu.Unlock()

	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := l.scanDir(dir); err != nil {
			return err
		}
	}

	return nil
}

func (l *Loader) LoadFromConfig(ctx context.Context, cfg *Config) error {
	var dirs []string

	for _, dir := range cfg.Dirs {
		if dir == "" {
			continue
		}
		absDir, err := filepath.Abs(dir)
		if err != nil {
			absDir = dir
		}
		dirs = append(dirs, absDir)
	}

	homeDir, err := os.UserHomeDir()
	if err == nil {
		dirs = append(dirs, filepath.Join(homeDir, ExternalDirName, "skills"))
		dirs = append(dirs, filepath.Join(homeDir, AgentsDirName, "skills"))
	}

	dirs = append(dirs, filepath.Join(OpenCodeDirName, "skills"))

	return l.LoadFromDirs(ctx, dirs)
}

func (l *Loader) scanDir(dir string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}

	info, err := os.Stat(absDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	if !info.IsDir() {
		return nil
	}

	walkFn := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && strings.EqualFold(info.Name(), SkillFileName) {
			if skill, err := l.parseSkillFile(path); err == nil {
				l.addSkill(skill)
			} else {
				fmt.Fprintf(os.Stderr, "[Skills] Failed to load skill %s: %v\n", path, err)
			}
		}
		return nil
	}

	return filepath.Walk(absDir, walkFn)
}

func (l *Loader) parseSkillFile(path string) (*Skill, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var frontmatterLines []string
	var contentLines []string
	inFrontmatter := false
	frontmatterDone := false

	const maxLines = 20000
	lineCount := 0

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
		return nil, err
	}

	if inFrontmatter {
		return nil, fmt.Errorf("frontmatter was not closed in %s", path)
	}

	var meta map[string]interface{}
	if len(frontmatterLines) > 0 {
		if err := yaml.Unmarshal([]byte(strings.Join(frontmatterLines, "\n")), &meta); err != nil {
			return nil, fmt.Errorf("failed to parse frontmatter in %s: %w", path, err)
		}
	}

	skill := &Skill{
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

	l.parseTriggers(meta, skill)
	l.parseMeta(meta, skill)

	return skill, nil
}

func (l *Loader) parseTriggers(meta map[string]interface{}, skill *Skill) {
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
		if m, ok := item.(map[string]interface{}); ok {
			if v, ok := m["type"].(string); ok {
				t.Type = v
			}
			if v, ok := m["pattern"].(string); ok {
				t.Pattern = v
			}
		} else if m, ok := item.(map[interface{}]interface{}); ok {
			if v, ok := m["type"].(string); ok {
				t.Type = v
			}
			if v, ok := m["pattern"].(string); ok {
				t.Pattern = v
			}
		}

		if t.Type != "" && t.Pattern != "" {
			skill.Triggers = append(skill.Triggers, t)
		}
	}
}

func (l *Loader) parseMeta(meta map[string]interface{}, skill *Skill) {
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
		skill.Priority = v
	}
	if v := getInt("retries"); v != 0 {
		skill.Retries = v
	}

	if v, ok := meta["timeout"].(string); ok {
		if d, err := time.ParseDuration(v); err == nil {
			skill.Timeout = d
		}
	}

	if v, ok := meta["tags"].([]interface{}); ok {
		for _, t := range v {
			if s, ok := t.(string); ok {
				skill.Tags = append(skill.Tags, s)
			}
		}
	}
}

func (l *Loader) addSkill(skill *Skill) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.skills[skill.Name] = skill

	for _, t := range skill.Triggers {
		switch t.Type {
		case "command":
			cmd := strings.ToLower(strings.TrimSpace(t.Pattern))
			if !strings.HasPrefix(cmd, "/") {
				cmd = "/" + cmd
			}
			l.commandIndex[cmd] = skill
		case "keyword":
			l.keywordIndex[strings.ToLower(strings.TrimSpace(t.Pattern))] = skill
		case "regex":
			if re, err := regexp.Compile(t.Pattern); err == nil {
				l.regexIndex = append(l.regexIndex, &regexEntry{re: re, skill: skill})
			}
		}
	}
}

func (l *Loader) Get(name string) (*Skill, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	s, ok := l.skills[name]
	return s, ok
}

func (l *Loader) All() []*Skill {
	l.mu.RLock()
	defer l.mu.RUnlock()

	result := make([]*Skill, 0, len(l.skills))
	for _, s := range l.skills {
		result = append(result, s)
	}
	return result
}

func (l *Loader) LookupCommand(cmd string) (*Skill, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	s, ok := l.commandIndex[strings.ToLower(cmd)]
	return s, ok
}

func (l *Loader) LookupKeyword(text string) (*Skill, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	lower := strings.ToLower(text)
	for kw, skill := range l.keywordIndex {
		if strings.Contains(lower, kw) {
			return skill, true
		}
	}
	return nil, false
}

func (l *Loader) MatchRegex(text string) (*Skill, []string) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	for _, entry := range l.regexIndex {
		if matches := entry.re.FindStringSubmatch(text); matches != nil {
			return entry.skill, matches
		}
	}
	return nil, nil
}

func (l *Loader) MatchAll(text string) []*Skill {
	l.mu.RLock()
	defer l.mu.RUnlock()

	uniqueSkills := make(map[*Skill]struct{})
	lower := strings.ToLower(strings.TrimSpace(text))

	if strings.HasPrefix(lower, "/") {
		parts := strings.Fields(lower)
		if len(parts) > 0 {
			if s, ok := l.commandIndex[parts[0]]; ok {
				uniqueSkills[s] = struct{}{}
			}
		}
	}

	for kw, skill := range l.keywordIndex {
		if strings.Contains(lower, kw) {
			uniqueSkills[skill] = struct{}{}
		}
	}

	for _, entry := range l.regexIndex {
		if entry.re.MatchString(text) {
			uniqueSkills[entry.skill] = struct{}{}
		}
	}

	result := make([]*Skill, 0, len(uniqueSkills))
	for s := range uniqueSkills {
		result = append(result, s)
	}
	return result
}

func (l *Loader) Dirs() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.dirs
}

func BuildSystemPromptInjection(skills []*Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n## Available Skills\n\n")
	sb.WriteString("You have access to the following skills that can help fulfill user requests:\n\n")

	for _, skill := range skills {
		sb.WriteString(fmt.Sprintf("### %s\n", skill.Name))
		sb.WriteString(fmt.Sprintf("%s\n", skill.Description))
		if len(skill.Triggers) > 0 {
			sb.WriteString("Triggers: ")
			var triggers []string
			for _, t := range skill.Triggers {
				triggers = append(triggers, fmt.Sprintf("%s:%s", t.Type, t.Pattern))
			}
			sb.WriteString(strings.Join(triggers, ", "))
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\nWhen a user request matches a skill's triggers, you should use that skill's instructions to fulfill the request.\n")

	return sb.String()
}

type Config struct {
	Dirs []string
}

func DefaultConfig() *Config {
	return &Config{
		Dirs: []string{
			".claude/skills",
			".agents/skills",
			".opencode/skills",
			"skills",
		},
	}
}
