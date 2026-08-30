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

func TestUsageErrorsAppendExactHelp(t *testing.T) {
	help, err := os.ReadFile("testdata/help.golden.txt")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		args   []string
		prefix string
	}{
		{
			name:   "unknown flag",
			args:   []string{"--nope"},
			prefix: "sitrep: flag provided but not defined: --nope\n\n",
		},
		{
			name: "missing Selector",
			prefix: "sitrep: a Selector is required: pass one or more Refs, \"-\" for stdin, " +
				"or --query\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := cli.Run(tt.args, &stdout, &stderr); code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			want := tt.prefix + string(help)
			if got := stderr.String(); got != want {
				t.Errorf("stderr does not append exact help\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}
