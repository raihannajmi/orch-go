// Command orch runs coding agents in real pseudo-terminals.
//
// It supports agy, command-code, and copilot, sizing the pty to the current
// terminal and forwarding keystrokes verbatim so each agent's native UI and its
// own permission prompts behave exactly as they do when launched directly.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

const version = "0.1.0"

// errHelp lets flag parsing unwind back to a usage message.
var errHelp = errors.New("help requested")

func main() {
	os.Exit(orchMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// orchMain is main without the process, so the command line can be tested.
func orchMain(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}

	switch args[0] {
	case "list", "ls":
		return listAgents(stdout)
	case "run":
		return cmdRun(args[1:], stdin, stdout, stderr)
	case "build":
		return cmdBuild(args[1:], stdin, stdout, stderr)
	case "status":
		return cmdStatus(args[1:], stdout, stderr)
	case "logs":
		return cmdLogs(args[1:], stdout, stderr)
	case "resume":
		return cmdResume(args[1:], stdin, stdout, stderr)
	case "version", "-v", "--version":
		fmt.Fprintf(stdout, "orch %s\n", version)
		return 0
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "orch: unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
}

// runOptions is one `orch run` invocation.
type runOptions struct {
	dir    string   // working directory for the agents
	cont   bool     // resume each agent's most recent session
	prompt string   // first message for each agent
	agents []string // agent names, in the order they run
	extra  []string // args after `--`, handed to the agent untouched
}

func cmdRun(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	opts, err := parseRunArgs(args)
	if errors.Is(err, errHelp) {
		usage(stdout)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "orch: %v\n", err)
		return 2
	}
	if len(opts.agents) == 0 {
		fmt.Fprintln(stderr, "orch: run needs at least one agent (see `orch list`)")
		return 2
	}
	if len(opts.agents) > 1 && len(opts.extra) > 0 {
		fmt.Fprintln(stderr, "orch: pass-through args after `--` are only accepted for a single agent")
		return 2
	}

	// Resolve and validate every agent command line before touching the system.
	// Argument validation (the permission-bypass denylist) must not depend on the
	// agent being installed, so every plan entry is checked first: a forbidden
	// flag is rejected with exit 2 even when the binary is missing. Only once the
	// whole plan is valid do we require the binaries, so a missing one still fails
	// fast with 127 rather than after a session has already started.
	plan := make([]Agent, 0, len(opts.agents))
	argvs := make([][]string, 0, len(opts.agents))
	for _, name := range opts.agents {
		a, ok := lookupAgent(name)
		if !ok {
			fmt.Fprintf(stderr, "orch: unknown agent %q (see `orch list`)\n", name)
			return 2
		}
		argv, err := buildArgv(a, opts)
		if err != nil {
			fmt.Fprintf(stderr, "orch: %v\n", err)
			return 2
		}
		plan = append(plan, a)
		argvs = append(argvs, argv)
	}
	for _, a := range plan {
		if _, err := exec.LookPath(a.Bin); err != nil {
			fmt.Fprintf(stderr, "orch: %s is not on PATH; install %s first\n", a.Bin, a.Name)
			return 127
		}
	}

	// Agents run one after another, each owning the terminal. A non-zero exit
	// stops the chain and becomes orch's exit code.
	for i, a := range plan {
		if len(plan) > 1 {
			fmt.Fprintf(stderr, "orch: [%d/%d] %s\n", i+1, len(plan), a.Name)
		}

		code, err := Run(argvs[i], Options{Dir: opts.dir, Stdin: stdin, Stdout: stdout})
		if err != nil {
			fmt.Fprintf(stderr, "orch: %s: %v\n", a.Name, err)
			return 1
		}
		if code != 0 {
			return code
		}
	}
	return 0
}

// parseRunArgs reads orch's own flags, leaving agent names positional so that
// both `orch run agy -c` and `orch run -c agy` work. Everything after `--` goes
// to the agent untouched.
func parseRunArgs(args []string) (runOptions, error) {
	var opts runOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			opts.extra = append(opts.extra, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			opts.agents = append(opts.agents, arg)
			continue
		}

		name, value, hasValue := strings.Cut(arg, "=")
		var err error
		switch name {
		case "-h", "--help":
			return opts, errHelp
		case "-c", "--continue":
			opts.cont = true
		case "-C", "--dir":
			opts.dir, i, err = flagValue(args, i, name, value, hasValue)
		case "-p", "--prompt":
			opts.prompt, i, err = flagValue(args, i, name, value, hasValue)
		default:
			return opts, fmt.Errorf("unknown flag %q for run", arg)
		}
		if err != nil {
			return opts, err
		}
	}
	return opts, nil
}

// flagValue returns a flag's value along with the index to resume from,
// accepting both `--flag value` and `--flag=value`.
func flagValue(args []string, i int, name, value string, hasValue bool) (string, int, error) {
	if hasValue {
		return value, i, nil
	}
	if i+1 >= len(args) {
		return "", i, fmt.Errorf("%s needs a value", name)
	}
	return args[i+1], i + 1, nil
}

