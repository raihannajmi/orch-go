package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// defaultMaxLogBytes bounds an on-disk transcript. An agent's terminal output is
// tee'd to <stage>.log, and a noisy or wedged agent could otherwise fill the
// disk. The cap guards the file only:
//
//   - live stdout is never cut, so the interactive session is unchanged;
//   - the stage's Markdown artifact carries the completion marker and is written
//     by the agent to a different file, so it is never truncated;
//   - truncation is explicit: one notice is appended to the log, so a reader
//     knows the transcript is incomplete rather than silently shortened.
//
// ORCH_MAX_LOG_BYTES sets the cap in bytes; "0" or "unlimited" disables it. An
// unparseable value falls back to the default rather than disabling the bound.
const defaultMaxLogBytes = 64 << 20 // 64 MiB

func maxLogBytes() int64 {
	v := strings.TrimSpace(os.Getenv("ORCH_MAX_LOG_BYTES"))
	switch v {
	case "":
		return defaultMaxLogBytes
	case "0", "unlimited":
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return defaultMaxLogBytes
	}
	return n
}

// limitWriter writes to w up to limit bytes, appends one explicit truncation
// notice, and then discards everything further. It reports over-limit bytes as
// consumed so an io.Copy feeding it keeps running and the session is not
// disturbed. A limit <= 0 means unlimited (newLimitWriter returns w itself).
type limitWriter struct {
	w         io.Writer
	limit     int64
	written   int64
	truncated bool
}

func newLimitWriter(w io.Writer, limit int64) io.Writer {
	if limit <= 0 {
		return w
	}
	return &limitWriter{w: w, limit: limit}
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.written >= l.limit {
		return len(p), l.note()
	}
	if remaining := l.limit - l.written; int64(len(p)) > remaining {
		n, err := l.w.Write(p[:remaining])
		l.written += int64(n)
		if err != nil {
			return n, err
		}
		return len(p), l.note()
	}
	n, err := l.w.Write(p)
	l.written += int64(n)
	return n, err
}

// note appends the truncation notice exactly once.
func (l *limitWriter) note() error {
	if l.truncated {
		return nil
	}
	l.truncated = true
	_, err := fmt.Fprintf(l.w, "\n[orch: transcript truncated at %d bytes; set ORCH_MAX_LOG_BYTES to change the limit]\n", l.limit)
	return err
}
