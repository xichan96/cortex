package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xichan96/cortex/agent/runtime"
	"github.com/xichan96/cortex/dino"
)

// The CLI must link the OpenAI-compatible provider: DefaultConfig uses
// provider type "openai", and ValidateConfig rejects unregistered types.
func TestDefaultProviderRegistered(t *testing.T) {
	cfg := dino.DefaultConfig()
	if err := dino.ValidateConfig(cfg); err != nil {
		t.Fatalf("default provider %q is not usable: %v", cfg.Provider.Type, err)
	}
}

func TestBuildConfigOverlayAndOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cortex.yaml")
	if err := os.WriteFile(path, []byte(`
default_model: yaml-model
provider:
  type: openai
  base_url: https://yaml.example
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := buildConfig(cliOptions{
		configPath: path,
		model:      "cli-model",
		baseURL:    "https://cli.example",
		apiKey:     "key",
	})
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if cfg.DefaultModel != "cli-model" {
		t.Errorf("model = %q, want cli-model", cfg.DefaultModel)
	}
	if cfg.Provider.Models["default"] != "cli-model" {
		t.Errorf("provider default = %q", cfg.Provider.Models["default"])
	}
	if cfg.Provider.BaseURL != "https://cli.example" {
		t.Errorf("base url = %q", cfg.Provider.BaseURL)
	}
	if cfg.Provider.APIKey != "key" {
		t.Errorf("api key = %q", cfg.Provider.APIKey)
	}
}

func TestBuildConfigRequiresModel(t *testing.T) {
	cfg, err := buildConfig(cliOptions{})
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if cfg.DefaultModel == "" {
		t.Fatal("DefaultConfig should provide a model")
	}
}

type fakeRuntime struct {
	mu      sync.Mutex
	subs    []func(runtime.Event)
	settled chan struct{}
	once    sync.Once
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{settled: make(chan struct{})}
}

func (f *fakeRuntime) Prompt(_ context.Context, _ runtime.PromptInput) (runtime.Disposition, error) {
	f.emit(runtime.Event{Type: runtime.EventAgentStart, SessionID: "fake", Seq: 1})
	f.once.Do(func() { close(f.settled) })
	return runtime.DispositionStarted, nil
}
func (f *fakeRuntime) Steer(context.Context, runtime.Message) error    { return nil }
func (f *fakeRuntime) FollowUp(context.Context, runtime.Message) error { return nil }
func (f *fakeRuntime) Abort(context.Context) error                     { return nil }
func (f *fakeRuntime) Subscribe(fn func(runtime.Event)) runtime.Unsubscribe {
	f.mu.Lock()
	id := len(f.subs)
	f.subs = append(f.subs, fn)
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		if id < len(f.subs) {
			f.subs[id] = func(runtime.Event) {}
		}
		f.mu.Unlock()
	}
}
func (f *fakeRuntime) State() runtime.State { return runtime.State{SessionID: "fake"} }
func (f *fakeRuntime) Entries(context.Context, runtime.EntryQuery) (*runtime.EntriesResult, error) {
	return &runtime.EntriesResult{}, nil
}
func (f *fakeRuntime) Settled() <-chan struct{}    { return f.settled }
func (f *fakeRuntime) Close(context.Context) error { return nil }

func (f *fakeRuntime) emit(ev runtime.Event) {
	f.mu.Lock()
	subs := make([]func(runtime.Event), 0, len(f.subs))
	for _, fn := range f.subs {
		subs = append(subs, fn)
	}
	f.mu.Unlock()
	for _, fn := range subs {
		fn(ev)
	}
}

func TestRunJSONStreamsEvents(t *testing.T) {
	rt := newFakeRuntime()
	var out bytes.Buffer
	if err := runJSON(context.Background(), rt, "hello", &out); err != nil {
		t.Fatalf("runJSON: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("output = %q, want disposition + event", out.String())
	}
	sawDisposition, sawStart := false, false
	for _, line := range lines {
		var probe map[string]any
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			t.Fatalf("decode line %q: %v", line, err)
		}
		if probe["type"] == "prompt" {
			if probe["disposition"] != "started" {
				t.Fatalf("disposition line = %v", probe)
			}
			sawDisposition = true
		}
		if probe["type"] == string(runtime.EventAgentStart) {
			sawStart = true
		}
	}
	if !sawDisposition || !sawStart {
		t.Fatalf("output = %q, want prompt disposition and agent_start", out.String())
	}
}
