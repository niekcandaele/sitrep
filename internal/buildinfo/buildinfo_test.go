// The tests live in package buildinfo rather than beside it, because the one
// thing worth proving about the go install fallback is what it does with a
// given debug.BuildInfo — and pinning that means replacing the reader rather
// than depending on how this test binary was built.
package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestStringDefaults(t *testing.T) {
	// Nothing injected and nothing embedded: a `go build` from a source tree.
	setBuildInfo(t, nil, false)

	got := String()
	want := "sitrep dev (commit none, built unknown)"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestStringInjectedValues(t *testing.T) {
	setVar(t, &Version, "0.1.0")
	setVar(t, &Commit, "abc1234")
	setVar(t, &Date, "2026-08-21T10:00:00Z")

	got := String()
	want := "sitrep 0.1.0 (commit abc1234, built 2026-08-21T10:00:00Z)"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// Nothing here parses the injected date, so the release pipeline is free to
// stamp the commit's own timestamp — which is what makes two builds of one
// commit identical — in whatever spelling its template produces.
func TestStringDoesNotParseTheDate(t *testing.T) {
	setVar(t, &Version, "0.1.0")
	setVar(t, &Commit, "abc1234")
	setVar(t, &Date, "Fri Aug 21 10:00:00 2026 +0000")

	got := String()
	want := "sitrep 0.1.0 (commit abc1234, built Fri Aug 21 10:00:00 2026 +0000)"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// A release archive is linked with all three values, and what the toolchain
// embedded must not touch them: §7.1's CI check asserts the same thing against
// a real goreleaser build.
func TestInjectedValuesBeatTheEmbeddedOnes(t *testing.T) {
	setVar(t, &Version, "0.1.0")
	setVar(t, &Commit, "abc1234")
	setVar(t, &Date, "2026-08-21T10:00:00Z")
	setBuildInfo(t, buildInfo("v9.9.9", "deadbeefdeadbeef", "1999-01-01T00:00:00Z"), true)

	got := String()
	want := "sitrep 0.1.0 (commit abc1234, built 2026-08-21T10:00:00Z)"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// A `go install …@latest` build carries no ldflags, so the module version and
// the VCS stamp are all there is — and they are enough to make a bug report
// useful.
func TestGoInstallFallback(t *testing.T) {
	tests := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{
			name: "a tagged module with a VCS stamp",
			info: buildInfo("v0.1.0", "abc1234def5678", "2026-08-21T10:00:00Z"),
			want: "sitrep v0.1.0 (commit abc1234def5678, built 2026-08-21T10:00:00Z)",
		},
		{
			// A module built from a working tree says "(devel)", which is no
			// more informative than "dev" and must not be reported as a version.
			name: "a working-tree module",
			info: buildInfo("(devel)", "abc1234def5678", "2026-08-21T10:00:00Z"),
			want: "sitrep dev (commit abc1234def5678, built 2026-08-21T10:00:00Z)",
		},
		{
			name: "a module version and nothing else",
			info: buildInfo("v0.1.0", "", ""),
			want: "sitrep v0.1.0 (commit none, built unknown)",
		},
		{
			name: "nothing at all",
			info: buildInfo("", "", ""),
			want: "sitrep dev (commit none, built unknown)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setBuildInfo(t, tt.info, true)

			if got := String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// buildInfo assembles the shape the Go toolchain stamps into a binary. An empty
// value is a setting the toolchain did not record.
func buildInfo(version, revision, buildTime string) *debug.BuildInfo {
	info := &debug.BuildInfo{}
	info.Main.Version = version
	if revision != "" {
		info.Settings = append(info.Settings, debug.BuildSetting{Key: "vcs.revision", Value: revision})
	}
	if buildTime != "" {
		info.Settings = append(info.Settings, debug.BuildSetting{Key: "vcs.time", Value: buildTime})
	}
	return info
}

// setBuildInfo replaces the toolchain's build-information reader for the
// duration of the test.
func setBuildInfo(t *testing.T, info *debug.BuildInfo, ok bool) {
	t.Helper()
	original := readBuildInfo
	readBuildInfo = func() (*debug.BuildInfo, bool) { return info, ok }
	t.Cleanup(func() { readBuildInfo = original })
}

// setVar overrides a package-level build variable for the duration of the test,
// mirroring what -ldflags -X does at link time.
func setVar(t *testing.T, target *string, value string) {
	t.Helper()
	original := *target
	*target = value
	t.Cleanup(func() { *target = original })
}
