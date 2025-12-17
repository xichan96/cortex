package config

import "time"

type Config struct {
	LLM    LLMConfig    `yaml:"llm"`
	Tools  ToolsConfig  `yaml:"tools"`
	Memory MemoryConfig `yaml:"memory"`
	Agent  AgentConfig  `yaml:"agent"`
}

type LLMConfig struct {
	Provider string         `yaml:"provider"`
	OpenAI   OpenAIConfig   `yaml:"openai"`
	DeepSeek DeepSeekConfig `yaml:"deepseek"`
	Volce    VolceConfig    `yaml:"volce"`
}

type OpenAIConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
	OrgID   string `yaml:"org_id"`
	APIType string `yaml:"api_type"`
}

type DeepSeekConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

type VolceConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

type ToolsConfig struct {
	MCP     []MCPConfig   `yaml:"mcp"`
	HTTP    HTTPConfig    `yaml:"http"`
	Builtin BuiltinConfig `yaml:"builtin"`
}

type MCPConfig struct {
	Enabled   bool              `yaml:"enabled"`
	URL       string            `yaml:"url"`
	Transport string            `yaml:"transport"`
	Headers   map[string]string `yaml:"headers"`
}

type HTTPConfig struct {
	Enabled bool `yaml:"enabled"`
}

type BuiltinConfig struct {
	Enabled bool            `yaml:"enabled"`
	SSH     ToolConfig      `yaml:"ssh"`
	File    ToolConfig      `yaml:"file"`
	Email   EmailToolConfig `yaml:"email"`
	Command ToolConfig      `yaml:"command"`
	Math    ToolConfig      `yaml:"math"`
	Ping    ToolConfig      `yaml:"ping"`
	Time    ToolConfig      `yaml:"time"`
}

type ToolConfig struct {
	Enabled bool `yaml:"enabled"`
}

type EmailToolConfig struct {
	Enabled bool        `yaml:"enabled"`
	Config  EmailConfig `yaml:"config"`
}

type EmailConfig struct {
	Address string `yaml:"address"`
	Name    string `yaml:"name"`
	Pwd     string `yaml:"pwd"`
	Host    string `yaml:"host"`
	Port    int    `yaml:"port"`
}

type MemoryConfig struct {
	Provider           string        `yaml:"provider"`
	SessionID          string        `yaml:"session_id"`
	MaxHistoryMessages int           `yaml:"max_history_messages"`
	Redis              RedisConfig   `yaml:"redis"`
	MongoDB            MongoDBConfig `yaml:"mongodb"`
}

type RedisConfig struct {
	Host      string `yaml:"host"`
	Port      int    `yaml:"port"`
	DB        int    `yaml:"db"`
	Username  string `yaml:"username"`
	Password  string `yaml:"password"`
	KeyPrefix string `yaml:"key_prefix"`
}

type MongoDBConfig struct {
	URI         string `yaml:"uri"`
	Username    string `yaml:"username"`
	Password    string `yaml:"password"`
	Database    string `yaml:"database"`
	Collection  string `yaml:"collection"`
	MaxPoolSize int    `yaml:"max_pool_size"`
	MinPoolSize int    `yaml:"min_pool_size"`
}

type AgentConfig struct {
	MaxIterations      int        `yaml:"max_iterations"`
	SystemMessage      string     `yaml:"system_message"`
	Temperature        float64    `yaml:"temperature"`
	MaxTokens          int         `yaml:"max_tokens"`
	TopP               float64     `yaml:"top_p"`
	FrequencyPenalty   float64    `yaml:"frequency_penalty"`
	PresencePenalty    float64    `yaml:"presence_penalty"`
	Timeout            string      `yaml:"timeout"`
	RetryAttempts      int         `yaml:"retry_attempts"`
	EnableToolRetry    bool        `yaml:"enable_tool_retry"`
	MaxHistoryMessages int         `yaml:"max_history_messages"`
	MCP                MCPMetadata `yaml:"mcp"`
}

type MCPMetadata struct {
	Server MCPServerMetadata `yaml:"server"`
	Tool   MCPToolMetadata   `yaml:"tool"`
}

type MCPServerMetadata struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

type MCPToolMetadata struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func (a *AgentConfig) TimeoutDuration() (time.Duration, error) {
	return time.ParseDuration(a.Timeout)
}
