// Package model defines the transport-neutral LLM layer described in the
// agent framework design (§8): model catalog metadata with project/user
// overrides, the provider/stream contracts, incremental tool-argument
// parsing, retry helpers, and credential resolution.
//
// The package depends only on the standard library. Providers adapt their
// wire protocol to Provider/Stream; the engine consumes StreamEvent values and
// never sees provider-specific types.
package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Modality is an input modality accepted by a model.
type Modality string

const (
	ModalityText  Modality = "text"
	ModalityImage Modality = "image"
	ModalityAudio Modality = "audio"
	ModalityPDF   Modality = "pdf"
)

// ThinkingLevel is a provider-neutral reasoning effort level. The empty value
// means "provider default / off" and is always valid.
type ThinkingLevel string

const (
	ThinkingOff     ThinkingLevel = "off"
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
	ThinkingMax     ThinkingLevel = "max"
)

// AllThinkingLevels lists the levels a model may declare, in ascending order.
func AllThinkingLevels() []ThinkingLevel {
	return []ThinkingLevel{
		ThinkingOff, ThinkingMinimal, ThinkingLow,
		ThinkingMedium, ThinkingHigh, ThinkingXHigh, ThinkingMax,
	}
}

// IsValid reports whether l is a declared thinking level (or the empty value).
func (l ThinkingLevel) IsValid() bool {
	switch l {
	case "", ThinkingOff, ThinkingMinimal, ThinkingLow,
		ThinkingMedium, ThinkingHigh, ThinkingXHigh, ThinkingMax:
		return true
	default:
		return false
	}
}

// API names a wire protocol (for example "anthropic-messages").
// Custom provider packages may declare their own API values.
type API string

const (
	APIAnthropicMessages API = "anthropic-messages"
	APIOpenAICompletions API = "openai-completions"
	APIOpenAIResponses   API = "openai-responses"
)

// CacheRetention selects the prompt-cache lifetime requested from a provider.
type CacheRetention string

const (
	CacheRetentionNone  CacheRetention = "none"
	CacheRetentionShort CacheRetention = "short"
	CacheRetentionLong  CacheRetention = "long"
)

// IsValid reports whether r is a declared cache retention tier. The empty
// value means CacheRetentionNone.
func (r CacheRetention) IsValid() bool {
	switch r {
	case "", CacheRetentionNone, CacheRetentionShort, CacheRetentionLong:
		return true
	default:
		return false
	}
}

// CostTier is request-wide pricing that applies once the request input exceeds
// InputTokensAbove. All four rates are required for a tier; the highest
// matching threshold applies to the whole request.
type CostTier struct {
	InputTokensAbove     int     `json:"inputTokensAbove"`
	InputPerMillion      float64 `json:"inputPerMillion"`
	OutputPerMillion     float64 `json:"outputPerMillion"`
	CacheReadPerMillion  float64 `json:"cacheReadPerMillion"`
	CacheWritePerMillion float64 `json:"cacheWritePerMillion"`
}

// Cost is catalog pricing in USD per million tokens.
type Cost struct {
	InputPerMillion      float64    `json:"inputPerMillion"`
	OutputPerMillion     float64    `json:"outputPerMillion"`
	CacheReadPerMillion  float64    `json:"cacheReadPerMillion"`
	CacheWritePerMillion float64    `json:"cacheWritePerMillion"`
	Tiers                []CostTier `json:"tiers,omitempty"`
}

// RatesForInput returns the rates that apply to a request whose total input is
// inputTokens. The highest matching tier wins; otherwise the base rates apply.
func (c Cost) RatesForInput(inputTokens int) (input, output, cacheRead, cacheWrite float64) {
	input, output = c.InputPerMillion, c.OutputPerMillion
	cacheRead, cacheWrite = c.CacheReadPerMillion, c.CacheWritePerMillion

	matched := false
	var threshold int
	for _, tier := range c.Tiers {
		if inputTokens > tier.InputTokensAbove && (!matched || tier.InputTokensAbove > threshold) {
			input, output = tier.InputPerMillion, tier.OutputPerMillion
			cacheRead, cacheWrite = tier.CacheReadPerMillion, tier.CacheWritePerMillion
			threshold, matched = tier.InputTokensAbove, true
		}
	}
	return input, output, cacheRead, cacheWrite
}

