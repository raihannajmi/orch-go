package main

import (
	"fmt"
	"slices"
	"strings"
)

// Agent describes how to launch one coding agent interactively.
//
// orch deliberately contains no approval-skipping configuration: each agent is
// handed a real terminal and runs its own permission prompts. checkPermissionFlags
// enforces that for anything passed through from the command line.
type Agent struct {
	Name     string                // name used on the orch command line
	Bin      string                // executable looked up on PATH
	Summary  string                // one-line description for `orch list`
	Continue []string              // args that resume the most recent session
	Prompt   func(string) []string // args that start a session with an initial prompt
	Deny     []denyFlag            // agent-specific permission flags to refuse
}

// agents is the supported set, in `orch list` order.
var agents = []Agent{
	{
		Name:     "agy",
		Bin:      "agy",
		Summary:  "Antigravity CLI (native terminal UI)",
		Continue: []string{"--continue"},
		Prompt:   func(p string) []string { return []string{"--prompt-interactive", p} },
	},
	{
		Name:     "command-code",
		Bin:      "command-code",
		Summary:  "Command Code coding agent",
		Continue: []string{"--continue"},
		Prompt:   func(p string) []string { return []string{p} }, // initial message is positional
		Deny:     []denyFlag{{flag: "--trust"}, {flag: "-t"}},
	},
	{
		Name:     "copilot",
		Bin:      "copilot",
		Summary:  "GitHub Copilot CLI",
		Continue: []string{"--continue"},
		Prompt:   func(p string) []string { return []string{"--interactive", p} },
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
// values means the flag is refused outright; otherwise only those values are.
type denyFlag struct {
	flag   string
	values []string
}

// sharedDeny covers the permission-suppression flags across the supported
// agents. Refusing them keeps orch's promise that permissions are never
// bypassed, auto-approved, or suppressed on the agent's behalf.
var sharedDeny = []denyFlag{
	{flag: "--dangerously-skip-permissions"},
	{flag: "--yolo"},
	{flag: "--auto-accept"},
	{flag: "--tools-all"},
	{flag: "--permission-mode", values: []string{"auto-accept", "accept-all"}},
	{flag: "--mode", values: []string{"accept-edits"}},
}

// denied lists every flag refused for this agent.
func (a Agent) denied() []denyFlag {
	out := make([]denyFlag, 0, len(sharedDeny)+len(a.Deny))
	out = append(out, sharedDeny...)
	return append(out, a.Deny...)
}

// checkPermissionFlags rejects argv entries that would auto-approve agent tool
// use. It understands both `--flag value` and `--flag=value` spellings.
func checkPermissionFlags(argv []string, deny []denyFlag) error {
	for i, arg := range argv {
		name, value, hasValue := strings.Cut(arg, "=")
		for _, d := range deny {
			if name != d.flag {
				continue
			}
			if len(d.values) == 0 {
				return fmt.Errorf("%s would auto-approve permissions; orch will not pass it through", arg)
			}
			v := value
			if !hasValue && i+1 < len(argv) {
				v = argv[i+1]
			}
			if slices.Contains(d.values, v) {
				return fmt.Errorf("%s %s would auto-approve permissions; orch will not pass it through", d.flag, v)
			}
		}
	}
	return nil
}

// buildArgv assembles the command line for one session: the agent binary, then
// orch's own flags mapped to the agent's spelling, then the user's pass-through
// args. The result is validated so no bypass flag can slip in.
func buildArgv(a Agent, opts runOptions) ([]string, error) {
	argv := []string{a.Bin}
	if opts.cont {
		argv = append(argv, a.Continue...)
	}
	if opts.prompt != "" {
		argv = append(argv, a.Prompt(opts.prompt)...)
	}
	argv = append(argv, opts.extra...)

	if err := checkPermissionFlags(argv[1:], a.denied()); err != nil {
		return nil, fmt.Errorf("%s: %w", a.Name, err)
	}
	return argv, nil
}
