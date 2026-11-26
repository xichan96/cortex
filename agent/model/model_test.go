package model

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func baseModel() Model {
	return Model{
		ID:            "claude-sonnet-5-5",
		Name:          "Claude Sonnet 5.5",
		Provider:      "anthropic",
		API:           APIAnthropicMessages,
		BaseURL:       "https://api.anthropic.com",
		Input:         []Modality{ModalityText, ModalityImage},
		ContextWindow: 200_000,
		MaxTokens:     64_000,
		Reasoning:     true,
		ThinkingLevels: map[ThinkingLevel]any{
			ThinkingLow:  "low",
			ThinkingHigh: "high",
		},
		PromptCache: &PromptCacheSpec{ShortTTLSeconds: 300, LongTTLSeconds: 3600},
		Cost: Cost{
			InputPerMillion:      3,
			OutputPerMillion:     15,
			CacheReadPerMillion:  0.3,
			CacheWritePerMillion: 3.75,
			Tiers: []CostTier{
				{InputTokensAbove: 100, InputPerMillion: 1, OutputPerMillion: 2, CacheReadPerMillion: 0.1, CacheWritePerMillion: 1.2},
			},
		},
		Compat:  json.RawMessage(`{"supportsToolStream":true}`),
		Headers: map[string]string{"anthropic-beta": "prompt-caching-2024-07-31"},
	}
}

func TestCostRatesForInput(t *testing.T) {
	cost := Cost{
		InputPerMillion:      3,
		OutputPerMillion:     15,
		CacheReadPerMillion:  0.3,
		CacheWritePerMillion: 3.75,
		Tiers: []CostTier{
			{InputTokensAbove: 100, InputPerMillion: 1, OutputPerMillion: 2, CacheReadPerMillion: 0.1, CacheWritePerMillion: 1.2},
			{InputTokensAbove: 1_000, InputPerMillion: 0.5, OutputPerMillion: 1, CacheReadPerMillion: 0.05, CacheWritePerMillion: 0.6},
		},
	}

	tests := []struct {
		name      string
		input     int
		wantInput float64
	}{
		{"below first tier", 50, 3},
		{"exactly at threshold does not match", 100, 3},
		{"first tier", 101, 1},
		{"second tier", 1_001, 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, output, cacheRead, cacheWrite := cost.RatesForInput(tt.input)
			if input != tt.wantInput {
				t.Fatalf("input rate = %v, want %v", input, tt.wantInput)
			}
			if output <= 0 || cacheRead <= 0 || cacheWrite <= 0 {
				t.Fatalf("unexpected rates: %v %v %v", output, cacheRead, cacheWrite)
			}
		})
	}
}

func TestCostEstimateUsage(t *testing.T) {
	cost := Cost{
		InputPerMillion:      3,
		OutputPerMillion:     15,
		CacheReadPerMillion:  0.3,
		CacheWritePerMillion: 3.75,
	}
	// Input is the total: 1M uncached + 1M cache read + 1M cache write.
	usage := Usage{Input: 3_000_000, Output: 1_000_000, CacheRead: 1_000_000, CacheWrite: 1_000_000}
	got := cost.EstimateUsage(usage)
	if !approxEqual(got.Input, 3) || !approxEqual(got.Output, 15) ||
		!approxEqual(got.CacheRead, 0.3) || !approxEqual(got.CacheWrite, 3.75) {
		t.Fatalf("unexpected cost components: %+v", got)
	}
	if !approxEqual(got.Total, 3+15+0.3+3.75) {
		t.Fatalf("total = %v", got.Total)
	}
	if got.TotalFromProvider {
		t.Fatal("catalog estimate must not claim provider billing")
	}

	// 1h cache writes are billed at twice the input rate.
	longWrite := cost.EstimateUsage(Usage{Input: 1_000_000, CacheWrite: 1_000_000, CacheWrite1h: 1_000_000})
	if !approxEqual(longWrite.CacheWrite, 6) {
		t.Fatalf("1h cache write cost = %v, want 6", longWrite.CacheWrite)
	}

	// Inconsistent provider accounting never produces a negative input charge.
	overReported := cost.EstimateUsage(Usage{Input: 10, CacheRead: 20})
	if overReported.Input < 0 {
		t.Fatalf("negative input cost: %v", overReported.Input)
	}
}

