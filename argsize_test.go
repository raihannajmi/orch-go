package main

import (
	"strings"
	"testing"
)

// TestCheckArgvSize covers the OS argument bound: a normal command passes, one
// oversized argument or an oversized total is refused.
func TestCheckArgvSize(t *testing.T) {
	if err := checkArgvSize([]string{"agy", "--prompt-interactive", "hello"}); err != nil {
		t.Errorf("normal command rejected: %v", err)
	}
	// Exactly at the single-argument limit is allowed.
	if err := checkArgvSize([]string{strings.Repeat("z", maxArgBytes)}); err != nil {
		t.Errorf("argument exactly at the limit rejected: %v", err)
	}
	// One byte over is refused, with an actionable message.
	err := checkArgvSize([]string{strings.Repeat("z", maxArgBytes+1)})
	if err == nil {
		t.Fatal("oversized single argument accepted")
	}
	if !strings.Contains(err.Error(), "OS limit") {
		t.Errorf("err = %q, want it to mention the OS limit", err)
	}
	// A command line over the total limit is refused even when no single
	// argument is.
	var many []string
	for i := 0; i < 6; i++ {
		many = append(many, strings.Repeat("y", 100*1024))
	}
	if err := checkArgvSize(many); err == nil {
		t.Error("oversized command line accepted")
	}
}

// TestBuildArgvRejectsOversizedPrompt covers the user-facing path: an oversized
// prompt is refused before the agent is launched, and the task is never silently
// truncated.
func TestBuildArgvRejectsOversizedPrompt(t *testing.T) {
	a, ok := lookupAgent("agy")
	if !ok {
		t.Fatal("agy is not registered")
	}

	if _, err := buildArgv(a, runOptions{prompt: "a normal prompt"}); err != nil {
		t.Fatalf("normal prompt rejected: %v", err)
	}

	huge := strings.Repeat("x", maxArgBytes+1)
	_, err := buildArgv(a, runOptions{prompt: huge})
	if err == nil {
		t.Fatal("oversized prompt accepted")
	}
	if !strings.Contains(err.Error(), "OS limit") {
		t.Errorf("err = %q, want it to mention the OS limit", err)
	}

	// Security validation still runs first: a forbidden flag is reported even
	// when the prompt is also oversized.
	_, err = buildArgv(a, runOptions{prompt: huge, extra: []string{"--yolo"}})
	if err == nil || !strings.Contains(err.Error(), "auto-approve") {
		t.Errorf("err = %q, want the permission refusal to take precedence", err)
	}
}
