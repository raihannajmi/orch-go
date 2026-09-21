package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestParseRunArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want runOptions
	}{
		{"single agent", []string{"agy"}, runOptions{agents: []string{"agy"}}},
		{"flags before agent", []string{"-c", "agy"}, runOptions{cont: true, agents: []string{"agy"}}},
		{"flags after agents", []string{"agy", "copilot", "-c"}, runOptions{cont: true, agents: []string{"agy", "copilot"}}},
		{"dir split", []string{"-C", "/tmp", "agy"}, runOptions{dir: "/tmp", agents: []string{"agy"}}},
		{"dir joined", []string{"--dir=/tmp", "agy"}, runOptions{dir: "/tmp", agents: []string{"agy"}}},
		{"prompt split", []string{"-p", "fix the bug", "agy"}, runOptions{prompt: "fix the bug", agents: []string{"agy"}}},
		{"prompt joined", []string{"--prompt=fix the bug", "agy"}, runOptions{prompt: "fix the bug", agents: []string{"agy"}}},
		{"passthrough", []string{"agy", "--", "--model", "opus"}, runOptions{agents: []string{"agy"}, extra: []string{"--model", "opus"}}},
		{"passthrough after flags", []string{"--continue", "agy", "--", "-c"}, runOptions{cont: true, agents: []string{"agy"}, extra: []string{"-c"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRunArgs(tt.args)
			if err != nil {
				t.Fatalf("parseRunArgs(%q) = %v", tt.args, err)
			}
			if strings.Join(got.agents, ",") != strings.Join(tt.want.agents, ",") {
				t.Errorf("agents = %q, want %q", got.agents, tt.want.agents)
			}
			if strings.Join(got.extra, ",") != strings.Join(tt.want.extra, ",") {
				t.Errorf("extra = %q, want %q", got.extra, tt.want.extra)
			}
			if got.dir != tt.want.dir || got.cont != tt.want.cont || got.prompt != tt.want.prompt {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseRunArgsErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--nope", "agy"},
		{"-C"},
		{"--prompt"},
		{"agy", "--model", "opus"}, // agent flags must follow `--`
	} {
		if _, err := parseRunArgs(args); err == nil {
			t.Errorf("parseRunArgs(%q) = nil error, want a usage error", args)
		}
	}

	if _, err := parseRunArgs([]string{"-h"}); !errors.Is(err, errHelp) {
		t.Errorf("parseRunArgs(-h) = %v, want errHelp", err)
	}
}

func TestOrchMainDispatch(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"no args", nil, 2},
		{"unknown command", []string{"frobnicate"}, 2},
		{"version", []string{"version"}, 0},
		{"help", []string{"help"}, 0},
		{"run without agent", []string{"run"}, 2},
		{"run unknown agent", []string{"run", "nope"}, 2},
		{"run passthrough with a chain", []string{"run", "agy", "copilot", "--", "-x"}, 2},
		{"run refused flag", []string{"run", "agy", "--", "--dangerously-skip-permissions"}, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := orchMain(tt.args, nil, &stdout, &stderr); got != tt.want {
				t.Errorf("orchMain(%q) = %d, want %d (stderr: %s)", tt.args, got, tt.want, stderr.String())
			}
		})
	}
}

func TestListAgents(t *testing.T) {
	var stdout bytes.Buffer
	if code := listAgents(&stdout); code != 0 {
		t.Fatalf("listAgents = %d, want 0", code)
	}
	for _, a := range agents {
		if !strings.Contains(stdout.String(), a.Name) {
			t.Errorf("listing does not mention %s:\n%s", a.Name, stdout.String())
		}
	}
}
