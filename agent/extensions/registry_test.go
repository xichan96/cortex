package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

type fakeTool struct {
	name string
	tag  string
}

func (t *fakeTool) Name() string        { return t.name }
func (t *fakeTool) Description() string { return t.tag }
func (t *fakeTool) Execute(context.Context, json.RawMessage) (any, error) {
	return t.tag, nil
}

type fakeCommand struct {
	name string
}

func (c *fakeCommand) Name() string        { return c.name }
func (c *fakeCommand) Description() string { return c.name + " command" }
func (c *fakeCommand) Execute(context.Context, []string) (string, error) {
	return c.name, nil
}

type fakeResource struct {
	name string
	kind ResourceKind
	loc  string
}

func (r fakeResource) Name() string       { return r.name }
func (r fakeResource) Kind() ResourceKind { return r.kind }
func (r fakeResource) Location() string   { return r.loc }

type stubExtension struct {
	name       string
	initFn     func(context.Context, *API) error
	shutdownFn func(context.Context) error
	shutdowns  int
}

func (s *stubExtension) Name() string { return s.name }

func (s *stubExtension) Init(ctx context.Context, api *API) error {
	if s.initFn != nil {
		return s.initFn(ctx, api)
	}
	return nil
}

func (s *stubExtension) Shutdown(ctx context.Context) error {
	s.shutdowns++
	if s.shutdownFn != nil {
		return s.shutdownFn(ctx)
	}
	return nil
}

func mustRegisterTool(t *testing.T, reg *Registry, registration Registration, tag string) {
	t.Helper()
	if err := reg.RegisterTool(registration, &fakeTool{name: registration.Name, tag: tag}); err != nil {
		t.Fatalf("register tool %s: %v", registration.Name, err)
	}
}

func toolTag(t *testing.T, reg *Registry, name string) string {
	t.Helper()
	tool, _, ok := reg.LookupTool(name)
	if !ok {
		t.Fatalf("tool %q not found", name)
	}
	return tool.(*fakeTool).tag
}

func TestRegistryPrecedenceProjectOverUserOverPackageOverBuiltin(t *testing.T) {
	reg := NewRegistry()

	// Register in an order that deliberately contradicts precedence.
	mustRegisterTool(t, reg, Registration{Name: "read", Source: SourceInfo{Kind: SourceProject}}, "project")
	mustRegisterTool(t, reg, Registration{Name: "read", Source: SourceInfo{Kind: SourceBuiltin}}, "builtin")
	mustRegisterTool(t, reg, Registration{Name: "read", Source: SourceInfo{Kind: SourcePackage, ID: "acme"}}, "package")
	mustRegisterTool(t, reg, Registration{Name: "read", Source: SourceInfo{Kind: SourceUser}}, "user")

	if got := toolTag(t, reg, "read"); got != "project" {
		t.Fatalf("active tool = %q, want project", got)
	}
	if got := len(reg.ListTools()); got != 1 {
		t.Fatalf("ListTools = %d, want only the winner", got)
	}

	registration := reg.Snapshot().Tools[0]
	if registration.Source.Kind != SourceProject {
		t.Fatalf("snapshot source = %+v, want project origin recorded", registration.Source)
	}

	// Removing the winner restores the next-best registration.
	if !reg.UnregisterTool("read") {
		t.Fatal("UnregisterTool returned false")
	}
	if got := toolTag(t, reg, "read"); got != "user" {
		t.Fatalf("after removing project, tool = %q, want user", got)
	}
	if !reg.UnregisterTool("read") {
		t.Fatal("UnregisterTool returned false")
	}
	if got := toolTag(t, reg, "read"); got != "package" {
		t.Fatalf("after removing user, tool = %q, want package", got)
	}
	if !reg.UnregisterTool("read") {
		t.Fatal("UnregisterTool returned false")
	}
	if got := toolTag(t, reg, "read"); got != "builtin" {
		t.Fatalf("after removing package, tool = %q, want builtin", got)
	}
}