func TestMergeIsFieldLevelAndDoesNotMutateBase(t *testing.T) {
	base := baseModel()
	override := Model{
		Name:          "Sonnet (project)",
		BaseURL:       "https://proxy.example.com",
		Input:         []Modality{ModalityText},
		ContextWindow: 100_000,
		ThinkingLevels: map[ThinkingLevel]any{
			ThinkingLow: nil,
			ThinkingMax: "max",
		},
		PromptCache: &PromptCacheSpec{LongTTLSeconds: 7200},
		Cost: Cost{
			OutputPerMillion: 20,
			Tiers: []CostTier{
				{InputTokensAbove: 500, InputPerMillion: 2, OutputPerMillion: 4, CacheReadPerMillion: 0.2, CacheWritePerMillion: 2.4},
			},
		},
		Compat:  json.RawMessage(`{"supportsToolStream":false}`),
		Headers: map[string]string{"x-project": "1", "anthropic-beta": "override"},
	}

	merged := Merge(base, override)

	if merged.ID != base.ID || merged.Provider != base.Provider || merged.API != base.API {
		t.Fatalf("identity fields changed: %+v", merged)
	}
	if merged.Name != "Sonnet (project)" || merged.BaseURL != "https://proxy.example.com" {
		t.Fatalf("scalar override failed: %+v", merged)
	}
	if len(merged.Input) != 1 || merged.Input[0] != ModalityText {
		t.Fatalf("input override failed: %v", merged.Input)
	}
	if merged.ContextWindow != 100_000 || merged.MaxTokens != 64_000 {
		t.Fatalf("numeric merge failed: ctx=%d max=%d", merged.ContextWindow, merged.MaxTokens)
	}
	if !merged.Reasoning {
		t.Fatal("reasoning should stay enabled")
	}
	if value, ok := merged.ThinkingLevels[ThinkingLow]; !ok || value != nil {
		t.Fatalf("thinking level nil override not applied: %v", merged.ThinkingLevels)
	}
	if merged.ThinkingLevels[ThinkingHigh] != "high" || merged.ThinkingLevels[ThinkingMax] != "max" {
		t.Fatalf("thinking levels not merged: %v", merged.ThinkingLevels)
	}
	if merged.PromptCache == nil || merged.PromptCache.ShortTTLSeconds != 300 ||
		merged.PromptCache.LongTTLSeconds != 7200 {
		t.Fatalf("prompt cache merge failed: %+v", merged.PromptCache)
	}
	if merged.Cost.InputPerMillion != 3 || merged.Cost.OutputPerMillion != 20 {
		t.Fatalf("cost rates not merged field by field: %+v", merged.Cost)
	}
	if len(merged.Cost.Tiers) != 1 || merged.Cost.Tiers[0].InputTokensAbove != 500 {
		t.Fatalf("tiers not replaced: %+v", merged.Cost.Tiers)
	}
	if string(merged.Compat) != `{"supportsToolStream":false}` {
		t.Fatalf("compat not replaced: %s", merged.Compat)
	}
	if merged.Headers["x-project"] != "1" || merged.Headers["anthropic-beta"] != "override" {
		t.Fatalf("headers not merged: %v", merged.Headers)
	}

	// Base must remain untouched, including nested containers.
	if base.Name != "Claude Sonnet 5.5" || base.ThinkingLevels[ThinkingLow] != "low" ||
		base.PromptCache.LongTTLSeconds != 3600 || base.Headers["anthropic-beta"] != "prompt-caching-2024-07-31" {
		t.Fatalf("base model was mutated: %+v", base)
	}
}

func TestApplyOverridesOrderingAndNewModels(t *testing.T) {
	a := baseModel()
	b := baseModel()
	b.ID = "claude-opus-5-5"
	b.Provider = "anthropic"

	overrides := []Model{
		{Provider: "anthropic", ID: a.ID, Name: "override-a"},
		{Provider: "openai", ID: "gpt-x", API: APIOpenAIResponses, Name: "new"},
		{Provider: "anthropic", ID: b.ID, MaxTokens: 32_000},
	}
	got := ApplyOverrides([]Model{a, b}, overrides)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Name != "override-a" || got[1].MaxTokens != 32_000 || got[2].Name != "new" {
		t.Fatalf("unexpected merge result: %+v", got)
	}
}

