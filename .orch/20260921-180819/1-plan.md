# Implementation Plan: README.md for orch

## Objective
Provide a concise, comprehensive `README.md` at the repository root (`README.md`) documenting the `orch` CLI commands, usage patterns, and the three supported coding agents (`agy`, `command-code`, `copilot`), adhering strictly to the constraint of zero modifications to Go source files.

---

## 1. Scope & Constraints
- **Target File**: `/Users/najmiraihan/Developer/orch-go/README.md`
- **Zero Go Changes**: Strictly forbidden to modify any `*.go` files.
- **Source of Truth**: All documentation must directly reflect the implementation in `main.go`, `agent.go`, and `build.go` (and `usage()` in `main.go`).
- **Tone & Style**: Direct, clean, minimal (Ponytail principles: boring over clever, no unnecessary fluff).

---

## 2. Current State Assessment
- `README.md` already exists at the repository root and covers:
  - Architecture overview (Real PTYs, raw mode, sequential execution, zero permission bypass).
  - Prerequisites (Go 1.25+, macOS Darwin PTY raw mode vs `term_other.go`).
  - Supported agents registry table (`agy`, `command-code`, `copilot`).
  - Permission safety guarantees (shared and agent-specific denied flags).
  - CLI command reference (`list`/`ls`, `run`, `build`, `version`, `help`).
  - Exit code specifications (`0`, `1`, `2`, `127`, `>0`/`128+N`).
  - Build workflow and stage completion signal (`ORCH_STAGE_COMPLETE`).
- Existing Go source files have uncommitted changes implementing the `ORCH_STAGE_COMPLETE` polling mechanism in `pty.go`, `build.go`, `main.go`, and their corresponding test files. These are part of the runner infrastructure and must remain untouched by this task.

---

## 3. Implementation Steps (for Implementation Stage)
1. **Audit & Finalize `README.md`**:
   - Verify that all 5 CLI commands (`list`/`ls`, `run`, `build`, `version`, `help`) are documented with accurate flags and behavior.
   - Verify that all 3 supported agents (`agy`, `command-code`, `copilot`) are documented with correct binary names, invocation flags, continue flags, and denied flags.
   - Confirm accurate prerequisite specifications: Go 1.25+ (from `go.mod`), `github.com/creack/pty v1.1.24`, and macOS Darwin platform requirement for interactive raw mode.
   - Ensure documentation of the build workflow, artifact structure under `.orch/<run-id>/`, and the `ORCH_STAGE_COMPLETE` completion marker.
   - Verify exit codes table matches `orchMain`, `cmdRun`, `cmdBuild`, and `Run`.
2. **Verify No Go File Modifications**:
   - Verify with `git diff --stat -- '*.go'` that no Go files have been modified as part of documenting `README.md`.
3. **Repository Integrity Check**:
   - Run `gofmt -l .`, `go vet ./...`, and `go test ./...` to verify all tests continue to pass cleanly.

---

## 4. Verification Checklist
- [ ] `README.md` exists at repository root with valid markdown and balanced code blocks.
- [ ] All 5 commands (`list`/`ls`, `run`, `build`, `version`/`-v`/`--version`, `help`/`-h`/`--help`) are documented accurately.
- [ ] All 3 agents (`agy`, `command-code`, `copilot`) are documented using exact summaries and flag specifications from `agent.go`.
- [ ] Permission safety and denied flag policies match `agent.go` (`sharedDeny` and `Deny`).
- [ ] Platform requirements (macOS/Darwin raw mode vs non-darwin fallback) and Go version (1.25+) are accurately documented.
- [ ] Exit code table is present and matches the codebase.
- [ ] Zero Go source files are modified (`*.go` files remain untouched).
- [ ] `go test ./...` and `go vet ./...` pass.

ORCH_STAGE_COMPLETE
