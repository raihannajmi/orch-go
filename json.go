package main

import (
	"encoding/json"
	"fmt"
	"io"
)

// Machine-readable output contract.
//
// With --json, orch writes exactly one JSON document to stdout and nothing else
// there: the interactive agents' terminal output and every diagnostic are
// redirected to stderr, so stdout is valid JSON that a pipe can consume. Errors
// are represented inside the document (a stable {code,message}) while the
// process still exits with the documented code. The schemas below are the
// explicit, version-stable shapes; only fields relevant to a command are
// emitted.
type jsonError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// jsonHeader is shared by every response so the envelope is consistent.
type jsonHeader struct {
	Command string     `json:"command"`
	OK      bool       `json:"ok"`
	Error   *jsonError `json:"error,omitempty"`
}

type jsonAgent struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Summary   string `json:"summary"`
}

type jsonRun struct {
	ID            string `json:"id"`
	Stages        int    `json:"stages"`
	Verdict       string `json:"verdict"`
	TimedOut      string `json:"timedOut,omitempty"`
	Status        string `json:"status,omitempty"`
	LastCompleted int    `json:"lastCompleted,omitempty"`
	VerifyResult  string `json:"verifyResult,omitempty"`
	Failure       string `json:"failure,omitempty"`
	ExitCode      int    `json:"exitCode,omitempty"`
}

type jsonStage struct {
	Name   string `json:"name"`
	Agent  string `json:"agent"`
	Status string `json:"status"`
}

type jsonRunDetail struct {
	ID           string      `json:"id"`
	Dir          string      `json:"dir"`
	Status       string      `json:"status"`
	Failure      string      `json:"failure,omitempty"`
	ExitCode     int         `json:"exitCode,omitempty"`
	VerifyResult string      `json:"verifyResult,omitempty"`
	Stages       []jsonStage `json:"stages"`
}

type jsonArtifact struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Per-command responses. Embedding jsonHeader keeps the envelope identical.
type listResponse struct {
	jsonHeader
	Agents []jsonAgent `json:"agents"`
}

type statusResponse struct {
	jsonHeader
	Runs []jsonRun `json:"runs"`
}

type logsResponse struct {
	jsonHeader
	RunID     string         `json:"runId"`
	RunDir    string         `json:"runDir"`
	Artifacts []jsonArtifact `json:"artifacts"`
}

type runResponse struct {
	jsonHeader
	Run *jsonRunDetail `json:"run,omitempty"`
}

// errorResponse is a failure that carries no command-specific payload.
type errorResponse struct {
	jsonHeader
}

// jsonRequested reports whether --json was asked for, stopping at the "--"
// terminator so a task argument that happens to be "--json" is not mistaken for
// the flag.
func jsonRequested(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--json" {
			return true
		}
	}
	return false
}

// emitJSON writes one JSON document. The error is returned so the caller can
// keep the process exit code meaningful if writing itself fails.
func emitJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// emitFailure writes a JSON error document for a command.
func emitFailure(w io.Writer, command string, code int, msg string) error {
	return emitJSON(w, errorResponse{jsonHeader{
		Command: command,
		OK:      false,
		Error:   &jsonError{Code: code, Message: msg},
	}})
}

// cliError reports a command error and returns the exit code: a single JSON
// document on stdout when --json was requested, otherwise a human line on
// stderr. Errors therefore never corrupt the JSON stream, and the exit code is
// the same either way.
func cliError(command string, asJSON bool, stdout, stderr io.Writer, code int, format string, args ...any) int {
	msg := fmt.Sprintf(format, args...)
	if asJSON {
		if err := emitFailure(stdout, command, code, msg); err != nil {
			fmt.Fprintf(stderr, "orch: %v\n", err)
			return 1
		}
		return code
	}
	fmt.Fprintf(stderr, "orch: %s\n", msg)
	return code
}

// newRunResponse builds the build/resume result from the persisted run state.
// The task text is deliberately excluded: it is user content that can be
// sensitive, and it is not needed to interpret the outcome.
func newRunResponse(command, runDir string, st *runState, exitCode int, err error) runResponse {
	resp := runResponse{jsonHeader: jsonHeader{Command: command, OK: err == nil}}
	if st != nil {
		detail := &jsonRunDetail{
			ID:           st.ID,
			Dir:          runDir,
			Status:       st.Status,
			Failure:      st.Failure,
			ExitCode:     st.ExitCode,
			VerifyResult: st.VerifyResult,
			Stages:       []jsonStage{},
		}
		for _, s := range st.Stages {
			detail.Stages = append(detail.Stages, jsonStage{Name: s.Name, Agent: s.Agent, Status: s.Status})
		}
		resp.Run = detail
	}
	if err != nil {
		resp.Error = &jsonError{Code: exitCode, Message: err.Error()}
	}
	return resp
}
