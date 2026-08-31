package cli_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/cli"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
	"github.com/niekcandaele/sitrep/internal/tui"
)

// Neither --json nor --plain means the live monitor, and the monitor needs a
// terminal. In a test — a pipe on both ends — it cannot start, and the failure
// has to name the way out rather than leaving the user staring at a terminfo
// error.
func TestMonitorWithoutATerminalExplainsTheOneShotModes(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := cli.RunWith([]string{"111", "--no-mouse"}, &stdout, &stderr, cli.Deps{
		Provider: fake.New(),
		Stdin:    strings.NewReader(""),
	})

	if code != 1 {
		t.Errorf("exit code = %d, want 1 (stderr: %q)", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty: a failed monitor writes nothing to stdout", stdout.String())
	}
	for _, want := range []string{"sitrep: ", "needs a terminal", "--plain", "--json"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want it to mention %q", stderr.String(), want)
		}
	}
}

func TestOneShotModesIgnoreNoMouse(t *testing.T) {
	for _, mode := range []string{"--json", "--plain"} {
		without := run([]string{"111", mode}, fake.New())
		with := run([]string{"111", mode, "--no-mouse"}, fake.New())
		if with.code != 0 || with.stderr != "" {
			t.Fatalf("%s --no-mouse: code=%d stderr=%q", mode, with.code, with.stderr)
		}
		if with.stdout != without.stdout {
			t.Errorf("%s report changed with --no-mouse", mode)
		}
	}
}

func TestNoMouseReachesEveryMonitorEntryPath(t *testing.T) {
	tests := []struct {
		name             string
		args             []string
		provider         provider.Provider
		stdin            io.Reader
		stdinEntry       bool
		wantOpen         bool
		wantSeed         bool
		wantInitialError bool
	}{
		{
			name:     "ordinary seeded monitor",
			args:     []string{"--no-mouse", "acme/widgets#111"},
			provider: fake.New(),
			stdin:    strings.NewReader(""),
			wantSeed: true,
		},
		{
			name:     "decoded Ticket Detail monitor",
			args:     []string{"--no-mouse", "acme/widgets#112"},
			provider: fake.New(fake.WithSnapshot(fake.FixtureTicketSnapshot())),
			stdin:    strings.NewReader(""),
			wantOpen: true,
		},
		{
			name:             "retryable preflight monitor",
			args:             []string{"--no-mouse", "acme/widgets#111"},
			provider:         fake.New(fake.WithResolveError(provider.Errorf(provider.KindUnavailable, "network down"))),
			stdin:            strings.NewReader(""),
			wantInitialError: true,
		},
		{
			name:       "stdin-selected monitor",
			args:       []string{"--no-mouse", "-"},
			provider:   fake.New(),
			stdin:      strings.NewReader("acme/widgets#112"),
			stdinEntry: true,
			wantSeed:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured *tui.Options
			var tty *trackedReadCloser
			deps := cli.Deps{
				Provider: tt.provider,
				Stdin:    tt.stdin,
				RunMonitor: func(_ context.Context, opts tui.Options) error {
					captured = &opts
					return nil
				},
			}
			if tt.stdinEntry {
				tty = &trackedReadCloser{Reader: strings.NewReader("")}
				deps.OpenTTY = func() (io.ReadCloser, error) { return tty, nil }
			}

			var stdout, stderr bytes.Buffer
			code := cli.RunWith(tt.args, &stdout, &stderr, deps)
			if code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("result = code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
			}
			if captured == nil {
				t.Fatal("monitor runner was not called")
			}
			if !captured.NoMouse {
				t.Error("NoMouse was lost before the monitor runner")
			}
			if (captured.Open != nil) != tt.wantOpen {
				t.Errorf("Open present = %t, want %t", captured.Open != nil, tt.wantOpen)
			}
			if (captured.Initial != nil) != tt.wantSeed {
				t.Errorf("Initial present = %t, want %t", captured.Initial != nil, tt.wantSeed)
			}
			if (captured.InitialError != nil) != tt.wantInitialError {
				t.Errorf("InitialError present = %t, want %t", captured.InitialError != nil, tt.wantInitialError)
			}
			if tty != nil && !tty.closed {
				t.Error("stdin-selected monitor did not close its controlling terminal")
			}
		})
	}
}

// --interval is meaningless to a one-shot render, but a script that sets it
// once and switches modes should not start failing over a setting it is not
// using. The one-shot modes ignore it, including values the monitor rejects.
func TestOneShotModesIgnoreTheInterval(t *testing.T) {
	for _, mode := range []string{"--json", "--plain"} {
		got := run([]string{"111", mode, "--interval", "0"}, fake.New())

		if got.code != 0 {
			t.Errorf("%s with --interval 0: exit code = %d, want 0 (stderr: %q)", mode, got.code, got.stderr)
		}
		if got.stdout == "" {
			t.Errorf("%s with --interval 0 produced no report", mode)
		}
	}
}

