package memkit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xichan96/cortex/pkg/memkit/search"
	"github.com/xichan96/cortex/pkg/memkit/stores"
)

var buildPromptPriorityHigh = PriorityHigh

type tenantManager struct {
	inner Manager
	uid   string
}

func (t *tenantManager) Preferences() PreferenceStore {
	return &tenantPreferenceStore{inner: t.inner.Preferences(), uid: t.uid}
}

func (t *tenantManager) Knowledge() KnowledgeStore {
	return &tenantKnowledgeStore{inner: t.inner.Knowledge(), uid: t.uid}
}

func (t *tenantManager) Indexes() IndexStore {
	return &tenantIndexStore{inner: t.inner.Indexes(), uid: t.uid}
}

func (t *tenantManager) GetUserPreferences(ctx context.Context, _ string) (map[string]string, error) {
	return t.inner.GetUserPreferences(ctx, t.uid)
}

func (t *tenantManager) SetUserPreference(ctx context.Context, _, category, key, value string) error {
	return t.inner.SetUserPreference(ctx, t.uid, category, key, value)
}

func (t *tenantManager) GetUserPreference(ctx context.Context, _, category, key string) (string, error) {
	return t.inner.GetUserPreference(ctx, t.uid, category, key)
}

func (t *tenantManager) AddKnowledge(ctx context.Context, _ string, content string, tags ...string) error {
	return t.inner.AddKnowledgeWithCategory(ctx, t.uid, content, "project", tags...)
}

func (t *tenantManager) AddKnowledgeWithCategory(ctx context.Context, _ string, content, category string, tags ...string) error {
	return t.inner.AddKnowledgeWithCategory(ctx, t.uid, content, category, tags...)
}

func (t *tenantManager) SearchKnowledge(ctx context.Context, _ string, query string, limit int) ([]MemoryItem, error) {
	return t.inner.SearchKnowledge(ctx, t.uid, query, limit)
}

func (t *tenantManager) BuildIndex(ctx context.Context, _, sourceID, title, content string) (*IndexTree, error) {
	return t.inner.BuildIndex(ctx, t.uid, sourceID, title, content)
}

func (t *tenantManager) SearchIndexes(ctx context.Context, _ string, query string, limit int) (*IndexSearchResult, error) {
	return t.inner.SearchIndexes(ctx, t.uid, query, limit)
}

func (t *tenantManager) GetStats(ctx context.Context, _ string) (*MemoryStats, error) {
	return t.inner.GetStats(ctx, t.uid)
}

func (t *tenantManager) BuildSystemPrompt(ctx context.Context, _ string, maxTokens int) (string, error) {
	return t.inner.BuildSystemPrompt(ctx, t.uid, maxTokens)
}

func (t *tenantManager) ExportToMarkdown(ctx context.Context, _ string) (string, error) {
	return t.inner.ExportToMarkdown(ctx, t.uid)
}

func (t *tenantManager) ExportPageIndexToMarkdown(ctx context.Context, _ string) (string, error) {
	return t.inner.ExportPageIndexToMarkdown(ctx, t.uid)
}

func (t *tenantManager) PageIndex() PageIndexStore {
	p := t.inner.PageIndex()
	if p == nil {
		return nil
	}
	return &tenantPageIndexStore{inner: p, uid: t.uid}
}

func (t *tenantManager) SyncPageIndex(ctx context.Context, _ string) error {
	return t.inner.SyncPageIndex(ctx, t.uid)
}

func (t *tenantManager) AddPageDoc(ctx context.Context, _, title, text string, kind PageIndexKind) error {
	return t.inner.AddPageDoc(ctx, t.uid, title, text, kind)
}

type Manager interface {
	Preferences() PreferenceStore
	Knowledge() KnowledgeStore
	Indexes() IndexStore

	GetUserPreferences(ctx context.Context, userID string) (map[string]string, error)
	SetUserPreference(ctx context.Context, userID, category, key, value string) error
	GetUserPreference(ctx context.Context, userID, category, key string) (string, error)

	AddKnowledge(ctx context.Context, userID, content string, tags ...string) error
	AddKnowledgeWithCategory(ctx context.Context, userID, content, category string, tags ...string) error
	SearchKnowledge(ctx context.Context, userID, query string, limit int) ([]MemoryItem, error)

	BuildIndex(ctx context.Context, userID, sourceID, title, content string) (*IndexTree, error)
	SearchIndexes(ctx context.Context, userID, query string, limit int) (*IndexSearchResult, error)

	GetStats(ctx context.Context, userID string) (*MemoryStats, error)

	BuildSystemPrompt(ctx context.Context, userID string, maxTokens int) (string, error)
	ExportToMarkdown(ctx context.Context, userID string) (string, error)
	ExportPageIndexToMarkdown(ctx context.Context, userID string) (string, error)

	PageIndex() PageIndexStore
	SyncPageIndex(ctx context.Context, userID string) error
	AddPageDoc(ctx context.Context, userID, title, text string, kind PageIndexKind) error
}