func TestModelValidate(t *testing.T) {
	if err := baseModel().Validate(); err != nil {
		t.Fatalf("valid model rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Model)
	}{
		{"missing id", func(m *Model) { m.ID = "" }},
		{"missing provider", func(m *Model) { m.Provider = "" }},
		{"missing api", func(m *Model) { m.API = "" }},
		{"max exceeds context", func(m *Model) { m.MaxTokens = m.ContextWindow + 1 }},
		{"negative context", func(m *Model) { m.ContextWindow = -1 }},
		{"negative cost", func(m *Model) { m.Cost.InputPerMillion = -1 }},
		{"unsorted tiers", func(m *Model) {
			m.Cost.Tiers = []CostTier{
				{InputTokensAbove: 10},
				{InputTokensAbove: 5},
			}
		}},
		{"negative tier rate", func(m *Model) {
			m.Cost.Tiers = []CostTier{{InputTokensAbove: 10, OutputPerMillion: -1}}
		}},
		{"negative cache ttl", func(m *Model) { m.PromptCache.ShortTTLSeconds = -1 }},
		{"invalid compat", func(m *Model) { m.Compat = json.RawMessage(`{`) }},
		{"empty header", func(m *Model) { m.Headers[""] = "x" }},
		{"empty modality", func(m *Model) { m.Input = []Modality{""} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := baseModel()
			tt.mutate(&m)
			if err := m.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestStaticCatalog(t *testing.T) {
	catalog, err := NewStaticCatalog([]Model{baseModel()})
	if err != nil {
		t.Fatalf("NewStaticCatalog: %v", err)
	}
	ctx := context.Background()

	list, err := catalog.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}
	// Mutating the returned copy must not affect the catalog.
	list[0].Name = "mutated"
	list[0].Headers["anthropic-beta"] = "mutated"

	got, err := catalog.Get(ctx, "anthropic", "claude-sonnet-5-5")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "Claude Sonnet 5.5" || got.Headers["anthropic-beta"] != "prompt-caching-2024-07-31" {
		t.Fatalf("catalog entry mutated through List: %+v", got)
	}
	got.Headers["anthropic-beta"] = "mutated-again"
	again, _ := catalog.Get(ctx, "anthropic", "claude-sonnet-5-5")
	if again.Headers["anthropic-beta"] != "prompt-caching-2024-07-31" {
		t.Fatal("catalog entry mutated through Get")
	}

	if _, err := catalog.Get(ctx, "anthropic", "missing"); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("Get missing error = %v, want ErrModelNotFound", err)
	}
	if _, err := NewStaticCatalog([]Model{baseModel(), baseModel()}); err == nil {
		t.Fatal("duplicate model accepted")
	}
	invalid := baseModel()
	invalid.ID = ""
	if _, err := NewStaticCatalog([]Model{invalid}); err == nil {
		t.Fatal("invalid model accepted")
	}
}

func TestPromptCacheSpecSupports(t *testing.T) {
	spec := &PromptCacheSpec{ShortTTLSeconds: 300}
	if !spec.Supports(CacheRetentionShort) {
		t.Fatal("short tier should be supported")
	}
	if spec.Supports(CacheRetentionLong) || spec.Supports(CacheRetentionNone) {
		t.Fatal("unsupported tier reported as supported")
	}
	var nilSpec *PromptCacheSpec
	if nilSpec.Supports(CacheRetentionShort) {
		t.Fatal("nil spec reported as supported")
	}
}

func TestModelForCatalogAndPassthrough(t *testing.T) {
	known := Model{
		ID:       "known",
		Name:     "Known Model",
		Provider: "p",
		API:      APIOpenAICompletions,
		BaseURL:  "https://example.test",
	}
	p := &fakeProvider{id: "p", models: []Model{known}}

	if got := ModelFor(p, "known", APIAnthropicMessages, nil); got == nil ||
		got.Name != "Known Model" || got.API != APIOpenAICompletions {
		t.Fatalf("catalog hit = %+v", got)
	}
	if got := ModelFor(p, "", APIAnthropicMessages, nil); got == nil || got.ID != "known" {
		t.Fatalf("empty id should fall back to first model, got %+v", got)
	}

	got := ModelFor(p, "brand-new", APIAnthropicMessages, []Modality{ModalityText})
	if got == nil {
		t.Fatal("passthrough model is nil")
	}
	if got.ID != "brand-new" || got.Provider != "p" ||
		got.API != APIAnthropicMessages || got.BaseURL != "https://example.test" ||
		len(got.Input) != 1 || got.Input[0] != ModalityText {
		t.Fatalf("passthrough = %+v", got)
	}
	if ModelFor(nil, "x", APIAnthropicMessages, nil) != nil {
		t.Fatal("nil provider should return nil")
	}
}
