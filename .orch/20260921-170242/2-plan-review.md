# Plan Review: README.md for orch

**Verdict: REVISE — not ready to implement as written.** Two factual errors and one broken verification step, plus several undocumented behaviors. The overall shape of the plan is sound and the risk is low (README-only change), but the plan would produce a README with wrong prerequisites and a checklist that cannot pass.

## What the plan gets right (verified against source)

- Agent registry matches `agents` in `agent.go` exactly: `agy` / `command-code` / `copilot`, in that order.
- Prompt mappings are correct: `agy --prompt-interactive <p>`, `command-code <p>` (positional), `copilot --interactive <p>`.
- Continue mapping correct for all three: `--continue`.
- Agent-specific denies correct: `command-code` → `--trust`, `-t`.
- `sharedDeny` list is reproduced accurately (`--dangerously-skip-permissions`, `--yolo`, `--auto-accept`, `--tools-all`, `--permission-mode auto-accept|accept-all`, `--mode accept-edits`).
- Command set correct: `list`/`ls`, `run`, `build`, `version`, `help`; top-level aliases `-v`/`--version` and `-h`/`--help` all exist in `orchMain`.
- `run` flags correct (`-C/--dir`, `-c/--continue`, `-p/--prompt`, `--` pass-through), including the single-agent-only restriction on `--`.
- `run` halt-on-non-zero behavior correct (`return code` on agent exit).
- `build` stage sequence correct: agy plan → command-code plan-review → agy implement → command-code review → agy fix + review loop → verification.
- Verification step correct: `gofmt -l .`, `go vet ./...`, `go test ./...` (`verifyRepo`).
- `version` output correct: `orch 0.1.0` (`const version = "0.1.0"`).
- Target file correct: `README.md` does not currently exist; creating it is the right move. No Go source needs touching.

## Errors / wrong assumptions (must fix before implementing)

1. **Go version prerequisite is wrong.** Plan says "Go 1.22+". `go.mod` declares `go 1.25.6` and a `go` directive is a minimum toolchain requirement, so the README must say **Go 1.25+** (or "the toolchain pinned in `go.mod`"). Also worth naming the single dependency `github.com/creack/pty v1.1.24` and the module name `orch`.

2. **Platform claim is incomplete/misleading.** Raw-mode handling is darwin-only: `term_darwin.go` is `//go:build darwin`; `term_other.go` is `//go:build !darwin` and returns `errNoRawMode`, leaving the terminal untouched. Full-screen agent UIs therefore only work on macOS. The README must state this explicitly (the plan only says "macOS" in passing under prerequisites and never says what happens elsewhere).

3. **The verification step `git diff --stat *.go produces no output` cannot pass at baseline — and this is a blocking defect in the plan.** Current repo state before any work is already dirty:
   - `M main.go` — 13 insertions already present.
   - `?? build.go`, `?? build_test.go` — untracked (so `git diff` never shows them anyway).
   
   Running that command today prints `main.go | 13 +++`. The checklist item is therefore unverifiable and would falsely implicate this task in pre-existing changes. **Fix the verification to be baseline-relative**, e.g. record `git status --porcelain` (or `git stash create` / tree hash) before the edit and assert that the only *new* path is `README.md`, and state explicitly that the existing `main.go` diff and untracked `build*.go` are pre-existing and out of scope.

## Gaps — behaviors the plan should document or consciously exclude