// EstimateUsage returns the catalog-derived cost of a request. It never
// overwrites provider-reported billed cost; callers decide which to publish.
//
// Usage.Input is the total input (uncached + cache read + cache write), so the
// input rate applies only to the uncached remainder. Cache writes with 1h
// retention are billed at twice the input rate, matching Anthropic.
func (c Cost) EstimateUsage(u Usage) UsageCost {
	input, output, cacheRead, cacheWrite := c.RatesForInput(u.Input)

	uncached := u.Input - u.CacheRead - u.CacheWrite
	if uncached < 0 {
		uncached = 0
	}
	longWrite := u.CacheWrite1h
	if longWrite > u.CacheWrite {
		longWrite = u.CacheWrite
	}
	shortWrite := u.CacheWrite - longWrite

	cost := UsageCost{
		Input:      float64(uncached) * input / 1_000_000,
		Output:     float64(u.Output) * output / 1_000_000,
		CacheRead:  float64(u.CacheRead) * cacheRead / 1_000_000,
		CacheWrite: (float64(shortWrite)*cacheWrite + float64(longWrite)*input*2) / 1_000_000,
	}
	cost.Total = cost.Input + cost.Output + cost.CacheRead + cost.CacheWrite
	return cost
}

// PromptCacheSpec declares the best-effort prompt-cache lifetimes, in seconds,
// for the short and long retention tiers. Zero means the lifetime is unknown.
type PromptCacheSpec struct {
	ShortTTLSeconds int `json:"short,omitempty"`
	LongTTLSeconds  int `json:"long,omitempty"`
}

// Supports reports whether the model declares a lifetime for r.
func (s *PromptCacheSpec) Supports(r CacheRetention) bool {
	if s == nil {
		return false
	}
	switch r {
	case CacheRetentionShort:
		return s.ShortTTLSeconds > 0
	case CacheRetentionLong:
		return s.LongTTLSeconds > 0
	default:
		return false
	}
}

// Model is one catalog entry. Optional fields distinguish "not configured"
// (zero value) from a real value; Merge treats zero values as absent.
type Model struct {
	ID             string                `json:"id"`
	Name           string                `json:"name,omitempty"`
	Provider       string                `json:"provider"`
	API            API                   `json:"api"`
	BaseURL        string                `json:"baseUrl,omitempty"`
	Input          []Modality            `json:"input,omitempty"`
	ContextWindow  int                   `json:"contextWindow,omitempty"`
	MaxTokens      int                   `json:"maxTokens,omitempty"`
	Reasoning      bool                  `json:"reasoning,omitempty"`
	ThinkingLevels map[ThinkingLevel]any `json:"thinkingLevels,omitempty"`
	PromptCache    *PromptCacheSpec      `json:"promptCache,omitempty"`
	Cost           Cost                  `json:"cost,omitempty"`
	Compat         json.RawMessage       `json:"compat,omitempty"`
	Headers        map[string]string     `json:"headers,omitempty"`
}

// Key returns the catalog identity "provider/id".
func (m Model) Key() string {
	return m.Provider + "/" + m.ID
}

// ModelFor resolves id against the provider's catalog. When the catalog has no
// entry, it returns a minimal pass-through descriptor so gateways and newly
// released model IDs work without a cortex update. BaseURL is copied from the
// first catalog entry as metadata only; dispatch still goes through p.
//
// api and input describe the pass-through case's wire protocol and modalities;
// catalog hits are returned unchanged.
func ModelFor(p Provider, id string, api API, input []Modality) *Model {
	if p == nil {
		return nil
	}
	name := strings.TrimSpace(id)
	models := p.Models()
	if name == "" {
		if len(models) == 0 {
			return nil
		}
		m := models[0]
		return &m
	}
	for i := range models {
		if models[i].ID == name {
			m := models[i]
			return &m
		}
	}
	baseURL := ""
	if len(models) > 0 {
		baseURL = models[0].BaseURL
	}
	return &Model{
		ID:       name,
		Name:     name,
		Provider: p.ID(),
		API:      api,
		BaseURL:  baseURL,
		Input:    append([]Modality(nil), input...),
	}
}

