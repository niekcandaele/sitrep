package tui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestMain(m *testing.M) {
	if err := os.Setenv("GLAMOUR_STYLE", "dark"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// -update rewrites the golden files instead of comparing against them:
//
//	go test ./... -update
//
// Every package with goldens in sitrep uses that flag. Here it may already
// exist: teatest pulls in x/exp/golden, which registers an -update of its own,
// and declaring a second one panics the binary before a test runs. Adopting
// whichever flag is there keeps one house convention rather than two spellings
// of it.
func init() {
	if flag.Lookup("update") == nil {
		flag.Bool("update", false, "rewrite the testdata golden files")
	}
}

// updating reports whether the run was asked to rewrite the goldens.
func updating() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

// checkGolden compares got against testdata/<name>, or rewrites it under
// -update. On a mismatch it prints both documents and the command that would
// accept the new one.
func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join("testdata", name)
	if updating() {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("creating testdata: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v (run `go test ./... -update` to create it)", path, err)
	}
	if string(got) != string(want) {
		t.Errorf("frame does not match %s.\n--- got ---\n%s\n--- want ---\n%s\n"+
			"Run `go test ./... -update` if the new frame is correct.", path, got, want)
	}
}

// frame turns a rendered screen into the bytes a golden holds: ANSI stripped
// and trailing whitespace trimmed per line.
//
// Stripping is deliberate. A golden full of escape sequences is unreadable in
// a diff and turns every styling tweak into golden churn, while the layout —
// the grouping, the counts, the columns, what is present and what is silently
// absent — is exactly what survives stripping and exactly what these tests are
// about. Colour is asserted by looking at the thing.
func frame(s string) []byte {
	lines := strings.Split(ansi.Strip(s), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}
