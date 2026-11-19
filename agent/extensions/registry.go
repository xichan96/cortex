package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// RegistryOption configures a Registry.
type RegistryOption func(*registryOptions)

type registryOptions struct {
	sessions        SessionAccess
	ui              UserInterface
	handlerReporter ErrorReporter
}

// WithSessions installs the read-only session view handed to extensions.
func WithSessions(sessions SessionAccess) RegistryOption {
	return func(o *registryOptions) {
		if sessions != nil {
			o.sessions = sessions
		}
	}
}

// WithUI installs the user interface handed to extensions.
func WithUI(ui UserInterface) RegistryOption {
	return func(o *registryOptions) {
		if ui != nil {
			o.ui = ui
		}
	}
}

// WithHandlerErrorReporter observes handler failures emitted through the
// registry's dispatcher.
func WithHandlerErrorReporter(reporter ErrorReporter) RegistryOption {
	return func(o *registryOptions) {
		if reporter != nil {
			o.handlerReporter = reporter
		}
	}
}

// ExtensionInfo is a loaded extension in a snapshot.
type ExtensionInfo struct {
	Name   string
	Source SourceInfo
}

// Snapshot is an immutable view of every active registration. Extensions that
// were shadowed by a higher-precedence source are not listed.
type Snapshot struct {
	Tools      []Registration
	Commands   []Registration
	Models     []Registration
	Resources  []Registration
	Events     []EventRegistration
	Extensions []ExtensionInfo
}

type extensionState struct {
	ext    Extension
	source SourceInfo
}

// Registry is the thread-safe home of all extension contributions. It resolves
// same-name conflicts by source precedence (project > user > package/plugin >
// builtin), keeps shadowed registrations so that revocation restores them, and
// revokes everything an extension registered when it is unregistered.
type Registry struct {
	// lifecycleMu serializes RegisterExtension/UnregisterExtension so an
	// extension is never initialized or shut down concurrently with itself.
	lifecycleMu sync.Mutex
	mu          sync.RWMutex
	opts        registryOptions

	tools     *resourceStore[Tool]
	commands  *resourceStore[Command]
	models    *resourceStore[ModelInfo]
	resources *resourceStore[Resource]
	events    *Dispatcher

	extensions map[string]*extensionState
}

