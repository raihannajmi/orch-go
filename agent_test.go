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
		{"default permissions are fine", []string{"--permission-mode", "default"}, sharedDeny, ""},
		{"unrelated flags are fine", []string{"--model", "opus", "-c"}, sharedDeny, ""},
		{"standalone accept-edits", []string{"--accept-edits"}, sharedDeny, "auto-approve"},
		{"shared trust", []string{"--trust"}, sharedDeny, "auto-approve"},
		{"cluster yolo", []string{"-cy"}, sharedDeny, "auto-approve"},
		{"cluster yolo then trust", []string{"-yt"}, []denyFlag{{flag: "-t"}}, "auto-approve"},
		{"value case-insensitive", []string{"--permission-mode", "ACCEPT-ALL"}, sharedDeny, "auto-approve"},
		{"bypass permissions value", []string{"--permission-mode", "bypassPermissions"}, sharedDeny, "auto-approve"},
		{"approval mode", []string{"--approval-mode=auto"}, sharedDeny, "auto-approve"},
		{"sandbox off", []string{"--sandbox", "none"}, sharedDeny, "auto-approve"},
		{"after terminator is positional", []string{"--", "--yolo"}, sharedDeny, ""},
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

// TestEveryAgentRejectsBypassFlags runs the full bypass catalogue through every
// supported agent, so a new agent cannot be added without a denylist that holds.
func TestEveryAgentRejectsBypassFlags(t *testing.T) {
	bypass := [][]string{
		{"--dangerously-skip-permissions"},
		{"--dangerously-bypass-approvals-and-sandbox"},
		{"--skip-permissions"},
		{"--bypass-permissions"},
		{"--yolo"},
		{"--yolo=1"},
		{"-y"},
		{"--auto-accept"},
		{"--auto-accept-all"},
		{"--auto-approve"},
		{"--always-approve"},
		{"--accept-edits"},
		{"--accept-all"},
		{"--allow-all"},
		{"--allow-all-tools"},
		{"--allow-all-paths"},
		{"--tools-all"},
		{"--trust"},
		{"--full-auto"},
		{"--permission-mode=bypassPermissions"},
		{"--permission-mode", "auto-accept"},
		{"--permission-mode", "acceptEdits"},
		{"--approval-mode", "auto"},
		{"--mode", "accept-edits"},
		{"--sandbox", "none"},
		{"-cy"}, // continue + yolo
	}
	for _, a := range agents {
		for _, args := range bypass {
			if _, err := buildArgv(a, runOptions{extra: args}); err == nil {
				t.Errorf("agent %s accepted bypass args %q", a.Name, args)
			}
		}
	}
}

// TestClusteredShortFlags covers the compact spellings of the same bypasses.
func TestClusteredShortFlags(t *testing.T) {
	for _, a := range agents {
		for _, args := range [][]string{{"-y"}, {"-cy"}, {"-yc"}, {"-yt"}} {
			if _, err := buildArgv(a, runOptions{extra: args}); err == nil {
				t.Errorf("agent %s accepted clustered bypass %q", a.Name, args)
			}
		}
	}

	// -t is only a bypass for command-code; in a cluster there it must still be
	// caught, while agy leaves -t alone because it means nothing there.
	cc, _ := lookupAgent("command-code")
	if _, err := buildArgv(cc, runOptions{extra: []string{"-ct"}}); err == nil {
		t.Error("command-code accepted -ct (continue + trust)")
	}
}

// TestTaskTextIsNeverTreatedAsFlags is requirement 3: a prompt that merely
// mentions a bypass flag is data and must pass through untouched.
func TestTaskTextIsNeverTreatedAsFlags(t *testing.T) {
	prompts := []string{
		"add a note about --dangerously-skip-permissions to the docs",
		"explain --yolo, --trust and --auto-accept in the README",
		"the flag is spelled --permission-mode=accept-all",
	}
	for _, a := range agents {
		for _, p := range prompts {
			argv, err := buildArgv(a, runOptions{prompt: p})
			if err != nil {
				t.Errorf("agent %s rejected task text %q: %v", a.Name, p, err)
				continue
			}
			if argv[len(argv)-1] != p {
				t.Errorf("agent %s altered the prompt: argv = %q", a.Name, argv)
			}
		}
	}
}

