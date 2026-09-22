# orch

A minimal Go CLI orchestrator that runs coding agents in real pseudo-terminals (PTYs), preserving native terminal UIs and interactive permission prompts verbatim.

## Features & Philosophy

- **Portable Terminals:** Pty handling uses `creack/pty` and raw mode uses `golang.org/x/term`, so each agent's real terminal UI and resize handling (`SIGWINCH`) work on macOS and Linux.
- **Zero Permission Bypass:** `orch` adds no approval-skipping flags and refuses to forward any bypass flags passed after `--`. Native permission prompts are always preserved and answered interactively.
- **Sequential Execution:** Agents run one after another, each owning the terminal. If an agent exits with a non-zero code, subsequent agents are skipped and `orch` halts immediately with that code.
- **Liveness Watchdog:** Each build stage has a configurable timeout. A stage that stops making progress is terminated cleanly and the run stops there instead of waiting forever.
- **Optional Knowledge Layer:** Best-effort context retrieval and durable capture (Obsidian, Graphify) that never breaks a build or bypasses permissions.

## Prerequisites & Building

- **Go:** Go 1.25+ (from `go.mod`).
- **Platform:** macOS and Linux. Raw mode comes from `golang.org/x/term`, which covers the common Unix targets; non-Unix platforms do not build.
- **Dependencies:** `github.com/creack/pty v1.1.24` and `golang.org/x/term v0.45.0` (module `github.com/raihannajmi/orch-go`).
- **Agents:** The binaries for agents you wish to run must be installed and available on `$PATH`.

### Building from Source

```sh
go build -o orch .
```

## Supported Agents

The agent registry is defined in `agent.go`:

| Agent | Binary | Summary | Continue Flag | Interactive Prompt Flag |
|---|---|---|---|---|
| `agy` | `agy` | Antigravity CLI (native terminal UI) | `--continue` | `--prompt-interactive <text>` |
| `command-code` | `command-code` | Command Code coding agent | `--continue` | `<text>` (positional) |
| `copilot` | `copilot` | GitHub Copilot CLI | `--continue` | `--interactive <text>` |

### Permission Safety Guarantees

`orch` strictly enforces that no permissions are bypassed or auto-approved. It never injects approval-skipping flags, and every argument the agent's parser could read as a flag is checked against a denylist before launch. The boundary has two gates:

- **Per-agent allowlist:** the only flags `orch` itself may place on the command line are those an agent declares (`--continue`, and the prompt flag). `buildArgv` fails closed if it would emit anything else, so a bad registry entry cannot silently weaken a session.
- **Bypass denylist:** the user's pass-through arguments are refused when they name a permission-suppressing flag. Both `--flag value` and `--flag=value` spellings are handled, values are matched case-insensitively, and single-dash clusters are expanded (`-cy` is caught as `continue` + `yolo`). Scanning stops at `--`, after which arguments are positional for the agent.
- **Text is not a flag:** a prompt carried as a flag value (agy, copilot) is data and is never scanned; only a positional prompt (command-code) is checked, because there the agent's own parser would read a leading `-` as a flag.

Blocked flags include:

- **Outright:** `--dangerously-skip-permissions`, `--dangerously-bypass-approvals-and-sandbox`, `--skip-permissions`, `--bypass-permissions`, `--yolo` / `-y`, `--auto-accept`, `--auto-accept-all`, `--auto-approve`, `--always-approve`, `--accept-edits`, `--accept-all`, `--allow-all`, `--allow-all-tools`, `--allow-all-paths`, `--tools-all`, `--trust`, `--full-auto`.
- **By value:** `--permission-mode`, `--approval-mode`, `--mode`, `--sandbox` are refused when set to a suppressing value (`auto-accept`, `accept-edits`, `bypassPermissions`, `yolo`, `none`, …). Safe values such as `default`, `plan` and `standard` still pass.
- **Agent-specific:** `command-code`: `-t` (short form of `--trust`).

## CLI Reference

`usage()` in `main.go` is the source of truth for all CLI usage and flag specifications.

### `orch list` (alias: `orch ls`)

Lists all registered agents, their installation status on `$PATH` (`installed` or `not found on PATH`), and descriptions.

```sh
orch list
```

### `orch run <agent> [agent...] [flags] [-- <args...>]`

Runs one or more agents sequentially in the current terminal. Flag parsing is position-independent (for example, `orch run agy -c` and `orch run -c agy` are equivalent).

