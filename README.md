# orch

A minimal Go CLI orchestrator that runs coding agents in real pseudo-terminals (PTYs), preserving native terminal UIs and interactive permission prompts verbatim.

## Features & Philosophy

- **Portable Terminals:** Pty handling uses `creack/pty` and raw mode uses `golang.org/x/term`, so each agent's real terminal UI and resize handling (`SIGWINCH`) work on macOS and Linux.
- **Zero Permission Bypass:** `orch` adds no approval-skipping flags and refuses to forward any bypass flags passed after `--`. Native permission prompts are always preserved and answered interactively.
- **Sequential Execution:** Agents run one after another, each owning the terminal. If an agent exits with a non-zero code, subsequent agents are skipped and `orch` halts immediately with that code.
- **Liveness Watchdog:** Each build stage has a configurable timeout. A stage that stops making progress is terminated cleanly and the run stops there instead of waiting forever.
- **Optional Knowledge Layer:** Best-effort context retrieval and durable capture (Obsidian, Graphify) that never breaks a build or bypasses permissions.

## Prerequisites & Installation

- **Go:** the `go` directive in `go.mod` (currently `1.25.8`). It is the module's minimum Go version and acts as a toolchain floor — an older local toolchain is upgraded to it automatically — and it is kept at the release that fixes the standard-library advisory `govulncheck` reports for this code. CI builds the latest patch of the same minor (`go-version: '1.25.x'`).
- **Platform:** macOS and Linux. Raw mode comes from `golang.org/x/term`, which covers the common Unix targets; non-Unix platforms do not build.
- **Dependencies:** `github.com/creack/pty v1.1.24` and `golang.org/x/term v0.45.0` (module `github.com/raihannajmi/orch-go`).
- **Agents:** The binaries for agents you wish to run must be installed and available on `$PATH`.

### Install from source

```sh
go install github.com/raihannajmi/orch-go@latest   # installs `orch-go` into $(go env GOPATH)/bin
# or, from a checkout:
go build -o orch .
```

`go install` names the binary after the module path (`orch-go`); the `-o orch`
build, or renaming the installed binary, gives the `orch` command spelled
throughout this document.

Release builds embed the version, commit and date:

```sh
go build -ldflags "-X main.version=v0.2.0 \
  -X main.commit=$(git rev-parse --short HEAD) \
  -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o orch .
```

`orch version` prints `orch 0.1.0`, and appends the commit and date when they were
injected. Releases are cut as Git tags (`vX.Y.Z`) on `main`; there is no separate
release framework to learn.

### Quick start

```sh
orch list                              # agents orch knows, and which are installed
orch run agy                           # launch agy in a real terminal, permissions intact
orch run command-code -p "summarise this repository"
```

For an unattended workflow in a repository orch can verify (it has a `go.mod`,
`Cargo.toml`, or a `package.json` with a test/lint script):

```sh
orch build "add a --json flag to the report command"   # plan, review, implement, review, verify
orch status                                            # runs under .orch/, newest first
orch logs 20260101-120000                              # one run's reports and transcripts
orch resume 20260101-120000                            # continue an interrupted run
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
- **Bypass denylist:** the user's pass-through arguments are refused when they name a permission-suppressing flag. Both `--flag value` and `--flag=value` spellings are handled, values are matched case-insensitively, and single-dash clusters are expanded (`-cy` is caught as `continue` + `yolo`). The first short flag of any single-dash token is always checked, so a mixed-case spelling such as `-yX` is refused too, while an attached value such as `-Ctmp` is left alone. Scanning stops at `--`, after which arguments are positional for the agent.
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
Every run creates a timestamped directory under `.orch/<run-id>/` holding the Markdown reports, their raw terminal transcripts, and the run's own state and lock files:
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
├── run.json
├── run.lock
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
ORCH_KNOWLEDGE_VAULT=~/Obsidian/myvault orch build "harden the auth boundary" --knowledge

# with the optional Graphify enrichment layer
ORCH_KNOWLEDGE_VAULT=~/Obsidian/myvault ORCH_KNOWLEDGE_GRAPHIFY=1 \
  orch build "harden the auth boundary" --knowledge
```

