package knowledge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Vault layout this provider understands. It mirrors the shared Obsidian
// structure and never invents new folders.
const (
	projectsDir  = "01-Projects"
	decisionsDir = "03-Decisions"
	problemsDir  = "04-Problems"
	learningDir  = "05-Learning"

	buildLogName = "build-log.md"
)

// Limits keep a single load small. Knowledge is scoped to the task, never the
// whole vault, and a long note is truncated before it reaches a prompt.
const (
	maxNotesPerDir = 3
	maxNoteBytes   = 4000
	maxTaskBytes   = 200
)

// Obsidian reads context from, and records durable knowledge in, an Obsidian
// vault that already follows the shared structure. It is the first Provider;
// Graphify can later implement the same interface.
type Obsidian struct {
	Vault   string // vault root
	Project string // folder name under 01-Projects/
}

// Load gathers the project's context note plus notes related to the task. It
// reads one note per matched decision/problem/learning, so it never reads the
// whole vault and a task with no related notes loads only the project context.
func (o *Obsidian) Load(_ context.Context, req Request) ([]Document, error) {
	if o.Vault == "" {
		return nil, nil
	}

	var docs []Document
	if path := o.projectIndex(); path != "" {
		doc, err := o.readDocument("project context", path)
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}

	terms := keywords(req.Task)
	for _, group := range []struct{ dir, label string }{
		{decisionsDir, "decision"},
		{problemsDir, "problem"},
		{learningDir, "learning"},
	} {
		for _, path := range o.relatedNotes(group.dir, terms) {
			doc, err := o.readDocument(group.label, path)
			if err != nil {
				return nil, err
			}
			docs = append(docs, doc)
		}
	}
	return docs, nil
}

// Capture records one approved run in the project's build log. It updates that
// single note rather than creating a note per run, and it writes only durable
// facts — never prompts, transcripts, or debugging output. An unapproved run,
// or one with no project, is not captured.
func (o *Obsidian) Capture(_ context.Context, res Result) error {
	if o.Vault == "" || o.Project == "" || !res.Approved {
		return nil
	}

	note := filepath.Join(o.Vault, projectsDir, o.Project, buildLogName)
	if err := os.MkdirAll(filepath.Dir(note), 0o755); err != nil {
		return err
	}

	entry := []byte(buildLogEntry(res))
	body, err := os.ReadFile(note)
	switch {
	case err == nil:
		body = append(body, entry...)
	case os.IsNotExist(err):
		body = append([]byte(buildLogHeader), entry...)
	default:
		return err
	}
	return os.WriteFile(note, body, 0o644)
}

// Close does nothing: the provider holds no open resources.
func (o *Obsidian) Close() error { return nil }

// projectIndex finds the project's context note: the conventional
// <Project>.md, then index/README, then the folder's only note.
func (o *Obsidian) projectIndex() string {
	if o.Project == "" {
		return ""
	}
	dir := filepath.Join(o.Vault, projectsDir, o.Project)
	for _, name := range []string{o.Project + ".md", "index.md", "README.md"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	if notes := markdownFiles(dir); len(notes) == 1 {
		return notes[0]
	}
	return ""
}

// relatedNotes picks up to maxNotesPerDir notes whose filename matches a task
// keyword, best match first. Matching on the filename keeps it cheap and scoped.
func (o *Obsidian) relatedNotes(dir string, terms []string) []string {
	if len(terms) == 0 {
		return nil
	}
	type scored struct {
		path  string
		score int
	}
	var hits []scored
	for _, path := range markdownFiles(filepath.Join(o.Vault, dir)) {
		name := strings.ToLower(filepath.Base(path))
		score := 0
		for _, term := range terms {
			if strings.Contains(name, term) {
				score++
			}
		}
		if score > 0 {
			hits = append(hits, scored{path, score})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].path < hits[j].path
	})
	if len(hits) > maxNotesPerDir {
		hits = hits[:maxNotesPerDir]
	}
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.path)
	}
	return out
}

func (o *Obsidian) readDocument(label, path string) (Document, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Document{}, err
	}
	rel, err := filepath.Rel(o.Vault, path)
	if err != nil {
		rel = path
	}
	return Document{Label: label, Path: filepath.ToSlash(rel), Body: truncate(string(body))}, nil
}

// markdownFiles lists the .md files directly inside dir; a missing directory is
// not an error, it just has no notes.
func markdownFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// keywords reduces a task to the lowercase terms worth matching against note
// filenames. Short words and duplicates are dropped so matching stays specific.
func keywords(task string) []string {
	fields := strings.FieldsFunc(strings.ToLower(task), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	seen := make(map[string]bool, len(fields))
	var out []string
	for _, f := range fields {
		if len(f) < 4 || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxNoteBytes {
		return s
	}
	return strings.ToValidUTF8(s[:maxNoteBytes], "") + "\n… (truncated)"
}

const buildLogHeader = "# orch build log\n\n" +
	"Durable record of approved `orch build` runs for this project. " +
	"One entry per run; prompts and transcripts are never written here.\n\n"

// buildLogEntry renders the durable facts of one run: when, what, and how it
// ended. It deliberately omits anything transient.
func buildLogEntry(res Result) string {
	task := strings.TrimSpace(res.Task)
	if len(task) > maxTaskBytes {
		task = strings.ToValidUTF8(task[:maxTaskBytes], "") + "…"
	}
	return fmt.Sprintf("## %s — %s\n\n- Run: `%s`\n- Result: APPROVED\n\n",
		time.Now().UTC().Format("2006-01-02"), task, filepath.Base(res.RunDir))
}
