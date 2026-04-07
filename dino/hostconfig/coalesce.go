package hostconfig

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xichan96/cortex/dino"
	chatstore "github.com/xichan96/cortex/dino/chatstore"
)

const MCPToolAllowListEnvKey = "_goclaw_mcp_allow"

type HostDinoBrand struct {
	MemoryToolName    string
	ExtraAllowedTools []string
	DefaultWebTools   bool
	PromptMarker      string
	PromptAddendum    string
}

func ExpandConfigPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~/"))
		}
	}
	return filepath.Clean(os.ExpandEnv(p))
}

func MemoryPersistDirectory(cfg *dino.Config) string {
	if cfg == nil {
		wd, err := os.Getwd()
		if err != nil || wd == "" {
			wd = "."
		}
		dir := filepath.Join(wd, "dino_sessions")
		if abs, err := filepath.Abs(dir); err == nil {
			return abs
		}
		return dir
	}
	w := strings.TrimSpace(cfg.WorkspaceRoot)
	if w == "*" {
		if wd, err := os.Getwd(); err == nil && wd != "" {
			w = wd
		} else {
			w = "."
		}
	} else if w != "" {
		w = ExpandConfigPath(w)
		if !filepath.IsAbs(w) {
			if abs, err := filepath.Abs(w); err == nil {
				w = abs
			}
		}
	}
	pd := strings.TrimSpace(cfg.Memory.PersistDirectory)
	if pd == "" || pd == "./dino_sessions" {
		base := w
		if base == "" {
			base = "."
		}
		dir := filepath.Join(base, "dino_sessions")
		if abs, err := filepath.Abs(dir); err == nil {
			return abs
		}
		return dir
	}
	pd = ExpandConfigPath(pd)
	if !filepath.IsAbs(pd) {
		if abs, err := filepath.Abs(pd); err == nil {
			pd = abs
		}
	}
	return pd
}

func memoryPersistFileNameFrom(cfg *dino.Config) string {
	s := strings.TrimSpace(cfg.Memory.PersistFileName)
	if s == "" {
		return chatstore.DefaultSharedDBFile
	}
	return s
}

func MemoryPersistFileName(cfg *dino.Config, host *HostAppConfig, brand *HostDinoBrand) string {
	if cfg == nil {
		cfg = CoalesceDinoFromHost(nil, host, brand)
	}
	return memoryPersistFileNameFrom(cfg)
}

func MemoryStoreSpec(cfg *dino.Config, host *HostAppConfig, brand *HostDinoBrand) (dir, sqliteFile string) {
	c := CoalesceDinoFromHost(cfg, host, brand)
	return MemoryPersistDirectory(c), memoryPersistFileNameFrom(c)
}

func appendUniqueString(slice *[]string, s string) {
	for _, x := range *slice {
		if x == s {
			return
		}
	}
	*slice = append(*slice, s)
}

func MergeMCPServersIntoDino(out *dino.Config, entries []MCPToolConfig) {
	if out == nil || len(entries) == 0 {
		return
	}
	if out.MCP.Servers == nil {
		out.MCP.Servers = make(map[string]dino.MCPServerConfig)
	}
	for i, m := range entries {
		if m.Disabled {
			continue
		}
		u := strings.TrimSpace(m.URL)
		if u == "" {
			continue
		}
		name := strings.TrimSpace(m.Name)
		if name == "" {
			name = "mcp_" + strconv.Itoa(i)
		}
		headers := MCPHeadersFromTool(m)
		var env map[string]string
		if len(m.Tools) > 0 {
			parts := make([]string, 0, len(m.Tools))
			for _, t := range m.Tools {
				t = strings.TrimSpace(t)
				if t != "" {
					parts = append(parts, t)
				}
			}
			if len(parts) > 0 {
				env = map[string]string{MCPToolAllowListEnvKey: strings.Join(parts, "\x1e")}
			}
		}
		out.MCP.Servers[name] = dino.MCPServerConfig{
			Type:    NormalizeMCPTransport(m.Transport),
			URL:     u,
			Headers: headers,
			Env:     env,
			Enabled: true,
		}
	}
	if len(out.MCP.Servers) > 0 {
		out.MCP.Enabled = true
	}
}

