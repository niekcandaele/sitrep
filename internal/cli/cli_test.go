package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/cli"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		wantCode       int
		wantStdout     []string
		wantStderr     []string
		wantEmptyOut   bool
		wantEmptyError bool
	}{
		{
			name:           "version prints build info to stdout",
			args:           []string{"--version"},
			wantCode:       0,
			wantStdout:     []string{"sitrep "},
			wantEmptyError: true,
		},
		{
			name:           "long help prints usage to stdout",
			args:           []string{"--help"},
			wantCode:       0,
			wantStdout:     []string{"sitrep", "sitrep [flags] <ref>", "--version"},
			wantEmptyError: true,
		},
		{
			name:           "short help prints usage to stdout",
			args:           []string{"-h"},
			wantCode:       0,
			wantStdout:     []string{"sitrep [flags] <ref>"},
			wantEmptyError: true,
		},
		{
			name:         "unknown flag is a usage error on stderr",
			args:         []string{"--nope"},
			wantCode:     2,
			wantStderr:   []string{"nope", "sitrep [flags] <ref>"},
			wantEmptyOut: true,
		},
		{
			name:         "no arguments demands a ref",
			args:         nil,
			wantCode:     2,
			wantStderr:   []string{"Epic Ref is required", "sitrep [flags] <ref>"},
			wantEmptyOut: true,
		},
		{
			name:           "help documents the one-shot modes, the provider flag and the refresh cadence",
			args:           []string{"--help"},
			wantCode:       0,
			wantStdout:     []string{"--json", "--plain", "--provider", "--interval"},
			wantEmptyError: true,
		},
		{
			name:         "two refs are a usage error",
			args:         []string{"111", "112"},
			wantCode:     2,
			wantStderr:   []string{"only one Epic Ref"},
			wantEmptyOut: true,
		},
		{
			name:         "an unknown provider is a usage error",
			args:         []string{"--provider", "bogus", "111"},
			wantCode:     2,
			wantStderr:   []string{`unknown provider "bogus"`},
			wantEmptyOut: true,
		},
		{
			name:         "a zero refresh interval is a usage error",
			args:         []string{"--interval", "0", "123"},
			wantCode:     2,
			wantStderr:   []string{"refresh interval must be positive"},
			wantEmptyOut: true,
		},
		{
			name:         "a negative refresh interval is a usage error",
			args:         []string{"--interval", "-5s", "123"},
			wantCode:     2,
			wantStderr:   []string{"refresh interval must be positive"},
			wantEmptyOut: true,
		},
		{
			// sitrep polls a rate-limited API; a sub-second poll is a way to
			// get throttled, not a feature.
			name:         "a refresh interval below the floor is a usage error",
			args:         []string{"--interval", "1s", "123"},
			wantCode:     2,
			wantStderr:   []string{"refresh interval must be at least 5s"},
			wantEmptyOut: true,
		},
		{
			// The flag package's own wording named the flag with one dash,
			// which no sitrep document does, and said "parse error" where the
			// real problem is a duration with no unit.
			name:         "a malformed duration speaks sitrep's voice",
			args:         []string{"--interval=abc", "123"},
			wantCode:     2,
			wantStderr:   []string{"sitrep: ", "--interval", "durations need a unit"},
			wantEmptyOut: true,
		},
		{
			name:         "an unknown flag speaks sitrep's voice",
			args:         []string{"--badflag", "123"},
			wantCode:     2,
			wantStderr:   []string{"sitrep: ", "--badflag"},
			wantEmptyOut: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := cli.Run(tt.args, &stdout, &stderr)

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr: %q)", code, tt.wantCode, stderr.String())
			}
			for _, want := range tt.wantStdout {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
				}
			}
			for _, want := range tt.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
				}
			}
			if tt.wantEmptyOut && stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			if tt.wantEmptyError && stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}
