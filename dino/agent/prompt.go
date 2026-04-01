package agent

const SubagentSystemGuidelines = `Subagent usage:
- Fork a subagent for open-ended research, broad exploration, or implementation work that needs more than a few focused edits.
- Do not race the parent: avoid duplicating the same heavy work in parallel unless intentionally split.
- Do not peek at subagent artifacts: do not read or tail subagent output files unless the user explicitly asks.
- When writing a subagent prompt, state the goal, constraints, expected output shape, and which tools or paths are in scope.
- Prefer one in_progress task at a time in your own todo list when tracking multi-step work.`
