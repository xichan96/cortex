package hostconfig

type MemoryIngestRule struct {
	Name            string   `mapstructure:"name"`
	Action          string   `mapstructure:"action"`
	MaxTotalChars   int      `mapstructure:"max_total_chars"`
	MinTotalChars   int      `mapstructure:"min_total_chars"`
	PhrasesAny      []string `mapstructure:"phrases_any"`
	MaxUserMessages int      `mapstructure:"max_user_messages"`
}

type MemoryIngestConfig struct {
	Enabled             bool               `mapstructure:"enabled"`
	Interval            string             `mapstructure:"interval"`
	BatchMaxMessages    int                `mapstructure:"batch_max_messages"`
	MinNewMessages      int                `mapstructure:"min_new_messages"`
	Model               string             `mapstructure:"model"`
	SystemExtra         string             `mapstructure:"system_extra"`
	Rules               []MemoryIngestRule `mapstructure:"rules"`
	TrivialPhrases      []string           `mapstructure:"trivial_phrases"`
	MinContentLength    int                `mapstructure:"min_content_length"`
	EnableContentFilter bool               `mapstructure:"enable_content_filter"`
}

type MemorySettings struct {
	MaxBudgetTokens   int    `mapstructure:"max_budget_tokens"`
	CompactAfterTurns int    `mapstructure:"compact_after_turns"`
	CompressThreshold int    `mapstructure:"compress_threshold"`
	// EnableLLMCompress 显式开关：nil=未设置（沿用 dino 默认 true）；true/false=覆盖。
	// 用指针区分「未设置」与「显式 false」（默认是 true，零值 bool 无法表达）。
	EnableLLMCompress *bool `mapstructure:"enable_llm_compress"`
}

type SubagentSettings struct {
	Enabled bool `mapstructure:"enabled"`
}

type EmailToolConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
}

type ProviderConfig struct {
	APIKey  string   `mapstructure:"api_key"`
	BaseURL string   `mapstructure:"base_url"`
	Model   string   `mapstructure:"model"`
	Models  []string `mapstructure:"models"`
	OrgID   string   `mapstructure:"org_id"`
	APIType string   `mapstructure:"api_type"`
}

type LLMConfig struct {
	Provider  string                    `mapstructure:"provider"`
	Providers map[string]ProviderConfig `mapstructure:"providers"`
	OpenAI    ProviderConfig            `mapstructure:"openai"`
	Anthropic ProviderConfig            `mapstructure:"anthropic"`
}

type ToolConfig struct {
	Builtin               []string         `mapstructure:"builtin"`
	MCP                   []MCPToolConfig  `mapstructure:"mcp"`
	SafeBinaries          []string         `mapstructure:"safe_binaries"`
	Env                   []string         `mapstructure:"env"`
	ApprovalRequiredTools []string         `mapstructure:"approval_required_tools"`
	EmailConfig           *EmailToolConfig `mapstructure:"email_config"`
}

type CortexConfig struct {
	DefaultModel   string           `mapstructure:"default_model"`
	ImageInputMode string           `mapstructure:"image_input_mode"`
	AuthProfile    string           `mapstructure:"auth_profile"`
	Timeout        string           `mapstructure:"timeout"`
	MaxIterations  int              `mapstructure:"max_iterations"`
	SystemPrompt   string           `mapstructure:"system_prompt"`
	LLM            LLMConfig        `mapstructure:"llm"`
	ToolConfig     ToolConfig       `mapstructure:"tool_config"`
	SkillsRoot     string           `mapstructure:"skills_root"`
	AgentsDir      string           `mapstructure:"agents_dir"`
	DefaultAgent   string           `mapstructure:"default_agent"`
	Subagent       SubagentSettings `mapstructure:"subagent"`
	Memory         MemorySettings   `mapstructure:"memory"`
}

type GatewayConfig struct {
	Enabled      bool   `mapstructure:"enabled"`
	AuthToken    string `mapstructure:"auth_token"`
	MaxFrameSize int    `mapstructure:"max_frame_size"`
	Bind         string `mapstructure:"bind"`
}

type CanvasConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Port    int    `mapstructure:"port"`
	Root    string `mapstructure:"root"`
}

type ConsoleConfig struct {
	Enabled          bool   `mapstructure:"enabled"`
	ChatUserID       string `mapstructure:"chat_user_id"`
	ChatRoleID       string `mapstructure:"chat_role_id"`
	ChatRoleName     string `mapstructure:"chat_role_name"`
	Language         string `mapstructure:"language"`
	ServerURL        string `mapstructure:"server_url"`
	AutoStartBackend bool   `mapstructure:"auto_start_backend"`
}

type LogConfig struct {
	Level    string `mapstructure:"level"`
	Output   string `mapstructure:"output"`
	FilePath string `mapstructure:"file_path"`
}

type QueueConfig struct {
	Mode       string `mapstructure:"mode"`
	DebounceMS int    `mapstructure:"debounce_ms"`
	Cap        int    `mapstructure:"cap"`
}

type MessagesConfig struct {
	Queue QueueConfig `mapstructure:"queue"`
}

type ServerConfig struct {
	Port         int                `mapstructure:"port"`
	Mode         string             `mapstructure:"mode"`
	GracePeriod  string             `mapstructure:"grace_period"`
	Gateway      GatewayConfig      `mapstructure:"gateway"`
	Canvas       CanvasConfig       `mapstructure:"canvas"`
	Console      ConsoleConfig      `mapstructure:"console"`
	Cortex       CortexConfig       `mapstructure:"cortex"`
	Log          LogConfig          `mapstructure:"log"`
	Messages     MessagesConfig     `mapstructure:"messages"`
	MemoryIngest MemoryIngestConfig `mapstructure:"memory_ingest"`
}

type WorkspaceConfig struct {
	Root         string `mapstructure:"root"`
	AutoReload   bool   `mapstructure:"auto_reload"`
	Unrestricted bool   `mapstructure:"unrestricted"`
}

type DatabaseConfig struct {
	Driver   string `mapstructure:"driver"`
	DSN      string `mapstructure:"dsn"`
	LogLevel string `mapstructure:"log_level"`
}

type TasksConfig struct {
	StuckJobTimeout string `mapstructure:"stuck_job_timeout"`
	Locker          string `mapstructure:"locker"`
}

type ProfileConfig struct {
	Enabled            bool     `mapstructure:"enabled"`
	ExtractInterval    string   `mapstructure:"extract_interval"`
	MinMessages        int      `mapstructure:"min_messages"`
	WindowMessages     int      `mapstructure:"window_messages"`
	MaxMessageChars    int      `mapstructure:"max_message_chars"`
	SummaryInterval    string   `mapstructure:"summary_interval"`
	AllowedKeys        []string `mapstructure:"allowed_keys"`
	PendingThreshold   float64  `mapstructure:"pending_threshold"`
	ConfirmedThreshold float64  `mapstructure:"confirmed_threshold"`
}

type AudioConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	MaxBytes int    `mapstructure:"max_bytes"`
	Provider string `mapstructure:"provider"`
}

type FFmpegConfig struct {
	Path string `mapstructure:"path"`
}

type WhisperConfig struct {
	Provider string `mapstructure:"provider"`
	APIKey   string `mapstructure:"api_key"`
	BaseURL  string `mapstructure:"base_url"`
	Model    string `mapstructure:"model"`
}

type MediaConfig struct {
	Audio   AudioConfig   `mapstructure:"audio"`
	FFmpeg  FFmpegConfig  `mapstructure:"ffmpeg"`
	Whisper WhisperConfig `mapstructure:"whisper"`
}

type TelegramConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Token   string `mapstructure:"token"`
	Debug   bool   `mapstructure:"debug"`
}

type HostAppConfig struct {
	Server    ServerConfig    `mapstructure:"server"`
	Workspace WorkspaceConfig `mapstructure:"workspace"`
	Database  DatabaseConfig  `mapstructure:"database"`
	Tasks     TasksConfig     `mapstructure:"tasks"`
	Profile   ProfileConfig   `mapstructure:"profile"`
}