**Flags:**
- `-C, --dir <path>`: Working directory for the agents (default: current directory).
- `-c, --continue`: Resume each agent's most recent session.
- `-p, --prompt <text>`: Start each agent interactively with this initial prompt message.
- `-- <args...>`: Extra pass-through arguments handed to the agent untouched. Permitted for single-agent invocations only; permission-bypass flags are strictly rejected.

**Examples:**
```sh
# Start agy interactively with an initial prompt
orch run agy -p "Refactor the auth package"

# Resume an agy session in a specific project directory
orch run agy -c -C /path/to/project

# Run agy followed by command-code sequentially
orch run agy command-code

# Pass extra flags to a single agent
orch run copilot -- --model claude-3.5-sonnet
```

### `orch build "<task>" [flags]`

Executes an automated multi-stage build workflow combining planning, plan review, implementation, and review loops, followed by verification.

`orch build` is hard-wired to use:
- **Planner / Implementer:** `agy`
- **Reviewer:** `command-code`

**Workflow Stages:**
1. **Planning (`agy`):** Investigates the repository and writes an implementation plan.
2. **Plan Review (`command-code`):** Critically reviews the plan before any code is modified.
3. **Implementation (`agy`):** Implements code changes based on the plan and plan review.
4. **Implementation Review (`command-code`):** Inspects actual repository changes.
5. **Fix / Re-review Loop:** If changes are requested, `agy` addresses review points and `command-code` re-reviews. Orch allows up to 3 implementation/review cycles (`buildPlanCycles = 3`). The workflow proceeds only when a review artifact ends with an explicit `VERDICT: APPROVED`.
6. **Repository Verification:** Runs the configured verification commands in the repository (see [Verification](#verification) below).

**Flags:**
- `-C, --dir <path>`: Repository directory where agents work (default: current directory).
- `--stage-timeout <duration>`: End a stage that makes no progress for this long, e.g. `--stage-timeout 45m` (default `30m`; `0` disables the watchdog).
- `--verify <command>`: Verification command to run at the end, in order, and repeatable. Overrides auto-detection.
- `--knowledge`: Load relevant context before planning and capture durable outcomes after approved runs (see below).

### Verification

Every `orch build` (and `orch resume`) ends with verification commands run in the repository. A verification failure fails the run, is reported distinctly from an agent failure, and is recorded in `run.json` (`verifyResult: "failed"`).

Commands are chosen as follows:

1. **Explicit `--verify`**, when given. Repeatable; commands run in order. The value is split into argv by `orch` itself — there is no shell, so no pipes, redirection, globbing, command substitution or variable expansion.
2. **Auto-detection** from the repository, when no `--verify` is given:
   - `go.mod` → `gofmt -l .`, `go vet ./...`, `go test ./...`
   - `Cargo.toml` → `cargo check`, `cargo test`
   - `package.json` → `npm run <script>` for each of `lint`, `check`, `typecheck`, `test` that the package already defines (in that order). Scripts are never invented, and a bare package-manager command is never run on a guess.
3. **Otherwise** `orch` refuses to start, rather than run a workflow whose result it cannot check.

Commands execute in the repository directory (`-C/--dir`). The verification commands a run used are stored in `run.json`, so `orch resume` re-runs exactly the same checks.

```sh
# Go repository: auto-detected (gofmt, go vet, go test)
orch build "add a --json flag"

# Any repository: explicit commands, run in order
orch build "fix the parser" --verify "make lint" --verify "make test"
orch build "harden the auth boundary" -C ~/src/app --verify "pytest -q"
```

### Concurrency and artifact permissions

Only one mutating workflow runs against a repository at a time. `orch build` and `orch resume` take a repository-level lock at `.orch/workflow.lock` (an OS `flock`, held for the whole workflow and released on completion, failure, or process death). A second build or resume in the same repository is refused with a clear message instead of racing the first on the working tree. Read-only commands (`orch status`, `orch logs`) do not take the lock. Independent repositories lock independently and can run concurrently. Lock order is always repository lock first, then the per-run lock (`.orch/<run-id>/run.lock`), so the two can never deadlock.

Runtime artifacts under `.orch/` are owner-only: the `.orch` directory and new run directories are `0700`, and the state (`run.json`), lock files, transcripts (`.log`), stage artifacts (`.md`) and `verify.log` are `0600`. This keeps prompts, transcripts and agent output private on shared machines. Existing artifacts are never re-permissioned, and `status`/`logs` keep reading runs created before this change.

### Machine-readable output (`--json`)

`orch list`, `orch status`, `orch logs <run-id>`, `orch build` and `orch resume` accept `--json`. With it, orch writes **exactly one JSON document to stdout and nothing else there**; interactive agent output, progress and diagnostics all go to stderr, so `stdout` is valid JSON a pipe can consume. Exit codes are unchanged. `orch run` is interactive and has no `--json`.

Every document carries a common envelope, and errors are a stable object:

```json
{ "command": "status", "ok": true,  "runs": [ ... ] }
{ "command": "build",  "ok": false, "error": { "code": 1, "message": "verification failed: ..." },
  "run": { "id": "20260922-100305", "status": "failed", "failure": "verify",
           "verifyResult": "failed", "stages": [ ... ] } }
```

- `list` → `agents[]` (`name`, `installed`, `summary`).
- `status` → `runs[]` (`id`, `stages`, `verdict`, `timedOut`, and, when the run has `run.json`, `status`, `lastCompleted`, `verifyResult`, `failure`, `exitCode`).
- `logs` → `runId`, `runDir`, `artifacts[]` (`name`, `size`).
- `build`/`resume` → `run` (`id`, `dir`, `status`, `failure`, `exitCode`, `verifyResult`, `stages[]`).

The task text is deliberately **not** included, and no transcripts are inlined: JSON output never leaks prompts or agent output.

```sh
orch status --json | jq '.runs[0].status'
orch build "add a flag" --json --verify "make test"
```

### Limits: transcripts and large prompts

A stage transcript (`<stage>.log`) and `verify.log` are capped at 64 MiB and, when the cap is hit, end with an explicit `[orch: transcript truncated ...]` notice — truncation is never silent. Set `ORCH_MAX_LOG_BYTES` to change the cap (`0` or `unlimited` disables it). The stage Markdown artifacts (which carry the completion marker) are written by the agent to a different file and are **never** truncated, so a bounded transcript cannot break the workflow. Live terminal output is not capped.

Prompts and forwarded arguments reach the agent as command-line arguments, which the OS bounds (Linux `MAX_ARG_STRLEN` is 128 KiB). orch checks the assembled command line before exec and refuses an oversized one with an actionable error — it never silently truncates your task — and a kernel `E2BIG` is reported as a clear message rather than a raw crash.

### Timestamps and failure classification

Run ids are UTC timestamps (`YYYYMMDD-HHMMSS`) and the persisted `createdAt` is RFC3339 UTC, so ordering and identity are timezone-independent; existing local-time run ids remain readable. When a run ends, `run.json` records a machine-readable `failure` kind — `agent`, `timeout`, `verify`, `state` or `workflow` — plus the agent's `exitCode` when an agent exited non-zero, so an agent failure is never confused with an orch internal error or a failed verification. These surface in `status --json` and `build`/`resume --json`.

**Stage Completion Signal:**
Every stage is an interactive session, so after finishing its work the agent stays at its prompt. To avoid waiting on that prompt forever, each stage prompt requires the agent to write `ORCH_STAGE_COMPLETE` on a line by itself as the final line of its artifact. `orch` polls the artifact for that marker and cleanly ends the session (SIGTERM, then SIGKILL after a grace period) once it appears — so permission prompts stay available while the agent works, and the workflow continues automatically when the stage is done.

**Stage Timeout:**
If a stage neither writes its marker nor exits within the per-stage timeout, the watchdog ends that session the same way (SIGTERM, then SIGKILL), records `<stage>.timeout` in the run directory, and stops the run — no later stage starts. This is the guard against a wedged prompt or a hung tool call blocking the workflow forever.

**Artifacts Directory:**
Every run creates a timestamped directory under `.orch/<run-id>/` containing Markdown reports and raw terminal transcripts:
```
.orch/20260921-170242/
├── 1-plan.md
├── 1-plan.log
├── 2-plan-review.md
├── 2-plan-review.log
├── 3-implement.md
├── 3-implement.log
├── 4-review.md
├── 4-review.log
└── verify.log
```

If changes are requested, subsequent fix and review cycles continue numbering sequentially (e.g. `5-fix.md`, `6-review.md`, etc.).

### `orch build --knowledge` (optional knowledge layer)

`orch build` can optionally load context before a run and record durable outcomes after one. It is **off by default**: without `--knowledge`, `orch build "task"` behaves exactly as before and needs no external system. The orchestrator talks only to an internal `knowledge.Provider` interface whose default is a no-op, so Obsidian and Graphify stay behind that boundary.

The first backend is Obsidian, configured by environment (the CLI stays a single flag):

| Variable | Meaning |
| --- | --- |
| `ORCH_KNOWLEDGE_VAULT` | Absolute path to an Obsidian vault (required to enable) |
| `ORCH_KNOWLEDGE_PROJECT` | Project folder under `01-Projects/` (default: repository directory name) |
| `ORCH_KNOWLEDGE_GRAPHIFY` | Truthy (`1`/`true`/`yes`/`on`) to add the optional Graphify enrichment layer |
| `ORCH_KNOWLEDGE_GRAPH` | `graph.json` to query (default: `<vault>/graphify-out/graph.json`) |

```sh
ORCH_KNOWLEDGE_VAULT=~/Obsidian/najmiraihan orch build "harden the auth boundary" --knowledge

# with the optional Graphify enrichment layer
ORCH_KNOWLEDGE_VAULT=~/Obsidian/najmiraihan ORCH_KNOWLEDGE_GRAPHIFY=1 \
  orch build "harden the auth boundary" --knowledge
```

- **Obsidian is the source of truth.** It provides the project context and related notes, and it is the only place durable knowledge is written.
- **Graphify is optional enrichment.** When enabled independently via `ORCH_KNOWLEDGE_GRAPHIFY`, orch runs the documented `graphify query "<task>" --graph <graph.json>` CLI and adds the returned subgraph as extra reference context. Graphify is *never* a hard dependency: with the flag unset (or the CLI/graph missing) Obsidian works exactly as before. orch speaks no MCP protocol itself; the CLI is the supported mechanism.
- **Retrieval:** before planning, orch reads the project's context note plus only the decisions/problems/learning notes whose filenames match the task. The whole vault is never read. Graphify is queried for relationships, not ingested wholesale.
- **Trust boundary:** loaded knowledge — Obsidian and Graphify alike — is untrusted data. It is injected into the planning prompt only inside an explicit `<external_knowledge>…</external_knowledge>` fence, labeled as reference material that must not be followed as instructions, and it can never override the task or the workflow instructions. Content is preserved as-is (nothing is filtered), and a note cannot close the fence early because its own boundary tags are neutralized.
- **Capture:** after an approved run, orch appends one entry to the project's single `build-log.md`. It updates that note rather than creating a note per run, and never writes prompts, transcripts, or debugging output. Graphify is read-only and never receives captured knowledge.
- **Failure semantics:** knowledge is best-effort. A missing vault, a failed load/capture, or a failed Graphify query prints a warning and the build continues. A failed Graphify enrichment still leaves the Obsidian context in place — the knowledge layer can neither fail nor silently corrupt a run.

### `orch status`

Lists the workflow runs recorded under `.orch/`, newest first, with each run's stage count, the verdict of its final review (`APPROVED`, `REJECTED`, or `-` when no review artifact exists), and the stage a watchdog ended (`-` unless the run timed out). A missing `.orch/` is not an error; it reports that no runs exist yet.

```sh
orch status
```

```
RUN-ID            STAGES  VERDICT   TIMED OUT
20260921-180819        4  APPROVED  -
20260921-170242        4  -         3-implement
```

### `orch logs <run-id>`

Lists the artifacts of a single run — the numbered Markdown reports and their raw terminal transcripts, plus `verify.log` — in stage order with their sizes. The `<run-id>` is the timestamp directory name under `.orch/` and the first column of `orch status`. Invalid, unknown, or path-traversing run ids are rejected with exit code `2`.

```sh
orch logs 20260921-180819
```

```
artifacts in .orch/20260921-180819:
  1-plan.log         121.1 KB
  1-plan.md            3.5 KB
  2-plan-review.log  814.5 KB
  ...
  verify.log             19 B
```

### `orch version` (aliases: `-v`, `--version`)

Prints the current version (`orch 0.1.0`).

### `orch help` (aliases: `-h`, `--help`)

Prints CLI usage instructions, agent registry summaries, and flag references.

## Exit Codes

| Exit Code | Meaning |
|---|---|
| `0` | Success (or help/version requested). |
| `1` | PTY or runtime error, or stage / verification failure during `orch build`. `run.json` and `--json` classify it (`agent`, `timeout`, `verify`, `state`, `workflow`). |
| `2` | Command-line usage error, unknown command or agent, missing flag argument, or multiple agents specified with `--` pass-through args. (`orch` with no arguments prints usage to `stderr` and exits with `2`). |
| `127` | Required agent binary not found on `$PATH`. |
| `>0` / `128+N` | Exit status propagated directly from the agent process, following standard shell conventions (or `128 + signal` if killed by signal). |