func CoalesceDinoFromHost(in *dino.Config, host *HostAppConfig, brand *HostDinoBrand) *dino.Config {
	var out *dino.Config
	if in != nil {
		out = in
	} else {
		out = dino.DefaultConfig()
	}

	if host != nil {
		workspaceRoot := host.Workspace.Root
		if workspaceRoot == "" {
			workspaceRoot = "."
		}
		agentsMdPath := filepath.Join(ExpandConfigPath(workspaceRoot), "AGENTS.md")
		if content, err := os.ReadFile(agentsMdPath); err == nil && len(content) > 0 {
			agentsMdContent := strings.TrimSpace(string(content))
			if agentsMdContent != "" {
				if out.SystemPrompt == "" {
					out.SystemPrompt = dino.DefaultConfig().SystemPrompt
				}
				out.SystemPrompt += "\n\n## Project Context (from AGENTS.md)\n" + agentsMdContent
			}
		}
	}

	if brand != nil && brand.DefaultWebTools {
		out.Tools.Allowed = append(out.Tools.Allowed, "web_fetch", "web_search")
	}
	if brand != nil && strings.TrimSpace(brand.MemoryToolName) != "" {
		appendUniqueString(&out.Tools.Allowed, strings.TrimSpace(brand.MemoryToolName))
	}
	if brand != nil {
		for _, name := range brand.ExtraAllowedTools {
			name = strings.TrimSpace(name)
			if name != "" {
				appendUniqueString(&out.Tools.Allowed, name)
			}
		}
	}
	if host != nil {
		for _, b := range host.Server.Cortex.ToolConfig.Builtin {
			if strings.TrimSpace(b) == "mcp_client" {
				appendUniqueString(&out.Tools.Allowed, "mcp_client")
				break
			}
		}
	}
	if host != nil && len(host.Server.Cortex.ToolConfig.ApprovalRequiredTools) > 0 {
		out.Tools.ApprovalRequired = append([]string(nil), host.Server.Cortex.ToolConfig.ApprovalRequiredTools...)
	} else {
		out.Tools.ApprovalRequired = nil
	}
	if host != nil && host.Workspace.Unrestricted {
		out.WorkspaceRoot = "*"
	} else if host != nil {
		cr := strings.TrimSpace(host.Workspace.Root)
		if cr != "" && (in == nil || strings.TrimSpace(in.WorkspaceRoot) == "") {
			out.WorkspaceRoot = ExpandConfigPath(cr)
		}
	}
	if w := strings.TrimSpace(out.WorkspaceRoot); w != "" && w != "*" {
		w = ExpandConfigPath(w)
		if !filepath.IsAbs(w) {
			if abs, err := filepath.Abs(w); err == nil {
				w = abs
			}
		}
		out.WorkspaceRoot = w
	}

	out.Memory.PersistDirectory = MemoryPersistDirectory(out)
	if strings.TrimSpace(out.Memory.PersistFileName) == "" {
		out.Memory.PersistFileName = chatstore.DefaultSharedDBFile
	}
	out.Memory.EnableCompress = true
	if host != nil && host.Server.Cortex.Memory.MaxBudgetTokens > 0 {
		out.Memory.MaxBudgetTokens = host.Server.Cortex.Memory.MaxBudgetTokens
	}
	if host != nil && host.Server.Cortex.Memory.CompactAfterTurns > 0 {
		out.Memory.CompactAfterTurns = host.Server.Cortex.Memory.CompactAfterTurns
	}
	if out.Memory.MaxHistoryMessages == 0 {
		out.Memory.MaxHistoryMessages = 100
	}
	if out.Memory.CompressThreshold == 0 {
		out.Memory.CompressThreshold = 50
	}
	if host != nil && host.Server.Cortex.Memory.CompressThreshold > 0 {
		out.Memory.CompressThreshold = host.Server.Cortex.Memory.CompressThreshold
	}
	if out.Memory.KeepRecentCount == 0 {
		out.Memory.KeepRecentCount = 10
	}
	out.Memory.PersistEnabled = true
	out.Memory.Type = "sqlite"

	if out.ToolTimeouts == nil {
		out.ToolTimeouts = map[string]time.Duration{}
	}
	if brand != nil {
		mn := strings.TrimSpace(brand.MemoryToolName)
		if mn != "" {
			if _, ok := out.ToolTimeouts[mn]; !ok {
				out.ToolTimeouts[mn] = 45 * time.Second
			}
		}
	}
	if host != nil && strings.TrimSpace(out.Skills.Path) == "" {
		root := strings.TrimSpace(host.Server.Cortex.SkillsRoot)
		if root != "" {
			out.Skills.Path = ExpandConfigPath(root)
		}
	}
	if host != nil {
		MergeMCPServersIntoDino(out, host.Server.Cortex.ToolConfig.MCP)
	}
	if out.SystemPrompt == "" {
		out.SystemPrompt = dino.DefaultConfig().SystemPrompt
	}
	if brand != nil && brand.PromptMarker != "" && brand.PromptAddendum != "" {
		if !strings.Contains(out.SystemPrompt, brand.PromptMarker) {
			out.SystemPrompt += "\n\n" + brand.PromptMarker + "\n" + brand.PromptAddendum
		}
	}
	return out
}
