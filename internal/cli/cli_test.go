package cli_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/cli"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
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

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("stdin was read") }

type partialErrorReader struct {
	read bool
}

func (r *partialErrorReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, errors.New("upstream broke")
	}
	r.read = true
	return copy(p, "acme/widgets#112"), nil
}

func panicTTY() (io.ReadCloser, error) { panic("controlling terminal was opened") }

func TestEmptyStdinSelectionFailsBeforeRuntimeWork(t *testing.T) {
	inputs := []string{
		"",
		" \t\r\n",
		"   ",
	}
	for _, input := range inputs {
		t.Run(strings.ReplaceAll(input, "\n", `\n`), func(t *testing.T) {
			p := fake.New()
			var stdout, stderr bytes.Buffer
			code := cli.RunWith([]string{"--plain", "-"}, &stdout, &stderr, cli.Deps{
				Provider:   p,
				Stdin:      strings.NewReader(input),
				ConfigPath: "/config/must-not-be-read",
				OpenTTY:    panicTTY,
			})

			if code != 1 || stdout.Len() != 0 {
				t.Fatalf("result = code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
			}
			if got, want := stderr.String(), "sitrep: no Refs were provided on stdin\n"; got != want {
				t.Errorf("stderr = %q, want %q", got, want)
			}
			if p.ResolveCalls() != 0 || p.DetailCalls() != 0 {
				t.Errorf("calls = Resolve %d Detail %d, want none", p.ResolveCalls(), p.DetailCalls())
			}
		})
	}
}

func TestStdinReadFailureDiscardsPartialInput(t *testing.T) {
	p := fake.New()
	var stdout, stderr bytes.Buffer
	code := cli.RunWith([]string{"--json", "-"}, &stdout, &stderr, cli.Deps{
		Provider:   p,
		Stdin:      &partialErrorReader{},
		ConfigPath: "/config/must-not-be-read",
		OpenTTY:    panicTTY,
	})

	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("result = code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	if got, want := stderr.String(), "sitrep: reading Refs from stdin: upstream broke\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	if p.ResolveCalls() != 0 {
		t.Errorf("ResolveCalls = %d, want 0", p.ResolveCalls())
	}
}

func TestStdinSentinelMustBeTheOnlyPositionalArgument(t *testing.T) {
	for _, args := range [][]string{
		{"--plain", "-", "acme/widgets#112"},
		{"acme/widgets#112", "-", "--plain"},
	} {
		p := fake.New()
		var stdout, stderr bytes.Buffer
		code := cli.RunWith(args, &stdout, &stderr, cli.Deps{
			Provider:   p,
			Stdin:      panicReader{},
			ConfigPath: "/config/must-not-be-read",
			OpenTTY:    panicTTY,
		})

		if code != 2 || stdout.Len() != 0 {
			t.Fatalf("args %v: result = code %d stdout %q stderr %q", args, code, stdout.String(), stderr.String())
		}
		want := "sitrep: \"-\" reads Refs from stdin and must be the only positional argument\n\n"
		if !strings.HasPrefix(stderr.String(), want) {
			t.Errorf("args %v: stderr = %q, want prefix %q", args, stderr.String(), want)
		}
		if p.ResolveCalls() != 0 {
			t.Errorf("args %v: ResolveCalls = %d, want 0", args, p.ResolveCalls())
		}
	}
}

func TestEarlyCLIResultsDoNotReadStdin(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int
	}{
		{name: "help", args: []string{"--help", "-"}, code: 0},
		{name: "version", args: []string{"--version", "-"}, code: 0},
		{name: "unknown provider", args: []string{"--provider", "bogus", "-"}, code: 2},
		{name: "mode conflict", args: []string{"--plain", "-", "--json"}, code: 2},
		{name: "invalid interval", args: []string{"--interval", "0", "-"}, code: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := cli.RunWith(tt.args, &stdout, &stderr, cli.Deps{Stdin: panicReader{}, OpenTTY: panicTTY})
			if code != tt.code {
				t.Errorf("exit code = %d, want %d (stderr: %q)", code, tt.code, stderr.String())
			}
		})
	}
}

func TestPositionalSelectionNeverReadsStdin(t *testing.T) {
	p := fake.New()
	var stdout, stderr bytes.Buffer
	code := cli.RunWith([]string{"--json", "acme/widgets#111"}, &stdout, &stderr, cli.Deps{
		Provider: p,
		Stdin:    panicReader{},
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr.String())
	}
}
