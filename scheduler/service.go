package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/agent/skills"
	"github.com/xichan96/cortex/agent/tools"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/logger"
	"github.com/xichan96/cortex/pkg/xcron"
)

type Service struct {
	xcronScheduler *xcron.Scheduler
	llmProvider    types.LLMProvider
	agentConfig    *types.AgentConfig
	memoryProvider types.MemoryProvider
	memoryFactory  func(sessionID string) types.MemoryProvider
	toolRegistry   *tools.Registry
	skillRegistry  *skills.Registry
}

// NewService creates a new scheduler service
func NewService(s *xcron.Scheduler) *Service {
	return &Service{xcronScheduler: s}
}

// ConfigureAgent configures the base components for building agents for scheduled tasks
func (s *Service) ConfigureAgent(
	llmProvider types.LLMProvider,
	config *types.AgentConfig,
	memory types.MemoryProvider,
	toolRegistry *tools.Registry,
	skillRegistry *skills.Registry,
) {
	s.llmProvider = llmProvider
	s.agentConfig = config
	s.memoryProvider = memory
	s.toolRegistry = toolRegistry
	s.skillRegistry = skillRegistry

	s.ensureAgentConfig()

	// Register the task handler to xcron
	s.registerAgentHandler()
}

// ConfigureAgentWithMemoryFactory allows associating per-session memory via factory
func (s *Service) ConfigureAgentWithMemoryFactory(
	llmProvider types.LLMProvider,
	config *types.AgentConfig,
	memoryFactory func(sessionID string) types.MemoryProvider,
	toolRegistry *tools.Registry,
	skillRegistry *skills.Registry,
) {
	s.llmProvider = llmProvider
	s.agentConfig = config
	s.memoryFactory = memoryFactory
	s.toolRegistry = toolRegistry
	s.skillRegistry = skillRegistry

	s.ensureAgentConfig()

	s.registerAgentHandler()
}

// registerAgentHandler registers the xcron handler that triggers the agent
func (s *Service) registerAgentHandler() {
	s.xcronScheduler.RegisterHandler("agent_task", func(ctx context.Context, job *xcron.Job) error {
		if s.llmProvider == nil {
			return fmt.Errorf("agent LLM provider not configured")
		}

		payload := job.Payload
		logger.Info("⏰ Scheduled Task Triggered", slog.String("payload", payload), slog.String("session_id", job.SessionID))

		// Parse payload
		var instruction string
		var agentPayload xcron.AgentPayload

		// Try to parse as structured AgentPayload
		if err := json.Unmarshal([]byte(payload), &agentPayload); err == nil && agentPayload.Message != "" {
			instruction = agentPayload.Message
		} else {
			// Fallback: try as raw string (JSON string) or raw text
			var decoded string
			if err := json.Unmarshal([]byte(payload), &decoded); err == nil {
				instruction = decoded
			} else {
				instruction = payload
			}
		}

		if strings.HasPrefix(strings.TrimSpace(instruction), "{") {
			var nestedPayload xcron.AgentPayload
			if err := json.Unmarshal([]byte(instruction), &nestedPayload); err == nil && nestedPayload.Message != "" {
				instruction = nestedPayload.Message
				if len(agentPayload.Tools) == 0 && len(nestedPayload.Tools) > 0 {
					agentPayload.Tools = nestedPayload.Tools
				}
				if len(agentPayload.Skills) == 0 && len(nestedPayload.Skills) > 0 {
					agentPayload.Skills = nestedPayload.Skills
				}
				if agentPayload.MaxIterations == 0 && nestedPayload.MaxIterations > 0 {
					agentPayload.MaxIterations = nestedPayload.MaxIterations
				}
			}
		}

		if len(agentPayload.Skills) == 0 && s.skillRegistry != nil {
			logger.Info("🔍 Planning skills for instruction", slog.String("instruction", instruction))
			planner := skills.NewPlanner(s.skillRegistry)
			plan, err := planner.Plan(ctx, instruction)
			if err == nil && len(plan.Steps) > 0 {
				seen := map[string]struct{}{}
				for _, step := range plan.Steps {
					for _, sk := range step.Skills {
						if _, ok := seen[sk.Name]; ok {
							continue
						}
						seen[sk.Name] = struct{}{}
						agentPayload.Skills = append(agentPayload.Skills, sk.Name)
					}
				}
				logger.Info("✅ Planner identified skills", slog.Any("skills", agentPayload.Skills))
			} else if err != nil {
				logger.Warn("❌ Planner failed", slog.String("error", err.Error()))
			} else {
				logger.Warn("⚠️ Planner found no skills for instruction", slog.String("instruction", instruction))
			}
		} else {
			if s.skillRegistry == nil {
				logger.Warn("⚠️ Skill registry is nil, cannot plan skills")
			} else {
				logger.Info("ℹ️ Skills already present in payload", slog.Any("skills", agentPayload.Skills))
			}
		}

		taskConfig := s.buildTaskConfig(agentPayload)

		agentEngine := s.newAgentEngine(ctx, job, &taskConfig, agentPayload)

		// Execute Agent
		logger.Info("🚀 Executing Agent", slog.String("instruction", instruction), slog.Int("skills_count", len(agentPayload.Skills)))
		result, err := agentEngine.Execute(ctx, instruction, nil)
		if err != nil {
			logger.Error("❌ Agent execution failed", slog.Any("error", err))
			return err
		}

		logger.Info("✅ Agent Execution Result", slog.String("output", result.Output))
		return nil
	})
}

