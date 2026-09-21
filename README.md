# orch

A minimal Go CLI orchestrator that runs coding agents in real pseudo-terminals (PTYs), preserving native terminal UIs and interactive permission prompts verbatim.

## Features & Philosophy

- **Real PTYs & Raw Mode:** Each agent process gets its own controlling pseudo-terminal sized to your terminal window, forwarding keystrokes and handling window resizes (`SIGWINCH`).
- **Zero Permission Bypass:** `orch` adds no approval-skipping flags and refuses to forward any bypass flags passed after `--`. Native permission prompts are always preserved and answered interactively.
- **Sequential Execution:** Agents run one after another, each owning the terminal. If an agent exits with a non-zero code, subsequent agents are skipped and `orch` halts immediately with that code.
- **Darwin Optimized:** Terminal raw mode is implemented natively for macOS (`darwin`) via standard library `syscall` ioctls, requiring only a single dependency (`creack/pty`).

## Prerequisites & Building

- **Go:** Go 1.25.6+ (from `go.mod`).
- **Platform:** macOS (darwin) for interactive full-screen agent UIs. On non-darwin platforms (`term_other.go`), raw mode is not implemented (`errNoRawMode`); the terminal is left untouched, which allows non-interactive or piped execution but will not drive full-screen interactive UIs properly.
- **Dependencies:** `github.com/creack/pty v1.1.24` (module `orch`).
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

`orch` strictly enforces that no permissions are bypassed or auto-approved. It never injects approval-skipping flags, and `checkPermissionFlags` blocks any attempt to pass the following flags via `--`:

- **Shared Denied Flags:**
  - `--dangerously-skip-permissions`
  - `--yolo`
  - `--auto-accept`
  - `--tools-all`
  - `--permission-mode auto-accept` / `--permission-mode accept-all`
  - `--mode accept-edits`
- **Agent-Specific Denied Flags:**
  - `command-code`: `--trust`, `-t`

Both `--flag value` and `--flag=value` syntaxes are checked and rejected.

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
6. **Repository Verification:** Runs standard checks in the repository:
   - `gofmt -l .` (fails if files need formatting)
   - `go vet ./...`
   - `go test ./...`

**Flags:**
- `-C, --dir <path>`: Repository directory where agents work (default: current directory).

**Stage Completion Signal:**
Every stage is an interactive session, so after finishing its work the agent stays at its prompt. To avoid waiting on that prompt forever, each stage prompt requires the agent to write `ORCH_STAGE_COMPLETE` on a line by itself as the final line of its artifact. `orch` polls the artifact for that marker and cleanly ends the session (SIGTERM, then SIGKILL after a grace period) once it appears — so permission prompts stay available while the agent works, and the workflow continues automatically when the stage is done.

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

### `orch status`

Lists the workflow runs recorded under `.orch/`, newest first, with each run's stage count and the verdict of its final review (`APPROVED`, `REJECTED`, or `-` when no review artifact exists). A missing `.orch/` is not an error; it reports that no runs exist yet.

```sh
orch status
```

```
RUN-ID            STAGES  VERDICT
20260921-180819        4  APPROVED
20260921-170242        4  APPROVED
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
| `1` | PTY or runtime error, or stage / verification failure during `orch build`. |
| `2` | Command-line usage error, unknown command or agent, missing flag argument, or multiple agents specified with `--` pass-through args. (`orch` with no arguments prints usage to `stderr` and exits with `2`). |
| `127` | Required agent binary not found on `$PATH`. |
| `>0` / `128+N` | Exit status propagated directly from the agent process, following standard shell conventions (or `128 + signal` if killed by signal). |
