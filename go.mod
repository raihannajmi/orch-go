module github.com/raihannajmi/orch-go

// The go directive is the module's minimum Go version and doubles as a toolchain
// floor: with GOTOOLCHAIN=auto an older local toolchain is upgraded to at least
// this release. It is kept at the first release that fixes the standard-library
// advisory govulncheck reports for this code (GO-2026-4602, reached through
// os.ReadDir; fixed in 1.25.8), so the documented `govulncheck ./...` run is
// clean on any supported toolchain. CI builds the latest patch of this minor.
go 1.25.8

require (
	github.com/creack/pty v1.1.24
	golang.org/x/term v0.45.0
)

require golang.org/x/sys v0.47.0
