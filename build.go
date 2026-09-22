package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The `orch build` workflow runs a fixed sequence of interactive agent stages,
// each owning the terminal with its native UI and permission prompts intact:
//
//	1 agy          plan
//	2 command-code review the plan
//	3 agy          implement (context: plan + plan review)
//	4 command-code review the implementation
//	   ...repeat fix (agy) + review up to maxBuildCycles times
//	8 orch         final verification (gofmt, go vet, go test)
//
// Each stage writes a Markdown artifact under .orch/<run-id>/ and its raw
// terminal transcript alongside as <stage>.log. A review approves only when its
// artifact ends with a `VERDICT: APPROVED` line; orch never invents a verdict.
const (
	buildPlanAgent   = "agy"
	buildReviewAgent = "command-code"
	buildPlanCycles  = 3 // implementation/review cycles before orch gives up
	buildStateDir    = ".orch"

	// buildContextLimit caps how much of each prior artifact is inlined into the
	// next stage's prompt; the full file is always referenced by path.
	buildContextLimit = 8000

	// stageMarker is the explicit completion signal every stage writes as the
	// final line of its artifact. An interactive agent finishes its work but
	// stays at its prompt, so orch watches the artifact for this line and ends
	// the session once it lands instead of waiting on the agent to quit. It is
	// part of the artifact protocol, not an instruction the agent may ignore:
	// without it orch cannot tell "still working" from "done and idle".
	stageMarker = "ORCH_STAGE_COMPLETE"
	// stagePollInterval is how often orch re-reads the artifact for the marker.
	stagePollInterval = 200 * time.Millisecond

	// defaultStageTimeout is the per-stage watchdog. A stage that neither writes
	// its completion marker nor exits within this window is ended and the run is
	// marked timed out, so a wedged agent cannot stall the workflow forever.
	// Override with --stage-timeout; 0 disables the watchdog.
	defaultStageTimeout = 30 * time.Minute
)

// doneInstruction is appended to every stage prompt. It defines the completion
// signal the workflow depends on and tells the agent not to quit the session
// itself, since orch closes it once the marker is detected.
const doneInstruction = `

When, and only when, everything above is finished, write ` + stageMarker + ` on a line by itself as the very last line of that file. orch watches the file for that line and ends this session as soon as it appears, so write it only after all other work is done. Do not exit the session yourself.`

// buildOptions is one `orch build` invocation.
type buildOptions struct {
	task         string        // the task text, joined from the positional arguments
	dir          string        // repository the agents work in (default: current directory)
	stageTimeout time.Duration // per-stage watchdog; 0 disables it
}

func cmdBuild(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	opts, err := parseBuildArgs(args)
	if errors.Is(err, errHelp) {
		usage(stdout)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "orch: %v\n", err)
		return 2
	}
	if opts.task == "" {
		fmt.Fprintln(stderr, `orch: build needs a task, e.g. orch build "add a --json flag"`)
		return 2
	}

	dir := opts.dir
	if dir == "" {
		dir = "."
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(stderr, "orch: %v\n", err)
		return 2
	}

	// Resolve the agents before creating any state, so a missing binary fails
	// fast rather than after a partial run.
	for _, name := range []string{buildPlanAgent, buildReviewAgent} {
		a, ok := lookupAgent(name)
		if !ok {
			fmt.Fprintf(stderr, "orch: agent %q is not registered\n", name)
			return 2
		}
		if _, err := exec.LookPath(a.Bin); err != nil {
			fmt.Fprintf(stderr, "orch: %s is not on PATH; install %s first\n", a.Bin, a.Name)
			return 127
		}
	}

	runDir := filepath.Join(absDir, buildStateDir, time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "orch: %v\n", err)
		return 1
	}

	b := &builder{
		dir:    absDir,
		runDir: runDir,
		stdout: stdout,
		stderr: stderr,
		stage:  interactiveStage(absDir, runDir, stdin, stdout, opts.stageTimeout),
	}
	b.verify = b.verifyRepo

	b.logf("artifacts: %s", runDir)
	if opts.stageTimeout > 0 {
		b.logf("stage timeout: %s", opts.stageTimeout)
	} else {
		b.logf("stage timeout: disabled")
	}
	if err := b.build(opts.task); err != nil {
		fmt.Fprintf(stderr, "orch: build: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "orch: build complete; artifacts in %s\n", runDir)
	return 0
}

// parseBuildArgs reads orch's flags; the remaining positional arguments are
// joined into the task so both a single quoted task and bare words work.
func parseBuildArgs(args []string) (buildOptions, error) {
	opts := buildOptions{stageTimeout: defaultStageTimeout}
	var words []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			words = append(words, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			words = append(words, arg)
			continue
		}

		name, value, hasValue := strings.Cut(arg, "=")
		var err error
		switch name {
		case "-h", "--help":
			return opts, errHelp
		case "-C", "--dir":
			opts.dir, i, err = flagValue(args, i, name, value, hasValue)
		case "--stage-timeout":
			var raw string
			raw, i, err = flagValue(args, i, name, value, hasValue)
			if err == nil {
				opts.stageTimeout, err = parseStageTimeout(raw)
			}
		default:
			return opts, fmt.Errorf("unknown flag %q for build", arg)
		}
		if err != nil {
			return opts, err
		}
	}
	opts.task = strings.TrimSpace(strings.Join(words, " "))
	return opts, nil
}