// Validate checks the fields every dispatchable model needs. Unknown API and
// modality values are allowed so provider packages can extend the catalog.
func (m Model) Validate() error {
	if strings.TrimSpace(m.ID) == "" {
		return errors.New("model: id is required")
	}
	if strings.TrimSpace(m.Provider) == "" {
		return fmt.Errorf("model %q: provider is required", m.ID)
	}
	if strings.TrimSpace(string(m.API)) == "" {
		return fmt.Errorf("model %s: api is required", m.Key())
	}
	if m.ContextWindow < 0 {
		return fmt.Errorf("model %s: contextWindow must not be negative", m.Key())
	}
	if m.MaxTokens < 0 {
		return fmt.Errorf("model %s: maxTokens must not be negative", m.Key())
	}
	if m.ContextWindow > 0 && m.MaxTokens > m.ContextWindow {
		return fmt.Errorf("model %s: maxTokens %d exceeds contextWindow %d", m.Key(), m.MaxTokens, m.ContextWindow)
	}
	for _, modality := range m.Input {
		if strings.TrimSpace(string(modality)) == "" {
			return fmt.Errorf("model %s: empty input modality", m.Key())
		}
	}
	for level, value := range m.ThinkingLevels {
		if strings.TrimSpace(string(level)) == "" {
			return fmt.Errorf("model %s: empty thinking level key", m.Key())
		}
		_ = value // nil explicitly marks the level unsupported.
	}
	if err := m.Cost.validate(); err != nil {
		return fmt.Errorf("model %s: %w", m.Key(), err)
	}
	if m.PromptCache != nil {
		if m.PromptCache.ShortTTLSeconds < 0 || m.PromptCache.LongTTLSeconds < 0 {
			return fmt.Errorf("model %s: prompt cache TTL must not be negative", m.Key())
		}
	}
	if len(m.Compat) > 0 && !json.Valid(m.Compat) {
		return fmt.Errorf("model %s: compat is not valid JSON", m.Key())
	}
	for name := range m.Headers {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("model %s: empty header name", m.Key())
		}
	}
	return nil
}

func (c Cost) validate() error {
	rates := []struct {
		name  string
		value float64
	}{
		{"inputPerMillion", c.InputPerMillion},
		{"outputPerMillion", c.OutputPerMillion},
		{"cacheReadPerMillion", c.CacheReadPerMillion},
		{"cacheWritePerMillion", c.CacheWritePerMillion},
	}
	for _, rate := range rates {
		if rate.value < 0 {
			return fmt.Errorf("%s must not be negative", rate.name)
		}
	}
	previous := -1
	for i, tier := range c.Tiers {
		if tier.InputTokensAbove < 0 {
			return fmt.Errorf("cost tier %d: inputTokensAbove must not be negative", i)
		}
		if tier.InputTokensAbove <= previous {
			return fmt.Errorf("cost tiers must be sorted by strictly ascending inputTokensAbove")
		}
		previous = tier.InputTokensAbove
		values := []struct {
			name  string
			value float64
		}{
			{"inputPerMillion", tier.InputPerMillion},
			{"outputPerMillion", tier.OutputPerMillion},
			{"cacheReadPerMillion", tier.CacheReadPerMillion},
			{"cacheWritePerMillion", tier.CacheWritePerMillion},
		}
		for _, rate := range values {
			if rate.value < 0 {
				return fmt.Errorf("cost tier %d: %s must not be negative", i, rate.name)
			}
		}
	}
	return nil
}

// Clone returns a deep copy. Callers can mutate the result without touching
// the catalog.
func (m Model) Clone() Model {
	out := m
	if m.Input != nil {
		out.Input = append([]Modality(nil), m.Input...)
	}
	out.ThinkingLevels = cloneAnyMap(m.ThinkingLevels)
	if m.PromptCache != nil {
		spec := *m.PromptCache
		out.PromptCache = &spec
	}
	if m.Cost.Tiers != nil {
		out.Cost.Tiers = append([]CostTier(nil), m.Cost.Tiers...)
	}
	if m.Compat != nil {
		out.Compat = append(json.RawMessage(nil), m.Compat...)
	}
	out.Headers = cloneStringMap(m.Headers)
	return out
}

