package main

import (
	"fmt"
	"slices"
	"strings"
)

// Agent describes how to launch one coding agent interactively.
//
// orch deliberately contains no approval-skipping configuration: each agent is
// handed a real terminal and runs its own permission prompts. Two independent
// gates keep it that way:
//
//   - Flags is an explicit allowlist of the only flags orch itself may place on
//     the command line. buildArgv fails closed if it would emit anything else,
//     so a bad registry entry cannot quietly weaken every session.
//   - Deny (plus sharedDeny) is a denylist of permission-bypass flags refused
//     from the user's pass-through arguments.
type Agent struct {
	Name       string     // name used on the orch command line
	Bin        string     // executable looked up on PATH
	Summary    string     // one-line description for `orch list`
	Continue   []string   // flags that resume the most recent session
	PromptFlag string     // flag that carries the initial prompt; "" means positional
	Flags      []string   // allowlist: the only flags orch may place on the command line
	Deny       []denyFlag // agent-specific permission flags to refuse
}

// agents is the supported set, in `orch list` order.
var agents = []Agent{
	{
		Name:       "agy",
		Bin:        "agy",
		Summary:    "Antigravity CLI (native terminal UI)",
		Continue:   []string{"--continue"},
		PromptFlag: "--prompt-interactive",
		Flags:      []string{"--continue", "--prompt-interactive"},
	},
	{
		Name:       "command-code",
		Bin:        "command-code",
		Summary:    "Command Code coding agent",
		Continue:   []string{"--continue"},
		PromptFlag: "", // the initial message is positional
		Flags:      []string{"--continue"},
		Deny:       []denyFlag{{flag: "-t"}}, // short form of --trust
	},
	{
		Name:       "copilot",
		Bin:        "copilot",
		Summary:    "GitHub Copilot CLI",
		Continue:   []string{"--continue"},
		PromptFlag: "--interactive",
		Flags:      []string{"--continue", "--interactive"},
	},
}

// lookupAgent finds a registered agent by name.
func lookupAgent(name string) (Agent, bool) {
	for _, a := range agents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
}

// denyFlag names a flag that would hand an agent blanket approval. An empty
// values list means the flag is refused outright; otherwise only those values
// are (an allowlisted flag such as `--permission-mode plan` still passes).
type denyFlag struct {
	flag   string
	values []string
}

// sharedDeny covers the permission-suppression flags across the supported
// agents. Refusing them keeps orch's promise that permissions are never
// bypassed, auto-approved, or suppressed on the agent's behalf.
var sharedDeny = []denyFlag{
	// Flags that suppress permission prompts outright.
	{flag: "--dangerously-skip-permissions"},
	{flag: "--dangerously-bypass-approvals-and-sandbox"},
	{flag: "--skip-permissions"},
	{flag: "--bypass-permissions"},
	{flag: "--yolo"},
	{flag: "-y"},
	{flag: "--auto-accept"},
	{flag: "--auto-accept-all"},
	{flag: "--auto-approve"},
	{flag: "--auto-approve-all"},
	{flag: "--always-approve"},
	{flag: "--accept-edits"},
	{flag: "--accept-all"},
	{flag: "--allow-all"},
	{flag: "--allow-all-tools"},
	{flag: "--allow-all-paths"},
	{flag: "--tools-all"},
	{flag: "--trust"},
	{flag: "--full-auto"},

	// Flags whose value selects a permission-suppressing mode. The safe values
	// (default, plan, standard, ask) are deliberately not listed and still pass.
	{flag: "--permission-mode", values: []string{
		"acceptEdits", "accept-edits", "acceptAll", "accept-all",
		"auto", "auto-accept", "auto-approve",
		"bypassPermissions", "bypass", "danger-full-access", "full-access",
		"unrestricted", "yolo",
	}},
	{flag: "--approval-mode", values: []string{
		"auto", "auto-accept", "auto-approve", "accept-all", "accept-edits",
		"yolo", "full-auto", "never", "bypass",
	}},
	{flag: "--mode", values: []string{
		"accept-edits", "acceptEdits", "accept-all", "auto-accept", "bypass", "yolo",
	}},
	{flag: "--sandbox", values: []string{
		"none", "off", "disabled", "danger-full-access",
	}},
}

// denied lists every flag refused for this agent.
func (a Agent) denied() []denyFlag {
	out := make([]denyFlag, 0, len(sharedDeny)+len(a.Deny))
	out = append(out, sharedDeny...)
	return append(out, a.Deny...)
}

