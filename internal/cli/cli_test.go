package cli_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/cli"
	"github.com/niekcandaele/sitrep/internal/config"
	"github.com/niekcandaele/sitrep/internal/provider"
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
			wantStderr:   []string{"nope", "Run \"sitrep --help\" for usage."},
			wantEmptyOut: true,
		},
		{
			name:         "no arguments demands a ref",
			args:         nil,
			wantCode:     2,
			wantStderr:   []string{"a Selector is required", "Run \"sitrep --help\" for usage."},
			wantEmptyOut: true,
		},
		{
			name:           "help documents the one-shot modes, the provider flag and the refresh cadence",
			args:           []string{"--help"},
			wantCode:       0,
			wantStdout:     []string{"--json", "--plain", "--provider", "--interval", "--no-mouse          start the monitor without mouse capture"},
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

func TestFakeProviderFlagsValidateBeforeRuntimeWork(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		message string
	}{
		{
			name:    "fixture requires explicit fake Provider",
			args:    []string{"--fake-fixture", "blocking", "111"},
			message: "--fake-fixture requires --provider fake",
		},
		{
			name:    "delay requires explicit fake Provider",
			args:    []string{"--fake-delay", "1s", "111"},
			message: "--fake-delay requires --provider fake",
		},
		{
			name:    "fixture rejects a real Provider",
			args:    []string{"--provider", "github", "--fake-fixture", "blocking", "111"},
			message: "--fake-fixture requires --provider fake",
		},
		{
			name:    "empty fixture",
			args:    []string{"--provider", "fake", "--fake-fixture=", "111"},
			message: `--fake-fixture must be "blocking" or "no-blocking-links"`,
		},
		{
			name:    "unknown fixture",
			args:    []string{"--provider", "fake", "--fake-fixture", "dense", "111"},
			message: `--fake-fixture must be "blocking" or "no-blocking-links"`,
		},
		{
			name:    "explicit zero delay",
			args:    []string{"--provider", "fake", "--fake-delay=0", "111"},
			message: "--fake-delay must be positive",
		},
		{
			name:    "negative delay",
			args:    []string{"--provider", "fake", "--fake-delay", "-1s", "111"},
			message: "--fake-delay must be positive",
		},
		{
			name:    "malformed delay",
			args:    []string{"--provider", "fake", "--fake-delay=slow", "111"},
			message: `invalid value "slow" for flag --fake-delay: parse error (durations need a unit: 60s, 2m)`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := cli.RunWith(tt.args, &stdout, &stderr, cli.Deps{
				Stdin:      panicReader{},
				OpenTTY:    panicTTY,
				ConfigPath: "/config/must-not-be-read",
				RemoteLookup: func(context.Context, string, string) (string, error) {
					panic("origin was read")
				},
			})
			checkUsageError(t, code, &stdout, &stderr, tt.message)
		})
	}
}

