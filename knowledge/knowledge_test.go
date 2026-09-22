package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeNote(t *testing.T, vault, rel, body string) {
	t.Helper()
	path := filepath.Join(vault, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func TestNoopProvider(t *testing.T) {
	var p Provider = Noop{}
	docs, err := p.Load(context.Background(), Request{Task: "anything"})
	if err != nil || docs != nil {
		t.Errorf("Noop.Load = %v, %v; want nil, nil", docs, err)
	}
	if err := p.Capture(context.Background(), Result{Approved: true}); err != nil {
		t.Errorf("Noop.Capture = %v, want nil", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("Noop.Close = %v, want nil", err)
	}
}

func TestOpenWithoutVaultIsNoop(t *testing.T) {
	if _, ok := Open(Config{Project: "orch"}).(Noop); !ok {
		t.Errorf("Open with no vault = %T, want Noop", Open(Config{Project: "orch"}))
	}
}

// TestObsidianLoadScopesToTask checks that Load returns the project context and
// only the notes related to the task, never the whole vault.
func TestObsidianLoadScopesToTask(t *testing.T) {
	vault := t.TempDir()
	writeNote(t, vault, "01-Projects/orch/orch.md", "# orch project\n\nCLI orchestrator.")
	writeNote(t, vault, "03-Decisions/2026-09-21-real-pty-agent-orchestration.md", "# Decision: PTY orchestration")
	writeNote(t, vault, "03-Decisions/2026-09-01-unrelated-food.md", "# Decision: lunch")
	writeNote(t, vault, "05-Learning/postgres-pool.md", "# Learning: postgres")

	o := &Obsidian{Vault: vault, Project: "orch"}
	docs, err := o.Load(context.Background(), Request{Task: "harden the agent pty orchestration"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	paths := map[string]bool{}
	for _, d := range docs {
		paths[d.Path] = true
	}
	for _, want := range []string{
		"01-Projects/orch/orch.md",
		"03-Decisions/2026-09-21-real-pty-agent-orchestration.md",
	} {
		if !paths[want] {
			t.Errorf("Load did not return %q; got %v", want, keys(paths))
		}
	}
	if paths["03-Decisions/2026-09-01-unrelated-food.md"] {
		t.Errorf("Load returned an unrelated note: %v", keys(paths))
	}
}

func TestObsidianLoadMissingVaultIsEmpty(t *testing.T) {
	o := &Obsidian{Vault: filepath.Join(t.TempDir(), "absent"), Project: "orch"}
	docs, err := o.Load(context.Background(), Request{Task: "anything"})
	if err != nil {
		t.Fatalf("Load on missing vault = %v, want nil error", err)
	}
	if len(docs) != 0 {
		t.Errorf("Load = %d docs, want none", len(docs))
	}
}

// TestObsidianCaptureUpdatesOneNote covers the "no note per run" rule: the first
// approved run creates the project log, later runs append to it.
func TestObsidianCaptureUpdatesOneNote(t *testing.T) {
	vault := t.TempDir()
	o := &Obsidian{Vault: vault, Project: "orch"}
	note := filepath.Join(vault, "01-Projects", "orch", buildLogName)

	if err := o.Capture(context.Background(), Result{
		Task: "add the knowledge layer", RunDir: "/repo/.orch/20260101-120000", Approved: true,
	}); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	body := readFile(t, note)
	if !strings.Contains(body, "add the knowledge layer") || !strings.Contains(body, "20260101-120000") {
		t.Errorf("build log missing the run facts:\n%s", body)
	}
	if strings.Contains(body, "/repo/.orch") {
		t.Errorf("build log should not record the absolute run path:\n%s", body)
	}

	if err := o.Capture(context.Background(), Result{
		Task: "second task", RunDir: "/repo/.orch/20260102-090000", Approved: true,
	}); err != nil {
		t.Fatalf("second Capture: %v", err)
	}
	body = readFile(t, note)
	if !strings.Contains(body, "add the knowledge layer") || !strings.Contains(body, "second task") {
		t.Errorf("build log did not accumulate both runs:\n%s", body)
	}

	entries, err := os.ReadDir(filepath.Join(vault, "01-Projects", "orch"))
	if err != nil {
		t.Fatalf("read project dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("project dir has %d files, want exactly the build log", len(entries))
	}
}

func TestObsidianCaptureSkipsNonDurableRuns(t *testing.T) {
	vault := t.TempDir()
	note := filepath.Join(vault, "01-Projects", "orch", buildLogName)

	o := &Obsidian{Vault: vault, Project: "orch"}
	if err := o.Capture(context.Background(), Result{Task: "failed run", Approved: false}); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if _, err := os.Stat(note); !os.IsNotExist(err) {
		t.Errorf("an unapproved run was captured: stat err = %v", err)
	}

	noProject := &Obsidian{Vault: vault}
	if err := noProject.Capture(context.Background(), Result{Task: "x", Approved: true}); err != nil {
		t.Fatalf("Capture without project: %v", err)
	}
	if _, err := os.Stat(note); !os.IsNotExist(err) {
		t.Errorf("a run without a project was captured: stat err = %v", err)
	}
}

func TestKeywords(t *testing.T) {
	// Words shorter than four characters are dropped so common short words do
	// not match half the vault; duplicates collapse.
	got := strings.Join(keywords("Harden the Agent PTY, orchestration and agent!"), ",")
	if got != "harden,agent,orchestration" {
		t.Errorf("keywords = %q", got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
