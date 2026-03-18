package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xichan96/cortex/dino"
)

func main() {
	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║          Dino Terminal Chat - Advanced Example            ║")
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")

	cfg := dino.DefaultConfig()

	cfg.Provider = dino.ProviderConfig{
		Type:    "openai",
		APIKey:  "",
		BaseURL: "",
		Models: map[string]string{
			"default": "gpt-5.2",
		},
	}

	cfg.DefaultModel = "gpt-5.2"
	cfg.WorkspaceRoot = "./"
	cfg.SystemPrompt = `You are Dino, a helpful AI assistant with access to various tools.
You can execute commands, read/write files, search the web, and use skills.
Always provide clear and concise responses.`

	cfg.MaxIterations = 10
	cfg.Temperature = 0.7
	cfg.MaxTokens = 2048
	cfg.Timeout = 60 * time.Second
	cfg.ToolExecutionTimeout = 120 * time.Second

	cfg.Skills.Path = "./skills"
	cfg.Skills.AutoLoad = true

	cfg.Tools.Allowed = []string{
		"read_file",
		"write_file",
		"edit_file",
		"glob",
		"grep",
		"bash",
		"list_directory",
		"skill",
		"question",
		"job_kill",
		"job_output",
		"web_fetch",
		"web_search",
		"todo",
	}
	cfg.Tools.ApprovalRequired = []string{}
	cfg.Tools.Denied = []string{}

	cfg.ToolTimeouts = map[string]time.Duration{
		"bash":       120 * time.Second,
		"web_fetch":  30 * time.Second,
		"web_search": 45 * time.Second,
		"glob":       10 * time.Second,
		"grep":       30 * time.Second,
		"read_file":  15 * time.Second,
		"write_file": 30 * time.Second,
		"edit_file":  15 * time.Second,
	}

	cfg.ToolTimeoutCalculator = func(toolName string, input map[string]interface{}) time.Duration {
		if toolName == "bash" {
			if cmd, ok := input["command"].(string); ok {
				cmdLower := strings.ToLower(cmd)
				if strings.Contains(cmdLower, "sleep") {
					if match := regexp.MustCompile(`sleep\s+(\d+)`).FindStringSubmatch(cmd); len(match) > 1 {
						if seconds, _ := strconv.Atoi(match[1]); seconds > 0 {
							return time.Duration(seconds+30) * time.Second
						}
					}
				}
				if strings.Contains(cmdLower, "curl") || strings.Contains(cmdLower, "wget") {
					return 180 * time.Second
				}
				if strings.Contains(cmdLower, "git") && (strings.Contains(cmdLower, "clone") || strings.Contains(cmdLower, "pull")) {
					return 300 * time.Second
				}
				if strings.Contains(cmdLower, "npm") || strings.Contains(cmdLower, "yarn") || strings.Contains(cmdLower, "pip") {
					return 240 * time.Second
				}
				if strings.Contains(cmdLower, "docker") {
					return 180 * time.Second
				}
			}
		}
		if toolName == "web_search" {
			return 60 * time.Second
		}
		return 0
	}

	cfg.LoopDetection.Enabled = true
	cfg.LoopDetection.MaxRepeats = 3
	cfg.LoopDetection.SimilarityThreshold = 0.8

	cfg.Budget.Enabled = true
	cfg.Budget.MaxTokens = 100000
	cfg.Budget.MaxToolCalls = 50
	cfg.Budget.MaxTimeMs = 300000

	cfg.PlannerMode.Enabled = false
	cfg.PlannerMode.PromptPlan = "Think silently about the steps needed. First explain your plan, then proceed."
	cfg.PlannerMode.AutoApprove = false

	cfg.Memory.EnableCompress = true
	cfg.Memory.MaxHistoryMessages = 100
	cfg.Memory.CompressThreshold = 50
	cfg.Memory.KeepRecentCount = 10
	cfg.Memory.PersistDirectory = "./dino_sessions"
	cfg.Memory.PersistEnabled = true
	cfg.Memory.Type = "sqlite"

	factory, err := dino.NewDinoFactory(cfg)
	if err != nil {
		log.Fatalf("Error creating Dino factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	client := dino.NewClient(factory)

	session, err := client.CreateSession(context.Background(), "chat-session")
	if err != nil {
		log.Fatalf("Error creating session: %v", err)
	}

	session.SubscribeFunc(func(event *dino.Event) {
		switch event.Type {
		case dino.EventTypeThinking:
			if event.Thinking != "" {
				fmt.Printf("\n🤔 Thinking: %s\n", truncate(event.Thinking, 200))
			}
		case dino.EventTypeMessage:
			fmt.Printf("\n🤖 Assistant: %s\n", event.Content)
		case dino.EventTypeToolCall:
			fmt.Printf("\n🔧 Tool Call: %s\n", event.ToolName)
			if event.ToolInput != nil {
				fmt.Printf("   Input: %s\n", truncate(fmt.Sprintf("%v", event.ToolInput), 100))
			}
		case dino.EventTypeToolStart:
			fmt.Printf("\n🚀 Tool Starting: %s\n", event.ToolName)
		case dino.EventTypeToolResult:
			fmt.Printf("   ✅ Result: %s\n", truncate(event.ToolOutput, 150))
		case dino.EventTypeTokenUsage:
			if event.Usage != nil {
				fmt.Printf("\n📊 Token Usage: Input=%d, Output=%d, Total=%d\n",
					event.Usage.PromptTokens,
					event.Usage.CompletionTokens,
					event.Usage.TotalTokens)
			}
		case dino.EventTypeApproval:
			fmt.Printf("\n⚠️  Approval Required: %s\n", event.ToolName)
			fmt.Printf("   Input: %v\n", event.ToolInput)
		case dino.EventTypeApproved:
			fmt.Printf("   ✅ Approved\n")
		case dino.EventTypePlan:
			fmt.Printf("\n📋 Plan:\n")
			if event.Plan != nil {
				for i, step := range event.Plan.Steps {
					fmt.Printf("   %d. Use %s\n", i+1, step.Tool)
				}
			}
		case dino.EventTypeError:
			fmt.Printf("\n❌ Error: %s\n", event.Error)
		case dino.EventTypeDone:
			fmt.Printf("\n" + strings.Repeat("─", 50) + "\n")
		}
	})

	reader := bufio.NewReader(os.Stdin)

	printHelp()

	for {
		fmt.Print("\n👤 User: ")
		userInput, _ := reader.ReadString('\n')
		userInput = strings.TrimSpace(userInput)

		if userInput == "quit" || userInput == "exit" {
			break
		}

		if userInput == "help" {
			printHelp()
			continue
		}

		if userInput == "skills" {
			skills := factory.GetSkills()
			fmt.Println("\n📦 Available Skills:")
			for _, s := range skills {
				fmt.Printf("  - %s: %s\n", s.Name, s.Description)
			}
			continue
		}

		if userInput == "clear" {
			fmt.Print("\033[2J\033[H")
			fmt.Println("=== Dino Terminal Chat ===")
			continue
		}

		if userInput == "" {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		event, err := session.SendAndWait(ctx, userInput)
		cancel()

		if err != nil {
			log.Printf("Error: %v", err)
			continue
		}

		if event.Error != "" {
			log.Printf("Session error: %s", event.Error)
		}
	}

	fmt.Println("\n👋 Goodbye!")
}

func printHelp() {
	fmt.Println(`
╔═══════════════════════════════════════════════════════════╗
║                     Available Commands                      ║
╠═══════════════════════════════════════════════════════════╣
║  help      - Show this help message                        ║
║  skills    - List available skills                         ║
║  clear     - Clear the screen                              ║
║  quit/exit - Exit the chat                                 ║
╚═══════════════════════════════════════════════════════════╝

You can chat naturally with the AI assistant.
The assistant has access to various tools for:
  - File operations (read, write, edit)
  - Command execution (bash)
  - Web search and fetch
  - Custom skills
`)
}

func truncate(v any, maxLen int) string {
	s, ok := v.(string)
	if !ok {
		if s2, ok := v.(fmt.Stringer); ok {
			s = s2.String()
		} else {
			return ""
		}
	}
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
