package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

const (
	ignoreFileName      = ".gitignore"
	ignoreFileNameExtra = ".ignore"
	nodeModulesDir      = "node_modules"
)

// DiscoverOptions controls deep skill discovery. The zero value is the
// recommended default: follow symlinks, accept standalone .md files directly
// under each root, and recurse without a depth limit.
type DiscoverOptions struct {
	// NoFollowSymlinks disables resolving symlinked roots and does not descend
	// into symlinked directories.
	NoFollowSymlinks bool
	// SkipRootMarkdown disables treating standalone .md files directly under a
	// root as skills.
	SkipRootMarkdown bool
	// MaxDepth limits recursion below each root. 0 means unlimited. A root's
	// own SKILL.md is always considered.
	MaxDepth int
}

// DiscoverResult is the outcome of a discovery pass.
type DiscoverResult struct {
	// Skills are metadata-only: Content is empty. Use Index.LoadBody to read
	// bodies lazily.
	Skills      []Skill
	Diagnostics []Diagnostic
}

// Discover recursively scans roots for skills. Directories containing a
// SKILL.md are treated as skill roots and are not descended into. Dot
// directories, node_modules, and paths matched by .gitignore/.ignore are
// skipped. Name collisions keep the first discovered skill (roots are scanned
// in order) and emit a diagnostic.
func Discover(ctx context.Context, roots []string, opts DiscoverOptions) (DiscoverResult, error) {
	entries, diags, err := discoverEntries(ctx, roots, opts)
	if err != nil {
		return DiscoverResult{Diagnostics: diags}, err
	}
	skills := make([]Skill, 0, len(entries))
	for _, e := range entries {
		skills = append(skills, e.Spec.ToSkill(""))
	}
	return DiscoverResult{Skills: skills, Diagnostics: diags}, nil
}

// Entry is one discovered skill. It carries the full parsed spec plus the
// location needed for lazy body loading.
type Entry struct {
	Name                   string
	Description            string
	Path                   string
	BaseDir                string
	DisableModelInvocation bool
	Spec                   *Spec
}

// Skill converts an Entry to the legacy Skill representation (without a body).
func (e *Entry) Skill() Skill {
	return e.Spec.ToSkill("")
}

type discoverer struct {
	ctx         context.Context
	opts        DiscoverOptions
	entries     []*Entry
	diagnostics []Diagnostic
	seenNames   map[string]string // name -> first path
	seenReal    map[string]struct{}
	visitedDirs map[string]struct{}
	ignores     []ignoreRule
}

func discoverEntries(ctx context.Context, roots []string, opts DiscoverOptions) ([]*Entry, []Diagnostic, error) {
	d := &discoverer{
		ctx:         ctx,
		opts:        opts,
		seenNames:   make(map[string]string),
		seenReal:    make(map[string]struct{}),
		visitedDirs: make(map[string]struct{}),
	}
	for _, root := range roots {
		if err := d.addRoot(root); err != nil {
			return d.entries, d.diagnostics, err
		}
	}
	return d.entries, d.diagnostics, nil
}

func (d *discoverer) addRoot(root string) error {
	if err := d.ctx.Err(); err != nil {
		return err
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}

	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	if !d.opts.NoFollowSymlinks {
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
	}

	info, err := os.Stat(abs)
	if err != nil {
		d.warn(DiagnosticReadError, abs, fmt.Sprintf("cannot access skill root: %v", err))
		return nil
	}
	if info.IsDir() {
		return d.scanDir(abs, 0, true)
	}
	if strings.EqualFold(filepath.Ext(abs), ".md") {
		d.loadFile(abs)
		return nil
	}
	d.warn(DiagnosticUnsupportedRoot, abs, "skill root is not a directory or markdown file")
	return nil
}

// scanDir scans one directory. If it contains SKILL.md it is a skill root and
// the walk stops there; otherwise subdirectories are scanned recursively and,
// for top-level roots, standalone .md files are accepted.
func (d *discoverer) scanDir(dir string, depth int, isRoot bool) error {
	if err := d.ctx.Err(); err != nil {
		return err
	}

	realDir := realpathKey(dir)
	if _, ok := d.visitedDirs[realDir]; ok {
		return nil
	}
	d.visitedDirs[realDir] = struct{}{}

	ignoreStart := d.pushIgnoreRules(dir)
	defer d.popIgnoreRules(ignoreStart)

	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		d.warn(DiagnosticUnreadableDir, dir, err.Error())
		return nil
	}

	// A directory containing SKILL.md is a skill root: do not descend further,
	// even if the file itself is ignored or invalid.
	for _, entry := range dirEntries {
		if !strings.EqualFold(entry.Name(), SkillFileName) {
			continue
		}
		full := filepath.Join(dir, entry.Name())
		if d.isIgnored(full, false) {
			d.warn(DiagnosticReadError, full, "skill file is ignored")
			return nil
		}
		d.loadFile(full)
		return nil
	}

	for _, entry := range dirEntries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}

		full := filepath.Join(dir, name)
		info, err := d.entryInfo(full, entry)
		if err != nil {
			continue
		}
		isDir := info.IsDir()

		if isDir && name == nodeModulesDir {
			continue
		}
		if d.isIgnored(full, isDir) {
			continue
		}

		if isDir {
			if d.opts.MaxDepth > 0 && depth+1 > d.opts.MaxDepth {
				continue
			}
			if err := d.scanDir(full, depth+1, false); err != nil {
				return err
			}
			continue
		}

		// Standalone .md files are a compatibility input, accepted only
		// directly under a root directory.
		if isRoot && !d.opts.SkipRootMarkdown && strings.EqualFold(filepath.Ext(name), ".md") {
			d.loadFile(full)
		}
	}
	return nil
}

