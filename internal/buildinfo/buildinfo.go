// Package buildinfo holds the build identity of the sitrep binary: the values
// injected at link time and the one-line string used to report them.
package buildinfo

import (
	"fmt"
	"runtime/debug"
)

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
	// Date is the RFC 3339 timestamp of the commit the binary was built from.
	// It is the commit's time rather than the build's so that two builds of one
	// commit are identical; nothing here parses it, so a release pipeline may
	// stamp any string a human can read.
	Date = "unknown"
)

// The placeholders the three vars hold when nothing was injected. A value still
// equal to its placeholder is the signal that the Go toolchain's own build
// information is worth consulting.
const (
	devVersion  = "dev"
	noCommit    = "none"
	unknownDate = "unknown"
)

// readBuildInfo is the toolchain's build-information reader. It is a package
// variable rather than a direct call so the fallback below can be tested
// without depending on how the test binary itself happened to be built —
// mirroring gitlab.getenv.
var readBuildInfo = debug.ReadBuildInfo

// String renders the one-line version report, e.g.
// "sitrep 0.1.0 (commit abc1234, built 2026-08-21T10:00:00Z)".
//
// A release archive is linked with all three values and reports them exactly as
// injected. A `go install github.com/niekcandaele/sitrep/cmd/sitrep@latest`
// build has no ldflags at all, so it would otherwise report "sitrep dev (commit
// none, built unknown)" — useless in a bug report, and go install is a
// documented install path. Where the toolchain embedded the module version and
// the VCS stamp, those fill the gaps. Injected values always win outright.
func String() string {
	version, commit, date := Version, Commit, Date

	if version == devVersion || commit == noCommit || date == unknownDate {
		modVersion, vcsRevision, vcsTime := embedded()
		if version == devVersion && modVersion != "" {
			version = modVersion
		}
		if commit == noCommit && vcsRevision != "" {
			commit = vcsRevision
		}
		if date == unknownDate && vcsTime != "" {
			date = vcsTime
		}
	}

	return fmt.Sprintf("%s %s (commit %s, built %s)", Name, version, commit, date)
}

// embedded reads what the Go toolchain stamped into the binary: the module
// version, the VCS revision and the VCS commit time. Each is "" when it is
// absent or says nothing — "(devel)" is a module built from a working tree,
// which is no more informative than "dev" is.
func embedded() (version, revision, buildTime string) {
	info, ok := readBuildInfo()
	if !ok {
		return "", "", ""
	}

	if v := info.Main.Version; v != "" && v != "(devel)" {
		version = v
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.time":
			buildTime = setting.Value
		}
	}
	return version, revision, buildTime
}