type manager struct {
	prefs PreferenceStore
	know  KnowledgeStore
	index IndexStore
	page  PageIndexStore
	cfg   *MemoryConfig
}

func NewManager(cfg *MemoryConfig, optPage ...PageIndexStore) Manager {
	if cfg == nil {
		cfg = DefaultMemoryConfig()
	}
	var page PageIndexStore
	if len(optPage) > 0 {
		page = optPage[0]
	}
	return &manager{
		prefs: stores.NewInMemoryPreferenceStore(),
		know:  stores.NewInMemoryKnowledgeStore(),
		index: stores.NewInMemoryIndexStore(),
		page:  page,
		cfg:   cfg,
	}
}

func NewManagerWithStores(prefs PreferenceStore, know KnowledgeStore, index IndexStore, cfg *MemoryConfig, optPage ...PageIndexStore) Manager {
	if cfg == nil {
		cfg = DefaultMemoryConfig()
	}
	var page PageIndexStore
	if len(optPage) > 0 {
		page = optPage[0]
	}
	return &manager{
		prefs: prefs,
		know:  know,
		index: index,
		page:  page,
		cfg:   cfg,
	}
}

func DefaultMemoryConfig() *MemoryConfig {
	return &MemoryConfig{
		MaxPreferences:      1000,
		MaxKnowledge:        5000,
		MaxContext:          100,
		MaxIndexNodes:       1000,
		ContextTTL:          24 * time.Hour,
		EnableExpiry:        true,
		EnableIndexThinning: false,
		MinIndexNodeTokens:  5000,
	}
}

func (m *manager) Preferences() PreferenceStore {
	return m.prefs
}

func (m *manager) Knowledge() KnowledgeStore {
	return m.know
}

func (m *manager) Indexes() IndexStore {
	return m.index
}

func (m *manager) PageIndex() PageIndexStore {
	return m.page
}

func (m *manager) SyncPageIndex(ctx context.Context, userID string) error {
	if m.page == nil {
		return nil
	}
	if err := m.page.DeleteByKinds(ctx, userID, []PageIndexKind{PageKindPreference, PageKindKnowledge}); err != nil {
		return err
	}
	prefs, err := m.prefs.GetByUser(ctx, userID)
	if err != nil {
		return err
	}
	for _, p := range prefs {
		id := "pi:pref:" + p.ID
		if p.ID == "" {
			id = fmt.Sprintf("pi:pref:%s:%s:%s", userID, p.Category, p.Key)
		}
		doc := PageIndexDoc{
			ID: id, UserID: userID, Kind: PageKindPreference,
			Title: p.Category + "." + p.Key, Text: p.Value,
			RefKind: "preference", RefID: p.ID,
			Priority: p.Priority, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		}
		if err := m.page.Upsert(ctx, doc); err != nil {
			return err
		}
	}
	const batch = 500
	for offset := 0; ; offset += batch {
		res, err := m.know.Search(ctx, userID, &SearchOptions{Limit: batch, Offset: offset})
		if err != nil {
			return err
		}
		for _, it := range res.Items {
			if it.Type != MemoryTypeKnowledge {
				continue
			}
			id := "pi:know:" + it.ID
			title := it.Key
			if it.Metadata != nil {
				if t, ok := it.Metadata["title"].(string); ok && t != "" {
					title = t
				}
			}
			doc := PageIndexDoc{
				ID: id, UserID: userID, Kind: PageKindKnowledge,
				Title: title, Text: it.Value,
				RefKind: "knowledge", RefID: it.ID,
				Priority: it.Priority, CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt,
			}
			if err := m.page.Upsert(ctx, doc); err != nil {
				return err
			}
		}
		if len(res.Items) < batch {
			break
		}
	}
	return nil
}

func (m *manager) AddPageDoc(ctx context.Context, userID, title, text string, kind PageIndexKind) error {
	if m.page == nil {
		return fmt.Errorf("memory: page index disabled")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("memory: text required")
	}
	if kind == "" {
		kind = PageKindLongTerm
	}
	return m.page.Upsert(ctx, PageIndexDoc{
		UserID: userID, Kind: kind, Title: title, Text: text, Priority: PriorityMedium,
	})
}

