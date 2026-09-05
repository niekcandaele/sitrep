package cli_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/cli"
)

func TestHelpMatchesGolden(t *testing.T) {
	golden, err := os.ReadFile("testdata/help.golden.txt")
	if err != nil {
		t.Fatal(err)
	}

	for _, flag := range []string{"--help", "-h"} {
		t.Run(flag, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := cli.Run([]string{flag}, &stdout, &stderr); code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr.String())
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
			checkGolden(t, "help.golden.txt", stdout.Bytes())
		})
	}

	help := string(golden)
	for _, marker := range []string{
		"sitrep [flags] <ref>\n",
		"sitrep [flags] <ref> <ref>...\n",
		"sitrep [flags] -\n",
		"sitrep [flags] --query <query>\n",
		"--plain", "--json", "--links", "--profile", "--provider", "--fake-fixture", "--fake-delay", "--interval", "--no-mouse",
		"blocking", "no-blocking-links", "omission keeps the legacy fixture",
		"A sole \"-\" reads whitespace-separated Refs", "one stdin Ref keeps exact Ref-list semantics",
	} {
		if !strings.Contains(help, marker) {
			t.Errorf("help does not contain %q", marker)
		}
	}
	if strings.Contains(strings.ToLower(help), "delegated epic") {
		t.Error("help retains the delegated-Epic product slogan")
	}
}

const usagePointer = "Run \"sitrep --help\" for usage.\n"

func checkUsageError(t *testing.T, code int, stdout, stderr *bytes.Buffer, message string) {
	t.Helper()

	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	want := "sitrep: " + message + "\n" + usagePointer
	if got := stderr.String(); got != want {
		t.Errorf("stderr = %q, want exact compact usage error %q", got, want)
	}
	if strings.Count(stderr.String(), "\n") != 2 || strings.Contains(stderr.String(), "\n\n") {
		t.Errorf("stderr = %q, want exactly two non-blank physical lines", stderr.String())
	}
	if strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("stderr = %q, must not contain full help", stderr.String())
	}
}

func TestUsageErrorsUseCompactPointer(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		message string
	}{
		{
			name:    "unknown flag",
			args:    []string{"--nope"},
			message: "flag provided but not defined: --nope",
		},
		{
			name:    "missing Selector",
			message: "a Selector is required: pass one or more Refs, \"-\" for stdin, or --query",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := cli.Run(tt.args, &stdout, &stderr)
			checkUsageError(t, code, &stdout, &stderr, tt.message)
		})
	}
}