// TestPositionalPromptIsCheckedAsArgument distinguishes a prompt carried as a
// flag value (agy, copilot — data, never a flag) from a positional prompt
// (command-code — sits where the agent's own parser reads flags).
func TestPositionalPromptIsCheckedAsArgument(t *testing.T) {
	cc, _ := lookupAgent("command-code")
	if _, err := buildArgv(cc, runOptions{prompt: "--yolo"}); err == nil {
		t.Error("command-code accepted a positional prompt that is a bypass flag")
	}

	for _, name := range []string{"agy", "copilot"} {
		a, _ := lookupAgent(name)
		argv, err := buildArgv(a, runOptions{prompt: "--yolo"})
		if err != nil {
			t.Errorf("%s rejected a prompt value: %v", name, err)
			continue
		}
		if argv[len(argv)-1] != "--yolo" {
			t.Errorf("%s altered the prompt value: argv = %q", name, argv)
		}
	}
}

// TestLegitimateArgumentsStillWork guards against an over-broad denylist.
func TestLegitimateArgumentsStillWork(t *testing.T) {
	allowed := [][]string{
		{"--model", "opus"},
		{"--verbose"},
		{"-c"},
		{"--permission-mode", "plan"},
		{"--permission-mode", "standard"},
		{"--permission-mode=default"},
		{"--mode", "plan"},
		{"--sandbox", "workspace-write"},
	}
	for _, a := range agents {
		for _, args := range allowed {
			if _, err := buildArgv(a, runOptions{extra: args}); err != nil {
				t.Errorf("agent %s rejected legitimate args %q: %v", a.Name, args, err)
			}
		}
	}
}

// TestAgentAllowlistsAreSafeAndComplete checks the invariant behind the
// allowlist: every agent declares the flags it emits, and none of them is a
// bypass flag.
func TestAgentAllowlistsAreSafeAndComplete(t *testing.T) {
	for _, a := range agents {
		if len(a.Flags) == 0 {
			t.Errorf("agent %s has no allowlist", a.Name)
		}
		for _, f := range a.Flags {
			if err := checkPermissionFlags([]string{f}, a.denied()); err != nil {
				t.Errorf("agent %s allowlists bypass flag %q: %v", a.Name, f, err)
			}
		}
		if err := a.checkAllowed(a.Continue); err != nil {
			t.Errorf("agent %s: %v", a.Name, err)
		}
		if a.PromptFlag != "" {
			if err := a.checkAllowed([]string{a.PromptFlag}); err != nil {
				t.Errorf("agent %s: %v", a.Name, err)
			}
		}
	}
}

// TestBuildArgvFailsClosedOnBadRegistry covers a registry that would emit a flag
// outside its allowlist, and one that allowlists a genuine bypass flag.
func TestBuildArgvFailsClosedOnBadRegistry(t *testing.T) {
	rogue := Agent{
		Name:     "rogue",
		Bin:      "rogue",
		Continue: []string{"--sneaky"},
		Flags:    []string{"--continue"},
	}
	if _, err := buildArgv(rogue, runOptions{cont: true}); err == nil {
		t.Error("buildArgv accepted a flag missing from the allowlist")
	}

	bypass := Agent{
		Name:     "bypass",
		Bin:      "bypass",
		Continue: []string{"--yolo"},
		Flags:    []string{"--yolo"},
	}
	if _, err := buildArgv(bypass, runOptions{cont: true}); err == nil {
		t.Error("buildArgv accepted an allowlisted bypass flag")
	}
}