// parseStageTimeout parses the per-stage watchdog duration. A zero duration is
// valid and disables the watchdog; a negative one is a usage error.
func parseStageTimeout(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid --stage-timeout %q: %w", s, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid --stage-timeout %q: must not be negative", s)
	}
	return d, nil
}

// builder drives one build run. The agent session and the verification pass are
// fields so the workflow's sequencing can be tested without launching agents.
type builder struct {
	dir    string // absolute repository directory for the agents and the checks
	runDir string // absolute artifact directory, .orch/<run-id>
	stdout io.Writer
	stderr io.Writer

	// stage runs one interactive agent stage and returns the text of the
	// artifact the agent wrote.
	stage func(agentName, name, prompt string) (string, error)
	// verify runs the final gofmt / go vet / go test pass.
	verify func() error

	n int // stage counter, used to number artifacts
}

func (b *builder) logf(format string, args ...any) {
	fmt.Fprintf(b.stderr, "orch: "+format+"\n", args...)
}

// artifactPath numbers and names the next stage artifact.
func (b *builder) artifactPath(label string) (name, path string) {
	b.n++
	name = fmt.Sprintf("%d-%s", b.n, label)
	return name, filepath.Join(b.runDir, name+".md")
}

func (b *builder) run(agentName, name, prompt string) (string, error) {
	b.logf("stage %s: %s", name, agentName)
	text, err := b.stage(agentName, name, prompt)
	if err != nil {
		return "", fmt.Errorf("stage %s (%s): %w", name, agentName, err)
	}
	return text, nil
}

// build performs the full plan → review → implement → review → fix workflow and
// ends with verification. The implementation is accepted only when a review
// explicitly approves; otherwise orch runs at most buildPlanCycles cycles and
// fails without pretending the work is done.
func (b *builder) build(task string) error {
	planName, planPath := b.artifactPath("plan")
	planText, err := b.run(buildPlanAgent, planName, planPrompt(task, b.dir, planPath))
	if err != nil {
		return err
	}
	plan := contextDoc{"plan", planPath, planText}

	planReviewName, planReviewPath := b.artifactPath("plan-review")
	planReviewText, err := b.run(buildReviewAgent, planReviewName, planReviewPrompt(task, b.dir, planReviewPath, plan))
	if err != nil {
		return err
	}
	planReview := contextDoc{"plan review", planReviewPath, planReviewText}

	approved := false
	var lastReview contextDoc
	for cycle := 1; cycle <= buildPlanCycles; cycle++ {
		var summaryName, summaryPath string
		var summaryText string
		if cycle == 1 {
			summaryName, summaryPath = b.artifactPath("implement")
			summaryText, err = b.run(buildPlanAgent, summaryName, implementPrompt(task, b.dir, summaryPath, plan, planReview))
		} else {
			summaryName, summaryPath = b.artifactPath("fix")
			summaryText, err = b.run(buildPlanAgent, summaryName, fixPrompt(task, b.dir, summaryPath, plan, lastReview))
		}
		if err != nil {
			return err
		}
		summary := contextDoc{"implementation summary", summaryPath, summaryText}

		reviewName, reviewPath := b.artifactPath("review")
		reviewText, err := b.run(buildReviewAgent, reviewName, reviewPrompt(task, b.dir, reviewPath, plan, summary))
		if err != nil {
			return err
		}
		lastReview = contextDoc{"previous review", reviewPath, reviewText}

		if reviewApproved(reviewText) {
			b.logf("review cycle %d: approved", cycle)
			approved = true
			break
		}
		b.logf("review cycle %d: changes requested", cycle)
	}
	if !approved {
		return fmt.Errorf("implementation was not approved after %d review cycles", buildPlanCycles)
	}

	return b.verify()
}

// verdictPattern matches the reviewer's machine-readable verdict line.
var verdictPattern = regexp.MustCompile(`(?im)^\s*VERDICT:\s*(APPROVED|REJECTED)\b`)

// reviewApproved reports whether a review artifact explicitly approves the work.
// A missing verdict is not an approval.
func reviewApproved(review string) bool {
	matches := verdictPattern.FindAllStringSubmatch(review, -1)
	if len(matches) == 0 {
		return false
	}
	return strings.EqualFold(matches[len(matches)-1][1], "APPROVED")
}

