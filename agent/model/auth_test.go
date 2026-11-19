package model

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu      sync.Mutex
	creds   map[string]Credential
	readErr error
}

func newMemoryStore() *memoryStore {
	return &memoryStore{creds: map[string]Credential{}}
}

func (s *memoryStore) Read(_ context.Context, provider string) (*Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	credential, ok := s.creds[provider]
	if !ok {
		return nil, nil
	}
	return credential.Clone(), nil
}

func (s *memoryStore) Write(_ context.Context, provider string, credential *Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creds[provider] = *credential.Clone()
	return nil
}

func (s *memoryStore) Delete(_ context.Context, provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.creds, provider)
	return nil
}

func (s *memoryStore) List(_ context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	providers := make([]string, 0, len(s.creds))
	for provider := range s.creds {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	return providers, nil
}

func TestResolvePrecedence(t *testing.T) {
	ctx := context.Background()
	env := func(name string) (string, bool) {
		if name == "ANTHROPIC_API_KEY" {
			return "env-key", true
		}
		return "", false
	}

	t.Run("request wins", func(t *testing.T) {
		store := newMemoryStore()
		store.creds["anthropic"] = Credential{Type: CredentialAPIKey, Key: "stored-key"}
		got, err := Resolve(ctx, "anthropic", ResolveOptions{
			Request: &Credential{Type: CredentialAPIKey, Key: "request-key"},
			Store:   store,
			Env:     env,
		})
		if err != nil || got.Key != "request-key" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})

	t.Run("store wins over env", func(t *testing.T) {
		store := newMemoryStore()
		store.creds["anthropic"] = Credential{Type: CredentialAPIKey, Key: "stored-key"}
		got, err := Resolve(ctx, "anthropic", ResolveOptions{Store: store, Env: env})
		if err != nil || got.Key != "stored-key" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})

	t.Run("env fallback", func(t *testing.T) {
		got, err := Resolve(ctx, "anthropic", ResolveOptions{Store: newMemoryStore(), Env: env})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got.Type != CredentialAPIKey || got.Key != "env-key" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("empty request falls through to store", func(t *testing.T) {
		store := newMemoryStore()
		store.creds["anthropic"] = Credential{Type: CredentialAPIKey, Key: "stored-key"}
		got, err := Resolve(ctx, "anthropic", ResolveOptions{Request: &Credential{}, Store: store})
		if err != nil || got.Key != "stored-key" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})

	t.Run("no credential", func(t *testing.T) {
		_, err := Resolve(ctx, "anthropic", ResolveOptions{Store: newMemoryStore(), Env: func(string) (string, bool) {
			return "", false
		}})
		if !errors.Is(err, ErrNoCredential) {
			t.Fatalf("err = %v, want ErrNoCredential", err)
		}
	})

	t.Run("store error does not fall back to env", func(t *testing.T) {
		store := newMemoryStore()
		store.readErr = errors.New("disk on fire")
		_, err := Resolve(ctx, "anthropic", ResolveOptions{Store: store, Env: env})
		if err == nil || errors.Is(err, ErrNoCredential) {
			t.Fatalf("err = %v, want store error", err)
		}
	})

	t.Run("invalid stored credential does not fall back to env", func(t *testing.T) {
		store := newMemoryStore()
		store.creds["anthropic"] = Credential{Type: CredentialAPIKey}
		_, err := Resolve(ctx, "anthropic", ResolveOptions{Store: store, Env: env})
		if err == nil || errors.Is(err, ErrNoCredential) {
			t.Fatalf("err = %v, want validation error", err)
		}
	})

	t.Run("expired oauth still wins over env", func(t *testing.T) {
		store := newMemoryStore()
		store.creds["anthropic"] = Credential{
			Type:    CredentialOAuth,
			Access:  "expired-access",
			Refresh: "refresh",
			Expires: time.Now().Add(-time.Hour).UnixMilli(),
		}
		got, err := Resolve(ctx, "anthropic", ResolveOptions{Store: store, Env: env})
		if err != nil || got.Access != "expired-access" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
}

func TestResolveEnvKeyAndExpansion(t *testing.T) {
	ctx := context.Background()
	t.Run("default env key", func(t *testing.T) {
		var lookedUp string
		got, err := Resolve(ctx, "github-copilot", ResolveOptions{
			Env: func(name string) (string, bool) {
				lookedUp = name
				return "copilot-key", true
			},
		})
		if err != nil || got.Key != "copilot-key" {
			t.Fatalf("got %+v, %v", got, err)
		}
		if lookedUp != "GITHUB_COPILOT_API_KEY" {
			t.Fatalf("looked up %q", lookedUp)
		}
		if DefaultEnvKey("openai") != "OPENAI_API_KEY" {
			t.Fatalf("DefaultEnvKey(openai) = %q", DefaultEnvKey("openai"))
		}
	})

	t.Run("explicit env key", func(t *testing.T) {
		got, err := Resolve(ctx, "custom", ResolveOptions{
			EnvKey: "CUSTOM_TOKEN",
			Env: func(name string) (string, bool) {
				if name != "CUSTOM_TOKEN" {
					t.Fatalf("unexpected lookup %q", name)
				}
				return "token", true
			},
		})
		if err != nil || got.Key != "token" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})

	t.Run("dollar expansion", func(t *testing.T) {
		store := newMemoryStore()
		store.creds["anthropic"] = Credential{
			Type: CredentialAPIKey,
			Key:  "$REAL_KEY",
			Env:  map[string]string{"ACCOUNT": "$ACCOUNT_ID"},
		}
		got, err := Resolve(ctx, "anthropic", ResolveOptions{
			Store: store,
			Env: func(name string) (string, bool) {
				switch name {
				case "REAL_KEY":
					return "secret", true
				case "ACCOUNT_ID":
					return "acct", true
				}
				return "", false
			},
		})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got.Key != "secret" || got.Env["ACCOUNT"] != "acct" {
			t.Fatalf("got %+v", got)
		}
		// The stored credential must not be mutated by expansion.
		stored, _ := store.Read(ctx, "anthropic")
		if stored.Key != "$REAL_KEY" {
			t.Fatalf("store mutated: %+v", stored)
		}
	})

	t.Run("missing environment reference", func(t *testing.T) {
		store := newMemoryStore()
		store.creds["anthropic"] = Credential{Type: CredentialAPIKey, Key: "$MISSING"}
		_, err := Resolve(ctx, "anthropic", ResolveOptions{
			Store: store,
			Env:   func(string) (string, bool) { return "", false },
		})
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("commands disabled by default", func(t *testing.T) {
		store := newMemoryStore()
		store.creds["anthropic"] = Credential{Type: CredentialAPIKey, Key: "!echo secret"}
		_, err := Resolve(ctx, "anthropic", ResolveOptions{Store: store})
		if err == nil {
			t.Fatal("expected command to be rejected")
		}
	})

	t.Run("commands with injected runner", func(t *testing.T) {
		store := newMemoryStore()
		store.creds["anthropic"] = Credential{Type: CredentialAPIKey, Key: "!keychain get cortex"}
		got, err := Resolve(ctx, "anthropic", ResolveOptions{
			Store:         store,
			AllowCommands: true,
			Command: func(_ context.Context, command string) (string, error) {
				if command != "keychain get cortex" {
					t.Fatalf("command = %q", command)
				}
				return "resolved-secret", nil
			},
		})
		if err != nil || got.Key != "resolved-secret" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
}

func TestCredentialValidateAndExpired(t *testing.T) {
	valid := []Credential{
		{Type: CredentialAPIKey, Key: "k"},
		{Type: CredentialAPIKey, Env: map[string]string{"A": "1"}},
		{Type: CredentialOAuth, Access: "a"},
		{Type: CredentialOAuth, Refresh: "r"},
	}
	for _, credential := range valid {
		if err := credential.Validate(); err != nil {
			t.Fatalf("%+v rejected: %v", credential, err)
		}
	}
	invalid := []Credential{
		{},
		{Type: CredentialAPIKey},
		{Type: CredentialOAuth},
		{Type: "mystery", Key: "k"},
		{Type: CredentialAPIKey, Key: "k", Expires: -1},
	}
	for _, credential := range invalid {
		if err := credential.Validate(); err == nil {
			t.Fatalf("%+v accepted", credential)
		}
	}

	now := time.Now()
	oauth := &Credential{Type: CredentialOAuth, Access: "a", Expires: now.Add(10 * time.Minute).UnixMilli()}
	if oauth.Expired(now, 0) {
		t.Fatal("valid token reported expired")
	}
	if !oauth.Expired(now, 15*time.Minute) {
		t.Fatal("token inside the validity window reported valid")
	}
	apiKey := &Credential{Type: CredentialAPIKey, Key: "k", Expires: now.Add(-time.Hour).UnixMilli()}
	if apiKey.Expired(now, time.Hour) {
		t.Fatal("api keys never expire")
	}
}

func TestFileCredentialStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "auth.json")
	store, err := NewFileCredentialStore(path)
	if err != nil {
		t.Fatalf("NewFileCredentialStore: %v", err)
	}
	if store.Path() != path {
		t.Fatalf("Path = %q, want %q", store.Path(), path)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("stat dir: %v", err)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %o, want 700", info.Mode().Perm())
	}

	// Missing file reads as no credential.
	credential, err := store.Read(ctx, "anthropic")
	if err != nil || credential != nil {
		t.Fatalf("Read missing = %+v, %v", credential, err)
	}

	want := Credential{Type: CredentialAPIKey, Key: "sk-test", Env: map[string]string{"A": "1"}}
	if err := store.Write(ctx, "anthropic", &want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatalf("stat file: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 600", info.Mode().Perm())
	}
	if info, err := os.Stat(path + ".lock"); err != nil {
		t.Fatalf("stat lock file: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode = %o, want 600", info.Mode().Perm())
	}

	got, err := store.Read(ctx, "anthropic")
	if err != nil || got == nil || got.Key != "sk-test" || got.Env["A"] != "1" {
		t.Fatalf("Read = %+v, %v", got, err)
	}

	// A second instance for the same path sees the persisted value.
	second, err := NewFileCredentialStore(path)
	if err != nil {
		t.Fatalf("second store: %v", err)
	}
	got, err = second.Read(ctx, "anthropic")
	if err != nil || got == nil || got.Key != "sk-test" {
		t.Fatalf("second Read = %+v, %v", got, err)
	}

	if err := store.Write(ctx, "openai", &Credential{Type: CredentialOAuth, Access: "a", Refresh: "r", Expires: 123}); err != nil {
		t.Fatalf("Write oauth: %v", err)
	}
	providers, err := store.List(ctx)
	if err != nil || len(providers) != 2 || providers[0] != "anthropic" || providers[1] != "openai" {
		t.Fatalf("List = %v, %v", providers, err)
	}

	if err := store.Delete(ctx, "anthropic"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if credential, err := store.Read(ctx, "anthropic"); err != nil || credential != nil {
		t.Fatalf("Read after delete = %+v, %v", credential, err)
	}
	if err := store.Delete(ctx, "missing"); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}

	// No temp files are left behind.
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".credentials-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temp files left: %v, %v", matches, err)
	}
}

func TestFileCredentialStoreValidationAndPermissions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	store, err := NewFileCredentialStore(path)
	if err != nil {
		t.Fatalf("NewFileCredentialStore: %v", err)
	}
	if err := store.Write(ctx, "anthropic", &Credential{Type: CredentialAPIKey}); err == nil {
		t.Fatal("invalid credential accepted")
	}
	if err := store.Write(ctx, "", &Credential{Type: CredentialAPIKey, Key: "k"}); err == nil {
		t.Fatal("empty provider accepted")
	}

	// A pre-existing world-readable file is tightened on read.
	content := []byte(`{"anthropic":{"type":"api_key","key":"legacy"}}`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}
	credential, err := store.Read(ctx, "anthropic")
	if err != nil || credential == nil || credential.Key != "legacy" {
		t.Fatalf("Read legacy = %+v, %v", credential, err)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatalf("stat: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}

	// Corrupt JSON surfaces as an error instead of a silent empty store.
	if err := os.WriteFile(path, []byte(`{"anthropic":`), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	if _, err := store.Read(ctx, "anthropic"); err == nil {
		t.Fatal("corrupt store accepted")
	}
}

func TestFileCredentialStoreConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "auth.json")
	const writers = 32
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store, err := NewFileCredentialStore(path)
			if err != nil {
				errs <- err
				return
			}
			provider := fmt.Sprintf("provider-%02d", i)
			if err := store.Write(ctx, provider, &Credential{Type: CredentialAPIKey, Key: "key"}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write: %v", err)
	}

	store, err := NewFileCredentialStore(path)
	if err != nil {
		t.Fatalf("NewFileCredentialStore: %v", err)
	}
	providers, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(providers) != writers {
		t.Fatalf("providers = %d, want %d: %v", len(providers), writers, providers)
	}
	for _, provider := range providers {
		credential, err := store.Read(ctx, provider)
		if err != nil || credential == nil || credential.Key != "key" {
			t.Fatalf("Read %s = %+v, %v", provider, credential, err)
		}
	}
}