func TestEveryUsageErrorRouteUsesCompactPointer(t *testing.T) {
	baseDeps := cli.Deps{
		Stdin:      panicReader{},
		OpenTTY:    panicTTY,
		ConfigPath: "/config/must-not-be-read",
		RemoteLookup: func(context.Context, string, string) (string, error) {
			panic("origin was read")
		},
	}
	tests := []struct {
		name    string
		args    []string
		message string
		deps    cli.Deps
	}{
		{"unknown flag", []string{"--nope"}, "flag provided but not defined: --nope", baseDeps},
		{"missing flag value", []string{"--query"}, "flag needs an argument: --query", baseDeps},
		{"invalid interval duration", []string{"--interval=abc", "123"}, `invalid value "abc" for flag --interval: parse error (durations need a unit: 60s, 2m)`, baseDeps},
		{"invalid fake delay duration", []string{"--provider", "fake", "--fake-delay=slow", "123"}, `invalid value "slow" for flag --fake-delay: parse error (durations need a unit: 60s, 2m)`, baseDeps},
		{"zero interval", []string{"--interval", "0", "123"}, "refresh interval must be positive", baseDeps},
		{"short interval", []string{"--interval", "1s", "123"}, "refresh interval must be at least 5s", baseDeps},
		{"invalid fake fixture", []string{"--provider", "fake", "--fake-fixture", "dense", "123"}, `--fake-fixture must be "blocking" or "no-blocking-links"`, baseDeps},
		{"incompatible render modes", []string{"--json", "--plain", "123"}, "--json and --plain are mutually exclusive", baseDeps},
		{"links without json", []string{"--links", "123"}, "--links requires --json", baseDeps},
		{"missing selector", nil, `a Selector is required: pass one or more Refs, "-" for stdin, or --query`, baseDeps},
		{"query with positional ref", []string{"--query", "q", "123"}, `--query cannot be combined with positional Refs or "-"`, baseDeps},
		{"stdin marker with positional ref", []string{"-", "123"}, `"-" reads Refs from stdin and must be the only positional argument`, baseDeps},
		{"unknown Provider", []string{"--provider", "unknown", "123"}, `unknown provider "unknown"`, baseDeps},
		{"fake fixture without fake Provider", []string{"--fake-fixture", "blocking", "123"}, "--fake-fixture requires --provider fake", baseDeps},
		{"fake delay without fake Provider", []string{"--fake-delay", "1s", "123"}, "--fake-delay requires --provider fake", baseDeps},
		{"links on decoded Ticket", []string{"112", "--json", "--links"}, "--links needs a Watchlist: #112 names a single Ticket", cli.Deps{Provider: decoder(), Stdin: panicReader{}, OpenTTY: panicTTY}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := cli.RunWith(tt.args, &stdout, &stderr, tt.deps)
			checkUsageError(t, code, &stdout, &stderr, tt.message)
		})
	}
}

func TestFakeProviderFlagsPreserveEarlierValidationAndEarlyResults(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "help", args: []string{"--help", "--fake-fixture", "blocking"}, wantCode: 0, wantStdout: "Usage:"},
		{name: "version", args: []string{"--version", "--provider", "fake", "--fake-delay", "1s"}, wantCode: 0, wantStdout: "sitrep "},
		{name: "mode conflict", args: []string{"--json", "--plain", "--fake-fixture", "blocking", "111"}, wantCode: 2, wantStderr: "mutually exclusive"},
		{name: "links mode", args: []string{"--links", "--fake-fixture", "blocking", "111"}, wantCode: 2, wantStderr: "--links requires --json"},
		{name: "monitor interval", args: []string{"--interval", "0", "--fake-fixture", "blocking", "111"}, wantCode: 2, wantStderr: "refresh interval must be positive"},
		{name: "unknown Provider", args: []string{"--provider", "bogus", "--fake-fixture", "blocking", "111"}, wantCode: 2, wantStderr: "unknown provider"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := cli.RunWith(tt.args, &stdout, &stderr, cli.Deps{Stdin: panicReader{}, OpenTTY: panicTTY})
			if code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d", code, tt.wantCode)
			}
			if tt.wantStdout != "" && !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want %q", stdout.String(), tt.wantStdout)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want %q", stderr.String(), tt.wantStderr)
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

