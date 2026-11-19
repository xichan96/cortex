package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// CredentialType discriminates Credential values.
type CredentialType string

const (
	CredentialAPIKey CredentialType = "api_key"
	CredentialOAuth  CredentialType = "oauth"
)

// ErrNoCredential reports that request, store and environment had no
// credential for a provider.
var ErrNoCredential = errors.New("model: no credential for provider")

// Credential is one provider credential. API-key credentials set Key or Env;
// OAuth credentials set Access/Refresh and an absolute Expires timestamp in
// Unix milliseconds. Key and Env values may reference "$ENV" or "!command"
// (commands only when resolution explicitly allows them). OAuth login flows
// live in the CLI/RPC layer; this package only stores and resolves tokens.
type Credential struct {
	Type    CredentialType    `json:"type"`
	Key     string            `json:"key,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Access  string            `json:"access,omitempty"`
	Refresh string            `json:"refresh,omitempty"`
	Expires int64             `json:"expires,omitempty"`
}

// Empty reports whether c carries no credential material.
func (c *Credential) Empty() bool {
	return c == nil || (c.Type == "" && c.Key == "" && c.Access == "" &&
		c.Refresh == "" && len(c.Env) == 0)
}

// Clone returns a deep copy.
func (c *Credential) Clone() *Credential {
	if c == nil {
		return nil
	}
	out := *c
	if c.Env != nil {
		out.Env = make(map[string]string, len(c.Env))
		for k, v := range c.Env {
			out.Env[k] = v
		}
	}
	return &out
}

// Validate checks that the credential carries the material its type needs.
func (c *Credential) Validate() error {
	if c == nil {
		return errors.New("credential is nil")
	}
	if c.Expires < 0 {
		return errors.New("credential expires must not be negative")
	}
	switch c.Type {
	case CredentialAPIKey:
		if strings.TrimSpace(c.Key) == "" && len(c.Env) == 0 {
			return errors.New("api_key credential needs key or env")
		}
	case CredentialOAuth:
		if c.Access == "" && c.Refresh == "" {
			return errors.New("oauth credential needs access or refresh")
		}
	default:
		return fmt.Errorf("unknown credential type %q", c.Type)
	}
	return nil
}

// Expired reports whether an OAuth credential cannot be used for at least
// minValidity. API-key credentials and tokens without an expiry never expire.
func (c *Credential) Expired(now time.Time, minValidity time.Duration) bool {
	if c == nil || c.Type != CredentialOAuth || c.Expires == 0 {
		return false
	}
	return now.Add(minValidity).UnixMilli() >= c.Expires
}

// CredentialStore persists one credential per provider. Implementations must
// be safe for concurrent use. Resolve deliberately does not fall back to the
// environment when the store returns a credential that fails validation; that
// avoids silently using a different account.
type CredentialStore interface {
	Read(ctx context.Context, provider string) (*Credential, error)
	Write(ctx context.Context, provider string, credential *Credential) error
	Delete(ctx context.Context, provider string) error
	List(ctx context.Context) ([]string, error)
}

// ResolveOptions configures credential resolution. Precedence is request >
// store > environment.
type ResolveOptions struct {
	// Request is a request-level credential (for example supplied over RPC).
	Request *Credential
	// Store is consulted only when Request is absent.
	Store CredentialStore
	// Env looks up environment variables; nil uses os.LookupEnv.
	Env func(string) (string, bool)
	// EnvKey overrides the derived environment variable name.
	EnvKey string
	// AllowCommands permits "!command" values in stored credentials. Only set
	// this for trusted configuration.
	AllowCommands bool
	// Command runs "!command" values; nil uses "sh -c".
	Command func(ctx context.Context, command string) (string, error)
}

// Resolve finds the credential for provider. A request-level credential wins
// over the store, which wins over the environment. If the store has an entry,
// its error or invalidity is returned rather than falling back to the
// environment, so a broken stored account cannot silently switch identity.
func Resolve(ctx context.Context, provider string, opts ResolveOptions) (*Credential, error) {
	if strings.TrimSpace(provider) == "" {
		return nil, errors.New("model: empty provider")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	env := opts.Env
	if env == nil {
		env = os.LookupEnv
	}

	if opts.Request != nil && !opts.Request.Empty() {
		request := opts.Request.Clone()
		if request.Type == "" && (request.Key != "" || len(request.Env) > 0) {
			request.Type = CredentialAPIKey
		}
		resolved, err := expandCredential(ctx, request, opts, env)
		if err != nil {
			return nil, fmt.Errorf("credential for %s: %w", provider, err)
		}
		if err := resolved.Validate(); err != nil {
			return nil, fmt.Errorf("credential for %s: %w", provider, err)
		}
		return resolved, nil
	}

	if opts.Store != nil {
		stored, err := opts.Store.Read(ctx, provider)
		if err != nil {
			return nil, fmt.Errorf("read credential for %s: %w", provider, err)
		}
		if stored != nil {
			resolved, err := expandCredential(ctx, stored.Clone(), opts, env)
			if err != nil {
				return nil, fmt.Errorf("credential for %s: %w", provider, err)
			}
			if err := resolved.Validate(); err != nil {
				return nil, fmt.Errorf("credential for %s: %w", provider, err)
			}
			return resolved, nil
		}
	}

	envKey := opts.EnvKey
	if envKey == "" {
		envKey = DefaultEnvKey(provider)
	}
	if value, ok := env(envKey); ok && strings.TrimSpace(value) != "" {
		return &Credential{Type: CredentialAPIKey, Key: value}, nil
	}
	return nil, fmt.Errorf("%w %q", ErrNoCredential, provider)
}

// DefaultEnvKey derives the conventional API-key environment variable for a
// provider: uppercase, non-alphanumeric characters become underscores, with an
// "_API_KEY" suffix (for example "github-copilot" -> GITHUB_COPILOT_API_KEY).
func DefaultEnvKey(provider string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(provider) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String() + "_API_KEY"
}

func expandCredential(ctx context.Context, credential *Credential, opts ResolveOptions, env func(string) (string, bool)) (*Credential, error) {
	if credential.Key != "" {
		value, err := expandValue(ctx, credential.Key, opts, env)
		if err != nil {
			return nil, err
		}
		credential.Key = value
	}
	for name, value := range credential.Env {
		expanded, err := expandValue(ctx, value, opts, env)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		credential.Env[name] = expanded
	}
	return credential, nil
}

func expandValue(ctx context.Context, value string, opts ResolveOptions, env func(string) (string, bool)) (string, error) {
	switch {
	case strings.HasPrefix(value, "$"):
		name := strings.TrimPrefix(value, "$")
		if name == "" {
			return "", errors.New("empty environment reference")
		}
		resolved, ok := env(name)
		if !ok || resolved == "" {
			return "", fmt.Errorf("environment variable %s is not set", name)
		}
		return resolved, nil
	case strings.HasPrefix(value, "!"):
		command := strings.TrimSpace(strings.TrimPrefix(value, "!"))
		if command == "" {
			return "", errors.New("empty credential command")
		}
		if !opts.AllowCommands {
			return "", errors.New("credential commands are disabled")
		}
		if opts.Command != nil {
			return opts.Command(ctx, command)
		}
		output, err := exec.CommandContext(ctx, "sh", "-c", command).Output()
		if err != nil {
			return "", fmt.Errorf("credential command failed: %w", err)
		}
		return strings.TrimSpace(string(output)), nil
	default:
		return value, nil
	}
}

// FileCredentialStore persists credentials in a JSON object keyed by provider
// at the given path. Writes are atomic (temp file + rename) and files are kept
// at mode 0600. Stores constructed for the same path share an in-process lock;
// on Unix, operations also take an exclusive flock on "<path>.lock" so
// concurrent processes cannot lose updates.
type FileCredentialStore struct {
	path     string
	lockPath string
	mu       *sync.Mutex
}

var fileStoreLocks sync.Map // absolute path -> *sync.Mutex

// NewFileCredentialStore opens (but does not create) the credential file and
// ensures its directory exists with mode 0700.
func NewFileCredentialStore(path string) (*FileCredentialStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("model: empty credential store path")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("credential store path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return nil, fmt.Errorf("credential store directory: %w", err)
	}
	return &FileCredentialStore{
		path:     absolute,
		lockPath: absolute + ".lock",
		mu:       lockForPath(absolute),
	}, nil
}

func lockForPath(path string) *sync.Mutex {
	if existing, ok := fileStoreLocks.Load(path); ok {
		return existing.(*sync.Mutex)
	}
	created := &sync.Mutex{}
	actual, _ := fileStoreLocks.LoadOrStore(path, created)
	return actual.(*sync.Mutex)
}

// Path returns the absolute file path.
func (s *FileCredentialStore) Path() string {
	return s.path
}

// Read implements CredentialStore.
func (s *FileCredentialStore) Read(ctx context.Context, provider string) (*Credential, error) {
	var credential *Credential
	err := s.withLock(ctx, func() error {
		credentials, err := s.load()
		if err != nil {
			return err
		}
		stored, ok := credentials[provider]
		if ok {
			credential = stored.Clone()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return credential, nil
}

// Write implements CredentialStore.
func (s *FileCredentialStore) Write(ctx context.Context, provider string, credential *Credential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(provider) == "" {
		return errors.New("model: empty provider")
	}
	if err := credential.Validate(); err != nil {
		return fmt.Errorf("credential for %s: %w", provider, err)
	}
	return s.withLock(ctx, func() error {
		credentials, err := s.load()
		if err != nil {
			return err
		}
		credentials[provider] = *credential.Clone()
		return s.save(credentials)
	})
}

// Delete implements CredentialStore.
func (s *FileCredentialStore) Delete(ctx context.Context, provider string) error {
	return s.withLock(ctx, func() error {
		credentials, err := s.load()
		if err != nil {
			return err
		}
		if _, ok := credentials[provider]; !ok {
			return nil
		}
		delete(credentials, provider)
		return s.save(credentials)
	})
}

// List implements CredentialStore.
func (s *FileCredentialStore) List(ctx context.Context) ([]string, error) {
	var providers []string
	err := s.withLock(ctx, func() error {
		credentials, err := s.load()
		if err != nil {
			return err
		}
		providers = make([]string, 0, len(credentials))
		for provider := range credentials {
			providers = append(providers, provider)
		}
		sort.Strings(providers)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return providers, nil
}

// withLock serializes one credential-store operation in-process and, on
// platforms that support flock, across processes. The lock file is separate
// from the data file so the atomic rename cannot change the locked inode.
func (s *FileCredentialStore) withLock(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	lock, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open credential lock %s: %w", s.lockPath, err)
	}
	defer lock.Close()
	if err := lockFile(lock); err != nil {
		return fmt.Errorf("lock credential store %s: %w", s.lockPath, err)
	}
	defer unlockFile(lock)
	return fn()
}

func (s *FileCredentialStore) load() (map[string]Credential, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Credential{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read credential store %s: %w", s.path, err)
	}
	if info, statErr := os.Stat(s.path); statErr == nil {
		if info.Mode().Perm()&0o077 != 0 {
			if chmodErr := os.Chmod(s.path, 0o600); chmodErr != nil {
				return nil, fmt.Errorf("secure credential store %s: %w", s.path, chmodErr)
			}
		}
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]Credential{}, nil
	}
	credentials := map[string]Credential{}
	if err := json.Unmarshal(data, &credentials); err != nil {
		return nil, fmt.Errorf("parse credential store %s: %w", s.path, err)
	}
	for provider, credential := range credentials {
		c := credential
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("credential store %s: provider %s: %w", s.path, provider, err)
		}
	}
	return credentials, nil
}

func (s *FileCredentialStore) save(credentials map[string]Credential) error {
	if credentials == nil {
		credentials = map[string]Credential{}
	}
	data, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credential store: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	temp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("create credential store temp file: %w", err)
	}
	tempPath := temp.Name()
	cleanup := func() {
		temp.Close()
		os.Remove(tempPath)
	}
	if err := temp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("chmod credential store temp file: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write credential store: %w", err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync credential store: %w", err)
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("close credential store: %w", err)
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("replace credential store: %w", err)
	}
	// Best-effort directory sync so the rename survives a crash.
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

var _ CredentialStore = (*FileCredentialStore)(nil)
