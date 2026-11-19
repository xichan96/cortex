// Package extensions defines the in-process extension surface of the general
// agent framework (design §7.2–§7.4).
//
// The package is deliberately self-contained: it does not import agent/pipeline
// or any other agent package. Hosts wire real tools, models, and sessions into
// the Registry through the registrar interfaces below.
//
// Three pieces make up the skeleton:
//
//   - Extension lifecycle (api.go): an Extension initializes against an API and
//     registers tools, commands, models, resources, and event handlers.
//   - Event model (events.go): a closed event catalogue with per-event classes
//     (notification/mutation/transform/gate/boundary/last-override) that define
//     how handler results are merged.
//   - Registry and dispatcher (registry.go, dispatcher.go): a thread-safe,
//     source-aware registry with revocation and snapshots, plus an ordered,
//     error-isolated event dispatcher.
package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Extension is a trusted in-process extension. Init receives an API scoped to
// the extension: everything registered through it is revoked automatically when
// the extension is unregistered or when Init fails.
type Extension interface {
	// Name is the extension's unique identity in a Registry.
	Name() string
	// Init registers the extension's contributions and subscribes to events.
	Init(ctx context.Context, api *API) error
	// Shutdown releases external resources. Registrations are revoked by the
	// Registry regardless of the error returned here.
	Shutdown(ctx context.Context) error
}

// SourceKind identifies where a registration came from. Source precedence is
// project > user > package/plugin > builtin; see winsOver in registry.go.
type SourceKind string

const (
	SourceBuiltin SourceKind = "builtin"
	SourceUser    SourceKind = "user"
	SourceProject SourceKind = "project"
	SourcePackage SourceKind = "package"
	SourcePlugin  SourceKind = "plugin"
)

// SourceInfo describes a registration's origin for diagnostics such as
// "/plugins" or "/tools".
type SourceInfo struct {
	Kind SourceKind
	// ID is the package/plugin name when Kind is package or plugin.
	ID string
	// Path is the on-disk location, when known.
	Path string
}

// String renders the source as "kind", "kind:id", or "kind:path".
func (s SourceInfo) String() string {
	kind := string(s.Kind)
	if kind == "" {
		kind = "unknown"
	}
	switch {
	case s.ID != "":
		return kind + ":" + s.ID
	case s.Path != "":
		return kind + ":" + s.Path
	default:
		return kind
	}
}

// Registration is the metadata every registrable item carries. Name is the
// conflict key; when empty it is derived from the registered value.
type Registration struct {
	Name    string
	Source  SourceInfo
	Version string
	// Priority breaks ties between registrations of equal source precedence;
	// higher wins. It also orders event handlers (see HandlerOptions).
	Priority int
	// Override lets a registration replace a builtin regardless of precedence.
	// It never lets a lower-precedence source replace project or user entries.
	Override bool
	// Owner is the extension that registered the item. It is stamped by the
	// Registry and is empty for host registrations.
	Owner string
}

// Tool is an executable tool contributed by an extension. Input is the raw JSON
// arguments object; the host is responsible for schema validation.
type Tool interface {
	Name() string
	Description() string
	Execute(ctx context.Context, input json.RawMessage) (any, error)
}

// Command is a slash command contributed by an extension.
type Command interface {
	Name() string
	Description() string
	Execute(ctx context.Context, args []string) (string, error)
}

// ModelInfo declares a model an extension contributes to the catalogue.
type ModelInfo struct {
	ID            string
	Name          string
	Provider      string
	ContextWindow int
	Metadata      json.RawMessage
}

// ResourceKind enumerates the resource classes a package may ship (design §7.6).
type ResourceKind string

const (
	ResourceSkill  ResourceKind = "skill"
	ResourcePrompt ResourceKind = "prompt"
	ResourceAgent  ResourceKind = "agent"
	ResourceMCP    ResourceKind = "mcp"
)

// Resource is a skill, prompt, agent definition, or MCP server declaration.
type Resource interface {
	Name() string
	Kind() ResourceKind
	// Location is the on-disk path or URI backing the resource.
	Location() string
}