// errStageTimeout marks a stage the watchdog had to end. It is a distinct
// sentinel so callers can tell a liveness failure from an agent error, and the
// build workflow can report the run as timed out.
var errStageTimeout = errors.New("stage timed out")

// interactiveStage is the production stage runner: it launches the agent on a
// real pty, tees the terminal transcript to <name>.log, then reads back the
// Markdown artifact the agent was asked to write.
//
// The agent does its work and writes the artifact, but an interactive agent
// keeps its session open at the prompt afterwards, so Run would block forever.
// orch instead watches the artifact for the stage's completion marker and ends
// the session once it appears; that keeps native permission prompts in place
// while the agent works and only stops it after the stage is actually done.
//
// A stage that neither completes nor exits is the other way to block forever —
// a wedged prompt, a hang, an agent that quietly stopped working. The watchdog
// (timeout > 0) ends that session too, marks the stage timed out on disk, and
// returns errStageTimeout, which aborts the run so no later stage starts.
func interactiveStage(dir, runDir string, stdin *os.File, stdout io.Writer, timeout time.Duration) func(string, string, string) (string, error) {
	return func(agentName, name, prompt string) (string, error) {
		a, ok := lookupAgent(agentName)
		if !ok {
			return "", fmt.Errorf("unknown agent %q", agentName)
		}
		argv, err := buildArgv(a, runOptions{prompt: prompt})
		if err != nil {
			return "", err
		}

		logPath := filepath.Join(runDir, name+".log")
		log, err := os.Create(logPath)
		if err != nil {
			return "", err
		}
		defer func() { _ = log.Close() }()

		artifact := filepath.Join(runDir, name+".md")

		// The artifact watcher and the watchdog both want to end the session;
		// endOnce makes sure the Stop channel is closed exactly once whichever
		// fires first. completed/timedOut record which reason won so the exit
		// code can be interpreted correctly afterwards.
		stop := make(chan struct{})
		sessionDone := make(chan struct{})
		completed := make(chan struct{})
		timedOut := make(chan struct{})

		var endOnce sync.Once
		endSession := func() { endOnce.Do(func() { close(stop) }) }

		go func() {
			if waitForArtifact(artifact, sessionDone) {
				close(completed)
			}
			endSession()
		}()

		if timeout > 0 {
			go func() {
				timer := time.NewTimer(timeout)
				defer timer.Stop()
				select {
				case <-timer.C:
					close(timedOut)
					endSession()
				case <-sessionDone:
				}
			}()
		}

		code, err := Run(argv, Options{
			Dir:    dir,
			Stdin:  stdin,
			Stdout: io.MultiWriter(stdout, log),
			Stop:   stop,
		})
		close(sessionDone)
		if err != nil {
			return "", err
		}

		select {
		case <-completed:
			// orch ended the session once the artifact was complete.
		case <-timedOut:
			// Best effort: the timeout error below is the outcome that matters,
			// and a missing marker must not mask it.
			_ = os.WriteFile(timeoutMarkerPath(runDir, name),
				[]byte(fmt.Sprintf("%s did not finish within %s\n", name, timeout)), 0o644)
			return "", fmt.Errorf("stage %s (%s) did not finish within %s: %w", name, agentName, timeout, errStageTimeout)
		default:
			if code != 0 {
				return "", fmt.Errorf("exited with code %d", code)
			}
		}

		text, err := os.ReadFile(artifact)
		if err != nil {
			return "", fmt.Errorf("expected the %s stage to write %s: %w", name, artifact, err)
		}
		return stripStageMarker(string(text)), nil
	}
}

// timeoutMarkerPath is where a timed-out stage records that it was ended by the
// watchdog, so `orch status` can report the run as timed out.
func timeoutMarkerPath(runDir, name string) string {
	return filepath.Join(runDir, name+".timeout")
}

// waitForArtifact polls the stage artifact until it ends with the completion
// marker, reporting true when it does. It reports false if the session ends
// first, so it never signals a stage that died before finishing.
func waitForArtifact(path string, sessionDone <-chan struct{}) bool {
	ticker := time.NewTicker(stagePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-sessionDone:
			return false
		case <-ticker.C:
			if artifactComplete(path) {
				return true
			}
		}
	}
}

// artifactComplete reports whether the artifact exists and its final content is
// the completion marker. Requiring the marker to be last means a partially
// written artifact is never mistaken for a finished one.
func artifactComplete(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.TrimSpace(string(b)), stageMarker)
}

