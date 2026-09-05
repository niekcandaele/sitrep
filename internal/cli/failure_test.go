package cli_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/niekcandaele/sitrep/internal/cli"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

// authFailure is what a rejected credential looks like coming out of a driver:
// one attributed line, classified so nothing downstream has to parse it.
var authFailure = provider.Errorf(provider.KindAuth,
	`github: authentication failed (401) — check "gh auth status" or GITHUB_TOKEN`)

// A pre-flight failure that no amount of retrying can fix prints one line and
// exits, rather than opening an alt-screen full of a retry loop. On a headless
// SSH box that loop is exactly the thing that makes a typo unreadable.
//
// This run passes no mode flag, so it would open the monitor — and needs no TTY
// precisely because it exits before tui.Run.
func TestNonRetryablePreflightFailureNeverOpensTheMonitor(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := cli.RunWith([]string{"111"}, &stdout, &stderr, cli.Deps{
		Provider: fake.New(fake.WithResolveError(authFailure)),
		Stdin:    strings.NewReader(""),
	})

	if code != 1 {
		t.Errorf("exit code = %d, want 1 (stderr: %q)", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty: the monitor never opened", stdout.String())
	}
	want := "sitrep: " + authFailure.Error() + "\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want exactly %q", stderr.String(), want)
	}
	// The monitor's own "you are in a pipe" failure would mean tui.Run was
	// reached, which is the bug this test exists to catch.
	if strings.Contains(stderr.String(), "needs a terminal") {
		t.Errorf("stderr = %q, want the driver's line rather than the monitor's", stderr.String())
	}
}

// The mirror image, and the monitor's rule intact: a failure the next tick could recover
// from still opens the monitor unseeded. Off a TTY that shows up as the
// monitor's own complaint, which is proof enough that tui.Run was reached.
func TestRetryablePreflightFailureStillOpensTheMonitor(t *testing.T) {
	failures := map[string]error{
		// An unclassified driver error stays retryable on purpose: a driver
		// that forgets to classify something must not make sitrep give up.
		"unclassified": errors.New("boom"),
		"unavailable":  provider.Errorf(provider.KindUnavailable, "github: requesting the API: connection refused"),
		"rate limited": provider.Errorf(provider.KindRateLimit, "github: API rate limit exceeded; resets at noon"),
	}

	for name, failure := range failures {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := cli.RunWith([]string{"111"}, &stdout, &stderr, cli.Deps{
				Provider: fake.New(fake.WithResolveError(failure)),
				Stdin:    strings.NewReader(""),
			})

			if code != 1 {
				t.Errorf("exit code = %d, want 1 (stderr: %q)", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "needs a terminal") {
				t.Errorf("stderr = %q, want the monitor's own failure: the monitor must still open",
					stderr.String())
			}
		})
	}
}

func TestNonRetryableQueryPreflightNeverOpensTheMonitor(t *testing.T) {
	tests := []struct {
		name string
		p    *fake.Provider
		want string
	}{
		{
			name: "malformed",
			p: fake.New(fake.WithResolveError(
				provider.Errorf(provider.KindBadRef, "fake: query rejected: invalid syntax"))),
			want: "sitrep: fake: query rejected: invalid syntax\n",
		},
		{
			name: "unsupported",
			p: fake.New(fake.WithCapabilities(model.Capabilities{Selectors: model.SelectorCapabilities{
				Epic: true, RefList: true,
			}})),
			want: "sitrep: fake: Query Selector is not supported\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := cli.RunWith([]string{"--query", " label:bug "}, &stdout, &stderr, cli.Deps{
				Provider: tt.p,
				Stdin:    strings.NewReader(""),
				OpenTTY:  panicTTY,
			})

			if code != 1 || stdout.Len() != 0 {
				t.Fatalf("result = code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
			}
			if stderr.String() != tt.want {
				t.Errorf("stderr = %q, want %q", stderr.String(), tt.want)
			}
			selector, ok := tt.p.LastSelector().(provider.QuerySelector)
			if !ok || selector.Query != " label:bug " {
				t.Errorf("selector = %#v, want exact Query", tt.p.LastSelector())
			}
			if tt.p.ResolveCalls() != 1 || tt.p.DetailCalls() != 0 {
				t.Errorf("calls = Resolve %d Detail %d, want 1 and 0", tt.p.ResolveCalls(), tt.p.DetailCalls())
			}
		})
	}
}

