---
name: cortex-ext
description: Demonstrates all cortex extension fields under metadata.cortex.
metadata:
  author: cortex
  cortex:
    triggers:
      - type: keyword
        pattern: pdf
      - type: command
        pattern: /pdf
    tags: [docs, pdf]
    priority: 42
    dependencies: [pdf-tools]
    paths: ["**/*.pdf", "docs/**"]
    when_to_use: When a PDF file is open in the workspace.
    timeout: 30s
    retries: 2
    fallback: pdf-tools
---

# Cortex Extension

This skill exists to exercise metadata.cortex parsing.
