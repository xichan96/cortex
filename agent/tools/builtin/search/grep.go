package search

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	agentfs "github.com/xichan96/cortex/agent/tools/builtin/fs"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

const (
	outputModeContent          = "content"
	outputModeFilesWithMatches = "files_with_matches"
	outputModeCount            = "count"
)

// typeExtensions maps the Claude Code `type` shorthand to file extensions.
var typeExtensions = map[string][]string{
	"go":   {".go"},
	"js":   {".js", ".jsx", ".mjs", ".cjs"},
	"ts":   {".ts", ".tsx", ".mts", ".cts"},
	"py":   {".py"},
	"rust": {".rs"},
	"java": {".java"},
	"c":    {".c", ".h"},
	"cpp":  {".cpp", ".cc", ".cxx", ".hpp", ".hh", ".hxx"},
	"rb":   {".rb"},
	"php":  {".php"},
	"sh":   {".sh", ".bash", ".zsh"},
	"md":   {".md", ".markdown"},
	"json": {".json"},
	"yaml": {".yaml", ".yml"},
	"toml": {".toml"},
	"html": {".html", ".htm"},
	"css":  {".css"},
	"xml":  {".xml"},
	"sql":  {".sql"},
}

type GrepTool struct {
	workspace string
}

func NewGrepTool(workspace string) types.Tool {
	return &GrepTool{workspace: workspace}
}

func (t *GrepTool) Name() string {
	return "Grep"
}

//go:embed grep.txt
var grepDescription string

func (t *GrepTool) Description() string {
	return grepDescription
}

func (t *GrepTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "The regular expression pattern to search for in file contents.",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "File or directory to search in (defaults to the workspace root).",
			},
			"glob": map[string]interface{}{
				"type":        "string",
				"description": "Glob pattern to filter files, e.g. \"*.go\" or \"src/**/*.ts\".",
			},
			"output_mode": map[string]interface{}{
				"type":        "string",
				"description": "Output format: \"content\" shows matching lines, \"files_with_matches\" shows only file paths, \"count\" shows match counts per file.",
				"enum":        []string{outputModeContent, outputModeFilesWithMatches, outputModeCount},
			},
			"-n": map[string]interface{}{
				"type":        "boolean",
				"description": "Show line numbers in content mode.",
			},
			"-i": map[string]interface{}{
				"type":        "boolean",
				"description": "Case-insensitive search.",
			},
			"-o": map[string]interface{}{
				"type":        "boolean",
				"description": "Print only the matched parts, one per line.",
			},
			"-A": map[string]interface{}{
				"type":        "integer",
				"description": "Lines of trailing context after each match.",
			},
			"-B": map[string]interface{}{
				"type":        "integer",
				"description": "Lines of leading context before each match.",
			},
			"-C": map[string]interface{}{
				"type":        "integer",
				"description": "Lines of leading and trailing context.",
			},
			"head_limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of output entries to return.",
			},
			"multiline": map[string]interface{}{
				"type":        "boolean",
				"description": "Allow patterns to match across line boundaries.",
			},
			"type": map[string]interface{}{
				"type":        "string",
				"description": "Filter files by language/type shorthand, e.g. \"go\", \"js\", \"py\".",
			},
		},
		"required": []string{"pattern"},
	}
}

func (t *GrepTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	pattern := inputString(input, "pattern")
	if pattern == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("pattern is required"))
	}

	searchPath := inputString(input, "path")
	if searchPath == "" {
		if t.workspace == "*" {
			searchPath = "."
		} else {
			searchPath = t.workspace
		}
	}
	absPath, err := agentfs.SafePath(ctx, t.workspace, searchPath)
	if err != nil {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(err)
	}

	outputMode := inputString(input, "output_mode")
	if outputMode == "" {
		outputMode = outputModeContent
	}
	switch outputMode {
	case outputModeContent, outputModeFilesWithMatches, outputModeCount:
	default:
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid output_mode %q", outputMode))
	}

	includeGlob := inputString(input, "glob")
	typeName := inputString(input, "type")
	var typeExts map[string]struct{}
	if typeName != "" {
		exts, ok := typeExtensions[strings.ToLower(typeName)]
		if !ok {
			return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("unknown file type %q", typeName))
		}
		typeExts = make(map[string]struct{}, len(exts))
		for _, ext := range exts {
			typeExts[ext] = struct{}{}
		}
	}

	pat := pattern
	ignoreCase := inputBool(input, "-i")
	if ignoreCase && !strings.HasPrefix(pat, "(?i)") {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid pattern: %w", err))
	}

	multiline := inputBool(input, "multiline")
	onlyMatching := inputBool(input, "-o")
	showLineNo := inputBool(input, "-n")

	if outputMode != outputModeContent {
		// Context and -o only affect content output.
		onlyMatching = false
	}
	before := inputInt(input, "-B")
	after := inputInt(input, "-A")
	if c := inputInt(input, "-C"); c > before {
		before = c
	}
	if c := inputInt(input, "-C"); c > after {
		after = c
	}
	if before < 0 {
		before = 0
	}
	if after < 0 {
		after = 0
	}
	headLimit := inputInt(input, "head_limit")

	files, err := t.collectFiles(ctx, absPath, includeGlob, typeExts)
	if err != nil {
		return nil, err
	}

	matcher := loadGitignore(t.workspace)
	ignore := func(p string) bool {
		return matcher.Match(relToWorkspace(t.workspace, absPath, p))
	}

	var contentEntries []string
	var countLines []string
	var filePaths []string
	totalOccurrences := 0

	for _, file := range files {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if ignore(file) {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			continue // unreadable/binary-ish files are skipped, like grep -I
		}
		if bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		rel := relToWorkspace(t.workspace, absPath, file)

		fileMatches, fileContent, occurrences := grepData(rel, data, re, multiline, onlyMatching, showLineNo, before, after)
		if !fileMatches {
			continue
		}
		totalOccurrences += occurrences
		filePaths = append(filePaths, rel)
		switch outputMode {
		case outputModeCount:
			countLines = append(countLines, rel+":"+strconv.Itoa(occurrences))
		case outputModeContent:
			contentEntries = append(contentEntries, fileContent...)
		}
	}

	switch outputMode {
	case outputModeFilesWithMatches:
		return joinLimited(filePaths, headLimit), nil
	case outputModeCount:
		shown := countLines
		if headLimit > 0 && len(shown) > headLimit {
			shown = shown[:headLimit]
		}
		result := strings.Join(shown, "\n")
		summary := fmt.Sprintf("Found %d total occurrences across %d files.", totalOccurrences, len(filePaths))
		if result == "" {
			return summary, nil
		}
		return result + "\n" + summary, nil
	default:
		return joinLimited(contentEntries, headLimit), nil
	}
}

