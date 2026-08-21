package jsonout_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// update rewrites the golden files instead of comparing against them:
//
//	go test ./... -update
//
// Every package with goldens in sitrep copies this convention.
var update = flag.Bool("update", false, "rewrite the testdata golden files")

// checkGolden compares got against testdata/<name>, or rewrites it under
// -update. On a mismatch it prints both documents and the command that would
// accept the new one.
func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join("testdata", name)
	if *update {
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
		t.Errorf("output does not match %s.\n--- got ---\n%s\n--- want ---\n%s\n"+
			"Run `go test ./... -update` if the new output is correct.", path, got, want)
	}
}
