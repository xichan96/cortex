package agent

import "strings"

// AgentPath 子代理在委派树中的路径（docs/design/subagent.md §3.3 铺路）。
// 本轮只做一层："/root"（根）| "/root/<agent>"（子代理）。多级路径留给 roadmap
// list_agents / depth limit。
type AgentPath string

const rootAgentPath AgentPath = "/root"

// RootAgentPath 返回根代理路径。
func RootAgentPath() AgentPath { return rootAgentPath }

// Join 追加一个子代理名形成子路径（"/root" + "general" -> "/root/general"）。
func (p AgentPath) Join(child string) AgentPath {
	return AgentPath(string(p) + "/" + child)
}

// Parent 返回父路径。根路径无父，返回 ok=false。
func (p AgentPath) Parent() (AgentPath, bool) {
	s := string(p)
	idx := strings.LastIndex(s, "/")
	if idx <= 0 {
		return "", false
	}
	return AgentPath(s[:idx]), true
}

// String 返回路径字符串。
func (p AgentPath) String() string { return string(p) }
