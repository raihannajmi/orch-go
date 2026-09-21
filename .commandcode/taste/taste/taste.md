# Taste
- When asked to build something, wants it implemented directly in the repository — create and write the actual files; do not just explain or output code snippets. Confidence: 0.85
- For Go work, expects the standard verification pass after implementing: `gofmt`, `go vet ./...`, `go test ./...`, and fixing any errors found. Confidence: 0.8
- Expects tests to be added for new logic where practical, rather than shipping features untested. Confidence: 0.7
- Prefers minimal architecture: keep the design small and avoid unnecessary machinery or dependencies (YAGNI-style). Confidence: 0.8
- Comfortable with macOS-specific (darwin-only) implementations when the task is macOS-focused, rather than portability for its own sake. Confidence: 0.7
- Never bypass, auto-approve, or suppress permission prompts/checks; interactively-surfaced approvals should remain in force. Confidence: 0.75
