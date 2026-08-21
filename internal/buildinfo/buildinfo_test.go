package buildinfo_test

import (
	"testing"

	"github.com/niekcandaele/sitrep/internal/buildinfo"
)

func TestStringDefaults(t *testing.T) {
	got := buildinfo.String()
	want := "sitrep dev (commit none, built unknown)"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestStringInjectedValues(t *testing.T) {
	setVar(t, &buildinfo.Version, "0.1.0")
	setVar(t, &buildinfo.Commit, "abc1234")
	setVar(t, &buildinfo.Date, "2026-08-21T10:00:00Z")

	got := buildinfo.String()
	want := "sitrep 0.1.0 (commit abc1234, built 2026-08-21T10:00:00Z)"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// setVar overrides a package-level build variable for the duration of the test,
// mirroring what -ldflags -X does at link time.
func setVar(t *testing.T, target *string, value string) {
	t.Helper()
	original := *target
	*target = value
	t.Cleanup(func() { *target = original })
}