func TestRegistryPriorityAndInsertionOrderBreakTies(t *testing.T) {
	reg := NewRegistry()

	mustRegisterTool(t, reg, Registration{Name: "t", Source: SourceInfo{Kind: SourceUser}, Priority: 1}, "low")
	mustRegisterTool(t, reg, Registration{Name: "t", Source: SourceInfo{Kind: SourceUser}, Priority: 5}, "high")
	if got := toolTag(t, reg, "t"); got != "high" {
		t.Fatalf("tool = %q, want higher priority", got)
	}

	mustRegisterTool(t, reg, Registration{Name: "u", Source: SourceInfo{Kind: SourceUser}}, "first")
	mustRegisterTool(t, reg, Registration{Name: "u", Source: SourceInfo{Kind: SourceUser}}, "second")
	if got := toolTag(t, reg, "u"); got != "second" {
		t.Fatalf("tool = %q, want later registration to win a full tie", got)
	}
}

func TestRegistryOverrideDoesNotEscalateOverProject(t *testing.T) {
	reg := NewRegistry()
	mustRegisterTool(t, reg, Registration{Name: "t", Source: SourceInfo{Kind: SourceProject}}, "project")
	mustRegisterTool(t, reg, Registration{Name: "t", Source: SourceInfo{Kind: SourcePackage, ID: "acme"}, Override: true}, "package")

	if got := toolTag(t, reg, "t"); got != "project" {
		t.Fatalf("tool = %q; Override must not beat project precedence", got)
	}
}

func TestRegistryOverrideReplacesBuiltin(t *testing.T) {
	reg := NewRegistry()
	mustRegisterTool(t, reg, Registration{Name: "t", Source: SourceInfo{Kind: SourceBuiltin}}, "builtin")
	// An unknown source would normally rank below builtin; Override lifts it.
	mustRegisterTool(t, reg, Registration{Name: "t", Source: SourceInfo{Kind: "legacy"}, Override: true}, "legacy")

	if got := toolTag(t, reg, "t"); got != "legacy" {
		t.Fatalf("tool = %q, want explicit override of builtin", got)
	}
}

func TestRegistryValidation(t *testing.T) {
	reg := NewRegistry()

	if err := reg.RegisterTool(Registration{Name: "x"}, nil); !errors.Is(err, ErrNilValue) {
		t.Fatalf("nil tool err = %v, want ErrNilValue", err)
	}
	if err := reg.RegisterTool(Registration{}, &fakeTool{name: ""}); !errors.Is(err, ErrEmptyName) {
		t.Fatalf("empty name err = %v, want ErrEmptyName", err)
	}
	if err := reg.RegisterTool(Registration{Name: "other"}, &fakeTool{name: "actual"}); !errors.Is(err, ErrNameMismatch) {
		t.Fatalf("mismatch err = %v, want ErrNameMismatch", err)
	}
	if err := reg.RegisterResource(Registration{Name: "r"}, fakeResource{name: "r"}); !errors.Is(err, ErrEmptyKind) {
		t.Fatalf("empty kind err = %v, want ErrEmptyKind", err)
	}
}

