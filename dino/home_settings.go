package dino

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xichan96/cortex/agent/home"
)

// defaultMemoryPersistDirectory mirrors the DefaultConfig value. It is spelled
// out here so the home-settings overlay can tell "host chose this directory"
// apart from "nobody configured one".
const defaultMemoryPersistDirectory = "./dino_sessions"

// applyHomeSettings folds the merged agent-home settings into cfg. It is the
// opt-in bridge between loadHomeEnv and the factory's normal resolution:
// without HomeConfig.ApplySettings the config is left untouched, and every
// value explicitly set by the host wins over the home layer.
//
// Nothing here is fatal. A malformed file or a bad entry becomes a warning
// diagnostic and construction continues with the remaining values.
func applyHomeSettings(cfg *Config, env *homeEnv) []home.Diagnostic {
	if cfg == nil || env == nil || !cfg.Home.ApplySettings {
		return nil
	}

	if env.resolved != nil {
		settings := env.resolved.Settings
		if cfg.DefaultModel == "" {
			cfg.DefaultModel = settings.DefaultModel
		}
		if cfg.DefaultProvider == "" {
			cfg.DefaultProvider = settings.DefaultProvider
		}
		// Only an empty host selection inherits settings.tools.default. The
		// modifier semantics ("+name"/"-name") are resolved here because the
		// fallback selection is empty: a list of modifiers alone yields just the
		// added tools.
		if len(cfg.Tools.Allowed) == 0 {
			if tools := home.ResolveDefaultTools(settings.Tools.Default, nil); len(tools) > 0 {
				cfg.Tools.Allowed = tools
			}
		}
	}

	// A host-selected session directory (Home.SessionDir) replaces the built-in
	// default persist directory, but never a path the host set explicitly on
	// Memory.PersistDirectory.
	if dir := strings.TrimSpace(cfg.Home.SessionDir); dir != "" && isDefaultMemoryPersistDirectory(cfg.Memory.PersistDirectory) {
		cfg.Memory.PersistDirectory = dir
	}

	return applyHomeMCPServers(cfg, env)
}

// isDefaultMemoryPersistDirectory reports whether dir is unset or still the
// DefaultConfig value, i.e. a path the host did not choose.
func isDefaultMemoryPersistDirectory(dir string) bool {
	d := strings.TrimSpace(dir)
	if d == "" {
		return true
	}
	return filepath.Clean(d) == filepath.Clean(defaultMemoryPersistDirectory)
}

// homeMCPFile is the on-disk mcp.json shape. Unknown per-server fields are
// ignored; only the fields dino.Config understands are mapped.
type homeMCPFile struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

type homeMCPServerEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Type    string            `json:"type"`
	// Enabled defaults to true; an explicit false keeps the server out of the
	// factory's connection loop while preserving the declaration.
	Enabled *bool `json:"enabled"`
}

// applyHomeMCPServers merges ~/.cortex/mcp.json and, when the project is
// trusted, <cwd>/.cortex/mcp.json into cfg.MCP. Project entries replace user
// entries of the same name; a server already declared by the host is left
// untouched (host explicit values win). Any discovered server enables cfg.MCP.
func applyHomeMCPServers(cfg *Config, env *homeEnv) []home.Diagnostic {
	var diags []home.Diagnostic

	servers := map[string]MCPServerConfig{}
	userServers, userDiags := loadHomeMCPFile(env.paths.MCPPath, home.LayerUser)
	diags = append(diags, userDiags...)
	for name, server := range userServers {
		servers[name] = server
	}
	if env.trusted {
		projectServers, projectDiags := loadHomeMCPFile(env.paths.ProjectMCPPath, home.LayerProject)
		diags = append(diags, projectDiags...)
		for name, server := range projectServers {
			servers[name] = server
		}
	}
	if len(servers) == 0 {
		return diags
	}

	if cfg.MCP.Servers == nil {
		cfg.MCP.Servers = make(map[string]MCPServerConfig, len(servers))
	}
	for name, server := range servers {
		if _, exists := cfg.MCP.Servers[name]; exists {
			continue
		}
		cfg.MCP.Servers[name] = server
	}
	cfg.MCP.Enabled = true
	return diags
}

// loadHomeMCPFile reads one mcp.json. A missing or empty file is not an error;
// malformed JSON or invalid entries yield warning diagnostics.
func loadHomeMCPFile(path string, layer home.Layer) (map[string]MCPServerConfig, []home.Diagnostic) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []home.Diagnostic{{
			File: path, Layer: layer, Severity: home.SeverityWarning,
			Reason: fmt.Sprintf("read failed: %v", err),
		}}
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}

	var file homeMCPFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, []home.Diagnostic{{
			File: path, Layer: layer, Severity: home.SeverityWarning,
			Reason: fmt.Sprintf("invalid JSON: %v", err),
		}}
	}

	names := make([]string, 0, len(file.MCPServers))
	for name := range file.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)

	servers := make(map[string]MCPServerConfig, len(file.MCPServers))
	var diags []home.Diagnostic
	for _, name := range names {
		server, err := decodeHomeMCPServer(file.MCPServers[name])
		if err != nil {
			diags = append(diags, home.Diagnostic{
				File: path, Key: "mcpServers." + name, Layer: layer,
				Severity: home.SeverityWarning, Reason: err.Error(),
			})
			continue
		}
		servers[name] = server
	}
	return servers, diags
}

func decodeHomeMCPServer(raw json.RawMessage) (MCPServerConfig, error) {
	var entry homeMCPServerEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return MCPServerConfig{}, fmt.Errorf("expected an object with command/url: %w", err)
	}
	command := strings.TrimSpace(entry.Command)
	url := strings.TrimSpace(entry.URL)
	if command == "" && url == "" {
		return MCPServerConfig{}, fmt.Errorf("server has neither command nor url")
	}

	server := MCPServerConfig{
		Type:    normalizeHomeMCPTransport(entry.Type, command, url),
		URL:     url,
		Command: command,
		Args:    entry.Args,
		Env:     entry.Env,
		Headers: entry.Headers,
		Enabled: true,
	}
	if entry.Enabled != nil {
		server.Enabled = *entry.Enabled
	}
	return server, nil
}

// normalizeHomeMCPTransport maps the mcp.json "type" spelling onto the
// transport names dino's MCP client accepts. When type is omitted it is
// inferred: a URL means streamable HTTP, a bare command means stdio.
func normalizeHomeMCPTransport(raw, command, url string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		if url == "" && command != "" {
			return "stdio"
		}
		return "httpStreamable"
	case "remote", "http", "https", "httpstreamable", "streamable", "streamable-http":
		return "httpStreamable"
	case "sse":
		return "sse"
	case "stdio", "local":
		return "stdio"
	default:
		return strings.TrimSpace(raw)
	}
}