// NewRegistry creates an empty registry.
func NewRegistry(opts ...RegistryOption) *Registry {
	options := registryOptions{
		sessions: noopSessions{},
		ui:       noopUI{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	registry := &Registry{
		opts:       options,
		tools:      newResourceStore[Tool](),
		commands:   newResourceStore[Command](),
		models:     newResourceStore[ModelInfo](),
		resources:  newResourceStore[Resource](),
		events:     NewDispatcher(WithErrorReporter(options.handlerReporter)),
		extensions: make(map[string]*extensionState),
	}
	return registry
}

// Events returns the registry's dispatcher so the host can emit lifecycle
// events and subscribe to them.
func (r *Registry) Events() *Dispatcher { return r.events }

// Emit dispatches a lifecycle event through the registry's dispatcher.
func (r *Registry) Emit(ctx context.Context, env *EventEnvelope) EmitResult {
	return r.events.Emit(ctx, env)
}

// RegisterExtension initializes ext and tracks everything it registers. When
// source.Kind is empty it defaults to SourceUser. An Init failure revokes any
// partial registrations before the error is returned.
func (r *Registry) RegisterExtension(ctx context.Context, ext Extension, source SourceInfo, cfg json.RawMessage) error {
	if ext == nil {
		return ErrNilExtension
	}
	name := ext.Name()
	if name == "" {
		return fmt.Errorf("%w: extension name", ErrEmptyName)
	}
	if source.Kind == "" {
		source = SourceInfo{Kind: SourceUser}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()

	r.mu.RLock()
	_, exists := r.extensions[name]
	r.mu.RUnlock()
	if exists {
		return fmt.Errorf("%w: %q", ErrExtensionExists, name)
	}

	api := r.apiFor(name, source, cfg)
	if err := safeInit(ctx, ext, api); err != nil {
		r.revoke(name)
		return fmt.Errorf("extensions: init %q: %w", name, err)
	}

	r.mu.Lock()
	r.extensions[name] = &extensionState{ext: ext, source: source}
	r.mu.Unlock()
	return nil
}

// UnregisterExtension shuts ext down and revokes every registration it made,
// including event subscriptions. Shutdown runs first so the extension can still
// use its API; registrations are revoked even when Shutdown fails.
func (r *Registry) UnregisterExtension(ctx context.Context, name string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()

	r.mu.Lock()
	state, ok := r.extensions[name]
	if ok {
		delete(r.extensions, name)
	}
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrExtensionNotFound, name)
	}

	shutdownErr := safeShutdown(ctx, state.ext)
	r.revoke(name)
	if shutdownErr != nil {
		return fmt.Errorf("extensions: shutdown %q: %w", name, shutdownErr)
	}
	return nil
}

// Extensions lists loaded extensions by name.
func (r *Registry) Extensions() []ExtensionInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ExtensionInfo, 0, len(r.extensions))
	for name, state := range r.extensions {
		out = append(out, ExtensionInfo{Name: name, Source: state.source})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Snapshot returns every active registration plus the loaded extension list.
func (r *Registry) Snapshot() Snapshot {
	r.mu.RLock()
	extensions := make([]ExtensionInfo, 0, len(r.extensions))
	for name, state := range r.extensions {
		extensions = append(extensions, ExtensionInfo{Name: name, Source: state.source})
	}
	r.mu.RUnlock()
	sort.Slice(extensions, func(i, j int) bool { return extensions[i].Name < extensions[j].Name })

	return Snapshot{
		Tools:      registrationsOf(r.tools.active()),
		Commands:   registrationsOf(r.commands.active()),
		Models:     registrationsOf(r.models.active()),
		Resources:  registrationsOf(r.resources.active()),
		Events:     r.events.Subscriptions(),
		Extensions: extensions,
	}
}

// RegisterTool registers a host-owned tool. Extension code uses API.Tools.
func (r *Registry) RegisterTool(reg Registration, tool Tool) error {
	return r.registerTool(reg, tool, "", SourceInfo{Kind: SourceBuiltin})
}

// UnregisterTool removes the active registration with the given name.
func (r *Registry) UnregisterTool(name string) bool { return r.tools.removeActive(name) }

// LookupTool returns the active tool with the given name and its registration.
func (r *Registry) LookupTool(name string) (Tool, Registration, bool) {
	return r.tools.lookup(name)
}

// ListTools returns the active tool registrations.
func (r *Registry) ListTools() []Registration { return registrationsOf(r.tools.active()) }

// RegisterCommand registers a host-owned command. Extension code uses API.Commands.
func (r *Registry) RegisterCommand(reg Registration, cmd Command) error {
	return r.registerCommand(reg, cmd, "", SourceInfo{Kind: SourceBuiltin})
}

// UnregisterCommand removes the active command with the given name.
func (r *Registry) UnregisterCommand(name string) bool { return r.commands.removeActive(name) }

// LookupCommand returns the active command with the given name.
func (r *Registry) LookupCommand(name string) (Command, Registration, bool) {
	return r.commands.lookup(name)
}

// ListCommands returns the active command registrations.
func (r *Registry) ListCommands() []Registration { return registrationsOf(r.commands.active()) }

// RegisterModel registers a host-owned model. Extension code uses API.Models.
func (r *Registry) RegisterModel(reg Registration, model ModelInfo) error {
	return r.registerModel(reg, model, "", SourceInfo{Kind: SourceBuiltin})
}

// UnregisterModel removes the active model with the given name.
func (r *Registry) UnregisterModel(name string) bool { return r.models.removeActive(name) }

// ListModels returns the active models.
func (r *Registry) ListModels() []ModelInfo {
	entries := r.models.active()
	out := make([]ModelInfo, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.val)
	}
	return out
}