// checkAllowed fails when orch would emit a flag that is not on the agent's
// explicit allowlist. It is the gate on orch's own flags, as opposed to the
// denylist, which guards the user's pass-through arguments.
func (a Agent) checkAllowed(flags []string) error {
	for _, f := range flags {
		if !slices.Contains(a.Flags, f) {
			return fmt.Errorf("%s: refusing to pass %q: not on the agent's allowlist", a.Name, f)
		}
	}
	return nil
}

// checkPermissionFlags rejects argv entries that would hand an agent blanket
// approval. It inspects real arguments, not arbitrary text: a value belonging to
// some other flag (a prompt, a model name) is never itself read as a flag. It
// understands `--flag=value`, `--flag value`, exact short flags, and single-dash
// clusters such as -cy (continue + yolo). Scanning stops at `--`, after which
// the remaining arguments are positional for the agent.
func checkPermissionFlags(argv []string, deny []denyFlag) error {
	for i, arg := range argv {
		if arg == "--" {
			break
		}
		name, value, hasValue := strings.Cut(arg, "=")
		next := ""
		if !hasValue && i+1 < len(argv) {
			next = argv[i+1]
		}
		if reason := deniedReason(name, value, hasValue, next, deny); reason != "" {
			return fmt.Errorf("%s", reason)
		}
		// A single-dash cluster packs several short flags; check each one.
		for _, short := range shortCluster(name) {
			if reason := deniedReason(short, "", false, "", deny); reason != "" {
				return fmt.Errorf("%s", reason)
			}
		}
	}
	return nil
}

// deniedReason returns a non-empty message when the flag token would be refused.
func deniedReason(name, value string, hasValue bool, next string, deny []denyFlag) string {
	for _, d := range deny {
		if name != d.flag {
			continue
		}
		if len(d.values) == 0 {
			return fmt.Sprintf("%s would auto-approve permissions; orch will not pass it through", name)
		}
		v := value
		if !hasValue {
			v = next
		}
		for _, dv := range d.values {
			if strings.EqualFold(dv, v) {
				return fmt.Sprintf("%s %s would auto-approve permissions; orch will not pass it through", d.flag, v)
			}
		}
	}
	return ""
}

// shortCluster expands a single-dash short-flag cluster such as -cy into its
// individual flags. It returns nil for long flags and for tokens that are not a
// plain lowercase cluster, so an attached value (-C/tmp, -Ctmp) is never
// mistaken for one.
func shortCluster(name string) []string {
	if len(name) < 3 || name[0] != '-' || name[1] == '-' {
		return nil
	}
	body := name[1:]
	for _, r := range body {
		if r < 'a' || r > 'z' {
			return nil
		}
	}
	out := make([]string, 0, len(body))
	for _, r := range body {
		out = append(out, "-"+string(r))
	}
	return out
}

// buildArgv assembles the command line for one session: the agent binary, then
// orch's own flags mapped to the agent's spelling, then the user's pass-through
// arguments.
//
// orch's flags are validated against the agent's allowlist, and every argument
// that could reach the agent's parser is validated against the bypass denylist.
// A prompt carried as a flag value (agy, copilot) is data and is never read as a
// flag; a positional prompt (command-code) sits where the agent's parser would
// read it as a flag, so it is checked too.
func buildArgv(a Agent, opts runOptions) ([]string, error) {
	var own []string // the flags orch adds on the user's behalf
	if opts.cont {
		own = append(own, a.Continue...)
	}
	if opts.prompt != "" && a.PromptFlag != "" {
		own = append(own, a.PromptFlag)
	}
	if err := a.checkAllowed(own); err != nil {
		return nil, err
	}
	// Defense in depth: nothing on the allowlist may itself be a bypass flag.
	if err := checkPermissionFlags(own, a.denied()); err != nil {
		return nil, fmt.Errorf("%s: %w", a.Name, err)
	}

	// Check the arguments the agent's parser can see as flags, in argv order.
	scan := make([]string, 0, len(opts.extra)+1)
	if opts.prompt != "" && a.PromptFlag == "" {
		scan = append(scan, opts.prompt)
	}
	scan = append(scan, opts.extra...)
	if err := checkPermissionFlags(scan, a.denied()); err != nil {
		return nil, fmt.Errorf("%s: %w", a.Name, err)
	}

	argv := make([]string, 0, 1+len(own)+1+len(opts.extra))
	argv = append(argv, a.Bin)
	argv = append(argv, own...)
	if opts.prompt != "" {
		argv = append(argv, opts.prompt)
	}
	argv = append(argv, opts.extra...)
	return argv, nil
}
