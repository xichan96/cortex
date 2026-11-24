package prompts

const (
	ReadToolGuidance = `File / read guidance:
- Use absolute paths when the workspace requires it.
- Output lines are prefixed with their 1-based line number and a tab ("<line>\t<content>"); strip that prefix and preserve exact text including indentation when editing.
- For large files, read a bounded range with offset/limit instead of the whole file.
- Directory paths return listings; use them before assuming file paths.`

	EditToolGuidance = `Edit guidance:
- Read the target file before editing when you need current contents.
- Preserve exact indentation and whitespace from the Read output (line numbers are not part of the content).
- Edits fail if old_string is not unique; use the smallest old_string that uniquely identifies the span (often 2–4 adjacent lines).
- Use replace_all only when renaming or repeating identical replacements across the file.`

	WriteToolGuidance = `Write guidance:
- Overwrites an existing file at file_path.
- Read first if the file already exists and you are not replacing it entirely.
- Prefer Edit for partial changes; use Write for new files or full rewrites.`

	DirExistsGuidance = `If the target directory already exists, write files directly with the Write tool (do not run mkdir or re-check existence unless you have a specific reason).`
)