// RegisterResource registers a host-owned resource. Extension code uses
// API.Resources.
func (r *Registry) RegisterResource(reg Registration, res Resource) error {
	return r.registerResource(reg, res, "", SourceInfo{Kind: SourceBuiltin})
}

// UnregisterResource removes the active resource with the given name in any
// kind.
func (r *Registry) UnregisterResource(name string) int {
	return r.resources.removeActiveWhere(func(key string) bool { return resourceKeyName(key) == name })
}

// ListResources returns the active resources.
func (r *Registry) ListResources() []Resource {
	entries := r.resources.active()
	out := make([]Resource, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.val)
	}
	return out
}

func (r *Registry) registerTool(reg Registration, tool Tool, owner string, source SourceInfo) error {
	if tool == nil {
		return ErrNilValue
	}
	prepared, err := prepareRegistration(reg, owner, source, tool.Name())
	if err != nil {
		return err
	}
	r.tools.put(prepared.Name, prepared, tool)
	return nil
}

func (r *Registry) registerCommand(reg Registration, cmd Command, owner string, source SourceInfo) error {
	if cmd == nil {
		return ErrNilValue
	}
	prepared, err := prepareRegistration(reg, owner, source, cmd.Name())
	if err != nil {
		return err
	}
	r.commands.put(prepared.Name, prepared, cmd)
	return nil
}

func (r *Registry) registerModel(reg Registration, model ModelInfo, owner string, source SourceInfo) error {
	prepared, err := prepareRegistration(reg, owner, source, model.ID)
	if err != nil {
		return err
	}
	r.models.put(prepared.Name, prepared, model)
	return nil
}

func (r *Registry) registerResource(reg Registration, res Resource, owner string, source SourceInfo) error {
	if res == nil {
		return ErrNilValue
	}
	prepared, err := prepareRegistration(reg, owner, source, res.Name())
	if err != nil {
		return err
	}
	kind := res.Kind()
	if kind == "" {
		return fmt.Errorf("%w: resource %q", ErrEmptyKind, prepared.Name)
	}
	r.resources.put(resourceKey(kind, prepared.Name), prepared, res)
	return nil
}

// apiFor builds the extension-scoped API handed to Init.
func (r *Registry) apiFor(owner string, source SourceInfo, cfg json.RawMessage) *API {
	return &API{
		Tools:     &ownedTools{registry: r, owner: owner, source: source},
		Commands:  &ownedCommands{registry: r, owner: owner, source: source},
		Models:    &ownedModels{registry: r, owner: owner, source: source},
		Resources: &ownedResources{registry: r, owner: owner, source: source},
		Events:    &ownedEventBus{dispatcher: r.events, owner: owner, source: source},
		Sessions:  r.opts.sessions,
		UI:        r.opts.ui,
		Config:    cfg,
	}
}

// revoke removes every registration owned by an extension.
func (r *Registry) revoke(owner string) {
	r.tools.removeOwner(owner)
	r.commands.removeOwner(owner)
	r.models.removeOwner(owner)
	r.resources.removeOwner(owner)
	r.events.UnsubscribeOwner(owner)
}

func safeInit(ctx context.Context, ext Extension, api *API) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("init panic: %v", recovered)
		}
	}()
	return ext.Init(ctx, api)
}

func safeShutdown(ctx context.Context, ext Extension) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("shutdown panic: %v", recovered)
		}
	}()
	return ext.Shutdown(ctx)
}

func prepareRegistration(reg Registration, owner string, source SourceInfo, valueName string) (Registration, error) {
	name := reg.Name
	if name == "" {
		name = valueName
	}
	if name == "" {
		return Registration{}, ErrEmptyName
	}
	if reg.Name != "" && valueName != "" && reg.Name != valueName {
		return Registration{}, fmt.Errorf("%w: %q vs %q", ErrNameMismatch, reg.Name, valueName)
	}
	reg.Name = name
	reg.Owner = owner
	if reg.Source.Kind == "" {
		if source.Kind == "" {
			source = SourceInfo{Kind: SourceBuiltin}
		}
		reg.Source = source
	}
	return reg, nil
}

