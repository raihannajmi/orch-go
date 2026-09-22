package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// runInfo summarizes one `orch build` run recorded under .orch/.
type runInfo struct {
	id       string
	stages   int    // number of Markdown stage artifacts
	verdict  string // outcome of the last review, or "-" when there is none
	timedOut string // stage the watchdog ended, or "" when the run never timed out
}

// cmdStatus lists the workflow runs under .orch/, newest first.
func cmdStatus(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		if args[0] == "-h" || args[0] == "--help" {
			usage(stdout)
			return 0
		}
		fmt.Fprintln(stderr, "orch: status takes no arguments")
		return 2
	}

	runs, err := listRuns(buildStateDir)
	if err != nil {
		fmt.Fprintf(stderr, "orch: %v\n", err)
		return 1
	}
	if len(runs) == 0 {
		fmt.Fprintf(stdout, "orch: no runs under %s yet; start one with `orch build \"<task>\"`\n", buildStateDir)
		return 0
	}

	fmt.Fprintf(stdout, "%-16s  %6s  %-9s  %s\n", "RUN-ID", "STAGES", "VERDICT", "TIMED OUT")
	for _, r := range runs {
		timedOut := "-"
		if r.timedOut != "" {
			timedOut = r.timedOut
		}
		fmt.Fprintf(stdout, "%-16s  %6d  %-9s  %s\n", r.id, r.stages, r.verdict, timedOut)
	}
	return 0
}

// listRuns reads every run directory under stateDir, newest first. A missing
// state directory is not an error: it just means no run has happened yet.
func listRuns(stateDir string) ([]runInfo, error) {
	entries, err := os.ReadDir(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var runs []runInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		r, err := readRun(filepath.Join(stateDir, e.Name()), e.Name())
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	// Run ids are timestamps (YYYYMMDD-HHMMSS), so name order is time order.
	sort.Slice(runs, func(i, j int) bool { return runs[i].id > runs[j].id })
	return runs, nil
}

// readRun summarizes one run directory: how many stage artifacts it holds and
// the outcome of its last review.
func readRun(dir, id string) (runInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return runInfo{}, err
	}

	r := runInfo{id: id, verdict: "-"}
	lastReview := -1
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(name, ".md") {
			r.stages++
		}
		if strings.HasSuffix(name, ".timeout") && r.timedOut == "" {
			r.timedOut = strings.TrimSuffix(name, ".timeout")
		}
		if !strings.HasSuffix(name, "-review.md") {
			continue
		}
		// Later fix cycles add higher-numbered reviews; the last one is the
		// effective verdict, so keep the review with the largest stage number.
		if stage := stageNumber(name); stage > lastReview {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				return runInfo{}, err
			}
			lastReview = stage
			r.verdict = "REJECTED"
			if reviewApproved(string(b)) {
				r.verdict = "APPROVED"
			}
		}
	}
	return r, nil
}

// cmdLogs lists the artifacts a single run wrote: the Markdown reports and the
// raw terminal transcripts recorded alongside them.
func cmdLogs(args []string, stdout, stderr io.Writer) int {
	var id string
	for _, arg := range args {
		switch {
		case arg == "-h" || arg == "--help":
			usage(stdout)
			return 0
		case strings.HasPrefix(arg, "-") && arg != "-":
			fmt.Fprintf(stderr, "orch: unknown flag %q for logs\n", arg)
			return 2
		case id == "":
			id = arg
		default:
			fmt.Fprintln(stderr, "orch: logs takes a single run id")
			return 2
		}
	}
	if id == "" {
		fmt.Fprintln(stderr, "orch: logs needs a run id, e.g. orch logs 20260101-120000")
		return 2
	}
	if err := validRunID(id); err != nil {
		fmt.Fprintf(stderr, "orch: %v\n", err)
		return 2
	}

	runDir := filepath.Join(buildStateDir, id)
	info, err := os.Stat(runDir)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "orch: unknown run %q (see `orch status`)\n", id)
		return 2
	}

	arts, err := runArtifacts(runDir)
	if err != nil {
		fmt.Fprintf(stderr, "orch: %v\n", err)
		return 1
	}
	if len(arts) == 0 {
		fmt.Fprintf(stdout, "orch: run %s has no artifacts\n", id)
		return 0
	}

	width := 0
	for _, a := range arts {
		width = max(width, len(a.name))
	}
	fmt.Fprintf(stdout, "artifacts in %s:\n", runDir)
	for _, a := range arts {
		fmt.Fprintf(stdout, "  %-*s  %8s\n", width, a.name, humanSize(a.size))
	}
	return 0
}

// validRunID rejects anything that is not a single directory name, so a run id
// can never escape .orch/ (no separators, no "..").
func validRunID(id string) error {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("invalid run id %q", id)
	}
	return nil
}

// artifact is one file inside a run directory.
type artifact struct {
	name string
	size int64
}

// runArtifacts lists a run's files in stage order: 1-plan.md before
// 2-plan-review.md before 10-fix.md, with unnumbered files (verify.log) last.
func runArtifacts(dir string) ([]artifact, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	arts := make([]artifact, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		arts = append(arts, artifact{name: e.Name(), size: info.Size()})
	}
	sort.Slice(arts, func(i, j int) bool {
		si, sj := stageNumber(arts[i].name), stageNumber(arts[j].name)
		if si != sj {
			switch {
			case si == 0:
				return false // unnumbered files sort last
			case sj == 0:
				return true
			default:
				return si < sj
			}
		}
		return arts[i].name < arts[j].name
	})
	return arts, nil
}

// stageNumber extracts the numeric prefix of a stage artifact name
// ("10-fix.md" -> 10), returning 0 for names without one.
func stageNumber(name string) int {
	i := strings.IndexByte(name, '-')
	if i <= 0 {
		return 0
	}
	n, err := strconv.Atoi(name[:i])
	if err != nil {
		return 0
	}
	return n
}

// humanSize renders a byte count compactly for the artifact listing.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