func (d *discoverer) loadFile(path string) {
	if d.ctx.Err() != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		d.warn(DiagnosticReadError, path, err.Error())
		return
	}

	doc, diags, err := ParseDocument(data, path)
	d.diagnostics = append(d.diagnostics, diags...)
	if err != nil {
		d.warn(DiagnosticParseError, path, err.Error())
		return
	}
	if strings.TrimSpace(doc.Spec.Description) == "" {
		// ParseDocument already emitted a missing-description diagnostic.
		return
	}

	realPath := realpathKey(path)
	if _, ok := d.seenReal[realPath]; ok {
		return
	}

	name := doc.Spec.Name
	if prev, ok := d.seenNames[name]; ok {
		d.warn(DiagnosticCollision, path,
			fmt.Sprintf("skill name %q already discovered at %s; keeping the first one", name, prev))
		return
	}

	d.seenNames[name] = path
	d.seenReal[realPath] = struct{}{}
	d.entries = append(d.entries, &Entry{
		Name:                   name,
		Description:            doc.Spec.Description,
		Path:                   doc.Spec.Path,
		BaseDir:                doc.Spec.BaseDir,
		DisableModelInvocation: doc.Spec.DisableModelInvocation,
		Spec:                   doc.Spec,
	})
}

func (d *discoverer) warn(kind, path, message string) {
	d.diagnostics = append(d.diagnostics, Diagnostic{
		Kind:     kind,
		Severity: SeverityWarning,
		Path:     path,
		Message:  message,
	})
}

func (d *discoverer) entryInfo(path string, entry os.DirEntry) (os.FileInfo, error) {
	if entry.Type()&os.ModeSymlink != 0 {
		if d.opts.NoFollowSymlinks {
			return nil, fmt.Errorf("symlink skipped")
		}
		return os.Stat(path)
	}
	return entry.Info()
}

// --- ignore handling -------------------------------------------------------

type ignoreRule struct {
	base    string // absolute directory the rule file lives in
	pattern string // slash-separated pattern, leading "/" stripped
	negate  bool
	dirOnly bool
}

func (d *discoverer) pushIgnoreRules(dir string) int {
	start := len(d.ignores)
	for _, fileName := range []string{ignoreFileName, ignoreFileNameExtra} {
		data, err := os.ReadFile(filepath.Join(dir, fileName))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			rule, ok := parseIgnoreLine(strings.TrimRight(line, "\r"))
			if !ok {
				continue
			}
			rule.base = dir
			d.ignores = append(d.ignores, rule)
		}
	}
	return start
}

func (d *discoverer) popIgnoreRules(start int) {
	d.ignores = d.ignores[:start]
}

func (d *discoverer) isIgnored(abs string, isDir bool) bool {
	ignored := false
	for _, rule := range d.ignores {
		if rule.dirOnly && !isDir {
			continue
		}
		rel, err := filepath.Rel(rule.base, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		rel = filepath.ToSlash(rel)
		if matchIgnorePattern(rule.pattern, rel) {
			ignored = !rule.negate
		}
	}
	return ignored
}

func parseIgnoreLine(line string) (ignoreRule, bool) {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "#") {
		return ignoreRule{}, false
	}

	rule := ignoreRule{}
	if strings.HasPrefix(s, "\\#") {
		s = s[1:]
	} else if strings.HasPrefix(s, "!") {
		rule.negate = true
		s = s[1:]
	} else if strings.HasPrefix(s, "\\!") {
		s = s[1:]
	}

	if strings.HasSuffix(s, "/") {
		rule.dirOnly = true
		s = strings.TrimSuffix(s, "/")
	}
	s = strings.TrimPrefix(s, "/")
	if s == "" {
		return ignoreRule{}, false
	}
	rule.pattern = filepath.ToSlash(s)
	return rule, true
}

func matchIgnorePattern(pattern, rel string) bool {
	if pattern == "" {
		return false
	}
	// Patterns without a slash match a basename at any depth.
	if !strings.Contains(pattern, "/") {
		for _, part := range strings.Split(rel, "/") {
			if ok, _ := doublestar.Match(pattern, part); ok {
				return true
			}
		}
		return false
	}
	ok, _ := doublestar.Match(pattern, rel)
	return ok
}
