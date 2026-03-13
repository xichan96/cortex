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

func parseAgentInstruction(payload string) (instruction string, out xcron.AgentPayload) {
	if err := json.Unmarshal([]byte(payload), &out); err == nil && out.Message != "" {
		return out.Message, out
	}
	var decoded string
	if err := json.Unmarshal([]byte(payload), &decoded); err == nil {
		payload = decoded
	}
	trimmed := strings.TrimSpace(payload)
	if strings.HasPrefix(trimmed, "{") {
		var nested xcron.AgentPayload
		if err := json.Unmarshal([]byte(trimmed), &nested); err == nil && nested.Message != "" {
			if len(out.Tools) == 0 {
				out.Tools = nested.Tools
			}
			if len(out.Skills) == 0 {
				out.Skills = nested.Skills
			}
			if out.MaxIterations == 0 {
				out.MaxIterations = nested.MaxIterations
			}
			return nested.Message, out
		}
	}
	return payload, out
}

func (s *Service) registerAgentHandler() {
	s.xcronScheduler.RegisterHandler(xcron.TaskTypeAgent, func(ctx context.Context, job *xcron.Job) error {
		if s.llmProvider == nil {
			return fmt.Errorf("agent LLM provider not configured")
		}

		payload := job.Payload
		logger.Info("⏰ Scheduled Task Triggered", slog.String("payload", payload), slog.String("session_id", job.SessionID))

		instruction, agentPayload := parseAgentInstruction(payload)

		if len(agentPayload.Skills) == 0 && s.skillRegistry != nil {
			logger.Info("🔍 Planning skills for instruction", slog.String("instruction", instruction))
			plan, err := skills.NewPlanner(s.skillRegistry).Plan(ctx, instruction)
			if err != nil {
				logger.Warn("❌ Planner failed", slog.String("error", err.Error()))
			} else if len(plan.Steps) > 0 {
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
			} else {
				logger.Warn("⚠️ Planner found no skills for instruction", slog.String("instruction", instruction))
			}
		} else if s.skillRegistry == nil {
			logger.Warn("⚠️ Skill registry is nil, cannot plan skills")
		} else {
			logger.Info("ℹ️ Skills already present in payload", slog.Any("skills", agentPayload.Skills))
		}

		taskConfig := s.buildTaskConfig(agentPayload)

		agentEngine := s.newAgentEngine(ctx, job, &taskConfig, agentPayload)

		// Execute Agent
		logger.Info("🚀 Executing Agent", slog.String("instruction", instruction), slog.Int("skills_count", len(agentPayload.Skills)))
		result, err := agentEngine.Execute(ctx, types.NewAgentInput(instruction), nil)
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

	taskType := input.TaskType
	if taskType == "" {
		taskType = string(xcron.TaskTypeAgent)
	}

	var payload interface{} = input.Payload
	if taskType == string(xcron.TaskTypeAgent) {
		if strPayload, ok := input.Payload.(string); ok {
			payload = xcron.AgentPayload{
				Message: strPayload,
			}
		} else if mapPayload, ok := input.Payload.(map[string]interface{}); ok {
			payload = mapPayload
		}
	}

	return s.xcronScheduler.AddJobWithOptions(ctx, input.Name, jobType, input.Schedule, xcron.TaskType(taskType), payload, input.SessionID, 0, input.ExecutionMode)
}

// ListJobs returns a list of scheduled jobs
func (s *Service) ListJobs(ctx context.Context, limit, offset int) ([]*xcron.Job, int64, error) {
	return s.xcronScheduler.ListJobs(ctx, offset, limit)
}

// ListJobsOptions defines filter and sort for listing jobs
type ListJobsOptions struct {
	Status    []string
	Type      []string
	SessionID string
	OrderBy   string
	Limit     int
	Offset    int
}

// ListJobsWithOptions returns jobs with filter and sort
func (s *Service) ListJobsWithOptions(ctx context.Context, opts ListJobsOptions) ([]*xcron.Job, int64, error) {
	xopts := xcron.ListOptions{Limit: opts.Limit, Offset: opts.Offset, SessionID: opts.SessionID, OrderBy: opts.OrderBy}
	for _, st := range opts.Status {
		xopts.Status = append(xopts.Status, xcron.JobStatus(st))
	}
	for _, ty := range opts.Type {
		xopts.Type = append(xopts.Type, xcron.JobType(ty))
	}
	if xopts.Limit <= 0 {
		xopts.Limit = 50
	}
	return s.xcronScheduler.ListJobsWithOptions(ctx, xopts)
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
	Name           string      `json:"name"`
	Type           string      `json:"type"`
	SessionID      string      `json:"session_id"`
	Schedule       string      `json:"schedule"`
	Payload        interface{} `json:"payload"`
	TaskType       string      `json:"task_type"`
	ExecutionMode  string      `json:"execution_mode"` // "serial" to run one-at-a-time with other serial jobs
}

// RegisterAgentTaskHandler registers a handler for agent tasks
// Deprecated: Use InitAgent instead for built-in integration
func RegisterAgentTaskHandler(s *xcron.Scheduler, agentExecutor func(input string) error) {
	s.RegisterHandler(xcron.TaskTypeAgent, func(ctx context.Context, job *xcron.Job) error {
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
