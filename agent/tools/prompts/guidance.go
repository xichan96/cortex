package prompts

const (
	ReadToolGuidance = `File / read guidance:
- Use absolute paths when the workspace requires it.
- Long outputs may use a line-oriented format (line number prefix + content); preserve exact text including indentation when editing.
- For large files, read a bounded range instead of the whole file when possible.
- Directory paths return listings; use them before assuming file paths.`

	EditToolGuidance = `Edit guidance:
- Read the target file before editing when you need current contents.
- Preserve exact indentation and whitespace from the read output.
- Edits fail if old_str is not unique; use the smallest old_str that uniquely identifies the span (often 2–4 adjacent lines).
- Use replace_all only when renaming or repeating identical replacements across the file.`

	WriteToolGuidance = `Write guidance:
- Overwrites an existing file at path.
- Read first if the file already exists and you are not replacing it entirely.
- Prefer edit_file for partial changes; use write_file for new files or full rewrites.`

	DirExistsGuidance = `If the target directory already exists, write files directly with the write tool (do not run mkdir or re-check existence unless you have a specific reason).`
)
