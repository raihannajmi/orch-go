package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestStateWriteFailureAbortsWorkflow covers the persistence policy: run.json is
// the recovery state a resume depends on, so a failed write aborts the workflow
// instead of letting it continue and later misreport a stage as complete.
func TestStateWriteFailureAbortsWorkflow(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions are not enforced")
	}

	runDir := t.TempDir()
	b := &builder{
		dir:    t.TempDir(),
		runDir: runDir,
		stdout: io.Discard,
		stderr: io.Discard,
		state:  &runState{Status: runStateRunning, Stages: []stageState{}},
	}
	stageCalled := false
	b.stage = func(agentName, name, prompt string) (string, error) {
		stageCalled = true
		return name + " body", nil
	}
	b.verify = func() error { return nil }

	if err := os.Chmod(runDir, 0o500); err != nil {
		t.Fatalf("chmod run dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(runDir, 0o700) })

	err := b.build("task")
	if err == nil {
		t.Fatal("build succeeded despite an unwritable state directory")
	}
	if !errors.Is(err, errStateWrite) {
		t.Errorf("err = %v, want it to wrap errStateWrite", err)
	}
	if stageCalled {
		t.Error("a stage ran even though its transition could not be persisted")
	}
	if _, statErr := os.Stat(filepath.Join(runDir, runStateFile)); statErr == nil {
		t.Error("run.json exists despite the failed write; a resume could trust it")
	}
}

// TestClassifyFailure pins the failure taxonomy: agent (with its exit code),
// timeout, verification, state and generic workflow.
func TestClassifyFailure(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantKind string
		wantCode int
	}{
		{"nil", nil, "", 0},
		{"agent", &agentExitError{Agent: "agy", Code: 7}, failureAgent, 7},
		{"wrapped agent", fmt.Errorf("stage %s: %w", "3-implement", &agentExitError{Agent: "agy", Code: 3}), failureAgent, 3},
		{"timeout", errStageTimeout, failureTimeout, 0},
		{"verify", fmt.Errorf("%w: go test failed", errVerifyFailed), failureVerify, 0},
		{"state", fmt.Errorf("%w: disk full", errStateWrite), failureState, 0},
		{"other", errors.New("boom"), failureWorkflow, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, code := classifyFailure(tt.err)
			if kind != tt.wantKind || code != tt.wantCode {
				t.Errorf("classifyFailure = %q, %d; want %q, %d", kind, code, tt.wantKind, tt.wantCode)
			}
		})
	}
}
