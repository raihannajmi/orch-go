package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestE2EBuildHelper is not a test: the end-to-end recovery test re-executes the
// test binary with this selector to run a real `orch build` in a subprocess.
func TestE2EBuildHelper(t *testing.T) {
	if os.Getenv("ORCH_E2E_HELPER") != "1" {
		t.Skip("helper process")
	}
	os.Exit(orchMain([]string{"build", "-C", os.Getenv("ORCH_E2E_REPO"), "e2e task", "--verify", "true"}, nil, os.Stdout, os.Stderr))
}

// sq single-quotes a path for embedding in a /bin/sh script.
func sq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// agyScript is a stand-in agy. It records the artifact it is asked to write,
// optionally blocks on the implement stage (so the workflow can be interrupted),
// and writes a complete artifact ending in the completion marker otherwise.
func agyScript(calls, block, marker, pidFile string) string {
	return fmt.Sprintf(`#!/bin/sh
out=$(printf '%%s\n' "$@" | grep -m1 -E '^[^[:space:]]*\.md$')
[ -n "$out" ] || exit 3
printf 'agy %%s\n' "$out" >> %s
%s
{
  echo "stand-in body"
  case "$out" in *-review.md) echo "VERDICT: APPROVED";; esac
  echo %s
} > "$out"
`, sq(calls), block, marker)
}

// ccScript is a stand-in command-code reviewer; it always approves.
func ccScript(calls, marker string) string {
	return fmt.Sprintf(`#!/bin/sh
out=$(printf '%%s\n' "$@" | grep -m1 -E '^[^[:space:]]*\.md$')
[ -n "$out" ] || exit 3
printf 'cc %%s\n' "$out" >> %s
{
  echo "stand-in body"
  case "$out" in *-review.md) echo "VERDICT: APPROVED";; esac
  echo %s
} > "$out"
`, sq(calls), marker)
}

func installAgent(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatalf("install %s: %v", name, err)
	}
}

func singleRunID(t *testing.T, stateDir string) string {
	t.Helper()
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	if len(ids) != 1 {
		t.Fatalf("run directories = %v, want exactly 1", ids)
	}
	return ids[0]
}

// TestE2EInterruptedBuildResumes kills a real `orch build` mid-implement, checks
// the recovery state, then resumes it to completion and verifies the resume
// re-entered at the interrupted stage without any destructive git operation.
func TestE2EInterruptedBuildResumes(t *testing.T) {
	repo := initGitRepo(t)
	base, err := gitHead(repo)
	if err != nil {
		t.Fatalf("gitHead: %v", err)
	}

	work := t.TempDir()
	marker := filepath.Join(work, "implement-started")
	pidFile := filepath.Join(work, "implement.pid")
	callsKill := filepath.Join(work, "calls-kill.log")
	callsResume := filepath.Join(work, "calls-resume.log")

	// The first phase blocks on implement; the resume phase never does.
	block := fmt.Sprintf("case \"$out\" in *-implement.md) echo $$ > %s; : > %s; sleep 600 ;; esac", sq(pidFile), sq(marker))
	binBlock := t.TempDir()
	installAgent(t, binBlock, "agy", agyScript(callsKill, block, stageMarker, pidFile))
	installAgent(t, binBlock, "command-code", ccScript(callsKill, stageMarker))

	binResume := t.TempDir()
	installAgent(t, binResume, "agy", agyScript(callsResume, "", stageMarker, pidFile))
	installAgent(t, binResume, "command-code", ccScript(callsResume, stageMarker))

	cmd := exec.Command(os.Args[0], "-test.run=TestE2EBuildHelper")
	cmd.Env = append(os.Environ(),
		"ORCH_E2E_HELPER=1",
		"ORCH_E2E_REPO="+repo,
		"PATH="+binBlock+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	killed := false
	defer func() {
		if !killed {
			_ = cmd.Process.Kill()
		}
		_, _ = cmd.Process.Wait()
	}()

	// Wait (deterministically, on a file) for the implement stage to start.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			killed = true
			t.Fatalf("the implement stage never started; helper stderr:\n%s", errBuf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Interrupt the workflow, then reap the agent it left behind.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_, _ = cmd.Process.Wait()
	killed = true
	if pid := readPID(t, pidFile); pid > 0 {
		// The agent leads its own session, so killing the group takes children.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}

	// The repository lock must be free: the kernel released it on death.
	stateDir := filepath.Join(repo, buildStateDir)
	lock, err := lockWorkflow(stateDir)
	if err != nil {
		t.Fatalf("the workflow lock was not released after the kill: %v", err)
	}
	_ = lock.Close()

	id := singleRunID(t, stateDir)
	runDir := filepath.Join(stateDir, id)

	st, err := readRunState(runDir)
	if err != nil {
		t.Fatalf("readRunState: %v", err)
	}
	if st.Status != runStateRunning {
		t.Errorf("status = %q, want running (interrupted)", st.Status)
	}
	if st.LastCompleted != 2 {
		t.Errorf("lastCompleted = %d, want 2 (plan and plan-review)", st.LastCompleted)
	}
	if artifactComplete(filepath.Join(runDir, "3-implement.md")) {
		t.Error("the interrupted implement artifact is marked complete")
	}
	ts, err := time.Parse(time.RFC3339, st.CreatedAt)
	if err != nil {
		t.Errorf("createdAt %q is not RFC3339: %v", st.CreatedAt, err)
	} else if _, off := ts.Zone(); off != 0 {
		t.Errorf("createdAt %q is not UTC (offset %d)", st.CreatedAt, off)
	}

	// orch must not have touched the repository's history or files.
	if head, err := gitHead(repo); err != nil || head != base {
		t.Errorf("git HEAD = %q (base %q, err %v); the interruption must not move it", head, base, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "README.md")); err != nil {
		t.Errorf("a tracked file disappeared, suggesting a destructive reset: %v", err)
	}

	// Resume in-process with agents that complete.
	t.Setenv("PATH", binResume+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Chdir(repo)

	var out, errb bytes.Buffer
	if code := orchMain([]string{"resume", id}, nil, &out, &errb); code != 0 {
		t.Fatalf("resume = %d, want 0 (stderr: %s)", code, errb.String())
	}

	final, err := readRunState(runDir)
	if err != nil {
		t.Fatalf("readRunState: %v", err)
	}
	if final.Status != runStateCompleted {
		t.Errorf("status = %q, want completed", final.Status)
	}
	if final.VerifyResult != verifyPassed {
		t.Errorf("verifyResult = %q, want passed (verification is explicit)", final.VerifyResult)
	}
	if _, err := os.Stat(filepath.Join(runDir, "verify.log")); err != nil {
		t.Errorf("verification did not run: %v", err)
	}

	// Resume must have re-entered at the interrupted stage: the completed
	// prefixes are replayed from disk, never re-launched.
	calls, err := os.ReadFile(callsResume)
	if err != nil {
		t.Fatalf("read resume call log: %v", err)
	}
	got := string(calls)
	for _, replayed := range []string{"1-plan.md", "2-plan-review.md"} {
		if strings.Contains(got, replayed) {
			t.Errorf("resume re-ran a completed stage (%s):\n%s", replayed, got)
		}
	}
	for _, want := range []string{"3-implement.md", "4-review.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("resume did not run %s:\n%s", want, got)
		}
	}

	// Still no destructive git operation after the resume.
	if head, err := gitHead(repo); err != nil || head != base {
		t.Errorf("git HEAD = %q after resume (base %q, err %v)", head, base, err)
	}
}
