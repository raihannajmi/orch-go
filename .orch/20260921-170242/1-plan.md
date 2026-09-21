# Implementation Plan: README.md for orch

## Objective
Add a concise, comprehensive `README.md` to the root of the repository (`/Users/najmiraihan/Developer/orch-go/README.md`) documenting the `orch` CLI commands, usage patterns, and the three supported coding agents (`agy`, `command-code`, `copilot`), adhering strictly to the constraint of not modifying any Go source files.

---

## 1. Scope & Constraints
- **Target File**: `README.md` (root directory).
- **Zero Go Changes**: Do not touch or modify any `*.go` files.
- **Accuracy**: Base documentation directly on actual implementation in `main.go`, `agent.go`, and `build.go`.
- **Tone & Style**: Direct, clean, minimal (Ponytail style).

---

## 2. Content Structure for `README.md`

### 2.1 Header & Overview
- What `orch` is: A minimal Go CLI orchestrator that runs coding agents in real pseudo-terminals (PTYs), preserving native terminal UIs and interactive permission prompts verbatim.
- Key philosophy:
  - Real PTY allocation and raw mode terminal handling.
  - Zero permission bypass: does not pass `--dangerously-skip-permissions`, `--yolo`, `--auto-accept`, `--trust`, etc.
  - Sequential execution: agents run one after another, each owning the terminal.

### 2.2 Supported Agents
Document the three supported agents and their CLI integrations:
1. **`agy`**: Google Antigravity CLI (native terminal UI).
   - Binary: `agy`
   - Prompt flag: `--prompt-interactive <text>`
   - Continue flag: `--continue`
2. **`command-code`**: Command Code coding agent.
   - Binary: `command-code`
   - Prompt: Positional argument
   - Continue flag: `--continue`
   - Denied flags: `--trust`, `-t`
3. **`copilot`**: GitHub Copilot CLI.
   - Binary: `copilot`
   - Prompt flag: `--interactive <text>`
   - Continue flag: `--continue`
- Common denied flags across all agents: `--dangerously-skip-permissions`, `--yolo`, `--auto-accept`, `--tools-all`, `--permission-mode auto-accept|accept-all`, `--mode accept-edits`.

### 2.3 Installation & Building
- Prerequisites: Go 1.22+, macOS (Darwin PTY implementation), agent binaries on `$PATH`.
- Build instructions:
  ```sh
  go build -o orch .
  ```

### 2.4 CLI Commands Reference
Document syntax, options, and behavior for all commands:

1. **`orch list` (alias: `orch ls`)**
   - Lists registered agents, their installation status on `$PATH`, and descriptions.

2. **`orch run <agent> [agent...] [flags]`**
   - Runs one or more agents sequentially in the current terminal.
   - Flags:
     - `-C, --dir <path>`: Working directory for agents (default: current directory).
     - `-c, --continue`: Resume each agent's most recent session.
     - `-p, --prompt <text>`: Initial prompt to start the session interactively.
     - `-- <args...>`: Raw pass-through flags handed to the agent (single-agent invocations only; permission-bypass flags strictly blocked).
   - Behavior: If an agent exits non-zero, execution halts immediately and `orch` exits with that status code.

3. **`orch build "<task>" [flags]`**
   - Automated multi-agent lifecycle orchestrating `agy` and `command-code`:
     1. Stage 1: `agy` plans the task.
     2. Stage 2: `command-code` reviews the plan.
     3. Stage 3: `agy` implements the task based on plan and review.
     4. Stage 4: `command-code` reviews implementation (repeats fix/review loop up to 3 cycles until `VERDICT: APPROVED`).
     5. Final Stage: Runs verification (`gofmt`, `go vet ./...`, `go test ./...`).
   - Flags:
     - `-C, --dir <path>`: Repository directory where agents work (default: current directory).
   - Artifacts: Stored under `.orch/<timestamp>/` containing Markdown documents (`*-plan.md`, `*-review.md`, etc.) and raw terminal logs (`*.log`).

4. **`orch version`** (`-v`, `--version`)
   - Prints version (`orch 0.1.0`).

5. **`orch help`** (`-h`, `--help`)
   - Displays CLI usage and flag help.

---

## 3. Implementation Steps (for Implementation Stage)
1. Draft `/Users/najmiraihan/Developer/orch-go/README.md` following the structure above.
2. Review markdown formatting and link syntax.
3. Validate that no `.go` files were altered (`git status --porcelain`).
4. Run project checks (`go vet ./...` and `go test ./...`) to ensure repository integrity remains 100% green.

---

## 4. Verification Checklist
- [ ] `README.md` exists and is readable.
- [ ] All 5 commands documented (`list`/`ls`, `run`, `build`, `version`, `help`).
- [ ] All 3 agents documented (`agy`, `command-code`, `copilot`).
- [ ] Permission philosophy and safety guarantees clearly stated.
- [ ] No changes to Go files (`git diff --stat *.go` produces no output).
- [ ] `go test ./...` passes.