// ToolRegistrar is the extension-scoped tool registration surface. Unregister
// only removes items registered by the calling extension.
type ToolRegistrar interface {
	Register(reg Registration, tool Tool) error
	Unregister(name string)
	Lookup(name string) (Tool, bool)
	List() []Registration
}

// CommandRegistrar is the extension-scoped command registration surface.
type CommandRegistrar interface {
	Register(reg Registration, cmd Command) error
	Unregister(name string)
	Lookup(name string) (Command, bool)
	List() []Registration
}

// ModelRegistrar is the extension-scoped model registration surface.
type ModelRegistrar interface {
	Register(reg Registration, model ModelInfo) error
	Unregister(name string)
	List() []ModelInfo
}

// ResourceRegistrar is the extension-scoped resource registration surface.
// Resource names are unique per kind.
type ResourceRegistrar interface {
	Register(reg Registration, res Resource) error
	Unregister(name string)
	List() []Resource
}

// SessionRef is a read-only session descriptor exposed to extensions.
type SessionRef struct {
	ID        string
	ParentID  string
	Title     string
	UpdatedAt time.Time
}

// SessionAccess is the read-only session view handed to extensions. The host
// provides the implementation; the default denies access.
type SessionAccess interface {
	Current() (SessionRef, bool)
	Get(id string) (SessionRef, bool)
	List() []SessionRef
}

// NotifyLevel classifies a user notification.
type NotifyLevel string

const (
	NotifyInfo    NotifyLevel = "info"
	NotifyWarning NotifyLevel = "warning"
	NotifyError   NotifyLevel = "error"
)

// UIOption is one choice in a select prompt.
type UIOption struct {
	Value       string
	Label       string
	Description string
}

// UserInterface lets an extension interact with the user. Hosts that do not
// support UI leave the default no-op implementation, where interactive methods
// return ErrUINotAvailable.
type UserInterface interface {
	Notify(ctx context.Context, level NotifyLevel, message string)
	Confirm(ctx context.Context, title, message string) (bool, error)
	Input(ctx context.Context, title, placeholder string) (string, error)
	Select(ctx context.Context, title string, options []UIOption) (string, error)
}

// API is the capability set an Extension receives in Init. Registrar fields are
// scoped to the extension: unregistering the extension revokes everything it
// added through them.
type API struct {
	Tools     ToolRegistrar
	Commands  CommandRegistrar
	Models    ModelRegistrar
	Resources ResourceRegistrar
	Events    EventBus
	Sessions  SessionAccess
	UI        UserInterface
	// Config is the extension's own configuration section, verbatim.
	Config json.RawMessage
}

// Errors returned by the extension APIs.
var (
	ErrEmptyName         = errors.New("extensions: empty name")
	ErrEmptyKind         = errors.New("extensions: empty resource kind")
	ErrNameMismatch      = errors.New("extensions: registration name does not match value name")
	ErrNilValue          = errors.New("extensions: nil value")
	ErrNilExtension      = errors.New("extensions: nil extension")
	ErrNilHandler        = errors.New("extensions: nil handler")
	ErrUnknownEvent      = errors.New("extensions: unknown event")
	ErrExtensionExists   = errors.New("extensions: extension already registered")
	ErrExtensionNotFound = errors.New("extensions: extension not registered")
	ErrUINotAvailable    = errors.New("extensions: user interface not available")
)

type noopSessions struct{}

func (noopSessions) Current() (SessionRef, bool)   { return SessionRef{}, false }
func (noopSessions) Get(string) (SessionRef, bool) { return SessionRef{}, false }
func (noopSessions) List() []SessionRef            { return nil }

type noopUI struct{}

func (noopUI) Notify(context.Context, NotifyLevel, string) {}

func (noopUI) Confirm(context.Context, string, string) (bool, error) {
	return false, ErrUINotAvailable
}

func (noopUI) Input(context.Context, string, string) (string, error) {
	return "", ErrUINotAvailable
}

func (noopUI) Select(context.Context, string, []UIOption) (string, error) {
	return "", ErrUINotAvailable
}