func (s *Service) ensureAgentConfig() {
	if s.agentConfig == nil {
		s.agentConfig = types.NewAgentConfig()
	}
	if s.agentConfig.MaxIterations <= 0 {
		s.agentConfig.MaxIterations = 30
	}
}

func (s *Service) buildTaskConfig(agentPayload xcron.AgentPayload) types.AgentConfig {
	tc := *s.agentConfig
	tc.ChatMessageRole = "system"
	if agentPayload.MaxIterations > 0 {
		tc.MaxIterations = agentPayload.MaxIterations
	}
	if len(agentPayload.Skills) > 0 && s.skillRegistry != nil {
		var loadedSkills []skills.Skill
		for _, skillName := range agentPayload.Skills {
			if skill, ok := s.skillRegistry.Get(skillName); ok {
				loadedSkills = append(loadedSkills, *skill)
			} else {
				logger.Warn("Skill not found", slog.String("skill", skillName))
			}
		}
		if len(loadedSkills) > 0 {
			skillsPrompt := skills.BuildSystemPromptInjection(loadedSkills)
			if skillsPrompt != "" {
				tc.SystemMessage += "\n" + skillsPrompt
			}
		}
	}
	return tc
}

func (s *Service) newAgentEngine(ctx context.Context, job *xcron.Job, taskConfig *types.AgentConfig, agentPayload xcron.AgentPayload) *engine.AgentEngine {
	ae := engine.NewAgentEngine(s.llmProvider, taskConfig)
	if s.memoryFactory != nil && job.SessionID != "" {
		ae.SetMemory(ctx, s.memoryFactory(job.SessionID))
	} else if s.memoryProvider != nil {
		ae.SetMemory(ctx, s.memoryProvider)
	} else {
		ae.SetMemory(ctx, providers.NewSimpleMemoryProviderWithLimit(100))
	}
	// If skills are present, ensure minimal tools are available even if payload.tools omitted
	if len(agentPayload.Skills) > 0 && s.toolRegistry != nil {
		if t, err := s.toolRegistry.Get("file"); err == nil {
			ae.AddTool(ctx, t)
		}
		if t, err := s.toolRegistry.Get("command"); err == nil {
			ae.AddTool(ctx, t)
		}
	}
	if len(agentPayload.Tools) > 0 && s.toolRegistry != nil {
		disallowed := map[string]struct{}{
			"schedule_job": {},
			"list_jobs":    {},
			"delete_job":   {},
		}
		toolLines := make([]string, 0, len(agentPayload.Tools))
		for _, toolName := range agentPayload.Tools {
			if _, blocked := disallowed[toolName]; blocked {
				logger.Warn("Tool is disallowed for scheduled agent", slog.String("tool", toolName))
				continue
			}
			tool, err := s.toolRegistry.Get(toolName)
			if err == nil {
				ae.AddTool(ctx, tool)
				toolLines = append(toolLines, fmt.Sprintf("- %s: %s", tool.Name(), tool.Description()))
			} else {
				logger.Warn("Tool not found", slog.String("tool", toolName))
			}
		}
		if len(toolLines) > 0 {
			var b strings.Builder
			b.WriteString("You are executing a scheduled task.\n")
			b.WriteString("You may ONLY use the following tools:\n")
			b.WriteString(strings.Join(toolLines, "\n"))
			taskConfig.SystemMessage += "\n" + b.String()
		}
	}
	return ae
}

