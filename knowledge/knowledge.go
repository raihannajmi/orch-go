// Package knowledge defines orch's optional knowledge-provider boundary.
//
// The orchestrator never talks to Obsidian or Graphify directly. It loads
// context before a workflow and captures durable knowledge after one through
// the Provider interface, and the default implementation is Noop. That keeps
// `orch build "task"` working with no external knowledge system, and lets a new
// backend (Graphify, or another vault) be added by implementing Provider alone,
// without touching the workflow.
package knowledge

import "context"

// Document is one piece of knowledge returned by Load, ready to be inlined into
// a stage prompt.
type Document struct {
	Label string // short human label, e.g. "project context" or "decision"
	Path  string // vault-relative path, so a prompt can cite the note
	Body  string // Markdown content
}

// Request describes the work that is about to start.
type Request struct {
	Task    string // the task text handed to the workflow
	Project string // project name, e.g. the 01-Projects/<Project> folder
	RepoDir string // repository the agents work in
}

// Result describes a finished run. Only approved runs carry durable knowledge.
type Result struct {
	Task     string
	Project  string
	RepoDir  string
	RunDir   string // artifact directory, .orch/<run-id>
	Approved bool
}

// Provider is the knowledge boundary. Implementations are used from the single
// workflow goroutine, so they need no internal locking.
type Provider interface {
	// Load returns context relevant to req, or nil when there is none. The
	// workflow inlines the result into the planning stage.
	Load(ctx context.Context, req Request) ([]Document, error)
	// Capture persists durable knowledge about a run that finished.
	Capture(ctx context.Context, res Result) error
	// Close releases any resources the provider holds.
	Close() error
}

// Noop is the default provider: it keeps no knowledge and never fails. It is
// what makes the knowledge layer optional.
type Noop struct{}

// Load returns no documents.
func (Noop) Load(context.Context, Request) ([]Document, error) { return nil, nil }

// Capture discards the result.
func (Noop) Capture(context.Context, Result) error { return nil }

// Close does nothing.
func (Noop) Close() error { return nil }

// Config selects and configures a provider.
type Config struct {
	Vault    string          // absolute path to an Obsidian vault root
	Project  string          // project folder name under 01-Projects/
	Graphify *GraphifyConfig // nil disables the optional Graphify enrichment layer
}

// Open returns the provider for cfg. An empty vault yields Noop, so a partially
// configured request needs no special casing by the caller.
//
// Obsidian is always the source of truth. When Graphify is configured it is
// wrapped around Obsidian as a read-only enricher, never as a replacement.
func Open(cfg Config) Provider {
	if cfg.Vault == "" {
		return Noop{}
	}
	primary := Provider(&Obsidian{Vault: cfg.Vault, Project: cfg.Project})
	if cfg.Graphify == nil {
		return primary
	}
	return &Composite{Primary: primary, Enricher: NewGraphify(*cfg.Graphify)}
}

// Composite pairs a primary provider (Obsidian, the source of truth) with an
// optional enricher (Graphify).
//
// Load merges their context. A failed primary fails the load; a failed enricher
// does not — its error is returned alongside the primary's documents, so a
// broken enricher cannot take Obsidian down with it. Capture only ever reaches
// the primary, so durable knowledge keeps going to Obsidian.
type Composite struct {
	Primary  Provider
	Enricher Provider
}

// Load returns the primary's documents plus any the enricher adds. The enricher
// error is reported separately so the caller can warn without losing context.
func (c *Composite) Load(ctx context.Context, req Request) ([]Document, error) {
	docs, err := c.Primary.Load(ctx, req)
	if err != nil {
		return nil, err
	}
	extra, err := c.Enricher.Load(ctx, req)
	return append(docs, extra...), err
}

// Capture writes durable knowledge to the primary provider only.
func (c *Composite) Capture(ctx context.Context, res Result) error {
	return c.Primary.Capture(ctx, res)
}

// Close closes both providers, preferring to report the primary's error.
func (c *Composite) Close() error {
	err := c.Primary.Close()
	if cerr := c.Enricher.Close(); err == nil {
		err = cerr
	}
	return err
}
