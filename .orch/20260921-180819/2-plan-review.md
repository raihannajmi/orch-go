# Plan Review: README.md for orch

**Verdict: NEEDS REVISION before implementation.** The plan is factually accurate about the codebase, but it does not specify a concrete, verifiable deliverable, and its single mechanical check (`git diff --stat -- '*.go'`) is broken by pre-existing uncommitted Go changes. As written, the implementation stage would likely produce a no-op diff while the integrity check either passes vacuously or gives a false failure.

---

## What the plan gets right

- **Scope/constraint handling is correct.** Forbidding `*.go` changes is right, and the plan correctly notes that `build.go`, `build_test.go`, `main.go`, `pty.go`, and `pty_test.go` already carry uncommitted work (the `ORCH_STAGE_COMPLETE` polling mechanism) that is **not** part of this task and must stay untouched.
- **Command inventory is accurate.** `orchMain` dispatches exactly `list`/`ls`, `run`, `build`, `version`/`-v`/`--version`, `help`/`-h`/`--help` — five commands, matching the plan.
- **Agent facts are accurate.** `agents` in `agent.go` defines `agy`, `command-code`, `copilot`; the continue flag is `--continue` for all three; prompt flags are `--prompt-interactive <text>`, positional `<text>`, and `--interactive <text>` respectively — all matching the README.
- **Permission-flag list is accurate.** `sharedDeny` plus `command-code`'s `Deny` (`--trust`, `-t`) match the README's "Permission Safety Guarantees", including the `--flag value` / `--flag=value` note.
- **Exit codes are accurate** and match `orchMain` (no args / unknown command → 2; help/version → 0), `cmdRun` (usage → 2, missing binary → 127, `Run` error → 1, agent code propagated), `cmdBuild` (0/1/2/127), and `Run`/`exitCode` (`128+N` when signalled).
- **Dependencies verified.** `go.mod` is `module orch`, `go 1.25.6`, `github.com/creack/pty v1.1.24`; `term_darwin.go` (`//go:build darwin`) implements raw mode while `term_other.go` returns `errNoRawMode`. Readme claims hold.

## Wrong assumptions / gaps

1. **The task is already satisfied, and the plan does not confront that.** `README.md` was added in commit `2e184e1` and already documents all five commands and all three agents in detail; the working tree even has a further uncommitted edit adding the "Stage Completion Signal" section (`git diff README.md` shows +3 lines). The plan's step 1 is an "audit and finalize" with **no stated concrete change**. Because the task literally reads "Add a simple README.md", the plan must decide and declare one of:
   - the existing README already satisfies the task (documentation-only, minimal or zero edits), or
   - the specific edits/corrections to make (see item 2).
   Without this, the implementation stage has undefined output, and the downstream implementation-review stage may reject a zero-diff result even though the requirement is met.

2. **The plan lists "verify" items but silently leaves two known inaccuracies uncorrected.** These are the only real edits the task likely needs:
   - **Go version:** README says "Go 1.25+" but `go.mod`'s `go 1.25.6` directive means **at least 1.25.6**. Should read `1.25.6+`.
   - **Artifacts example is misleading:** README shows `3-implement.md (or 3-fix.md on subsequent cycles)`. `builder.artifactPath` increments a single counter, so a cycle-2 fix is actually `5-fix.md` with `6-review.md`, not `3-fix.md`. The example tree should reflect the real sequential numbering (or drop the parenthetical).

3. **The plan's own verification step 2 is unsound.** `git diff --stat -- '*.go'` already reports five modified Go files from pre-existing work. Running it after the implementation stage will *always* show Go changes, so it cannot distinguish "README task touched Go" from "Go was already dirty". Replace with a before/after comparison — e.g. record `git diff -- '*.go' | shasum` (or the mtimes) at the start of the implementation stage and assert it is unchanged at the end; or simply assert that `git status --porcelain` shows **no new** Go paths relative to the baseline.

4. **"Simple README" vs. the existing ~140-line document is unresolved.** The task asks for a *simple* README; the current file is thorough. The plan should state explicitly that coverage/accuracy wins over brevity and that the existing structure is kept, otherwise the implementation or review stage may argue for trimming content the task actually requires.

5. **No acceptance criterion tied to the task wording.** The plan front-loads exit codes, build workflow, and artifact layout. It should separate the **required** floor (document the CLI commands + the three agents) from **nice-to-have** detail, so a minimal-but-correct README can be judged as passing.

## Risky steps

- **Editing the artifact-naming example requires care**: `3-fix.md`→`5-fix.md` etc. must match `artifactPath` numbering exactly, or the "verification" checklist will propagate a new inaccuracy.
- **Re-running the full integrity suite is fine** (`gofmt -l .` clean, `go vet ./...` clean, `go test ./...` passes in ~2.2s — verified), but note `TestInteractiveStageEndsLingeringAgent` launches a stand-in `sleep 3600` on a PATH stub; do not weaken/alter it while documenting.
- **Do not "fix" the uncommitted Go work** to make a clean `git diff` — that directly violates the task constraint.

## Required changes before implementation

1. State the concrete deliverable: confirm the existing README is the artifact, plus the specific corrections in item 2 (Go version, artifact-naming example). If the intent is truly "no change needed", say so explicitly so a zero-diff result is a legitimate, approved outcome.
2. Replace verification step 2 with a **baseline-relative** check for Go-file immutability (hash or porcelain diff captured before/after), not a bare `git diff --stat -- '*.go'`.
3. Add an explicit acceptance criterion: all five commands and all three agents are documented and factually match `main.go`/`agent.go`; mark exit-code/build-workflow sections as supplementary.
4. Resolve the "simple" ambiguity: keep the existing coverage, state that accuracy takes precedence over brevity.

ORCH_STAGE_COMPLETE
