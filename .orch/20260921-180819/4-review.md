# Implementation Review: README.md for orch

## Verdict
The task is complete and correct. `README.md` documents all five CLI commands and all three supported agents, and no Go source files were modified by this work.

## What I verified (against the real repository, not the summary)

### 1. README exists at repo root and is valid Markdown
- `README.md` exists with 146 lines, balanced fenced code blocks, and a well-formed table.
- It is a modification of the pre-existing file (from commit `2e184e1`), not a new file. The task ("add a simple README that documents commands and agents") is satisfied: the file fully documents both.

### 2. All CLI commands documented accurately (`main.go: orchMain`)
- `list` / `ls` — documented at `README.md:55`; behavior (name, install status `installed`/`not found on PATH`, summary) matches `listAgents` (`main.go:171`).
- `run` — documented at `README.md:63` with flags `-C/--dir`, `-c/--continue`, `-p/--prompt`, and `-- <args>`; matches `parseRunArgs` (`main.go:124`) and usage text.
- `build` — documented at `README.md:88`; flags `-C/--dir` match `parseBuildArgs` (`build.go:127`).
- `version` / `-v` / `--version` — documented at `README.md:130`; matches `main.go:40`, prints `orch 0.1.0` (const `version`, `main.go:17`).
- `help` / `-h` / `--help` — documented at `README.md:134`; matches `main.go:43`.

### 3. All three agents documented accurately (`agent.go`)
- `agy` — binary `agy`, `--continue`, prompt `--prompt-interactive <text>`; matches `agents[0]`.
- `command-code` — binary `command-code`, `--continue`, positional prompt, deny `--trust`/`-t`; matches `agents[1]`.
- `copilot` — binary `copilot`, `--continue`, prompt `--interactive <text>`; matches `agents[2]`.

### 4. Permission-safety section matches the code
- `README.md:39-48` lists exactly `sharedDeny` (`--dangerously-skip-permissions`, `--yolo`, `--auto-accept`, `--tools-all`, `--permission-mode auto-accept`/`accept-all`, `--mode accept-edits`) plus command-code's `--trust`/`-t` (`agent.go:69-76`, `:38`).
- The claim that both `--flag value` and `--flag=value` are checked matches `checkPermissionFlags` (`agent.go:87`).

### 5. Platform / dependency / build-workflow claims match the code
- Go `1.25.6+` matches `go.mod:3`; dependency `github.com/creack/pty v1.1.24` matches `go.mod:5`.
- Darwin raw mode via stdlib ioctls (`term_darwin.go`) and non-darwin `errNoRawMode` fallback (`term_other.go`) match `README.md:15`.
- Stage completion signal `ORCH_STAGE_COMPLETE`, artifact numbering under `.orch/<run-id>/`, and "up to 3 cycles" (`buildPlanCycles = 3`) match `build.go:28-47`, `:181-185`, `:217`.
- The "SIGTERM, then SIGKILL after a grace period" wording matches `stopSession` + `stopGracePeriod = 2s` (`pty.go:18`, `:153-165`).
- Exit-code table (`README.md:138-146`) matches `orchMain` (usage error `2`, not-found `127`), `cmdRun`/`cmdBuild` (runtime `1`), and `exitCode` (`pty.go:206`, `128+signal`).

### 6. Constraint: no Go source files modified
- `git status` shows `build.go`, `build_test.go`, `main.go`, `pty.go`, `pty_test.go` as modified — but these are the pre-existing uncommitted `ORCH_STAGE_COMPLETE` runner changes that the plan explicitly identified as infra that must remain untouched (`git diff -- '*.go'` is entirely the marker-polling mechanism and its tests).
- The current Go diff hash is `e546e7fbc63c6a4a787a966373e26c67ffda2997`, identical to the hash recorded in the implementation summary — so the documentation stage did not alter any Go file.
- The only content change introduced by this task is to `README.md`, three hunks: Go-version wording, artifact-tree correction (`3-fix.md` note replaced with a sequential-numbering sentence), and the new Stage Completion Signal paragraph. All accurate.

### 7. Repository integrity
- `gofmt -l .` → clean (no output).
- `go vet ./...` → passed.
- `go test -count=1 ./...` → `ok orch 2.006s`.

## Required changes
None. The README is accurate against the implementation, the scope constraint (no Go edits) is honored, and all validation checks pass.

VERDICT: APPROVED
ORCH_STAGE_COMPLETE