// Merge layers override on top of base field by field. Non-zero scalars,
// non-nil slices/maps and non-empty strings in override win; maps merge
// key by key. Reasoning cannot be turned off by override (false means
// "unset"); use the full model when an explicit false is required.
func Merge(base, override Model) Model {
	out := base.Clone()

	if override.ID != "" {
		out.ID = override.ID
	}
	if override.Name != "" {
		out.Name = override.Name
	}
	if override.Provider != "" {
		out.Provider = override.Provider
	}
	if override.API != "" {
		out.API = override.API
	}
	if override.BaseURL != "" {
		out.BaseURL = override.BaseURL
	}
	if override.Input != nil {
		out.Input = append([]Modality(nil), override.Input...)
	}
	if override.ContextWindow != 0 {
		out.ContextWindow = override.ContextWindow
	}
	if override.MaxTokens != 0 {
		out.MaxTokens = override.MaxTokens
	}
	if override.Reasoning {
		out.Reasoning = true
	}
	if override.ThinkingLevels != nil {
		out.ThinkingLevels = cloneAnyMap(base.ThinkingLevels)
		if out.ThinkingLevels == nil {
			out.ThinkingLevels = make(map[ThinkingLevel]any, len(override.ThinkingLevels))
		}
		for level, value := range override.ThinkingLevels {
			out.ThinkingLevels[level] = cloneAnyValue(value)
		}
	}
	if override.PromptCache != nil {
		if out.PromptCache == nil {
			out.PromptCache = &PromptCacheSpec{}
		}
		if override.PromptCache.ShortTTLSeconds != 0 {
			out.PromptCache.ShortTTLSeconds = override.PromptCache.ShortTTLSeconds
		}
		if override.PromptCache.LongTTLSeconds != 0 {
			out.PromptCache.LongTTLSeconds = override.PromptCache.LongTTLSeconds
		}
	}
	if override.Cost.Tiers != nil {
		out.Cost.Tiers = append([]CostTier(nil), override.Cost.Tiers...)
	}
	if override.Cost.InputPerMillion != 0 {
		out.Cost.InputPerMillion = override.Cost.InputPerMillion
	}
	if override.Cost.OutputPerMillion != 0 {
		out.Cost.OutputPerMillion = override.Cost.OutputPerMillion
	}
	if override.Cost.CacheReadPerMillion != 0 {
		out.Cost.CacheReadPerMillion = override.Cost.CacheReadPerMillion
	}
	if override.Cost.CacheWritePerMillion != 0 {
		out.Cost.CacheWritePerMillion = override.Cost.CacheWritePerMillion
	}
	if len(override.Compat) > 0 {
		out.Compat = append(json.RawMessage(nil), override.Compat...)
	}
	if override.Headers != nil {
		if out.Headers == nil {
			out.Headers = make(map[string]string, len(override.Headers))
		}
		for name, value := range override.Headers {
			out.Headers[name] = value
		}
	}
	return out
}

// ApplyOverrides merges overrides into base by "provider/id". An override
// whose key is absent from base is appended as a new model. Ordering of base
// is preserved; new models keep the override order.
func ApplyOverrides(base, overrides []Model) []Model {
	out := make([]Model, len(base))
	index := make(map[string]int, len(base))
	for i, m := range base {
		out[i] = m.Clone()
		index[m.Key()] = i
	}
	for _, override := range overrides {
		if i, ok := index[override.Key()]; ok {
			out[i] = Merge(out[i], override)
			continue
		}
		index[override.Key()] = len(out)
		out = append(out, override.Clone())
	}
	return out
}

// Catalog is the model lookup surface used by the runtime and engine.
type Catalog interface {
	// List returns all catalog entries. The slice is owned by the caller.
	List(ctx context.Context) ([]Model, error)
	// Get returns one entry by provider and id.
	Get(ctx context.Context, provider, id string) (*Model, error)
}

// ErrModelNotFound is returned by Catalog.Get for an unknown model.
var ErrModelNotFound = errors.New("model not found")

// StaticCatalog is an immutable in-memory Catalog. It is safe for concurrent
// use once constructed.
type StaticCatalog struct {
	models []Model
	index  map[string]int
}

// NewStaticCatalog validates models and builds a catalog. Duplicate
// "provider/id" entries are rejected so override mistakes surface early.
func NewStaticCatalog(models []Model) (*StaticCatalog, error) {
	c := &StaticCatalog{
		models: make([]Model, 0, len(models)),
		index:  make(map[string]int, len(models)),
	}
	for i, m := range models {
		if err := m.Validate(); err != nil {
			return nil, fmt.Errorf("catalog entry %d: %w", i, err)
		}
		key := m.Key()
		if _, exists := c.index[key]; exists {
			return nil, fmt.Errorf("catalog: duplicate model %s", key)
		}
		c.index[key] = len(c.models)
		c.models = append(c.models, m.Clone())
	}
	return c, nil
}

// List implements Catalog.
func (c *StaticCatalog) List(_ context.Context) ([]Model, error) {
	out := make([]Model, len(c.models))
	for i, m := range c.models {
		out[i] = m.Clone()
	}
	return out, nil
}

// Get implements Catalog.
func (c *StaticCatalog) Get(_ context.Context, provider, id string) (*Model, error) {
	i, ok := c.index[provider+"/"+id]
	if !ok {
		return nil, fmt.Errorf("%w: %s/%s", ErrModelNotFound, provider, id)
	}
	m := c.models[i].Clone()
	return &m, nil
}

func cloneAnyMap[K comparable](in map[K]any) map[K]any {
	if in == nil {
		return nil
	}
	out := make(map[K]any, len(in))
	for k, v := range in {
		out[k] = cloneAnyValue(v)
	}
	return out
}

// cloneAnyValue deep-copies the JSON-shaped containers that thinking-level
// values may hold. Other values are immutable and copied by assignment.
func cloneAnyValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			out[k] = cloneAnyValue(v)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, v := range typed {
			out[i] = cloneAnyValue(v)
		}
		return out
	default:
		return value
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
