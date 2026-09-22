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

// TestRunValidatesFlagsBeforeBinaryLookup guards the ordering requirement: the
// permission-bypass denylist must be enforced even when the agent binary is not
// installed, so a forbidden flag returns 2, not the 127 a missing binary yields.
// PATH points at an empty directory, so no agent exists on this machine.
func TestRunValidatesFlagsBeforeBinaryLookup(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	tests := []struct {
		name    string
		args    []string
		want    int
		wantErr string // substring expected on stderr; "" means no assertion
	}{
		{
			"forbidden flag with missing binary",
			[]string{"run", "agy", "--", "--dangerously-skip-permissions"},
			2, "auto-approve",
		},
		{
			"yolo with missing binary",
			[]string{"run", "agy", "--", "--yolo"},
			2, "auto-approve",
		},
		{
			"short yolo with missing binary",
			[]string{"run", "agy", "--", "-y"},
			2, "auto-approve",
		},
		{
			"clustered yolo with missing binary",
			[]string{"run", "agy", "--", "-cy"},
			2, "auto-approve",
		},
		{
			"permission-mode value with missing binary",
			[]string{"run", "command-code", "--", "--permission-mode=accept-all"},
			2, "auto-approve",
		},
		{
			"agent-specific trust with missing binary",
			[]string{"run", "command-code", "--", "-t"},
			2, "auto-approve",
		},
		{
			"allowed flag with missing binary",
			[]string{"run", "agy", "--", "--verbose"},
			127, "",
		},
		{
			"no extra args with missing binary",
			[]string{"run", "agy"},
			127, "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := orchMain(tt.args, nil, &stdout, &stderr); got != tt.want {
				t.Errorf("orchMain(%q) = %d, want %d (stderr: %s)", tt.args, got, tt.want, stderr.String())
			}
			if tt.wantErr != "" && !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want it to mention %q (the flag must be rejected, not the binary)", stderr.String(), tt.wantErr)
			}
		})
	}
}

// TestRunChecksBinariesOnlyAfterValidation covers the chain path: with every
// command line valid, a missing binary is still reported as 127 (the binary
// check runs, but only after validation), while an unknown agent name is
// rejected as 2 beforehand.
func TestRunChecksBinariesOnlyAfterValidation(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	var stdout, stderr bytes.Buffer
	if got := orchMain([]string{"run", "agy", "copilot"}, nil, &stdout, &stderr); got != 127 {
		t.Errorf("orchMain(chain, missing binaries) = %d, want 127 (stderr: %s)", got, stderr.String())
	}

	stderr.Reset()
	if got := orchMain([]string{"run", "agy", "nope"}, nil, &stdout, &stderr); got != 2 {
		t.Errorf("orchMain(chain, unknown agent) = %d, want 2 (stderr: %s)", got, stderr.String())
	}
}