func (m *manager) GetUserPreferences(ctx context.Context, userID string) (map[string]string, error) {
	return m.prefs.GetAllAsMap(ctx, userID)
}

func (m *manager) SetUserPreference(ctx context.Context, userID, category, key, value string) error {
	if m.cfg.MaxPreferences > 0 {
		existing, err := m.prefs.Get(ctx, userID, category, key)
		if err != nil {
			return err
		}
		if existing == nil {
			prefs, err := m.prefs.GetByUser(ctx, userID)
			if err != nil {
				return err
			}
			if len(prefs) >= m.cfg.MaxPreferences {
				return fmt.Errorf("%w: max preferences", ErrMaxLimitExceeded)
			}
		}
	}
	pref := Preference{
		UserID:   userID,
		Category: category,
		Key:      key,
		Value:    value,
		Priority: PriorityMedium,
	}
	return m.prefs.Set(ctx, pref)
}

func (m *manager) GetUserPreference(ctx context.Context, userID, category, key string) (string, error) {
	pref, err := m.prefs.Get(ctx, userID, category, key)
	if err != nil {
		return "", err
	}
	if pref == nil {
		return "", nil
	}
	return pref.Value, nil
}

func (m *manager) AddKnowledge(ctx context.Context, userID, content string, tags ...string) error {
	return m.AddKnowledgeWithCategory(ctx, userID, content, "project", tags...)
}

func (m *manager) AddKnowledgeWithCategory(ctx context.Context, userID, content, category string, tags ...string) error {
	if m.cfg.MaxKnowledge > 0 {
		n, err := m.know.GetStats(ctx, userID)
		if err != nil {
			return err
		}
		if n >= m.cfg.MaxKnowledge {
			return fmt.Errorf("%w: max knowledge", ErrMaxLimitExceeded)
		}
	}
	entry := KnowledgeEntry{
		UserID:   userID,
		Content:  content,
		Tags:     tags,
		Category: category,
		Priority: PriorityMedium,
		Source:   "user",
	}
	return m.know.Add(ctx, entry)
}