// collectFiles returns the candidate files under root (or root itself when it
// is a file), sorted for stable output. Noise directories are pruned while
// walking; .gitignore filtering is applied by the caller so both tools can
// share it.
func (t *GrepTool) collectFiles(ctx context.Context, root string, includeGlob string, typeExts map[string]struct{}) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("path %q: %w", root, err))
	}
	if !info.IsDir() {
		return []string{root}, nil
	}

	var files []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			return nil // partial results are better than none
		}
		if d.IsDir() {
			if p != root && shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if includeGlob != "" {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return nil
			}
			if !globFilterMatches(includeGlob, filepath.ToSlash(rel), d.Name()) {
				return nil
			}
		}
		if typeExts != nil {
			if _, ok := typeExts[strings.ToLower(filepath.Ext(p))]; !ok {
				return nil
			}
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	sort.Strings(files)
	return files, nil
}

// globFilterMatches applies a ripgrep-style --glob filter. Patterns without a
// slash match the basename at any depth; patterns with a slash match the
// root-relative path.
func globFilterMatches(filter, rel, base string) bool {
	if strings.Contains(filter, "/") {
		ok, _ := doublestar.Match(filter, rel)
		return ok
	}
	if ok, _ := doublestar.Match(filter, base); ok {
		return true
	}
	ok, _ := doublestar.Match(filter, rel)
	return ok
}

// grepData scans one file's bytes and returns whether it matched, the formatted
// content entries (content mode) and the occurrence count.
func grepData(rel string, data []byte, re *regexp.Regexp, multiline, onlyMatching, showLineNo bool, before, after int) (bool, []string, int) {
	lines := strings.Split(string(data), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}

	matchSet := map[int]struct{}{}   // 1-based matching line numbers
	matchTexts := map[int][]string{} // 1-based line -> matched substrings (for -o)
	occurrences := 0

	if multiline {
		content := string(data)
		for _, loc := range re.FindAllStringIndex(content, -1) {
			start, end := loc[0], loc[1]
			lineNo := strings.Count(content[:start], "\n") + 1
			matchSet[lineNo] = struct{}{}
			occurrences++
			if onlyMatching {
				matchTexts[lineNo] = append(matchTexts[lineNo], content[start:end])
			}
		}
	} else {
		for i, line := range lines {
			matches := re.FindAllStringIndex(line, -1)
			if len(matches) == 0 {
				continue
			}
			lineNo := i + 1
			matchSet[lineNo] = struct{}{}
			occurrences += len(matches)
			if onlyMatching {
				for _, loc := range matches {
					matchTexts[lineNo] = append(matchTexts[lineNo], line[loc[0]:loc[1]])
				}
			}
		}
	}

	if len(matchSet) == 0 {
		return false, nil, 0
	}

	if onlyMatching {
		var out []string
		for lineNo := 1; lineNo <= len(lines); lineNo++ {
			for _, text := range matchTexts[lineNo] {
				out = append(out, formatEntry(rel, lineNo, text, ':', showLineNo))
			}
		}
		return true, out, occurrences
	}

	show := make(map[int]struct{}, len(matchSet))
	for lineNo := range matchSet {
		lo := lineNo - before
		if lo < 1 {
			lo = 1
		}
		hi := lineNo + after
		if hi > len(lines) {
			hi = len(lines)
		}
		for n := lo; n <= hi; n++ {
			show[n] = struct{}{}
		}
	}

	var out []string
	for lineNo := 1; lineNo <= len(lines); lineNo++ {
		if _, ok := show[lineNo]; !ok {
			continue
		}
		sep := byte('-')
		if _, ok := matchSet[lineNo]; ok {
			sep = ':'
		}
		out = append(out, formatEntry(rel, lineNo, lines[lineNo-1], sep, showLineNo))
	}
	return true, out, occurrences
}

func formatEntry(rel string, lineNo int, text string, sep byte, showLineNo bool) string {
	if !showLineNo {
		return rel + string(sep) + text
	}
	return rel + string(sep) + strconv.Itoa(lineNo) + string(sep) + text
}

func joinLimited(entries []string, limit int) string {
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return strings.Join(entries, "\n")
}

func (t *GrepTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "Grep",
		IsFromToolkit:  false,
		ToolType:       "search",
		Extra: map[string]interface{}{
			"no_cache": true,
		},
	}
}
