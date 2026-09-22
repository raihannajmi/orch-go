package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeFiles writes a set of files under dir, creating parent directories.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func specNames(specs []verifySpec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

func TestSplitCommand(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"simple", "go test ./...", []string{"go", "test", "./..."}},
		{"collapses whitespace", "  go   test  ", []string{"go", "test"}},
		{"double quotes keep spaces", `sh -c "echo hello world"`, []string{"sh", "-c", "echo hello world"}},
		{"single quotes keep spaces", `grep 'a b' file`, []string{"grep", "a b", "file"}},
		{"backslash escapes a space", `echo a\ b`, []string{"echo", "a b"}},
		{"quoted dash value is literal", `tool --flag "-x y"`, []string{"tool", "--flag", "-x y"}},
		{"empty quoted argument", `tool ""`, []string{"tool", ""}},
		{"bypass-looking value is data", "--yolo", []string{"--yolo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := splitCommand(tt.in)
			if err != nil {
				t.Fatalf("splitCommand(%q) = %v", tt.in, err)
			}
			if strings.Join(got, "\x00") != strings.Join(tt.want, "\x00") {
				t.Errorf("splitCommand(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	for _, in := range []string{`echo "unterminated`, `echo 'unterminated`, `echo trailing\`} {
		if _, err := splitCommand(in); err == nil {
			t.Errorf("splitCommand(%q) = nil error, want a parse error", in)
		}
	}
}

// TestDetectVerify covers the auto-detection recipes: Go default, Rust, Node
// with and without real scripts, and an undetected repository.
func TestDetectVerify(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  []string
		ok    bool
	}{
		{"go repository", map[string]string{"go.mod": "module example.com/x\n"}, []string{"gofmt", "go vet", "go test"}, true},
		{"rust repository", map[string]string{"Cargo.toml": "[package]\nname = \"x\"\n"}, []string{"cargo check", "cargo test"}, true},
		{
			"node repository with scripts",
			map[string]string{"package.json": `{"name":"x","scripts":{"test":"jest","lint":"eslint ."}}`},
			[]string{"npm run lint", "npm run test"},
			true,
		},
		{"node repository without scripts", map[string]string{"package.json": `{"name":"x"}`}, nil, false},
		{"undetected repository", map[string]string{"README.md": "hi"}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, tt.files)
			specs, ok := detectVerify(dir)
			if ok != tt.ok {
				t.Fatalf("detectVerify ok = %v, want %v", ok, tt.ok)
			}
			if got := specNames(specs); strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("detectVerify = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGoVerifySpecsPreserveDefaults pins the exact Go recipe so the P0 behavior
// cannot drift: gofmt -l . (empty output only), go vet ./..., go test ./...
func TestGoVerifySpecsPreserveDefaults(t *testing.T) {
	specs := goVerifySpecs()
	if len(specs) != 3 {
		t.Fatalf("go spec count = %d, want 3", len(specs))
	}
	want := []struct {
		name    string
		argv    string
		emptyOK bool
	}{
		{"gofmt", "gofmt -l .", true},
		{"go vet", "go vet ./...", false},
		{"go test", "go test ./...", false},
	}
	for i, w := range want {
		got := specs[i]
		if got.Name != w.name || strings.Join(got.Argv, " ") != w.argv || got.EmptyOK != w.emptyOK {
			t.Errorf("spec %d = {%q %q emptyOK=%v}, want {%q %q emptyOK=%v}",
				i, got.Name, strings.Join(got.Argv, " "), got.EmptyOK, w.name, w.argv, w.emptyOK)
		}
	}
}

func TestResolveVerify(t *testing.T) {
	goDir := t.TempDir()
	writeFiles(t, goDir, map[string]string{"go.mod": "module example.com/x\n"})

	t.Run("auto-detect in a go repository", func(t *testing.T) {
		specs, err := resolveVerify(goDir, nil)
		if err != nil {
			t.Fatalf("resolveVerify: %v", err)
		}
		if got := strings.Join(specNames(specs), ","); got != "gofmt,go vet,go test" {
			t.Errorf("specs = %v, want the Go default", specNames(specs))
		}
	})

	t.Run("explicit overrides detection and keeps order", func(t *testing.T) {
		specs, err := resolveVerify(goDir, []string{"make lint", "make test"})
		if err != nil {
			t.Fatalf("resolveVerify: %v", err)
		}
		if got := strings.Join(specNames(specs), ","); got != "make lint,make test" {
			t.Errorf("specs = %v, want the explicit commands in order", specNames(specs))
		}
		if strings.Join(specs[1].Argv, " ") != "make test" {
			t.Errorf("spec[1].Argv = %v, want [make test]", specs[1].Argv)
		}
		if specs[0].EmptyOK {
			t.Error("explicit checks must not use the emptyOK (gofmt) rule")
		}
	})

	t.Run("undetected with no explicit is an error", func(t *testing.T) {
		if _, err := resolveVerify(t.TempDir(), nil); !errors.Is(err, errVerifyNotConfigured) {
			t.Errorf("err = %v, want errVerifyNotConfigured", err)
		}
	})

	t.Run("invalid explicit commands", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := resolveVerify(dir, []string{`echo "x`}); err == nil {
			t.Error("unterminated quote accepted")
		}
		if _, err := resolveVerify(dir, []string{"   "}); err == nil {
			t.Error("blank command accepted")
		}
	})
}

func TestParseBuildArgsVerify(t *testing.T) {
	opts, err := parseBuildArgs([]string{"--verify", "make lint", "--verify=make test", "do the thing"})
	if err != nil {
		t.Fatalf("parseBuildArgs: %v", err)
	}
	if got := strings.Join(opts.verify, "|"); got != "make lint|make test" {
		t.Errorf("verify = %q, want the two commands in order", got)
	}
	if opts.task != "do the thing" {
		t.Errorf("task = %q", opts.task)
	}
}

// TestVerifyRepoRunsInOrder proves commands execute sequentially in the
// configured order, in the repository directory.
func TestVerifyRepoRunsInOrder(t *testing.T) {
	dir := t.TempDir()
	order := filepath.Join(dir, "order.txt")
	b := &builder{
		dir: dir, runDir: t.TempDir(), stdout: io.Discard, stderr: io.Discard,
		verifySpecs: []verifySpec{
			{Name: "first", Argv: []string{"sh", "-c", "printf 'first\\n' >> '" + order + "'"}},
			{Name: "second", Argv: []string{"sh", "-c", "printf 'second\\n' >> '" + order + "'"}},
		},
	}
	if err := b.verifyRepo(); err != nil {
		t.Fatalf("verifyRepo: %v", err)
	}
	body, err := os.ReadFile(order)
	if err != nil {
		t.Fatalf("read order file: %v", err)
	}
	if string(body) != "first\nsecond\n" {
		t.Errorf("commands ran as %q, want first then second", body)
	}
}

// TestVerifyRepoFailureIsDistinct covers requirement 9: a verification failure
// must not be mistaken for an agent/stage failure.
func TestVerifyRepoFailureIsDistinct(t *testing.T) {
	if _, err := exec.LookPath("false"); err != nil {
		t.Skip("false is not available")
	}
	b := &builder{
		dir: t.TempDir(), runDir: t.TempDir(), stdout: io.Discard, stderr: io.Discard,
		verifySpecs: []verifySpec{{Name: "false", Argv: []string{"false"}}},
	}
	err := b.verifyRepo()
	if err == nil {
		t.Fatal("verifyRepo = nil, want a failure")
	}
	if !errors.Is(err, errVerifyFailed) {
		t.Errorf("err = %v, want it to wrap errVerifyFailed", err)
	}
	if errors.Is(err, errStageTimeout) {
		t.Error("a verification failure must not look like a stage timeout")
	}
	if !strings.Contains(err.Error(), "verification failed") {
		t.Errorf("err = %q, want it labeled as a verification failure", err)
	}
}

// TestVerifyRepoEmptyOK covers the gofmt-shaped rule: a command that prints must
// fail when EmptyOK is set, and pass otherwise.
func TestVerifyRepoEmptyOK(t *testing.T) {
	newBuilder := func(spec verifySpec) *builder {
		return &builder{dir: t.TempDir(), runDir: t.TempDir(), stdout: io.Discard, stderr: io.Discard, verifySpecs: []verifySpec{spec}}
	}

	if err := newBuilder(verifySpec{Name: "noisy", Argv: []string{"sh", "-c", "echo hi"}}).verifyRepo(); err != nil {
		t.Errorf("EmptyOK=false with output: err = %v, want nil", err)
	}
	if err := newBuilder(verifySpec{Name: "fmt", Argv: []string{"sh", "-c", "echo hi"}, EmptyOK: true}).verifyRepo(); !errors.Is(err, errVerifyFailed) {
		t.Errorf("EmptyOK=true with output: err = %v, want errVerifyFailed", err)
	}
}

// TestVerifyRepoGoDefault is the backward-compatibility check: a clean Go module
// verifies exactly as before, and an unformatted file fails verification.
func TestVerifyRepoGoDefault(t *testing.T) {
	for _, bin := range []string{"go", "gofmt"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not available", bin)
		}
	}
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":       "module example.com/verify\n\ngo 1.25\n",
		"main.go":      "package main\n\nfunc main() {}\n",
		"main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n",
	})
	b := &builder{dir: dir, runDir: t.TempDir(), stdout: io.Discard, stderr: io.Discard, verifySpecs: goVerifySpecs()}

	if err := b.verifyRepo(); err != nil {
		t.Fatalf("verifyRepo on a clean Go module: %v", err)
	}
	writeFiles(t, dir, map[string]string{"bad.go": "package main\n\nvar  x = 1\n"})
	if err := b.verifyRepo(); !errors.Is(err, errVerifyFailed) {
		t.Errorf("unformatted file: err = %v, want errVerifyFailed", err)
	}
}

// TestVerifySpecsPersistForResume checks that the commands a run used survive in
// run.json, so a resume verifies identically.
func TestVerifySpecsPersistForResume(t *testing.T) {
	dir := t.TempDir()
	want := []verifySpec{
		{Name: "gofmt", Argv: []string{"gofmt", "-l", "."}, EmptyOK: true},
		{Name: "make test", Argv: []string{"make", "test"}},
	}
	if err := writeRunState(dir, runState{Verify: want}); err != nil {
		t.Fatalf("writeRunState: %v", err)
	}
	st, err := readRunState(dir)
	if err != nil {
		t.Fatalf("readRunState: %v", err)
	}
	specs, err := resumeVerifySpecs(st)
	if err != nil {
		t.Fatalf("resumeVerifySpecs: %v", err)
	}
	if len(specs) != len(want) {
		t.Fatalf("specs = %v, want %v", specNames(specs), specNames(want))
	}
	for i := range want {
		if specs[i].Name != want[i].Name || strings.Join(specs[i].Argv, " ") != strings.Join(want[i].Argv, " ") || specs[i].EmptyOK != want[i].EmptyOK {
			t.Errorf("spec %d = %+v, want %+v", i, specs[i], want[i])
		}
	}
}

// TestResumeVerifySpecsFallback covers a run recorded before verification specs
// were persisted: resume falls back to auto-detection, and refuses when nothing
// is detected.
func TestResumeVerifySpecsFallback(t *testing.T) {
	goDir := t.TempDir()
	writeFiles(t, goDir, map[string]string{"go.mod": "module example.com/x\n"})
	specs, err := resumeVerifySpecs(runState{RepoDir: goDir})
	if err != nil {
		t.Fatalf("resumeVerifySpecs: %v", err)
	}
	if got := strings.Join(specNames(specs), ","); got != "gofmt,go vet,go test" {
		t.Errorf("specs = %v, want the Go default", specNames(specs))
	}

	if _, err := resumeVerifySpecs(runState{RepoDir: t.TempDir()}); !errors.Is(err, errVerifyNotConfigured) {
		t.Errorf("undetected legacy run: err = %v, want errVerifyNotConfigured", err)
	}
}
