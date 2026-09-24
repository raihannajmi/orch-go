# Security Policy

## Supported versions

orch is pre-1.0. Security fixes are made on `main` and released as a new tag;
please report against, and update to, the latest `main`.

## Reporting a vulnerability

Please report privately through GitHub's "Report a vulnerability" button on the
repository's **Security** tab rather than in a public issue. Include the orch
version (`orch version`), your OS and architecture, and a minimal reproduction.

## What orch guarantees

- **No permission bypass.** orch never adds an approval-skipping flag and refuses
  to pass one through from the command line (see `agent.go`). Each agent runs in
  a real PTY with its own native permission prompts.
- **No shell.** Agent prompts, verification commands and the graphify query are
  executed as argv, never through a shell, so there is no shell-injection surface.
- **Arguments are deny-checked before launch**, and that check runs even when the
  agent binary is not installed.
- **Runtime artifacts are private.** `.orch/` and new run directories are `0700`;
  state, lock files, transcripts and artifacts are `0600`. A symlinked `.orch` is
  refused so an untrusted repository cannot redirect those writes.
- **No destructive git.** orch never resets, checks out, cleans or stashes; resume
  refuses rather than touching an unexpected working tree.

## Threat model and known exposure

orch is a local developer tool: it assumes the user's own machine and repository.
It is not a sandbox and does not isolate agents from the repository.

- **The command line is visible in the process list.** A stage prompt (task text
  plus any inlined knowledge) is passed to the agent as a command-line argument,
  so other local users can see it via `ps` while a stage runs. Avoid putting
  secrets in a task if that matters on your machine.
- **Transcripts contain agent output.** `<stage>.log` records the agent's terminal
  output, which can echo the prompt and anything the agent prints. Transcripts are
  `0600` and bounded (`ORCH_MAX_LOG_BYTES`, default 64 MiB).
- **Trusted inputs.** `--verify` commands, the `ORCH_KNOWLEDGE_*` configuration and
  the Graphify CLI are supplied by the operator and trusted. Task text and loaded
  knowledge are handled as untrusted data (fenced, never executed), but that is a
  prompt boundary, not a sandbox.
- **Same-user filesystem.** Lock and state files live inside a `0700` run
  directory; orch does not defend against an attacker who already has the user's
  filesystem access.

## Dependencies

Dependencies and the standard library are scanned in CI with
`go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...`, which fails on known
vulnerabilities the code is affected by. The module's minimum Go version is kept
at the release that fixes the standard-library advisory the scanner reports for
this code, so an older toolchain is upgraded rather than built against it.
