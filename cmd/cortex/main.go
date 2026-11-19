// Command cortex is a small transport shell over a dino session.
//
// It exposes the same SessionRuntime as the HTTP gateway:
//
//	cortex -mode json -prompt "..."   one turn, events as JSONL
//	cortex -mode rpc                  JSONL RPC on stdin/stdout
//
// Configuration starts from dino.DefaultConfig(), optionally overlays a YAML
// file, then applies CLI overrides.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/xichan96/cortex/agent/runtime"
	"github.com/xichan96/cortex/dino"
)

type cliOptions struct {
	configPath string
	sessionID  string
	mode       string
	prompt     string
	workspace  string
	provider   string
	model      string
	baseURL    string
	apiKey     string
}

func main() {
	opts := parseFlags()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, opts, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "cortex:", err)
		os.Exit(1)
	}
}

func parseFlags() cliOptions {
	var opts cliOptions
	flag.StringVar(&opts.configPath, "config", "", "YAML config overlaid on dino.DefaultConfig")
	flag.StringVar(&opts.sessionID, "session", "cli", "session id")
	flag.StringVar(&opts.mode, "mode", "json", "json | rpc")
	flag.StringVar(&opts.prompt, "prompt", "", "prompt for json mode (reads stdin when empty)")
	flag.StringVar(&opts.workspace, "workspace", "", "workspace root override")
	flag.StringVar(&opts.provider, "provider", "", "provider type override (openai|anthropic|deepseek|volce)")
	flag.StringVar(&opts.model, "model", "", "default model override")
	flag.StringVar(&opts.baseURL, "base-url", "", "provider base URL override")
	flag.StringVar(&opts.apiKey, "api-key", "", "provider API key override")
	flag.Parse()
	return opts
}

func run(ctx context.Context, opts cliOptions, in io.Reader, out, errOut io.Writer) error {
	cfg, err := buildConfig(opts)
	if err != nil {
		return err
	}
	factory, err := dino.NewDinoFactory(cfg)
	if err != nil {
		return fmt.Errorf("create factory: %w", err)
	}
	defer factory.Shutdown(context.Background())

	client := dino.NewClient(factory)
	defer client.CloseAll()
	cs, err := client.CreateSession(ctx, opts.sessionID)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	rt := dino.NewSessionRuntime(cs)
	defer rt.Close(context.Background())

	switch opts.mode {
	case "rpc":
		return runtime.Server(rt, in, out)
	case "json":
		prompt := strings.TrimSpace(opts.prompt)
		if prompt == "" {
			data, err := io.ReadAll(io.LimitReader(in, 1<<20))
			if err != nil {
				return fmt.Errorf("read prompt: %w", err)
			}
			prompt = strings.TrimSpace(string(data))
		}
		if prompt == "" {
			return fmt.Errorf("empty prompt")
		}
		return runJSON(ctx, rt, prompt, out)
	default:
		return fmt.Errorf("unknown mode %q (want json or rpc)", opts.mode)
	}
}

func buildConfig(opts cliOptions) (*dino.Config, error) {
	cfg := dino.DefaultConfig()
	if opts.configPath != "" {
		data, err := os.ReadFile(opts.configPath)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	}
	if opts.workspace != "" {
		cfg.WorkspaceRoot = opts.workspace
	}
	if opts.provider != "" {
		cfg.Provider.Type = opts.provider
	}
	if opts.model != "" {
		cfg.DefaultModel = opts.model
		if cfg.Provider.Models == nil {
			cfg.Provider.Models = make(map[string]string)
		}
		cfg.Provider.Models["default"] = opts.model
	}
	if opts.baseURL != "" {
		cfg.Provider.BaseURL = opts.baseURL
	}
	if opts.apiKey != "" {
		cfg.Provider.APIKey = opts.apiKey
	}
	if cfg.Provider.APIKey == "" {
		cfg.Provider.APIKey = providerKeyFromEnv(cfg.Provider.Type)
	}
	if strings.TrimSpace(cfg.DefaultModel) == "" {
		return nil, fmt.Errorf("no model configured (set -model or default_model)")
	}
	return cfg, nil
}

func providerKeyFromEnv(provider string) string {
	names := []string{"CORTEX_API_KEY"}
	switch strings.ToLower(provider) {
	case "anthropic":
		names = append(names, "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN")
	case "deepseek":
		names = append(names, "DEEPSEEK_API_KEY")
	default:
		names = append(names, "OPENAI_API_KEY")
	}
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

// runJSON subscribes before prompting so the stream includes agent_start, then
// prints every runtime event as one JSON line and waits for settled.
func runJSON(ctx context.Context, rt runtime.Runtime, prompt string, out io.Writer) error {
	var mu sync.Mutex
	write := func(ev runtime.Event) {
		data, err := json.Marshal(ev)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprintln(out, string(data))
	}
	unsub := rt.Subscribe(write)
	defer unsub()

	disp, err := rt.Prompt(ctx, runtime.PromptInput{Text: prompt})
	if err != nil {
		return fmt.Errorf("prompt: %w", err)
	}
	mu.Lock()
	_, _ = fmt.Fprintf(out, "{\"type\":\"prompt\",\"disposition\":%q}\n", disp)
	mu.Unlock()

	select {
	case <-rt.Settled():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(10 * time.Minute):
		return fmt.Errorf("timed out waiting for settled")
	}
}