func TestRetryableQueryPreflightStillOpensTheMonitor(t *testing.T) {
	p := fake.New(fake.WithResolveError(
		provider.Errorf(provider.KindUnavailable, "fake: requesting the API: network down")))
	var stdout, stderr bytes.Buffer
	code := cli.RunWith([]string{"--query", " label:bug "}, &stdout, &stderr, cli.Deps{
		Provider: p,
		Stdin:    strings.NewReader(""),
		OpenTTY:  panicTTY,
	})

	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("result = code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "needs a terminal") {
		t.Errorf("stderr = %q, want monitor terminal failure", stderr.String())
	}
	selector, ok := p.LastSelector().(provider.QuerySelector)
	if !ok || selector.Query != " label:bug " {
		t.Errorf("selector = %#v, want exact Query", p.LastSelector())
	}
	if p.ResolveCalls() != 1 || p.DetailCalls() != 0 {
		t.Errorf("calls = Resolve %d Detail %d, want 1 and 0", p.ResolveCalls(), p.DetailCalls())
	}
}

// A one-shot mode prints the failure whatever its class: there is no monitor to
// keep open, so retryability changes nothing there.
func TestOneShotModesPrintEveryFailureClass(t *testing.T) {
	for _, mode := range []string{"--json", "--plain"} {
		got := run([]string{"111", mode}, fake.New(fake.WithResolveError(authFailure)))

		if got.code != 1 {
			t.Errorf("%s: exit code = %d, want 1", mode, got.code)
		}
		if got.stdout != "" {
			t.Errorf("%s: stdout = %q, want empty on failure", mode, got.stdout)
		}
		if !strings.Contains(got.stderr, "authentication failed (401)") {
			t.Errorf("%s: stderr = %q, want the driver's line", mode, got.stderr)
		}
	}
}

// interruptingProvider raises SIGINT at itself from inside the fetch and then
// waits for the run's own signal context to carry it, which is the real shape of
// a ctrl+c during a slow request: signal.NotifyContext has disabled the default
// kill, so the fetch is cancelled and the error that comes back names a
// cancellation rather than anything the Tracker did.
type interruptingProvider struct{}

func (interruptingProvider) Name() string                     { return "fake" }
func (interruptingProvider) Capabilities() model.Capabilities { return model.Capabilities{} }
func (interruptingProvider) FetchDetail(context.Context, model.TicketID) (model.Detail, error) {
	return model.Detail{}, errors.New("fake: not reached")
}

func (p interruptingProvider) FetchDetails(ctx context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, error) {
	return provider.FetchDetailsDefault(ctx, ids, p.FetchDetail)
}

func (interruptingProvider) Resolve(ctx context.Context, _ provider.Selector) (model.WatchlistSnapshot, error) {
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		return model.WatchlistSnapshot{}, err
	}
	<-ctx.Done()
	return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
		"fake: requesting the API: %w", ctx.Err())
}

// A ctrl+c is not a failure and sitrep does not blame the Tracker for it: no
// message at all, and the conventional 130.
func TestInterruptExitsQuietly(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := cli.RunWith([]string{"111", "--json"}, &stdout, &stderr, cli.Deps{
		Provider: interruptingProvider{},
	})

	if code != 130 {
		t.Errorf("exit code = %d, want 130 (stderr: %q)", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty: the user knows they pressed ctrl+c", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

// $SITREP_CONFIG names a file that does not exist. An error telling the user to
// add a Profile has to name that file — not the documented default, which has
// nothing to do with where they pointed sitrep.
//
// This is the one CLI-seam case that deliberately lets loadConfig resolve
// through config.DefaultPath, so it must inject Deps.Env and nothing else, and
// $SITREP_CONFIG must point inside a temp dir: without that this test would read
// the developer's real config file.
func TestConfigErrorsNameTheFileSitrepLookedFor(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yml")
	// cli.RunWith directly, not the runWith helper: the helper fills in a
	// ConfigPath, which is exactly the branch this case must not take.
	envOnly := func(args []string) result {
		var stdout, stderr bytes.Buffer
		code := cli.RunWith(args, &stdout, &stderr, cli.Deps{
			Now: fixedClock,
			Env: func(name string) string {
				if name == "SITREP_CONFIG" {
					return missing
				}
				return ""
			},
		})
		return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
	}

	t.Run("an unmatched key prefix", func(t *testing.T) {
		// A Jira-style key with no Profile to serve it is fatal, and the
		// error is where the user is told which file to write.
		got := envOnly([]string{"PROJ-12", "--json"})

		if got.code != 1 {
			t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
		}
		if !strings.Contains(got.stderr, missing) {
			t.Errorf("stderr = %q, want it to name %q", got.stderr, missing)
		}
		if strings.Contains(got.stderr, "~/.config/sitrep/config.yml") {
			t.Errorf("stderr = %q, want the file the user set rather than the documented default", got.stderr)
		}
	})

	t.Run("an unknown --profile", func(t *testing.T) {
		got := envOnly([]string{"--profile", "nope", "PROJ-12", "--json"})

		if got.code != 1 {
			t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
		}
		if !strings.Contains(got.stderr, missing) {
			t.Errorf("stderr = %q, want it to name %q", got.stderr, missing)
		}
	})
}
