---
name: legacy-skill
description: Legacy top-level extension fields should still parse with deprecation warnings.
triggers:
  - type: command
    pattern: /legacy
tags: [legacy, old]
priority: 7
timeout: 45s
retries: 1
fallback: other-skill
dependencies: [dep-skill]
paths: ["*.go"]
when_to_use: When testing legacy compatibility.
allowed_tools: [read, Grep]
---

# Legacy Skill

Legacy body.
