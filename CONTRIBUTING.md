# Contributing

Thanks for helping improve orch. It is a small Go CLI, so this is short.

## Development

Requires Go (see the `go` directive in `go.mod`; it is a toolchain floor, so an
older toolchain is upgraded to it automatically) on macOS or Linux. CI builds the
latest patch of the same minor.

```sh
go build ./...          # compile
go test ./...           # unit + integration tests
go test -race ./...     # race detector (CI runs this too)
go vet ./...
gofmt -l .              # must print nothing
```

Security scan, with no local install required:

```sh
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
```

The tests use stand-in shell scripts on `PATH` instead of real agents, so no agent
needs to be installed. Tests that need `git` or the Go toolchain skip when it is
not available.

## Rules the code must keep

- Never add an approval-skipping flag, and never relax the denylist in
  `agent.go`. Native permission prompts must stay intact.
- Do not run a shell: pass argv directly (see `verify.go`, `agent.go`,
  `knowledge/graphify.go`).
- Keep runtime artifacts private (`0700` directories, `0600` files), and keep
  `status`/`logs` able to read artifacts written by older runs.
- Preserve human-readable CLI output and existing exit codes; `--json` is
  additive.

## Before you open a pull request

Run the commands above, including `go test -race ./...` and `gofmt`. Add tests for
new behaviour and for error paths. Keep the change focused and explain what
changed and why.

## Reporting a security issue

Please do not open a public issue; see [SECURITY.md](SECURITY.md).