type trackedReadCloser struct {
	*strings.Reader
	closed bool
}

func (r *trackedReadCloser) Close() error {
	r.closed = true
	return nil
}

func TestStdinMonitorUsesAndClosesControllingTerminal(t *testing.T) {
	p := fake.New()
	input := &trackedReadCloser{Reader: strings.NewReader("")}
	opens := 0
	var stdout, stderr bytes.Buffer
	code := cli.RunWith([]string{"--no-mouse", "-"}, &stdout, &stderr, cli.Deps{
		Provider: p,
		Stdin:    strings.NewReader("acme/widgets#112"),
		OpenTTY: func() (io.ReadCloser, error) {
			opens++
			return input, nil
		},
	})

	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("result = code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	if opens != 1 {
		t.Errorf("OpenTTY calls = %d, want 1", opens)
	}
	if !input.closed {
		t.Error("controlling terminal was not closed after TUI terminal validation")
	}
	if p.ResolveCalls() != 1 {
		t.Errorf("ResolveCalls = %d, want one preflight before terminal acquisition", p.ResolveCalls())
	}
	if _, ok := p.LastSelector().(provider.RefListSelector); !ok {
		t.Errorf("selector = %T, want provider.RefListSelector", p.LastSelector())
	}
	if !strings.Contains(stderr.String(), "the monitor needs a terminal (use --plain or --json here)") {
		t.Errorf("stderr = %q, want terminal recommendation", stderr.String())
	}
}

func TestStdinMonitorReportsControllingTerminalOpenFailure(t *testing.T) {
	p := fake.New()
	opens := 0
	var stdout, stderr bytes.Buffer
	code := cli.RunWith([]string{"--no-mouse", "-"}, &stdout, &stderr, cli.Deps{
		Provider: p,
		Stdin:    strings.NewReader("acme/widgets#112"),
		OpenTTY: func() (io.ReadCloser, error) {
			opens++
			return nil, errors.New("no controlling terminal")
		},
	})

	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("result = code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	want := "sitrep: the monitor needs a terminal (use --plain or --json here): opening controlling terminal: no controlling terminal\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
	if opens != 1 || p.ResolveCalls() != 1 {
		t.Errorf("calls = OpenTTY %d Resolve %d, want 1 and 1", opens, p.ResolveCalls())
	}
}

func TestOrdinaryMonitorsNeverOpenControllingTerminal(t *testing.T) {
	for _, args := range [][]string{
		{"acme/widgets#111"},
		{"acme/widgets#112", "acme/widgets#115"},
		{"--query", "label:bug"},
	} {
		p := fake.New()
		var stdout, stderr bytes.Buffer
		code := cli.RunWith(args, &stdout, &stderr, cli.Deps{
			Provider: p,
			Stdin:    strings.NewReader(""),
			OpenTTY:  panicTTY,
		})
		if code != 1 || !strings.Contains(stderr.String(), "needs a terminal") {
			t.Errorf("args %v: result = code %d stderr %q", args, code, stderr.String())
		}
	}
}

func TestNonRetryableStdinPreflightFailureNeverOpensTerminal(t *testing.T) {
	p := fake.New(fake.WithResolveError(provider.Errorf(provider.KindBadRef, "bad selector")))
	var stdout, stderr bytes.Buffer
	code := cli.RunWith([]string{"--no-mouse", "-"}, &stdout, &stderr, cli.Deps{
		Provider: p,
		Stdin:    strings.NewReader("acme/widgets#112"),
		OpenTTY:  panicTTY,
	})
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "bad selector") {
		t.Fatalf("result = code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	if p.ResolveCalls() != 1 {
		t.Errorf("ResolveCalls = %d, want 1", p.ResolveCalls())
	}
}

func TestRetryableStdinPreflightFailureStillOpensTerminal(t *testing.T) {
	p := fake.New(fake.WithResolveError(provider.Errorf(provider.KindUnavailable, "network down")))
	opens := 0
	var stdout, stderr bytes.Buffer
	code := cli.RunWith([]string{"--no-mouse", "-"}, &stdout, &stderr, cli.Deps{
		Provider: p,
		Stdin:    strings.NewReader("acme/widgets#112"),
		OpenTTY: func() (io.ReadCloser, error) {
			opens++
			return nil, errors.New("no tty")
		},
	})
	if code != 1 || !strings.Contains(stderr.String(), "opening controlling terminal: no tty") {
		t.Fatalf("result = code %d stderr %q", code, stderr.String())
	}
	if opens != 1 || p.ResolveCalls() != 1 {
		t.Errorf("calls = OpenTTY %d Resolve %d, want 1 and 1", opens, p.ResolveCalls())
	}
	selector, ok := p.LastSelector().(provider.RefListSelector)
	if !ok || len(selector.Refs) != 1 || selector.Refs[0].Raw != "acme/widgets#112" {
		t.Errorf("selector = %#v, want the stdin Ref-list", p.LastSelector())
	}
}
