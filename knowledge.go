package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/raihannajmi/orch-go/knowledge"
)

// knowledgeSession adapts a knowledge.Provider to the workflow.
//
// The workflow talks only to this type and to the knowledge.Provider interface,
// never to Obsidian or Graphify. Provider errors are reported as warnings and
// never fail the run: optional knowledge must not be able to break a build, but
// it must not vanish silently either, so every failure is surfaced on stderr.
type knowledgeSession struct {
	provider knowledge.Provider
	project  string
	repoDir  string
	warn     func(format string, args ...any)
}

// load returns context for the task, or nil if the provider fails.
func (s *knowledgeSession) load(task string) []knowledge.Document {
	docs, err := s.provider.Load(context.Background(), knowledge.Request{
		Task:    task,
		Project: s.project,
		RepoDir: s.repoDir,
	})
	if err != nil {
		s.warn("knowledge: load failed: %v", err)
		return nil
	}
	return docs
}

// capture records a finished, approved run, or warns if the provider fails.
func (s *knowledgeSession) capture(task, runDir string) {
	err := s.provider.Capture(context.Background(), knowledge.Result{
		Task:     task,
		Project:  s.project,
		RepoDir:  s.repoDir,
		RunDir:   runDir,
		Approved: true,
	})
	if err != nil {
		s.warn("knowledge: capture failed: %v", err)
	}
}

func (s *knowledgeSession) close() {
	if err := s.provider.Close(); err != nil {
		s.warn("knowledge: close failed: %v", err)
	}
}

// openKnowledge builds the knowledge session for a build run. Knowledge is
// opt-in (`--knowledge`) and configured by environment so the CLI stays small:
//
//	ORCH_KNOWLEDGE_VAULT    path to an Obsidian vault (required to enable)
//	ORCH_KNOWLEDGE_PROJECT  project folder under 01-Projects/ (default: repo dir name)
//
// A request for knowledge that cannot be satisfied is a warning, not an error:
// the build continues without it. A nil session means "no knowledge".
func openKnowledge(enabled bool, repoDir string, warn func(string, ...any)) *knowledgeSession {
	if !enabled {
		return nil
	}
	vault := os.Getenv("ORCH_KNOWLEDGE_VAULT")
	if vault == "" {
		warn("knowledge: ORCH_KNOWLEDGE_VAULT is not set; continuing without knowledge")
		return nil
	}
	project := os.Getenv("ORCH_KNOWLEDGE_PROJECT")
	if project == "" {
		project = filepath.Base(repoDir)
	}
	return &knowledgeSession{
		provider: knowledge.Open(knowledge.Config{Vault: vault, Project: project}),
		project:  project,
		repoDir:  repoDir,
		warn:     warn,
	}
}

// toContextDocs converts loaded knowledge into the workflow's prompt context.
func toContextDocs(docs []knowledge.Document) []contextDoc {
	out := make([]contextDoc, 0, len(docs))
	for _, d := range docs {
		out = append(out, contextDoc{label: d.Label, path: d.Path, body: d.Body})
	}
	return out
}