func resourceKey(kind ResourceKind, name string) string {
	return string(kind) + "/" + name
}

func resourceKeyName(key string) string {
	if index := strings.IndexByte(key, '/'); index >= 0 {
		return key[index+1:]
	}
	return key
}

func registrationsOf[T any](entries []*storeEntry[T]) []Registration {
	out := make([]Registration, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.reg)
	}
	return out
}

// sourceRank encodes the design's precedence: project > user >
// package/plugin > builtin. Unknown sources rank lowest.
func sourceRank(kind SourceKind) int {
	switch kind {
	case SourceProject:
		return 4
	case SourceUser:
		return 3
	case SourcePackage, SourcePlugin:
		return 2
	case SourceBuiltin:
		return 1
	default:
		return 0
	}
}

// winsOver reports whether a should shadow b. Callers iterate in insertion
// order and pass the newer registration as a, so a full tie gives the newest
// registration the win.
func winsOver(a, b Registration) bool {
	rankA, rankB := sourceRank(a.Source.Kind), sourceRank(b.Source.Kind)
	if rankA != rankB {
		if a.Override && b.Source.Kind == SourceBuiltin {
			return true
		}
		return rankA > rankB
	}
	if a.Override != b.Override {
		return a.Override
	}
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	return true
}

type storeEntry[T any] struct {
	key string
	reg Registration
	val T
	seq uint64
}

// resourceStore keeps every registration for a key (including shadowed ones) so
// that removing the winner restores the next-best registration.
type resourceStore[T any] struct {
	mu    sync.RWMutex
	seq   uint64
	items map[string][]*storeEntry[T]
}

func newResourceStore[T any]() *resourceStore[T] {
	return &resourceStore[T]{items: make(map[string][]*storeEntry[T])}
}

