## What and why

<!-- One or two sentences: what changes, and why. Link an issue if there is one. -->

## Checklist

- [ ] `gofmt -l .` prints nothing
- [ ] `go vet ./...`, `go test ./...`, and `go test -race ./...` pass
- [ ] No approval-skipping flag added and the denylist in `agent.go` is unchanged
- [ ] No shell introduced: commands still run as argv
- [ ] Runtime artifacts stay private (`0700`/`0600`); human output and exit codes unchanged
- [ ] Tests cover the new behaviour and its error paths
