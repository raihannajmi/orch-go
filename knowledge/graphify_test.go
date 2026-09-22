package knowledge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// stubProvider is a Provider whose results the composite tests control.
type stubProvider struct {
	docs     []Document
	loadErr  error
	captures int
	closed   bool
}

func (s *stubProvider) Load(context.Context, Request) ([]Document, error) { return s.docs, s.loadErr }
func (s *stubProvider) Capture(context.Context, Result) error             { s.captures++; return nil }
func (s *stubProvider) Close() error                                      { s.closed = true; return nil }

func TestGraphifyUnavailable(t *testing.T) {
	g := NewGraphify(GraphifyConfig{
		Bin:   "graphify-not-installed-xyz",
		Graph: writeFile(t, "graph.json", "{}"),
	})
	docs, err := g.Load(context.Background(), Request{Task: "x"})
	if err == nil {
		t.Fatal("Load with a missing graphify binary = nil error, want one")
	}
	if docs != nil {
		t.Errorf("docs = %v, want nil", docs)
	}
}

func TestGraphifyMissingGraph(t *testing.T) {
	g := NewGraphify(GraphifyConfig{
		Bin:   writeFile(t, "graphify", "#!/bin/sh\necho hi\n"),
		Graph: filepath.Join(t.TempDir(), "absent.json"),
	})
	if _, err := g.Load(context.Background(), Request{Task: "x"}); err == nil {
		t.Fatal("Load with a missing graph = nil error, want one")
	}
}

// TestGraphifyEnabledQueriesTheGraph covers the enabled path: the provider runs
// the documented `graphify query "<question>" --graph <path>` and returns its
// output as one reference document.
func TestGraphifyEnabledQueriesTheGraph(t *testing.T) {
	graph := writeFile(t, "graph.json", "{}")

	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "graphify")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\n" +
		"echo 'NODE Foo'\necho 'EDGE Foo --rel--> Bar'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write stand-in graphify: %v", err)
	}

	g := NewGraphify(GraphifyConfig{Bin: bin, Graph: graph})
	docs, err := g.Load(context.Background(), Request{Task: "fix the pty bug"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(docs) != 1 || docs[0].Label != "graphify" {
		t.Fatalf("docs = %+v, want one graphify document", docs)
	}
	if !strings.Contains(docs[0].Body, "NODE Foo") || !strings.Contains(docs[0].Body, "EDGE Foo --rel--> Bar") {
		t.Errorf("body = %q, want the graphify output", docs[0].Body)
	}

	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read recorded args: %v", err)
	}
	for _, want := range []string{"query", "fix the pty bug", "--graph", graph} {
		if !strings.Contains(string(args), want) {
			t.Errorf("graphify was not invoked with %q; args:\n%s", want, args)
		}
	}
}

func TestGraphifyCaptureIsReadOnly(t *testing.T) {
	g := NewGraphify(GraphifyConfig{
		Bin:   writeFile(t, "graphify", "#!/bin/sh\necho hi\n"),
		Graph: writeFile(t, "graph.json", "{}"),
	})
	if err := g.Capture(context.Background(), Result{Approved: true}); err != nil {
		t.Errorf("Capture = %v, want nil (Graphify is read-only)", err)
	}
}

func TestOpenGraphifyDisabled(t *testing.T) {
	if _, ok := Open(Config{Vault: t.TempDir(), Project: "p"}).(*Obsidian); !ok {
		t.Errorf("Open without Graphify = %T, want *Obsidian", Open(Config{Vault: t.TempDir(), Project: "p"}))
	}
}

func TestOpenGraphifyEnabled(t *testing.T) {
	p := Open(Config{
		Vault:    t.TempDir(),
		Project:  "p",
		Graphify: &GraphifyConfig{Graph: "graph.json"},
	})
	if _, ok := p.(*Composite); !ok {
		t.Errorf("Open with Graphify = %T, want *Composite", p)
	}
}

func TestCompositeMergesContext(t *testing.T) {
	primary := &stubProvider{docs: []Document{{Label: "project context", Path: "p.md", Body: "A"}}}
	enricher := &stubProvider{docs: []Document{{Label: "graphify", Path: "graph.json", Body: "B"}}}
	c := &Composite{Primary: primary, Enricher: enricher}

	docs, err := c.Load(context.Background(), Request{Task: "t"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(docs) != 2 || docs[0].Body != "A" || docs[1].Body != "B" {
		t.Errorf("docs = %+v, want the primary then the enricher", docs)
	}
}

// TestCompositeKeepsPrimaryWhenEnricherFails is requirement 5: a broken Graphify
// enrichment must not discard the Obsidian context.
func TestCompositeKeepsPrimaryWhenEnricherFails(t *testing.T) {
	primary := &stubProvider{docs: []Document{{Label: "project context", Body: "A"}}}
	enricher := &stubProvider{loadErr: errors.New("graphify exploded")}
	c := &Composite{Primary: primary, Enricher: enricher}

	docs, err := c.Load(context.Background(), Request{Task: "t"})
	if err == nil {
		t.Error("Load = nil error, want the enricher error reported")
	}
	if len(docs) != 1 || docs[0].Body != "A" {
		t.Errorf("docs = %+v, want the primary document kept", docs)
	}
}

func TestCompositePrimaryFailureFailsLoad(t *testing.T) {
	primary := &stubProvider{loadErr: errors.New("vault on fire")}
	c := &Composite{Primary: primary, Enricher: &stubProvider{}}

	if docs, err := c.Load(context.Background(), Request{Task: "t"}); err == nil || docs != nil {
		t.Errorf("Load = %v, %v; want a failure with no documents", docs, err)
	}
}

func TestCompositeCaptureUsesPrimaryOnly(t *testing.T) {
	primary := &stubProvider{}
	enricher := &stubProvider{}
	c := &Composite{Primary: primary, Enricher: enricher}

	if err := c.Capture(context.Background(), Result{Approved: true}); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if primary.captures != 1 {
		t.Errorf("primary captures = %d, want 1", primary.captures)
	}
	if enricher.captures != 0 {
		t.Errorf("enricher captures = %d, want 0 (Graphify is read-only)", enricher.captures)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !primary.closed || !enricher.closed {
		t.Errorf("Close did not close both providers: primary=%v enricher=%v", primary.closed, enricher.closed)
	}
}