func (s *resourceStore[T]) put(key string, reg Registration, val T) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	entry := &storeEntry[T]{key: key, reg: reg, val: val, seq: s.seq}

	list := s.items[key]
	// Re-registering the same key from the same owner and source replaces the
	// existing entry instead of stacking a duplicate.
	for i, existing := range list {
		if existing.reg.Owner == reg.Owner && existing.reg.Source == reg.Source {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	s.items[key] = append(list, entry)
}

func (s *resourceStore[T]) lookup(key string) (T, Registration, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if entry, ok := bestEntry(s.items[key]); ok {
		return entry.val, entry.reg, true
	}
	var zero T
	return zero, Registration{}, false
}

func (s *resourceStore[T]) removeActive(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, ok := s.items[key]
	if !ok {
		return false
	}
	best, ok := bestEntry(list)
	if !ok {
		return false
	}
	s.items[key] = removeEntry(list, best)
	if len(s.items[key]) == 0 {
		delete(s.items, key)
	}
	return true
}

func (s *resourceStore[T]) removeActiveWhere(match func(key string) bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for key, list := range s.items {
		if !match(key) {
			continue
		}
		best, ok := bestEntry(list)
		if !ok {
			continue
		}
		list = removeEntry(list, best)
		removed++
		if len(list) == 0 {
			delete(s.items, key)
		} else {
			s.items[key] = list
		}
	}
	return removed
}

func (s *resourceStore[T]) removeOwnerKey(owner, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, ok := s.items[key]
	if !ok {
		return false
	}
	for _, entry := range list {
		if entry.reg.Owner != owner {
			continue
		}
		list = removeEntry(list, entry)
		if len(list) == 0 {
			delete(s.items, key)
		} else {
			s.items[key] = list
		}
		return true
	}
	return false
}

func (s *resourceStore[T]) removeOwner(owner string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for key, list := range s.items {
		kept := list[:0]
		for _, entry := range list {
			if entry.reg.Owner == owner {
				removed++
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) == 0 {
			delete(s.items, key)
		} else {
			s.items[key] = kept
		}
	}
	return removed
}

func (s *resourceStore[T]) active() []*storeEntry[T] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*storeEntry[T], 0, len(s.items))
	for _, list := range s.items {
		if entry, ok := bestEntry(list); ok {
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

func bestEntry[T any](list []*storeEntry[T]) (*storeEntry[T], bool) {
	var best *storeEntry[T]
	for _, entry := range list {
		if best == nil || winsOver(entry.reg, best.reg) {
			best = entry
		}
	}
	return best, best != nil
}

func removeEntry[T any](list []*storeEntry[T], target *storeEntry[T]) []*storeEntry[T] {
	for i, entry := range list {
		if entry != target {
			continue
		}
		kept := make([]*storeEntry[T], 0, len(list)-1)
		kept = append(kept, list[:i]...)
		kept = append(kept, list[i+1:]...)
		return kept
	}
	return list
}

// ownedTools is the extension-scoped view of the tool store.
type ownedTools struct {
	registry *Registry
	owner    string
	source   SourceInfo
}

var _ ToolRegistrar = (*ownedTools)(nil)

func (o *ownedTools) Register(reg Registration, tool Tool) error {
	return o.registry.registerTool(reg, tool, o.owner, o.source)
}

func (o *ownedTools) Unregister(name string) {
	o.registry.tools.removeOwnerKey(o.owner, name)
}

func (o *ownedTools) Lookup(name string) (Tool, bool) {
	tool, _, ok := o.registry.LookupTool(name)
	return tool, ok
}

func (o *ownedTools) List() []Registration { return o.registry.ListTools() }

// ownedCommands is the extension-scoped view of the command store.
type ownedCommands struct {
	registry *Registry
	owner    string
	source   SourceInfo
}

var _ CommandRegistrar = (*ownedCommands)(nil)

func (o *ownedCommands) Register(reg Registration, cmd Command) error {
	return o.registry.registerCommand(reg, cmd, o.owner, o.source)
}

func (o *ownedCommands) Unregister(name string) {
	o.registry.commands.removeOwnerKey(o.owner, name)
}

func (o *ownedCommands) Lookup(name string) (Command, bool) {
	cmd, _, ok := o.registry.LookupCommand(name)
	return cmd, ok
}

func (o *ownedCommands) List() []Registration { return o.registry.ListCommands() }

// ownedModels is the extension-scoped view of the model store.
type ownedModels struct {
	registry *Registry
	owner    string
	source   SourceInfo
}

var _ ModelRegistrar = (*ownedModels)(nil)

func (o *ownedModels) Register(reg Registration, model ModelInfo) error {
	return o.registry.registerModel(reg, model, o.owner, o.source)
}

func (o *ownedModels) Unregister(name string) {
	o.registry.models.removeOwnerKey(o.owner, name)
}

func (o *ownedModels) List() []ModelInfo { return o.registry.ListModels() }

// ownedResources is the extension-scoped view of the resource store.
type ownedResources struct {
	registry *Registry
	owner    string
	source   SourceInfo
}

var _ ResourceRegistrar = (*ownedResources)(nil)

func (o *ownedResources) Register(reg Registration, res Resource) error {
	return o.registry.registerResource(reg, res, o.owner, o.source)
}

func (o *ownedResources) Unregister(name string) {
	o.registry.resources.removeOwnerWhere(o.owner, func(key string) bool {
		return resourceKeyName(key) == name
	})
}

func (o *ownedResources) List() []Resource { return o.registry.ListResources() }

func (s *resourceStore[T]) removeOwnerWhere(owner string, match func(key string) bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for key, list := range s.items {
		if !match(key) {
			continue
		}
		kept := list[:0]
		for _, entry := range list {
			if entry.reg.Owner == owner {
				removed++
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) == 0 {
			delete(s.items, key)
		} else {
			s.items[key] = kept
		}
	}
	return removed
}

// compile-time checks for the default implementations.
var (
	_ SessionAccess = noopSessions{}
	_ UserInterface = noopUI{}
)
