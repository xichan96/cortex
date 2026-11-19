package skills

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// bodyCacheEntry caches a skill body keyed by the source file's mtime and size.
type bodyCacheEntry struct {
	modTime time.Time
	size    int64
	body    string
}

// Index is a lazily-loaded view of the discovered skills. At construction it
// parses metadata only; bodies are read on demand by LoadBody and cached until
// the file's mtime or size changes.
//
// An Index is safe for concurrent use.
type Index struct {
	mu          sync.RWMutex
	roots       []string
	opts        DiscoverOptions
	order       []string
	entries     map[string]*Entry
	cache       map[string]bodyCacheEntry
	diagnostics []Diagnostic
}

// LoadIndex discovers skills under roots and returns a metadata-only index.
// Bodies are not read until LoadBody is called.
func LoadIndex(ctx context.Context, roots []string, opts DiscoverOptions) (*Index, []Diagnostic, error) {
	entries, diags, err := discoverEntries(ctx, roots, opts)
	if err != nil {
		return nil, diags, err
	}

	ix := &Index{
		roots:       append([]string(nil), roots...),
		opts:        opts,
		entries:     make(map[string]*Entry, len(entries)),
		cache:       make(map[string]bodyCacheEntry),
		diagnostics: diags,
	}
	for _, entry := range entries {
		ix.order = append(ix.order, entry.Name)
		ix.entries[entry.Name] = entry
	}
	return ix, diags, nil
}

// Get returns a skill entry by name.
func (ix *Index) Get(name string) (*Entry, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	entry, ok := ix.entries[name]
	return entry, ok
}

// All returns all entries in discovery order.
func (ix *Index) All() []*Entry {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]*Entry, 0, len(ix.order))
	for _, name := range ix.order {
		if entry, ok := ix.entries[name]; ok {
			out = append(out, entry)
		}
	}
	return out
}

// Advertisable returns entries that may be advertised to the model. Skills
// with disable-model-invocation are excluded; they remain explicitly
// invocable through /skill:name.
func (ix *Index) Advertisable() []*Entry {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]*Entry, 0, len(ix.order))
	for _, name := range ix.order {
		entry, ok := ix.entries[name]
		if !ok || entry.DisableModelInvocation {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// Diagnostics returns the diagnostics produced by the last discovery pass.
func (ix *Index) Diagnostics() []Diagnostic {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return append([]Diagnostic(nil), ix.diagnostics...)
}

// LoadBody returns the skill body with frontmatter stripped. The body is
// cached by mtime+size and re-read when either changes.
func (ix *Index) LoadBody(name string) (string, error) {
	ix.mu.RLock()
	entry, ok := ix.entries[name]
	cached, hasCache := ix.cache[name]
	ix.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("skill %q not found", name)
	}

	info, err := os.Stat(entry.Path)
	if err != nil {
		return "", fmt.Errorf("stat skill %q: %w", name, err)
	}
	if hasCache && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
		return cached.body, nil
	}

	file, err := os.Open(entry.Path)
	if err != nil {
		return "", fmt.Errorf("read skill %q: %w", name, err)
	}
	defer file.Close()

	info, err = file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat skill %q: %w", name, err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("read skill %q: %w", name, err)
	}
	doc, _, err := ParseDocument(data, entry.Path)
	if err != nil {
		return "", fmt.Errorf("parse skill %q: %w", name, err)
	}

	ix.mu.Lock()
	// Ignore the update if Reload replaced this entry while we were reading.
	if current, ok := ix.entries[name]; ok && current == entry {
		ix.cache[name] = bodyCacheEntry{
			modTime: info.ModTime(),
			size:    info.Size(),
			body:    doc.Body,
		}
	}
	ix.mu.Unlock()

	return doc.Body, nil
}

// Invalidate drops the cached body for one skill.
func (ix *Index) Invalidate(name string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	delete(ix.cache, name)
}

// InvalidateAll drops every cached body.
func (ix *Index) InvalidateAll() {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.cache = make(map[string]bodyCacheEntry)
}

// Reload re-scans the original roots and replaces the index contents. Bodies
// cached from the previous pass are discarded. In-flight callers keep using
// the entry they already obtained.
func (ix *Index) Reload(ctx context.Context) ([]Diagnostic, error) {
	ix.mu.RLock()
	roots := append([]string(nil), ix.roots...)
	opts := ix.opts
	ix.mu.RUnlock()

	entries, diags, err := discoverEntries(ctx, roots, opts)

	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err != nil {
		return diags, err
	}
	ix.order = ix.order[:0]
	ix.entries = make(map[string]*Entry, len(entries))
	ix.cache = make(map[string]bodyCacheEntry)
	ix.diagnostics = diags
	for _, entry := range entries {
		ix.order = append(ix.order, entry.Name)
		ix.entries[entry.Name] = entry
	}
	return diags, nil
}

// FormatAvailableSkills renders the advertisable skills as the
// <available_skills> block described by the agentskills.io integration guide.
// Skills with disable-model-invocation are omitted.
func FormatAvailableSkills(entries []*Entry) string {
	var advertisable []*Entry
	for _, entry := range entries {
		if entry != nil && !entry.DisableModelInvocation {
			advertisable = append(advertisable, entry)
		}
	}
	if len(advertisable) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("<available_skills>\n")
	for _, entry := range advertisable {
		b.WriteString("  <skill>\n")
		fmt.Fprintf(&b, "    <name>%s</name>\n", escapeSkillXML(entry.Name))
		fmt.Fprintf(&b, "    <description>%s</description>\n", escapeSkillXML(entry.Description))
		fmt.Fprintf(&b, "    <location>%s</location>\n", escapeSkillXML(entry.Path))
		b.WriteString("  </skill>\n")
	}
	b.WriteString("</available_skills>")
	return b.String()
}

// FormatAvailableSkills renders the index's advertisable skills. It is a
// convenience wrapper around the package-level FormatAvailableSkills.
func (ix *Index) FormatAvailableSkills() string {
	return FormatAvailableSkills(ix.Advertisable())
}