// stripStageMarker removes the trailing completion marker so later stages see
// only the artifact's real content.
func stripStageMarker(s string) string {
	trimmed := strings.TrimSpace(s)
	if strings.HasSuffix(trimmed, stageMarker) {
		return strings.TrimSpace(strings.TrimSuffix(trimmed, stageMarker))
	}
	return trimmed
}

// verifyRepo runs the mandated final checks in the repository and fails if any
// of them report a problem.
func (b *builder) verifyRepo() error {
	type check struct {
		name    string
		argv    []string
		emptyOK bool // fail when the command produces output (gofmt -l)
	}
	checks := []check{
		{name: "gofmt", argv: []string{"gofmt", "-l", "."}, emptyOK: true},
		{name: "go vet", argv: []string{"go", "vet", "./..."}},
		{name: "go test", argv: []string{"go", "test", "./..."}},
	}

	logPath := filepath.Join(b.runDir, "verify.log")
	log, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	out := io.MultiWriter(b.stdout, log)

	var failures []string
	for _, c := range checks {
		b.logf("verify: %s", strings.Join(c.argv, " "))
		cmd := exec.Command(c.argv[0], c.argv[1:]...)
		cmd.Dir = b.dir
		output, runErr := cmd.CombinedOutput()
		_, _ = out.Write(output)
		if n := len(output); n > 0 && output[n-1] != '\n' {
			_, _ = out.Write([]byte("\n"))
		}

		switch {
		case c.emptyOK && strings.TrimSpace(string(output)) != "":
			failures = append(failures, c.name+" reported files that need formatting")
		case !c.emptyOK && runErr != nil:
			failures = append(failures, fmt.Sprintf("%s failed: %v", c.name, runErr))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("verification failed: %s", strings.Join(failures, "; "))
	}
	b.logf("verification passed")
	return nil
}

// contextDoc is one prior artifact handed to the next stage.
type contextDoc struct {
	label string
	path  string
	body  string
}

// render inlines each document, truncated to keep the prompt within argv limits,
// while always naming the full file on disk.
func render(docs ...contextDoc) string {
	var sb strings.Builder
	for _, d := range docs {
		fmt.Fprintf(&sb, "\n--- %s: %s ---\n%s\n", d.label, d.path, excerpt(d.body))
	}
	return sb.String()
}

func excerpt(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= buildContextLimit {
		return s
	}
	return strings.ToValidUTF8(s[:buildContextLimit], "") + "\n... (truncated; read the full file for the rest)"
}

// The stage prompts all share the task, repository, inherited context, and the
// requirement to write a file; each adds the instructions specific to its role.

func planPrompt(task, dir, out string) string {
	return fmt.Sprintf(`You are the PLANNING stage of an orch build workflow.

Task:
%s

Repository: %s

Investigate the repository and produce a concise implementation plan for the task above. Do not modify any files in this stage.

Write the plan as Markdown to:
%s`, task, dir, out) + doneInstruction
}

func planReviewPrompt(task, dir, out string, plan contextDoc) string {
	return fmt.Sprintf(`You are the PLAN REVIEW stage of an orch build workflow. Review the plan critically before any code is written.

Task:
%s

Repository: %s
%s
Check the plan against the actual repository and the task. Note gaps, wrong assumptions, and risky steps.

Write your review as Markdown to:
%s`, task, dir, render(plan), out) + doneInstruction
}

func implementPrompt(task, dir, out string, plan, planReview contextDoc) string {
	return fmt.Sprintf(`You are the IMPLEMENTATION stage of an orch build workflow. Implement the task in the repository now.

Task:
%s

Repository: %s
%s
Make the actual code changes the task requires, following the plan and addressing the plan review.

Write a short Markdown summary of what you changed and why to:
%s`, task, dir, render(plan, planReview), out) + doneInstruction
}

func reviewPrompt(task, dir, out string, plan, summary contextDoc) string {
	return fmt.Sprintf(`You are the IMPLEMENTATION REVIEW stage of an orch build workflow. Verify the work that was just done.

Task:
%s

Repository: %s
%s
Inspect the real repository and confirm whether the task is fully and correctly implemented. Do not take the summary's word for it.

Write your review as Markdown to:
%s

End the review with a final line in exactly one of these forms:

VERDICT: APPROVED
VERDICT: REJECTED

Choose APPROVED only when the task is complete and correct with no required changes. Otherwise choose REJECTED and list the specific changes required.`, task, dir, render(plan, summary), out) + doneInstruction
}

func fixPrompt(task, dir, out string, plan, review contextDoc) string {
	return fmt.Sprintf(`You are the FIX stage of an orch build workflow. A review rejected the previous implementation.

Task:
%s

Repository: %s
%s
Address every point raised by the review, then re-verify your own changes.

Write a short Markdown summary of the fixes to:
%s`, task, dir, render(plan, review), out) + doneInstruction
}