func (m *manager) SearchKnowledge(ctx context.Context, userID, query string, limit int) ([]MemoryItem, error) {
	opts := &SearchOptions{
		Query: query,
		Limit: limit,
	}
	result, err := m.know.Search(ctx, userID, opts)
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

func (m *manager) BuildIndex(ctx context.Context, userID, sourceID, title, content string) (*IndexTree, error) {
	parser := stores.NewMarkdownParser()
	nodes := parser.Parse(content)

	now := time.Now()
	for i, node := range nodes {
		var next *IndexNode
		if i+1 < len(nodes) {
			next = nodes[i+1]
		}
		node.Content = parser.ExtractNodeContent(content, node, next)
		if next != nil {
			node.EndLine = next.StartLine - 1
		}
		node.CreatedAt = now
		node.UpdatedAt = now
	}

	if m.cfg.EnableIndexThinning {
		minTok := m.cfg.MinIndexNodeTokens
		if minTok <= 0 {
			minTok = 5000
		}
		nodes = stores.ThinMarkdownIndexNodes(nodes, minTok)
		for _, node := range nodes {
			node.CreatedAt = now
			node.UpdatedAt = now
		}
	}

	if m.cfg.MaxIndexNodes > 0 && len(nodes) > m.cfg.MaxIndexNodes {
		return nil, fmt.Errorf("%w: max index nodes", ErrMaxLimitExceeded)
	}

	return m.index.CreateIndex(ctx, userID, sourceID, title, nodes)
}

func (m *manager) SearchIndexes(ctx context.Context, userID, query string, limit int) (*IndexSearchResult, error) {
	return m.index.SearchIndex(ctx, userID, query, limit)
}

func (m *manager) GetStats(ctx context.Context, userID string) (*MemoryStats, error) {
	prefs, err := m.prefs.GetByUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	knowCount, err := m.know.GetStats(ctx, userID)
	if err != nil {
		return nil, err
	}

	indexes, err := m.index.GetAllIndexes(ctx, userID)
	if err != nil {
		return nil, err
	}

	var indexCount int
	for _, idx := range indexes {
		indexCount += len(idx.Nodes)
	}

	byCategory := make(map[string]int)
	for _, p := range prefs {
		byCategory[p.Category]++
	}

	pageCount := 0
	if m.page != nil {
		var err error
		pageCount, err = m.page.CountByUser(ctx, userID)
		if err != nil {
			return nil, err
		}
	}

	return &MemoryStats{
		PreferenceCount: len(prefs),
		KnowledgeCount:  knowCount,
		IndexCount:      indexCount,
		PageIndexCount:  pageCount,
		ByCategory:      byCategory,
	}, nil
}

func (m *manager) BuildSystemPrompt(ctx context.Context, userID string, maxTokens int) (string, error) {
	var promptParts []string
	currentTokens := 0

	prefs, err := m.prefs.GetByUser(ctx, userID)
	if err != nil {
		return "", err
	}
	if len(prefs) > 0 {
		prefsHeader := "User Preferences:"
		currentTokens += stores.EstimateTokens(prefsHeader) + 1
		promptParts = append(promptParts, prefsHeader)
		for _, p := range prefs {
			if p.Priority >= PriorityMedium {
				line := fmt.Sprintf("- %s.%s: %s", p.Category, p.Key, p.Value)
				lineTokens := stores.EstimateTokens(line)
				if currentTokens+lineTokens > maxTokens {
					break
				}
				promptParts = append(promptParts, line)
				currentTokens += lineTokens + 1
			}
		}
	}

	opts := &SearchOptions{
		Limit:    10,
		Priority: &buildPromptPriorityHigh,
	}
	result, err := m.know.Search(ctx, userID, opts)
	if err != nil {
		return "", err
	}
	if len(result.Items) > 0 {
		knowledgeHeader := "Important User Knowledge:"
		currentTokens += stores.EstimateTokens(knowledgeHeader) + 1
		promptParts = append(promptParts, knowledgeHeader)
		for _, item := range result.Items {
			if currentTokens >= maxTokens {
				break
			}
			content := item.Value
			if len(content) > 500 {
				content = content[:500] + "..."
			}
			line := fmt.Sprintf("- [%s] %s", item.Key, content)
			lineTokens := stores.EstimateTokens(line)
			if currentTokens+lineTokens > maxTokens {
				break
			}
			promptParts = append(promptParts, line)
			currentTokens += lineTokens + 1
		}
	}

	prompt := ""
	if len(promptParts) > 0 {
		prompt = "User Context:\n" + strings.Join(promptParts, "\n")
	}

	return prompt, nil
}

type MultiTenantManager struct {
	manager       Manager
	defaultConfig *MemoryConfig
	defaultLLM    LLMProvider
}

func NewMultiTenantManager(cfg *MemoryConfig, llm LLMProvider) *MultiTenantManager {
	return NewMultiTenantManagerWithStores(nil, nil, nil, cfg, llm)
}

func NewMultiTenantManagerWithStores(prefs PreferenceStore, know KnowledgeStore, index IndexStore, cfg *MemoryConfig, llm LLMProvider, optPage ...PageIndexStore) *MultiTenantManager {
	if cfg == nil {
		cfg = DefaultMemoryConfig()
	}
	var pageOpt PageIndexStore
	if len(optPage) > 0 {
		pageOpt = optPage[0]
	}
	var manager Manager
	if prefs != nil || know != nil || index != nil || pageOpt != nil {
		if prefs == nil {
			prefs = stores.NewInMemoryPreferenceStore()
		}
		if know == nil {
			know = stores.NewInMemoryKnowledgeStore()
		}
		if index == nil {
			index = stores.NewInMemoryIndexStore()
		}
		manager = NewManagerWithStores(prefs, know, index, cfg, pageOpt)
	} else {
		manager = NewManager(cfg, pageOpt)
	}
	return &MultiTenantManager{
		manager:       manager,
		defaultConfig: cfg,
		defaultLLM:    llm,
	}
}

func (m *MultiTenantManager) GetManager(userID string) Manager {
	if userID == "" {
		return m.manager
	}
	return &tenantManager{inner: m.manager, uid: userID}
}

func (m *MultiTenantManager) PageIndexRAG(userID string) *search.PageIndexRAG {
	var st PageIndexStore
	if m.manager != nil {
		st = m.manager.PageIndex()
	}
	if userID != "" && st != nil {
		st = &tenantPageIndexStore{inner: st, uid: userID}
	}
	return search.NewPageIndexRAG(st, m.defaultLLM)
}

func (m *MultiTenantManager) TreeSearchWithMemory(ctx context.Context, userID, sourceID, query string) (*TreeSearchResult, error) {
	if m.defaultLLM == nil {
		return nil, fmt.Errorf("memory: tree search requires llm")
	}
	if strings.TrimSpace(userID) == "" {
		return nil, fmt.Errorf("memory: user id required for tree search")
	}
	mgr := m.GetManager(userID)
	tree, err := mgr.Indexes().GetIndex(ctx, userID, sourceID)
	if err != nil {
		return nil, err
	}
	if tree == nil {
		return nil, fmt.Errorf("memory: index not found for source %q", sourceID)
	}
	var expert string
	if p := mgr.PageIndex(); p != nil {
		hits, err := p.Search(ctx, userID, query, &PageIndexSearchOptions{
			Limit: 8,
			Kinds: []PageIndexKind{PageKindPreference, PageKindKnowledge, PageKindLongTerm, PageKindKnowledgeBase},
		})
		if err == nil {
			expert = search.FormatPageIndexHitsExpert(hits)
		}
	}
	return search.TreeSearchIndexTree(ctx, m.defaultLLM, tree, query, expert)
}

func (m *MultiTenantManager) GetBuilder(userID string) *search.IndexBuilder {
	var store IndexStore
	if m.manager != nil {
		store = m.manager.Indexes()
	}
	if userID != "" {
		store = &tenantIndexStore{inner: store, uid: userID}
	}
	return search.NewIndexBuilderWithStore(m.defaultLLM, nil, store)
}

func (m *MultiTenantManager) RemoveManager(userID string) error {
	if userID == "" || m.manager == nil {
		return nil
	}
	ctx := context.Background()
	if err := m.manager.Preferences().Clear(ctx, userID); err != nil {
		return err
	}
	if err := m.manager.Knowledge().Clear(ctx, userID); err != nil {
		return err
	}
	indexes, err := m.manager.Indexes().GetAllIndexes(ctx, userID)
	if err != nil {
		return err
	}
	for _, tree := range indexes {
		if err := m.manager.Indexes().DeleteIndex(ctx, userID, tree.SourceID); err != nil {
			return err
		}
	}
	if pg := m.manager.PageIndex(); pg != nil {
		if err := pg.DeleteByUser(ctx, userID); err != nil {
			return err
		}
	}
	return nil
}

// ExportToMarkdown 将用户记忆导出为 Claude Code 风格的 MEMORY.md 格式
func (m *manager) ExportToMarkdown(ctx context.Context, userID string) (string, error) {
	var lines []string
	lines = append(lines, "# User Memory")
	lines = append(lines, "")
	lines = append(lines, "> Auto-generated memory export")
	lines = append(lines, "")

	// 导出 Preferences
	prefs, err := m.prefs.GetByUser(ctx, userID)
	if err != nil {
		return "", err
	}
	if len(prefs) > 0 {
		lines = append(lines, "## Preferences")
		lines = append(lines, "")
		byCategory := make(map[string][]Preference)
		for _, p := range prefs {
			byCategory[p.Category] = append(byCategory[p.Category], p)
		}
		for category, items := range byCategory {
			lines = append(lines, "### "+category)
			for _, p := range items {
				lines = append(lines, fmt.Sprintf("- **%s**: %s", p.Key, p.Value))
			}
			lines = append(lines, "")
		}
	}

	// 导出 Knowledge
	opts := &SearchOptions{Limit: 100}
	result, err := m.know.Search(ctx, userID, opts)
	if err != nil {
		return "", err
	}
	if len(result.Items) > 0 {
		lines = append(lines, "## Knowledge")
		lines = append(lines, "")
		byCategory := make(map[string][]MemoryItem)
		for _, item := range result.Items {
			cat := item.Key
			if cat == "" {
				cat = "general"
			}
			byCategory[cat] = append(byCategory[cat], item)
		}
		for category, items := range byCategory {
			lines = append(lines, "### "+category)
			for _, item := range items {
				line := "- " + item.Value
				if len(item.Tags) > 0 {
					line += " (`" + strings.Join(item.Tags, ", ") + "`)"
				}
				lines = append(lines, line)
			}
			lines = append(lines, "")
		}
	}

	return strings.Join(lines, "\n"), nil
}

// ExportPageIndexToMarkdown 将 PageIndex 导出为 Markdown 格式
func (m *manager) ExportPageIndexToMarkdown(ctx context.Context, userID string) (string, error) {
	if m.page == nil {
		return "", nil
	}

	hits, err := m.page.Search(ctx, userID, "", &PageIndexSearchOptions{Limit: 100})
	if err != nil {
		return "", err
	}

	if len(hits) == 0 {
		return "", nil
	}

	var lines []string
	lines = append(lines, "## Page Index")
	lines = append(lines, "")

	byKind := make(map[PageIndexKind][]PageIndexHit)
	for _, h := range hits {
		byKind[h.Doc.Kind] = append(byKind[h.Doc.Kind], h)
	}

	for kind, items := range byKind {
		lines = append(lines, "### "+string(kind))
		for _, h := range items {
			lines = append(lines, fmt.Sprintf("- **%s**: %s", h.Doc.Title, truncateString(h.Doc.Text, 200)))
		}
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n"), nil
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
