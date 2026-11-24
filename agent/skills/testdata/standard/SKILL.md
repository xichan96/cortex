---
name: pdf-tools
description: Extract text and tables from PDFs. Use when the user mentions PDFs or document extraction.
license: Apache-2.0
compatibility: Requires pdftotext on PATH
allowed-tools:
  - read
  - Bash
disable-model-invocation: false
metadata:
  author: cortex
---

# PDF Tools

Use `pdftotext` to extract text from the supplied PDF.
