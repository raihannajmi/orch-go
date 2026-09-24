package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestReadOnlyCommandFlags pins the flag surface of the read-only commands:
// help and --json are accepted, anything else is a usage error.
func TestReadOnlyCommandFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"list help short", []string{"list", "-h"}, 0},
		{"list help long", []string{"ls", "--help"}, 0},
		{"list json", []string{"list", "--json"}, 0},
		{"list unknown arg", []string{"list", "bogus"}, 2},
		{"list json unknown arg", []string{"list", "--json", "bogus"}, 2},
		{"status help", []string{"status", "-h"}, 0},
		{"status json", []string{"status", "--json"}, 0},
		{"status extra arg", []string{"status", "bogus"}, 2},
		{"logs help", []string{"logs", "-h"}, 0},
		{"logs unknown flag", []string{"logs", "--nope"}, 2},
		{"logs missing id", []string{"logs"}, 2},
		{"resume help", []string{"resume", "-h"}, 0},
		{"version", []string{"version"}, 0},
		{"help", []string{"help"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// status/logs read .orch/ under the working directory; an empty temp
			// dir keeps that deterministic.
			t.Chdir(t.TempDir())
			var out, errb bytes.Buffer
			if got := orchMain(tt.args, nil, &out, &errb); got != tt.want {
				t.Errorf("orchMain(%q) = %d, want %d (stderr: %s)", tt.args, got, tt.want, errb.String())
			}
		})
	}
}

// TestParseBuildArgsJSON covers --json parsing and the "--" terminator, so a
// task that happens to contain --json is not mistaken for the flag.
func TestParseBuildArgsJSON(t *testing.T) {
	opts, err := parseBuildArgs([]string{"task", "--json"})
	if err != nil {
		t.Fatalf("parseBuildArgs: %v", err)
	}
	if !opts.json || opts.task != "task" {
		t.Errorf("opts = %+v, want json=true task=task", opts)
	}

	opts, err = parseBuildArgs([]string{"--json", "--", "task", "--json"})
	if err != nil {
		t.Fatalf("parseBuildArgs: %v", err)
	}
	if opts.json != true || opts.task != "task --json" {
		t.Errorf("opts = %+v, want json=true task=%q", opts, "task --json")
	}

	opts, err = parseBuildArgs([]string{"--", "--json"})
	if err != nil {
		t.Fatalf("parseBuildArgs: %v", err)
	}
	if opts.json {
		t.Error("--json after -- must be task text, not the flag")
	}
	if opts.task != "--json" {
		t.Errorf("task = %q, want %q", opts.task, "--json")
	}
}

// TestUsageMentionsJSON keeps the help text honest about the new flag.
func TestUsageMentionsJSON(t *testing.T) {
	var out bytes.Buffer
	usage(&out)
	if !strings.Contains(out.String(), "--json") {
		t.Error("usage does not mention --json")
	}
}

// TestVersionString covers the build-metadata rendering: a plain build keeps the
// simple output, and injected metadata is appended.
func TestVersionString(t *testing.T) {
	oldVersion, oldCommit, oldDate := version, commit, date
	t.Cleanup(func() { version, commit, date = oldVersion, oldCommit, oldDate })

	version, commit, date = "0.1.0", "", ""
	if got := versionString(); got != "0.1.0" {
		t.Errorf("versionString() = %q, want %q", got, "0.1.0")
	}

	version, commit, date = "1.2.3", "abc1234", "2026-09-22T00:00:00Z"
	if got := versionString(); got != "1.2.3 (abc1234, 2026-09-22T00:00:00Z)" {
		t.Errorf("versionString() = %q", got)
	}

	version, commit, date = "1.2.3", "", "2026-09-22T00:00:00Z"
	if got := versionString(); got != "1.2.3 (2026-09-22T00:00:00Z)" {
		t.Errorf("versionString() = %q", got)
	}
}