- **Obsidian is the source of truth.** It provides the project context and related notes, and it is the only place durable knowledge is written.
- **Graphify is optional enrichment.** When enabled independently via `ORCH_KNOWLEDGE_GRAPHIFY`, orch runs `graphify query --graph <graph.json> -- "<task>"` and adds the returned subgraph as extra reference context (the task follows the `--` terminator, so a task that looks like a flag can never change the invocation). Graphify is *never* a hard dependency: with the flag unset (or the CLI/graph missing) Obsidian works exactly as before. orch speaks no MCP protocol itself; the CLI is the supported mechanism.
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

Lists the files of a single run — the numbered Markdown reports and their raw terminal transcripts first, then the unnumbered ones (`run.json`, `run.lock`, `verify.log`, and any `<stage>.timeout`) — with their sizes. The `<run-id>` is the timestamp directory name under `.orch/` and the first column of `orch status`. Invalid, unknown, or path-traversing run ids are rejected with exit code `2`.

```sh
orch logs 20260921-180819
```

```
artifacts in .orch/20260921-180819:
  1-plan.log         121.1 KB
  1-plan.md            3.5 KB
  2-plan-review.log  814.5 KB
  ...
  run.json            1.2 KB
  run.lock                0 B
  verify.log             19 B
```

### `orch resume <run-id>`

Resumes an interrupted or failed run from its first incomplete stage, skipping the
stages whose artifacts already end with the completion marker. A live run is never
resumed underneath its owner: `resume` takes the same repository lock as `build`,
and re-runs an agent stage exactly as `build` would — interactively, with the
agent's native permission prompts.

```sh
orch resume 20260101-120000
```

Recovery is deliberately conservative. `orch` never resets, checks out, cleans or
stashes, and when the repository is not safe to continue it refuses rather than
touching your work:

- if `HEAD` moved since the run started, `resume` refuses so your commits are not disturbed;
- if the tree is dirty but no stage of the run could have changed it, `resume` refuses rather than continuing on an unexpected tree;
- an interrupted `-implement`/`-fix` stage is re-run as-is — changes it already made are **not** rolled back, and `orch` says so rather than implying a clean slate.

A run whose stages are all complete but whose verification failed resumes by
re-running verification, with the same commands the run recorded. Runs created
before `run.json` existed stay readable with `orch status`/`orch logs` but cannot
be resumed; `resume` reports that instead of guessing. As with `build`, `--json`
is supported.

### `orch version` (aliases: `-v`, `--version`)

Prints the current version (`orch 0.1.0`). When a release build injects them via
`-ldflags` (see [Install from source](#install-from-source)), the commit and date
are appended, e.g. `orch 0.2.0 (1a2b3c4, 2026-09-22T10:00:00Z)`.

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

## Security

What orch guarantees — no permission bypass, no shell, private `0700`/`0600`
artifacts, a symlinked `.orch` is refused, no destructive git — and its known
exposure (the command line is visible in `ps`; transcripts capture agent output)
are documented in [SECURITY.md](SECURITY.md). Report vulnerabilities privately
rather than in a public issue.

Dependencies and the standard library are scanned with `govulncheck` in CI, which
fails on known vulnerabilities the code is affected by. The module's minimum Go
version is kept at the release that fixes the standard-library advisory the
scanner reports for this code, so an older toolchain is upgraded rather than
built against it.

## Development & Contributing

```sh
go build ./... ; go test ./... ; go test -race ./... ; go vet ./... ; gofmt -l .
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full checklist and the rules the
code keeps. Bug reports use the issue template; dependency updates arrive via
Dependabot and are reviewed and merged by a human, never automatically.

## License

[MIT](LICENSE).