func TestRegistryExtensionLifecycleRevokesRegistrations(t *testing.T) {
	reg := NewRegistry()
	var sawConfig string
	ext := &stubExtension{
		name: "acme",
		initFn: func(_ context.Context, api *API) error {
			sawConfig = string(api.Config)
			if err := api.Tools.Register(Registration{}, &fakeTool{name: "echo", tag: "ext"}); err != nil {
				return err
			}
			if err := api.Commands.Register(Registration{}, &fakeCommand{name: "greet"}); err != nil {
				return err
			}
			if err := api.Models.Register(Registration{}, ModelInfo{ID: "acme-model"}); err != nil {
				return err
			}
			if err := api.Resources.Register(Registration{}, fakeResource{name: "guide", kind: ResourceSkill, loc: "/pkg/guide.md"}); err != nil {
				return err
			}
			_, err := api.Events.On(EventAgentStart, func(context.Context, *EventEnvelope) (EventResult, error) {
				return EventResult{}, nil
			})
			return err
		},
	}

	if err := reg.RegisterExtension(context.Background(), ext, SourceInfo{Kind: SourcePackage, ID: "acme-tools"}, json.RawMessage(`{"enabled":true}`)); err != nil {
		t.Fatalf("RegisterExtension: %v", err)
	}
	if sawConfig != `{"enabled":true}` {
		t.Fatalf("Config = %q, want extension section verbatim", sawConfig)
	}

	tool, registration, ok := reg.LookupTool("echo")
	if !ok || tool.(*fakeTool).tag != "ext" {
		t.Fatal("extension tool not registered")
	}
	if registration.Source.Kind != SourcePackage || registration.Source.ID != "acme-tools" {
		t.Fatalf("source = %+v, want package:acme-tools", registration.Source)
	}
	if registration.Owner != "acme" {
		t.Fatalf("owner = %q, want acme", registration.Owner)
	}
	if len(reg.ListCommands()) != 1 || len(reg.ListModels()) != 1 || len(reg.ListResources()) != 1 {
		t.Fatalf("snapshot incomplete: commands=%d models=%d resources=%d",
			len(reg.ListCommands()), len(reg.ListModels()), len(reg.ListResources()))
	}
	if reg.Events().HandlerCount(EventAgentStart) != 1 {
		t.Fatal("extension event handler not registered")
	}
	if got := len(reg.Extensions()); got != 1 {
		t.Fatalf("Extensions = %d, want 1", got)
	}

	if err := reg.UnregisterExtension(context.Background(), "acme"); err != nil {
		t.Fatalf("UnregisterExtension: %v", err)
	}
	if ext.shutdowns != 1 {
		t.Fatalf("shutdowns = %d, want 1", ext.shutdowns)
	}
	if _, _, ok := reg.LookupTool("echo"); ok {
		t.Fatal("tool survived extension removal")
	}
	snapshot := reg.Snapshot()
	if len(snapshot.Tools)+len(snapshot.Commands)+len(snapshot.Models)+len(snapshot.Resources)+len(snapshot.Events) != 0 {
		t.Fatalf("snapshot not empty after removal: %+v", snapshot)
	}
	if len(snapshot.Extensions) != 0 {
		t.Fatalf("extensions = %+v, want empty", snapshot.Extensions)
	}

	if err := reg.UnregisterExtension(context.Background(), "acme"); !errors.Is(err, ErrExtensionNotFound) {
		t.Fatalf("second unregister err = %v, want ErrExtensionNotFound", err)
	}
}

func TestRegistryInitFailureRevokesPartialRegistrations(t *testing.T) {
	reg := NewRegistry()
	ext := &stubExtension{
		name: "broken",
		initFn: func(_ context.Context, api *API) error {
			if err := api.Tools.Register(Registration{}, &fakeTool{name: "half"}); err != nil {
				return err
			}
			_, _ = api.Events.On(EventAgentStart, func(context.Context, *EventEnvelope) (EventResult, error) {
				return EventResult{}, nil
			})
			return errors.New("boom")
		},
	}

	err := reg.RegisterExtension(context.Background(), ext, SourceInfo{Kind: SourceUser}, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want init failure", err)
	}
	if _, _, ok := reg.LookupTool("half"); ok {
		t.Fatal("partial tool registration survived failed init")
	}
	if reg.Events().HandlerCount(EventAgentStart) != 0 {
		t.Fatal("partial event subscription survived failed init")
	}
	if len(reg.Extensions()) != 0 {
		t.Fatal("failed extension was recorded as loaded")
	}
}

func TestRegistryInitPanicIsContained(t *testing.T) {
	reg := NewRegistry()
	ext := &stubExtension{
		name: "panics",
		initFn: func(context.Context, *API) error {
			panic("init exploded")
		},
	}
	err := reg.RegisterExtension(context.Background(), ext, SourceInfo{Kind: SourceUser}, nil)
	if err == nil || !strings.Contains(err.Error(), "init panic") {
		t.Fatalf("err = %v, want recovered init panic", err)
	}
	if len(reg.Extensions()) != 0 {
		t.Fatal("panicking extension was recorded as loaded")
	}
}

func TestRegistryShutdownErrorStillRevokes(t *testing.T) {
	reg := NewRegistry()
	ext := &stubExtension{
		name: "leaky",
		initFn: func(_ context.Context, api *API) error {
			return api.Tools.Register(Registration{}, &fakeTool{name: "leaky"})
		},
		shutdownFn: func(context.Context) error { return errors.New("close failed") },
	}
	if err := reg.RegisterExtension(context.Background(), ext, SourceInfo{Kind: SourceUser}, nil); err != nil {
		t.Fatal(err)
	}
	err := reg.UnregisterExtension(context.Background(), "leaky")
	if err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("err = %v, want shutdown failure surfaced", err)
	}
	if _, _, ok := reg.LookupTool("leaky"); ok {
		t.Fatal("registrations must be revoked even when Shutdown fails")
	}
}

