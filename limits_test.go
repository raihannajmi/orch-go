package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLimitWriter(t *testing.T) {
	t.Run("unlimited passes through", func(t *testing.T) {
		var buf bytes.Buffer
		w := newLimitWriter(&buf, 0)
		if _, err := w.Write([]byte("abcdef")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if buf.String() != "abcdef" {
			t.Errorf("got %q, want abcdef", buf.String())
		}
	})

	t.Run("exactly at the limit has no notice", func(t *testing.T) {
		var buf bytes.Buffer
		w := newLimitWriter(&buf, 6)
		_, _ = w.Write([]byte("abc"))
		_, _ = w.Write([]byte("def"))
		if buf.String() != "abcdef" {
			t.Errorf("got %q, want abcdef (no truncation notice exactly at the limit)", buf.String())
		}
	})

	t.Run("over the limit truncates once and reports bytes consumed", func(t *testing.T) {
		var buf bytes.Buffer
		w := newLimitWriter(&buf, 4)
		n, err := w.Write([]byte("abcdef"))
		if err != nil || n != 6 {
			t.Fatalf("Write = %d, %v; want 6, nil (over-limit bytes must be reported consumed)", n, err)
		}
		_, _ = w.Write([]byte("more"))
		got := buf.String()
		if !strings.HasPrefix(got, "abcd") {
			t.Errorf("output = %q, want it to start with abcd", got)
		}
		if strings.Count(got, "truncated") != 1 {
			t.Errorf("output = %q, want exactly one truncation notice", got)
		}
		if strings.Contains(got, "more") {
			t.Errorf("output = %q, want bytes after the limit discarded", got)
		}
	})

	t.Run("writes after the limit are discarded", func(t *testing.T) {
		var buf bytes.Buffer
		w := newLimitWriter(&buf, 2)
		_, _ = w.Write([]byte("ab"))
		n, err := w.Write([]byte("cd"))
		if err != nil || n != 2 {
			t.Fatalf("Write after the limit = %d, %v; want 2, nil", n, err)
		}
		if strings.Contains(buf.String(), "cd") {
			t.Errorf("post-limit bytes were written: %q", buf.String())
		}
	})
}

func TestMaxLogBytesEnv(t *testing.T) {
	tests := []struct {
		env  string
		want int64
	}{
		{"4096", 4096},
		{"0", 0},
		{"unlimited", 0},
		{"nonsense", defaultMaxLogBytes},
		{"-5", defaultMaxLogBytes},
	}
	for _, tt := range tests {
		t.Setenv("ORCH_MAX_LOG_BYTES", tt.env)
		if got := maxLogBytes(); got != tt.want {
			t.Errorf("maxLogBytes() with %q = %d, want %d", tt.env, got, tt.want)
		}
	}
}

// TestStageTranscriptIsTruncated proves the limit applies to a real stage: a
// noisy agent's log is cut with an explicit notice, while the Markdown artifact
// (which carries the completion marker) is left intact.
func TestStageTranscriptIsTruncated(t *testing.T) {
	t.Setenv("ORCH_MAX_LOG_BYTES", "1024")

	dir := t.TempDir()
	runDir := t.TempDir()
	binDir := t.TempDir()
	artifact := filepath.Join(runDir, "1-plan.md")

	script := "#!/bin/sh\n" +
		"i=0; while [ $i -lt 500 ]; do echo 'noise noise noise noise noise'; i=$((i+1)); done\n" +
		"printf 'planned\\n" + stageMarker + "\\n' > '" + artifact + "'\n"
	if err := os.WriteFile(filepath.Join(binDir, "agy"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stand-in agent: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stage := interactiveStage(dir, runDir, nil, io.Discard, defaultStageTimeout)
	text, err := stage("agy", "1-plan", "prompt")
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if text != "planned" {
		t.Errorf("stage text = %q, want planned", text)
	}

	logPath := filepath.Join(runDir, "1-plan.log")
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	// The cap plus the truncation notice; generously bounded to allow the notice.
	if info.Size() > 1024+256 {
		t.Errorf("log size = %d, want the transcript bounded near 1024 bytes", info.Size())
	}
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(body), "transcript truncated") {
		t.Errorf("log has no truncation notice:\n%s", body)
	}

	// The artifact the workflow depends on must never be truncated.
	art, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if !strings.HasSuffix(strings.TrimSpace(string(art)), stageMarker) {
		t.Errorf("artifact lost its completion marker:\n%s", art)
	}
}
