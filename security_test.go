package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestEnsureStateDirRejectsSymlink guards the run-state directory: a repository
// that ships a symlinked .orch must not be able to redirect orch's writes
// (prompts, transcripts, state) to a directory of its choosing.
func TestEnsureStateDirRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	link := filepath.Join(root, buildStateDir)
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks are not generally available on Windows")
		}
		t.Skipf("symlink unavailable: %v", err)
	}

	if err := ensureStateDir(link); err == nil {
		t.Fatal("ensureStateDir followed a symlinked state directory")
	}
	if _, _, err := createRunDir(link); err == nil {
		t.Error("createRunDir accepted a symlinked state directory")
	}
	if entries, err := os.ReadDir(target); err == nil && len(entries) != 0 {
		t.Errorf("orch wrote into the symlink target: %v", entries)
	}

	// An ordinary directory is created and accepted.
	normal := filepath.Join(t.TempDir(), buildStateDir)
	if err := ensureStateDir(normal); err != nil {
		t.Errorf("ensureStateDir(%s) = %v, want nil", normal, err)
	}
	if info, err := os.Stat(normal); err != nil || !info.IsDir() {
		t.Errorf("state dir was not created: %v", err)
	}
}

// TestCIToolchainMatchesGoDirective guards the CI toolchain policy. go.mod's go
// directive is the module's minimum Go version and stays at that minimum; CI
// deliberately installs the latest patched release of the same minor
// (go-version: '1.25.x') so builds are security-patched. The two must agree, or a
// minor bump in go.mod would quietly leave CI testing a different Go release.
func TestCIToolchainMatchesGoDirective(t *testing.T) {
	gomod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	m := regexp.MustCompile(`(?m)^go (\d+\.\d+)(?:\.\d+)?$`).FindSubmatch(gomod)
	if m == nil {
		t.Fatal("go.mod has no go directive")
	}
	minor := string(m[1]) // e.g. "1.25", patch component ignored

	ci, err := os.ReadFile(filepath.Join(".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	if want := "go-version: '" + minor + ".x'"; !strings.Contains(string(ci), want) {
		t.Errorf("CI does not set %q to match the go directive's minor %s", want, minor)
	}
}
