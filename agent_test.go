package main

import (
	"strings"
	"testing"
)

func TestBuildArgvUsesAgentSpelling(t *testing.T) {
	tests := []struct {
		agent string
		opts  runOptions
		want  []string
	}{
		{"agy", runOptions{prompt: "hello"}, []string{"agy", "--prompt-interactive", "hello"}},
		{"agy", runOptions{cont: true}, []string{"agy", "--continue"}},
		{"command-code", runOptions{cont: true, prompt: "hello"}, []string{"command-code", "--continue", "hello"}},
		{"copilot", runOptions{prompt: "hello"}, []string{"copilot", "--interactive", "hello"}},
	}

	for _, tt := range tests {
		a, ok := lookupAgent(tt.agent)
		if !ok {
			t.Fatalf("agent %q is not registered", tt.agent)
		}
		got, err := buildArgv(a, tt.opts)
		if err != nil {
			t.Fatalf("buildArgv(%s): %v", tt.agent, err)
		}
		if strings.Join(got, " ") != strings.Join(tt.want, " ") {
			t.Errorf("buildArgv(%s) = %q, want %q", tt.agent, got, tt.want)
		}
	}
}

// TestRegistryNeverBypassesPermissions guards the registry itself: buildArgv
// validates the completed argv, so a bypass flag added to an Agent definition
// fails here rather than silently weakening every session.
func TestRegistryNeverBypassesPermissions(t *testing.T) {
	for _, a := range agents {
		opts := runOptions{cont: true, prompt: "hello", extra: []string{"--verbose"}}
		argv, err := buildArgv(a, opts)
		if err != nil {
			t.Fatalf("agent %s builds refused args: %v", a.Name, err)
		}
		if argv[0] != a.Bin {
			t.Errorf("agent %s: argv[0] = %q, want %q", a.Name, argv[0], a.Bin)
		}
	}
}

func TestCheckPermissionFlags(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		deny []denyFlag
		want string // substring expected in the error; empty means allowed
	}{
		{"agy skip permissions", []string{"--dangerously-skip-permissions"}, sharedDeny, "auto-approve"},
		{"command-code yolo", []string{"--yolo"}, sharedDeny, "auto-approve"},
		{"auto accept", []string{"--auto-accept"}, sharedDeny, "auto-approve"},
		{"tools all", []string{"--tools-all"}, sharedDeny, "auto-approve"},
		{"permission mode joined", []string{"--permission-mode=auto-accept"}, sharedDeny, "auto-approve"},
		{"permission mode split", []string{"--permission-mode", "auto-accept"}, sharedDeny, "auto-approve"},
		{"accept edits", []string{"--mode", "accept-edits"}, sharedDeny, "auto-approve"},
		{"agent specific trust", []string{"--trust"}, []denyFlag{{flag: "--trust"}}, "auto-approve"},
		{"agent specific short trust", []string{"-t"}, []denyFlag{{flag: "-t"}}, "auto-approve"},
		{"plan mode is fine", []string{"--mode", "plan"}, sharedDeny, ""},
		{"standard permissions are fine", []string{"--permission-mode", "standard"}, sharedDeny, ""},
		{"unrelated flags are fine", []string{"--model", "opus", "-c"}, sharedDeny, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkPermissionFlags(tt.argv, tt.deny)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("checkPermissionFlags(%q) = %v, want nil", tt.argv, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkPermissionFlags(%q) = nil, want an error", tt.argv)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestBuildArgvRefusesBypassFromCommandLine(t *testing.T) {
	a, ok := lookupAgent("agy")
	if !ok {
		t.Fatal("agy is not registered")
	}
	_, err := buildArgv(a, runOptions{extra: []string{"--dangerously-skip-permissions"}})
	if err == nil {
		t.Fatal("pass-through bypass flag was accepted")
	}
	if !strings.Contains(err.Error(), "agy") {
		t.Errorf("error %q should name the agent", err)
	}

	cc, _ := lookupAgent("command-code")
	if _, err := buildArgv(cc, runOptions{extra: []string{"--permission-mode", "accept-all"}}); err == nil {
		t.Fatal("accept-all was accepted")
	}
}
