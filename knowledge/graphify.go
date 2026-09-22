package knowledge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultGraphifyBin     = "graphify"
	defaultGraphifyTimeout = 20 * time.Second
)

// GraphifyConfig configures the optional Graphify enrichment layer.
//
// Graphify is a read-only query layer over a graph built from the vault (see the
// vault's .agents/rules/graphify.md): it enriches the context Obsidian already
// provides, and never replaces it.
type GraphifyConfig struct {
	Bin     string        // graphify executable; default "graphify" (looked up on PATH)
	Graph   string        // path to the graph.json to query
	Timeout time.Duration // bounds one query; default 20s
}

// Graphify queries the graphify CLI for context related to the task. It shells
// out to the documented command `graphify query "<question>" --graph <path>`,
// the same mechanism the vault's graphify rules use; it speaks no MCP protocol
// of its own.
type Graphify struct {
	bin     string
	graph   string
	timeout time.Duration
}

// NewGraphify returns a Graphify provider with defaults applied.
func NewGraphify(cfg GraphifyConfig) *Graphify {
	g := &Graphify{bin: cfg.Bin, graph: cfg.Graph, timeout: cfg.Timeout}
	if g.bin == "" {
		g.bin = defaultGraphifyBin
	}
	if g.timeout <= 0 {
		g.timeout = defaultGraphifyTimeout
	}
	return g
}

// Load asks graphify for a scoped subgraph around the task and returns it as one
// reference document. A missing CLI, a missing graph, a timeout, or a non-zero
// exit all surface as an error; the caller keeps whatever Obsidian provided and
// warns instead of failing.
func (g *Graphify) Load(ctx context.Context, req Request) ([]Document, error) {
	if g.graph == "" {
		return nil, errors.New("graphify: no graph path configured")
	}
	if _, err := os.Stat(g.graph); err != nil {
		return nil, fmt.Errorf("graphify: graph unavailable: %w", err)
	}
	bin, err := exec.LookPath(g.bin)
	if err != nil {
		return nil, fmt.Errorf("graphify: %q not found on PATH: %w", g.bin, err)
	}

	qctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	cmd := exec.CommandContext(qctx, bin, "query", req.Task, "--graph", g.graph)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = "no diagnostics"
		}
		return nil, fmt.Errorf("graphify query failed: %w (%s)", err, msg)
	}

	body := strings.TrimSpace(stdout.String())
	if body == "" {
		return nil, nil
	}
	return []Document{{Label: "graphify", Path: g.graph, Body: body}}, nil
}

// Capture is a no-op: Graphify is read-only, and Obsidian is where durable
// knowledge is written.
func (g *Graphify) Capture(context.Context, Result) error { return nil }

// Close does nothing: the provider holds no open resources.
func (g *Graphify) Close() error { return nil }
