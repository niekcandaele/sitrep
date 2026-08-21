// Package buildinfo holds the build identity of the sitrep binary: the values
// injected at link time and the one-line string used to report them.
package buildinfo

import "fmt"

// Name is the binary name reported in the version string.
const Name = "sitrep"

// Version, Commit and Date are overridden at link time with
// -X github.com/niekcandaele/sitrep/internal/buildinfo.<Name>=<value>.
// They are vars, not consts, so the linker can set them; the goreleaser
// config and the Makefile both reference these exact paths.
var (
	// Version is the released version, e.g. "0.1.0".
	Version = "dev"
	// Commit is the short commit hash the binary was built from.
	Commit = "none"
	// Date is the RFC 3339 build timestamp.
	Date = "unknown"
)

// String renders the one-line version report, e.g.
// "sitrep 0.1.0 (commit abc1234, built 2026-08-21T10:00:00Z)".
func String() string {
	return fmt.Sprintf("%s %s (commit %s, built %s)", Name, Version, Commit, Date)
}
