package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Verification is the read-only "is this actually done?" pass that ends an
// `orch build` run. The commands are chosen in three ways, in order of
// precedence:
//
//  1. explicit --verify flags, run verbatim and in order;
//  2. auto-detection from the repository's manifest, using a known recipe per
//     ecosystem;
//  3. otherwise none, which is an error: orch refuses to run a workflow whose
//     result it cannot check rather than pretending the work is verified.
//
// A command is never run through a shell. A --verify value is split into argv
// here, so the operator's string carries no shell semantics (no pipes,
// redirection, globbing, or variable expansion). The operator supplies the
// string, so it is trusted exactly as the task text is; it is never derived from
// agent output.
var (
	// errVerifyFailed marks a verification failure, keeping it distinguishable
	// from an agent/stage failure (which surfaces as its own error).
	errVerifyFailed = errors.New("verification failed")
	// errVerifyNotConfigured marks a repository orch has no way to verify.
	errVerifyNotConfigured = errors.New(`no verification configured: no go.mod, Cargo.toml, or package.json with scripts found; pass --verify "<command>" (repeatable)`)
)

// verifySpec is one verification command.
type verifySpec struct {
	Name    string   `json:"name"`              // label used in logs and failures
	Argv    []string `json:"argv"`              // command and arguments, executed without a shell
	EmptyOK bool     `json:"emptyOK,omitempty"` // pass only when the command prints nothing
}

// goVerifySpecs is orch's long-standing Go verification, unchanged.
func goVerifySpecs() []verifySpec {
	return []verifySpec{
		{Name: "gofmt", Argv: []string{"gofmt", "-l", "."}, EmptyOK: true},
		{Name: "go vet", Argv: []string{"go", "vet", "./..."}},
		{Name: "go test", Argv: []string{"go", "test", "./..."}},
	}
}

// cargoVerifySpecs is the Rust recipe: a compile/borrow check, then the tests.
func cargoVerifySpecs() []verifySpec {
	return []verifySpec{
		{Name: "cargo check", Argv: []string{"cargo", "check"}},
		{Name: "cargo test", Argv: []string{"cargo", "test"}},
	}
}

// nodeVerifySpecs runs only package.json scripts that already exist and clearly
// denote verification. It never invents a script and never falls back to a bare
// package-manager command, so a project without such a script is simply not
// auto-detected.
func nodeVerifySpecs(dir string) ([]verifySpec, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil, false
	}
	var specs []verifySpec
	for _, name := range []string{"lint", "check", "typecheck", "test"} {
		if _, ok := pkg.Scripts[name]; !ok {
			continue
		}
		specs = append(specs, verifySpec{Name: "npm run " + name, Argv: []string{"npm", "run", name}})
	}
	return specs, len(specs) > 0
}

// detectVerify picks a verification recipe from the repository's manifests. It
// returns ok=false when nothing is recognized, so the caller can require an
// explicit --verify instead of guessing.
func detectVerify(dir string) ([]verifySpec, bool) {
	switch {
	case fileExists(filepath.Join(dir, "go.mod")):
		return goVerifySpecs(), true
	case fileExists(filepath.Join(dir, "Cargo.toml")):
		return cargoVerifySpecs(), true
	case fileExists(filepath.Join(dir, "package.json")):
		return nodeVerifySpecs(dir)
	default:
		return nil, false
	}
}

// resolveVerify returns the commands a run will verify with: the explicit
// --verify flags when present, otherwise what the repository auto-detects. It
// errors when neither yields anything.
func resolveVerify(dir string, explicit []string) ([]verifySpec, error) {
	if len(explicit) > 0 {
		specs := make([]verifySpec, 0, len(explicit))
		for _, raw := range explicit {
			argv, err := splitCommand(raw)
			if err != nil {
				return nil, fmt.Errorf("invalid --verify %q: %w", raw, err)
			}
			if len(argv) == 0 {
				return nil, fmt.Errorf("invalid --verify %q: empty command", raw)
			}
			specs = append(specs, verifySpec{Name: strings.Join(argv, " "), Argv: argv})
		}
		return specs, nil
	}
	if specs, ok := detectVerify(dir); ok {
		return specs, nil
	}
	return nil, errVerifyNotConfigured
}

// resumeVerifySpecs returns the commands to verify a resumed run with: the ones
// the run was created with, or — for a run recorded before they were persisted —
// what the repository auto-detects now.
func resumeVerifySpecs(st runState) ([]verifySpec, error) {
	if len(st.Verify) > 0 {
		return st.Verify, nil
	}
	if specs, ok := detectVerify(st.RepoDir); ok {
		return specs, nil
	}
	return nil, errVerifyNotConfigured
}

// splitCommand splits a verification command into argv without invoking a shell.
// It understands single and double quotes and backslash escapes; every other
// character is literal. It performs no variable expansion, globbing, command
// substitution, piping or redirection: that boundary is stated here rather than
// hidden behind a shell.
func splitCommand(s string) ([]string, error) {
	var (
		args    []string
		cur     strings.Builder
		inArg   bool
		quote   rune
		escaped bool
	)
	flush := func() {
		if inArg {
			args = append(args, cur.String())
			cur.Reset()
			inArg = false
		}
	}
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
			inArg = true
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				cur.WriteRune(r)
			}
		case r == '\\':
			escaped = true
			inArg = true
		case r == '\'' || r == '"':
			quote = r
			inArg = true
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	if escaped {
		return nil, errors.New("trailing backslash")
	}
	flush()
	return args, nil
}

// fileExists reports whether path is an existing regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