// ScheduleJob schedules a new job
func (s *Service) ScheduleJob(ctx context.Context, input ScheduleJobInput) (string, error) {
	var jobType xcron.JobType
	switch input.Type {
	case "oneshot":
		jobType = xcron.JobTypeOneShot
	case "periodic":
		jobType = xcron.JobTypePeriodic
	case "cron":
		jobType = xcron.JobTypeCron
	default:
		return "", fmt.Errorf("invalid job type: %s", input.Type)
	}

	// Default task type to agent_task if not specified
	taskType := input.TaskType
	if taskType == "" {
		taskType = "agent_task"
	}

	// For agent tasks, ensure payload matches AgentPayload structure
	var payload interface{} = input.Payload
	if taskType == "agent_task" {
		// If input payload is a string, wrap it in AgentPayload
		if strPayload, ok := input.Payload.(string); ok {
			payload = xcron.AgentPayload{
				Message: strPayload,
			}
		} else if mapPayload, ok := input.Payload.(map[string]interface{}); ok {
			// Already a map, structure it
			// We can pass map directly to xcron as it marshals to JSON
			payload = mapPayload
		}
	}

	return s.xcronScheduler.AddJob(ctx, input.Name, jobType, input.Schedule, xcron.TaskType(taskType), payload, input.SessionID, 0)
}

// ListJobs returns a list of scheduled jobs
func (s *Service) ListJobs(ctx context.Context, limit, offset int) ([]*xcron.Job, int64, error) {
	return s.xcronScheduler.ListJobs(ctx, offset, limit)
}

// DeleteJob removes a job
func (s *Service) DeleteJob(ctx context.Context, jobID string) error {
	// Ensure status is updated to stopped before deletion
	if err := s.xcronScheduler.StopJob(ctx, jobID); err != nil {
		// Continue deletion even if stop fails, but prefer to log status update
		logger.Warn("Failed to stop job before deletion", slog.String("job_id", jobID), slog.String("error", err.Error()))
	}
	return s.xcronScheduler.RemoveJob(ctx, jobID)
}

func (s *Service) StopJob(ctx context.Context, jobID string) error {
	return s.xcronScheduler.StopJob(ctx, jobID)
}

// ScheduleJobInput defines the input for scheduling a job
type ScheduleJobInput struct {
	Name      string      `json:"name"`
	Type      string      `json:"type"`       // oneshot, periodic, cron
	SessionID string      `json:"session_id"` // Optional session ID for memory context
	Schedule  string      `json:"schedule"`   // duration or cron expression
	Payload   interface{} `json:"payload"`    // string or object
	TaskType  string      `json:"task_type"`
}

// RegisterAgentTaskHandler registers a handler for agent tasks
// Deprecated: Use InitAgent instead for built-in integration
func RegisterAgentTaskHandler(s *xcron.Scheduler, agentExecutor func(input string) error) {
	s.RegisterHandler("agent_task", func(ctx context.Context, job *xcron.Job) error {
		payload := job.Payload
		// Try to unmarshal as AgentPayload first
		var agentPayload xcron.AgentPayload
		if err := json.Unmarshal([]byte(payload), &agentPayload); err == nil && agentPayload.Message != "" {
			return agentExecutor(agentPayload.Message)
		}

		// Fallback: try as raw string
		var decoded string
		if err := json.Unmarshal([]byte(payload), &decoded); err == nil {
			return agentExecutor(decoded)
		}

		// Fallback: use raw payload
		return agentExecutor(payload)
	})
}
