package runner

import "strings"

const sprintContractAppendix = `

## Sprint contract (harness)
- Scope: what this segment will deliver.
- Definition of Done: checks others can run (tests, scripts, artifacts).
- How to verify: commands or paths (e.g. under ArtifactDir / VerifyCommand).
`

func AppendSprintContractPrompt(userGoal string) string {
	g := strings.TrimSpace(userGoal)
	if g == "" {
		return strings.TrimSpace(sprintContractAppendix)
	}
	return g + sprintContractAppendix
}
