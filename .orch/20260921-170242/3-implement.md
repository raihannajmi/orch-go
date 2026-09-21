# Implementation Summary: README.md for orch

## Changes Made
Added root [README.md](file:///Users/najmiraihan/Developer/orch-go/README.md) documenting the `orch` CLI orchestrator, its commands, options, exit codes, and supported coding agents, without modifying any Go source files.

## Review Feedback Addressed
All feedback from the Plan Review ([2-plan-review.md](file:///Users/najmiraihan/Developer/orch-go/.orch/20260921-170242/2-plan-review.md)) was addressed:

1. **Prerequisites Accuracy:**
   - Documented Go toolchain requirement as Go 1.25+ (`go.mod` pins `go 1.25.6`).
   - Mentioned single runtime dependency `github.com/creack/pty v1.1.24` and module name `orch`.

2. **Platform Constraints:**
   - Explicitly documented that full-screen interactive agent UIs require macOS (`darwin`) where terminal raw mode is implemented natively via `syscall` ioctls (`term_darwin.go`).
   - Clarified that on non-darwin platforms (`term_other.go`), raw mode is unsupported (`errNoRawMode`) and the terminal is left untouched (permitting piped/non-interactive execution, but not interactive TUIs).

3. **Zero Go Source Modifications (Baseline Relative):**
   - No `*.go` files were created or edited. Pre-existing changes to `main.go` and untracked `build*.go` remain completely untouched.
   - Baseline diff was preserved with `git status --porcelain` confirming only `README.md` and this summary file were added.

4. **Agent Registry & Summaries:**
   - Used verbatim agent summaries from `agents` in `agent.go`:
     - `agy`: `Antigravity CLI (native terminal UI)`
     - `command-code`: `Command Code coding agent`
     - `copilot`: `GitHub Copilot CLI`
   - Omitted unverified attributions (e.g. "Google").
   - Documented exact invocation flags (`--prompt-interactive`, positional, `--interactive`) and continue flags (`--continue`).

5. **Permission Safety Guarantees:**
   - Reflected the exact policy in `checkPermissionFlags`: `orch` adds no bypass flags and strictly blocks user pass-through of bypass flags (`--dangerously-skip-permissions`, `--yolo`, `--auto-accept`, `--tools-all`, `--permission-mode`, `--mode accept-edits`, and `command-code`'s `--trust`/`-t`).

6. **Workflow & Command Nuances Documented:**
   - Clarified that `orch build` is hard-wired to `buildPlanAgent = agy` and `buildReviewAgent = command-code`.
   - Included real `.orch/<run-id>/` directory artifact structure and the 3-cycle review cap (`buildPlanCycles = 3`).
   - Documented position-independent flag parsing for `orch run`.
   - Added complete exit code table (`0`, `1`, `2`, `127`, `>0` / `128+N`).
   - Referenced `usage()` in `main.go` as the source of truth.

## Verification
- Verified `git status --porcelain`: only `README.md` and `.orch/.../3-implement.md` added.
- Verified test suite uncached: `go test -count=1 ./...` passed in 1.38s.
- Verified vet check: `go vet ./...` passed with zero warnings.