func TestRegistryRejectsDuplicateExtensionName(t *testing.T) {
	reg := NewRegistry()
	first := &stubExtension{name: "same"}
	second := &stubExtension{name: "same"}
	if err := reg.RegisterExtension(context.Background(), first, SourceInfo{Kind: SourceUser}, nil); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterExtension(context.Background(), second, SourceInfo{Kind: SourceUser}, nil); !errors.Is(err, ErrExtensionExists) {
		t.Fatalf("err = %v, want ErrExtensionExists", err)
	}
	if len(reg.Extensions()) != 1 {
		t.Fatalf("Extensions = %d, want 1", len(reg.Extensions()))
	}
}

func TestRegistryRegistrarIsScopedToOwner(t *testing.T) {
	reg := NewRegistry()
	owner := &stubExtension{
		name: "owner",
		initFn: func(_ context.Context, api *API) error {
			return api.Tools.Register(Registration{}, &fakeTool{name: "owned"})
		},
	}
	if err := reg.RegisterExtension(context.Background(), owner, SourceInfo{Kind: SourceUser}, nil); err != nil {
		t.Fatal(err)
	}

	other := &stubExtension{
		name: "other",
		initFn: func(_ context.Context, api *API) error {
			api.Tools.Unregister("owned")
			return nil
		},
	}
	if err := reg.RegisterExtension(context.Background(), other, SourceInfo{Kind: SourceUser}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := reg.LookupTool("owned"); !ok {
		t.Fatal("one extension removed another extension's tool")
	}
}

func TestRegistryResourceKindsAreIndependent(t *testing.T) {
	reg := NewRegistry()
	if err := reg.RegisterResource(Registration{}, fakeResource{name: "shared", kind: ResourceSkill}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterResource(Registration{}, fakeResource{name: "shared", kind: ResourcePrompt}); err != nil {
		t.Fatal(err)
	}
	if got := len(reg.ListResources()); got != 2 {
		t.Fatalf("resources = %d, want 2 (one per kind)", got)
	}
	if removed := reg.UnregisterResource("shared"); removed != 2 {
		t.Fatalf("removed = %d, want both kinds", removed)
	}
	if got := len(reg.ListResources()); got != 0 {
		t.Fatalf("resources = %d, want 0", got)
	}
}

func TestRegistrySnapshotIncludesEventsAndExtensions(t *testing.T) {
	reg := NewRegistry()
	ext := &stubExtension{
		name: "observer",
		initFn: func(_ context.Context, api *API) error {
			_, err := api.Events.OnWithOptions(EventTurnEnd, func(context.Context, *EventEnvelope) (EventResult, error) {
				return EventResult{}, nil
			}, HandlerOptions{Priority: 7})
			return err
		},
	}
	if err := reg.RegisterExtension(context.Background(), ext, SourceInfo{Kind: SourceProject}, nil); err != nil {
		t.Fatal(err)
	}

	snapshot := reg.Snapshot()
	if len(snapshot.Events) != 1 {
		t.Fatalf("events = %+v, want 1", snapshot.Events)
	}
	event := snapshot.Events[0]
	if event.Event != EventTurnEnd || event.Owner != "observer" || event.Priority != 7 || event.Source.Kind != SourceProject {
		t.Fatalf("event registration = %+v", event)
	}
	if len(snapshot.Extensions) != 1 || snapshot.Extensions[0].Name != "observer" {
		t.Fatalf("extensions = %+v", snapshot.Extensions)
	}
}

func TestRegistryConcurrentRegistrationAndEmit(t *testing.T) {
	reg := NewRegistry()
	const toolCount = 16

	var wg sync.WaitGroup
	for i := 0; i < toolCount; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("tool-%02d", i)
			if err := reg.RegisterTool(Registration{Name: name}, &fakeTool{name: name}); err != nil {
				t.Errorf("register %s: %v", name, err)
			}
		}()
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reg.Emit(context.Background(), &EventEnvelope{Event: EventAgentStart})
			_ = reg.Snapshot()
		}()
	}
	wg.Wait()

	if got := len(reg.ListTools()); got != toolCount {
		t.Fatalf("tools = %d, want %d", got, toolCount)
	}
}