- **Exit codes are undocumented.** `run`: `2` for usage/unknown agent/no agents/pass-through-with-multiple-agents, `127` for a missing binary, `1` on PTY/runtime error, otherwise the agent's own code is propagated. `build`: `2` on arg errors, `127` on missing binary, `1` on stage/verification failure, `0` on success. `orch` with no args prints usage to **stderr** and exits `2`. Either add a short table or say explicitly these are out of scope.
- **`orch build` is hard-wired** to `buildPlanAgent = agy` and `buildReviewAgent = command-code`. Nothing in the plan says the other agents are unusable with `build` — the README should.
- **Artifact naming is numbered, not a flat glob.** Actual names: `1-plan.md`, `2-plan-review.md`, `3-implement.md` (cycle 1) or `3-fix.md` (later cycles), `4-review.md`, per-stage `<name>.log`, and `verify.log`. The plan's `*-plan.md` / `*-review.md` sketch is loose; show a real example directory.
- **`build` limits/limits-facing knobs unstated:** at most `buildPlanCycles = 3` implementation/review cycles, then failure; prior-artifact context is truncated to `buildContextLimit = 8000` bytes with the full file always referenced by path. The cycle cap is worth a sentence; the truncation limit is not README material.
- **Pass-through is validated, not "untouched."** The plan's own wording is accurate ("permission-bypass flags strictly blocked") but the README's tone should match `usage()` — orch *adds* no approval-skipping flags and *refuses* to forward those supplied after `--`; avoid implying orch sanitizes the agent's internal behavior.
- **Agent descriptions:** use the registry/usage strings verbatim (`Antigravity CLI (native terminal UI)`, `Command Code coding agent`, `GitHub Copilot CLI`). The plan's "**Google** Antigravity CLI" is an added attribution not present in the code — either drop it or verify it; drift between README and `usage()` is the failure mode to avoid.
- **Flag parsing position-independence** (`orch run agy -c` and `orch run -c agy` both work; unknown flag → exit 2 with message) is undocumented. One line suffices.
- **Duplication risk:** `usage()` already carries a full flag/agent summary. The README should either mirror it exactly or defer to it. The plan does not address keeping the two in sync — worth an explicit note that `usage()` is the source of truth.

## Risks

- **Low overall risk.** No Go files are modified; worst case is documentation drift.
- **Main risk is drift from `usage()` and `go.mod`** — already materialized in the plan's Go version and platform claims.
- **Step 4 (`go vet` / `go test`) is harmless but should not be used to judge this change**: tests are unaffected by a README, so a failure there is pre-existing and must not be attributed to this task (and must not trigger a "fix" of Go files, which the task forbids).
- **`README.md` precedence:** confirm there is no existing README (there is not) and no docs/ directory convention to follow.

## Recommended changes to the plan

1. Replace "Go 1.22+" with "Go 1.25+" (per `go.mod`); mention the single `creack/pty` dependency.
2. State macOS/darwin as the supported platform for interactive agent UIs, with the off-darwin (`errNoRawMode`) limitation.
3. Rewrite step 3 and the checklist item on Go changes to a baseline-relative check; explicitly mark `M main.go` and `?? build.go`/`?? build_test.go` as pre-existing.
4. Add a short exit-code table (or declare it out of scope).
5. Note `orch build` is fixed to `agy` + `command-code` and show a real `.orch/<run-id>/` listing.
6. Use the registry's agent summaries verbatim; drop the unverified "Google" attribution.
7. State that `usage()` in `main.go` is the source of truth the README mirrors.

## Corrected verification checklist

- [ ] `README.md` exists at repo root and is readable; balanced code fences (renders without raw backticks leaking).
- [ ] All 5 commands documented (`list`/`ls`, `run`, `build`, `version`/`-v`, `help`/`-h`).
- [ ] All 3 agents documented (`agy`, `command-code`, `copilot`) using the exact summaries from `agents` in `agent.go`.
- [ ] Every flag listed in the README exists in `parseRunArgs` / `parseBuildArgs`; no flag documented that the code rejects.
- [ ] Documented Go version matches `go.mod` (`1.25.6`); platform note matches the `darwin` / `!darwin` build tags.
- [ ] Permission-safety wording matches `usage()` and `checkPermissionFlags` (adds none, refuses forwarded ones) without overclaiming.
- [ ] **Baseline-relative:** `git status --porcelain` after the change differs from the pre-change snapshot only by the addition of `README.md`; the pre-existing `main.go` modification and untracked `build.go`/`build_test.go` are unchanged in both snapshots.
- [ ] `go vet ./...` and `go test ./...` pass (integrity check only; failures, if any, are pre-existing and must not be fixed via Go edits).