// listAgents prints the registry with installation status.
func listAgents(stdout io.Writer) int {
	width := 0
	for _, a := range agents {
		width = max(width, len(a.Name))
	}
	for _, a := range agents {
		status := "not found on PATH"
		if _, err := exec.LookPath(a.Bin); err == nil {
			status = "installed"
		}
		fmt.Fprintf(stdout, "%-*s  %-17s  %s\n", width, a.Name, status, a.Summary)
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprint(w, `orch - run coding agents in real pseudo-terminals, permissions untouched

Usage
  orch list                          list agents and whether they are installed
  orch run <agent> [agent...] [flags] run agents in the current terminal
  orch build "<task>" [flags]        plan, implement, review and verify a task
  orch status                        show the build runs under .orch/, newest first
  orch logs <run-id>                 list the artifacts of one run
  orch resume <run-id>               resume an interrupted run from its first incomplete stage
  orch version                       print the orch version
  orch help                          print this message

Examples
  orch list
  orch run agy -p "explain how the pty handoff works"
  orch run agy command-code -C ~/src/app
  orch build "add a --json flag to the report command"
  orch status
  orch logs 20260101-120000
  orch resume 20260101-120000

Agents
  agy           Antigravity CLI (native terminal UI)
  command-code  Command Code coding agent
  copilot       GitHub Copilot CLI

Flags for run
  -C, --dir <path>     working directory for the agents (default: current)
  -c, --continue       resume each agent's most recent session
  -p, --prompt <text>  start each agent interactively with this first message
  -- <args...>         extra args for the agent (single-agent runs only)

Flags for build
  -C, --dir <path>     repository the agents work in (default: current)
  --stage-timeout <d>  end a stage that makes no progress for <d> (default 30m, 0 disables)
  --verify <command>   verification command to run at the end (repeatable; default: auto-detect)
  --knowledge          load and capture durable knowledge (see Knowledge below)

Knowledge
  With --knowledge, orch loads relevant context before the run and records the
  approved outcome afterwards. It is off by default and never required. The
  backend is configured by environment:
    ORCH_KNOWLEDGE_VAULT     path to an Obsidian vault (required to enable)
    ORCH_KNOWLEDGE_PROJECT   project folder under 01-Projects/ (default: repo dir name)
    ORCH_KNOWLEDGE_GRAPHIFY  truthy to add the read-only Graphify enrichment layer
    ORCH_KNOWLEDGE_GRAPH     graph.json to query (default: <vault>/graphify-out/graph.json)
  Obsidian is the source of truth; Graphify only enriches context. Knowledge
  problems are warnings: a failure to load or capture never fails the build.

Verification
  Every build ends with verification commands run in the repository. They come
  from --verify when given (repeatable, run in order), otherwise from a recipe
  auto-detected from the repository: go.mod runs gofmt -l ., go vet ./... and
  go test ./...; Cargo.toml runs cargo check and cargo test; package.json runs
  npm run for the lint/check/typecheck/test scripts it already defines. A
  repository with none of these needs an explicit --verify: orch refuses to run
  a workflow it cannot verify. Commands run without a shell (no pipes,
  redirection, globbing or variable expansion), and a verification failure is
  reported and recorded separately from an agent failure.

Status and logs
  orch status lists each run under .orch/ with its stage count, the verdict
  of its final review, and the stage a watchdog ended if the run timed out.
  orch logs <run-id> lists that run's Markdown artifacts and raw terminal
  transcripts so you can read or open them; the run id is the directory name
  under .orch/ and the first column of orch status.

Resume
  orch resume <run-id> re-runs an interrupted or failed run from its first
  incomplete stage, skipping the stages whose artifacts already end with the
  completion marker. It refuses when the repository is not safe to resume (HEAD
  moved since the run started, or an unexpected dirty tree) and never resets,
  checks out or cleans the working tree. An implement stage that was interrupted
  is re-run as-is: any changes it already made to the repository are not rolled
  back. Runs recorded before run.json existed stay readable with status/logs but
  cannot be resumed.

Agents run one after another, each owning the terminal; if one exits non-zero
the rest are skipped and orch exits with that code.

Every agent gets its own pseudo-terminal, so its native UI and its own
permission prompts work as usual. orch adds no approval-skipping flags, and
refuses to pass through any you supply after --.

orch build runs agy to plan, command-code to review the plan, agy to implement,
then command-code to review the implementation, fixing and re-reviewing up to
three times until a review explicitly approves. Each stage is a normal
interactive session with its own permission prompts, and every stage's output
is saved under .orch/<run-id>/. Once a stage writes its completion marker,
orch ends that session and moves on, so a finished stage cannot stall the run.
A stage that stops making progress is ended by a per-stage watchdog (see
--stage-timeout) and the run stops there instead of waiting forever.
The run finishes with the verification commands (see Verification above),
auto-detected and overridable with --verify.
Only one build or resume runs per repository at a time; a second is refused.
Runtime artifacts under .orch/ are owner-only (dirs 0700, files 0600).
`)
}