func TestMalformedRefFromStdinHasCompactDiagnosticBeforeProviderWork(t *testing.T) {
	const inputBytes = 100_000
	input := strings.Repeat("x", inputBytes)
	var stdout, stderr bytes.Buffer
	code := cli.RunWith([]string{"--plain", "-"}, &stdout, &stderr, cli.Deps{
		Stdin:   strings.NewReader(input),
		Config:  &config.Config{},
		OpenTTY: panicTTY,
		RemoteLookup: func(context.Context, string, string) (string, error) {
			panic("origin was read")
		},
	})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if stderr.Len() >= 1024 {
		t.Errorf("stderr is %d bytes, want a compact diagnostic", stderr.Len())
	}
	message := stderr.String()
	for _, want := range []string{"sitrep: cannot parse", "(100000 bytes)", "owner/repo#123", "--help"} {
		if !strings.Contains(message, want) {
			t.Errorf("stderr = %q, want %q", message, want)
		}
	}
	if strings.Contains(message, input) {
		t.Error("stderr contains the complete malformed stdin Ref")
	}
	if strings.Count(message, "\n") != 1 || !strings.HasSuffix(message, "\n") {
		t.Errorf("stderr is not exactly one physical line: %q", message)
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

		checkUsageError(t, code, &stdout, &stderr,
			`"-" reads Refs from stdin and must be the only positional argument`)
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

func TestQuerySelectorPreservesExactFlagValue(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		query string
	}{
		{name: "opaque", args: []string{"--plain", "--query", "  label:bug assignee:@me  "}, query: "  label:bug assignee:@me  "},
		{name: "empty", args: []string{"--query=", "--plain"}, query: ""},
		{name: "whitespace", args: []string{"--plain", "--query", " \t "}, query: " \t "},
		{name: "interspersed and repeated", args: []string{"--query", "first", "--plain", "--query=last & exact"}, query: "last & exact"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := fake.New()
			var stdout, stderr bytes.Buffer
			code := cli.RunWith(tt.args, &stdout, &stderr, cli.Deps{
				Provider: p,
				Stdin:    panicReader{},
				OpenTTY:  panicTTY,
			})
			if code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr.String())
			}
			selector, ok := p.LastSelector().(provider.QuerySelector)
			if !ok {
				t.Fatalf("selector = %T, want provider.QuerySelector", p.LastSelector())
			}
			if selector.Query != tt.query {
				t.Errorf("query = %q, want exact %q", selector.Query, tt.query)
			}
			if p.ResolveCalls() != 1 || p.DetailCalls() != 0 {
				t.Errorf("calls = Resolve %d Detail %d, want 1 and 0", p.ResolveCalls(), p.DetailCalls())
			}
		})
	}
}

func TestQueryRejectsEveryPositionalSelectorBeforeRuntimeWork(t *testing.T) {
	for _, positional := range [][]string{{"111"}, {"111", "112"}, {"-"}} {
		args := append([]string{"--query", "q", "--plain"}, positional...)
		p := fake.New()
		var stdout, stderr bytes.Buffer
		code := cli.RunWith(args, &stdout, &stderr, cli.Deps{
			Provider:   p,
			Stdin:      panicReader{},
			OpenTTY:    panicTTY,
			ConfigPath: "/config/must-not-be-read",
			RemoteLookup: func(context.Context, string, string) (string, error) {
				panic("origin was read")
			},
		})
		checkUsageError(t, code, &stdout, &stderr,
			`--query cannot be combined with positional Refs or "-"`)
		if p.ResolveCalls() != 0 || p.DetailCalls() != 0 {
			t.Errorf("args %v: Provider was called", args)
		}
	}
}

func TestQueryFlagParsingAndEarlyResultPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "missing value", args: []string{"--query"}, wantCode: 2, wantStderr: "flag needs an argument: --query"},
		{name: "help", args: []string{"--help", "--query", "q", "111"}, wantCode: 0, wantStdout: "Usage:"},
		{name: "version", args: []string{"--version", "--query", "q", "111"}, wantCode: 0, wantStdout: "sitrep "},
		{name: "mode conflict", args: []string{"--json", "--plain", "--query", "q"}, wantCode: 2, wantStderr: "mutually exclusive"},
		{name: "invalid monitor interval", args: []string{"--interval", "0", "--query", "q"}, wantCode: 2, wantStderr: "refresh interval must be positive"},
		{name: "unknown provider", args: []string{"--provider", "bogus", "--query", "q"}, wantCode: 2, wantStderr: "unknown provider"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := cli.RunWith(tt.args, &stdout, &stderr, cli.Deps{Stdin: panicReader{}, OpenTTY: panicTTY})
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if tt.wantStdout != "" && !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want %q", stdout.String(), tt.wantStdout)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}
