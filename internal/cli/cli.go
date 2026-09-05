// Package cli is the testable seam between the sitrep process and its
// behaviour: everything the binary does is reachable through Run, which takes
// its arguments and writers as parameters and returns an exit code instead of
// calling os.Exit.
//
// Beneath Run sits RunWith, which takes the run's dependencies — the Provider
// and the clock — explicitly, so a test can drive the whole program against the
// fake Provider with a fixed clock and compare bytes.
package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/niekcandaele/sitrep/internal/buildinfo"
	"github.com/niekcandaele/sitrep/internal/config"
	"github.com/niekcandaele/sitrep/internal/detailfanout"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/github"
	"github.com/niekcandaele/sitrep/internal/provider/gitlab"
	"github.com/niekcandaele/sitrep/internal/ref"
	"github.com/niekcandaele/sitrep/internal/render/jsonout"
	"github.com/niekcandaele/sitrep/internal/render/plain"
	"github.com/niekcandaele/sitrep/internal/termtext"
	"github.com/niekcandaele/sitrep/internal/tui"
)

// Exit codes returned by Run. They are a contract: 0 means the report was
// produced, 1 means sitrep tried and failed, 2 means the command line was
// wrong, and 130 means a signal ended the run.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
	// exitInterrupted is the conventional shell exit code for a process ended
	// by SIGINT (128 + 2). sitrep needs its own, because signal.NotifyContext
	// disables the default SIGINT kill: without this a ctrl+c during a slow
	// fetch would exit 1 and blame the Tracker for the user's own interrupt.
	exitInterrupted = 130
)

// interrupted reports whether the run was cancelled by a signal rather than
// failing. A ctrl+c is not an error and prints nothing: the user knows what
// they did.
func interrupted(ctx context.Context) (int, bool) {
	if ctx.Err() != nil {
		return exitInterrupted, true
	}
	return exitFailure, false
}

const usage = `sitrep - a read-only terminal ticket viewer.

Usage:
  sitrep [flags] <ref>
  sitrep [flags] <ref> <ref>...
  sitrep [flags] -
  sitrep [flags] --query <query>

Selectors:
  One Ref selects an Epic Watchlist. If it resolves to a plain Ticket instead,
  sitrep opens that Ticket's Detail; --links needs a Watchlist and is refused
  there.

  Two or more positional Refs select exact Tickets in order. They need no common
  Epic, but must use one Tracker and connection.

  A sole "-" reads whitespace-separated Refs from stdin. It must be the only
  positional argument; even one stdin Ref keeps exact Ref-list semantics.

  --query passes the exact, opaque tracker-native Query to the selected Tracker.
  It cannot be combined with positional Refs or stdin. Use an explicit Profile,
  or an unambiguous supported GitHub/GitLab origin in the current clone.

Ref forms:
  111                                        bare issue number
  #111                                       bare issue number
  acme/widgets#111                           owner, repository, number
  ABC-123                                    Jira key; its prefix matches a Profile
  acme&12                                    GitLab native epic Ref
  acme/widgets%3                             GitLab milestone Ref
  https://github.com/acme/widgets/issues/111
                                             GitHub issue URL
  https://acme.atlassian.net/browse/ABC-123
                                             Jira browse URL
  https://gitlab.com/acme/widgets/-/issues/7
                                             GitLab issue/work-item URL
  https://gitlab.com/groups/acme/-/epics/12
                                             GitLab native epic URL
  https://gitlab.com/acme/widgets/-/milestones/3
                                             GitLab milestone URL

  A bare number is resolved through the current clone's origin remote; the other
  forms work anywhere. GitLab's &N names a native Epic; %N is the milestone-as-
  Epic fallback available on GitLab Free.

Monitor:
  The monitor opens on the Watchlist. Press v for the Frontier, which draws the
  same Watchlist as nodes and blocking edges to answer one question: which
  Tickets can be picked up right now.

Flags:
  -h, --help              show this help and exit
      --interval <dur>    how often the monitor refreshes (default 60s,
                          minimum 5s)
      --no-mouse          start the monitor without mouse capture
      --json              print a one-shot JSON report and exit
      --links             with --json, add each Ticket's unmet blockers and
                          whether it is Actionable - Todo with every blocker
                          finished or cancelled. Needs a Watchlist, and costs
                          one Detail fetch per Ticket
      --plain             print a one-shot Watchlist or Ticket report and exit
      --profile <name>    Profile from ~/.config/sitrep/config.yml (default:
                          matched from the route or Refs)
      --provider <name>   Provider to read from: "auto" (the default) detects
                          from the route or Refs; "github", "gitlab", or "jira"
                          forces that driver; "fake" serves a fixture Watchlist
      --fake-fixture <name>
                          with --provider fake, use "blocking" or
                          "no-blocking-links"; omission keeps the legacy fixture
      --fake-delay <dur>  with --provider fake, add artificial per-read latency
                          for observing loading and progress
      --query <query>     exact tracker-native Query selecting a Watchlist
      --version           show version information and exit
`

// Deps are the injectable dependencies of a sitrep run. Every field's zero
// value is the production behaviour, which is what Run passes.
type Deps struct {
	// Provider serves the run. When nil it is resolved from the Ref and
	// --provider; when set it wins outright and nothing is constructed.
	Provider provider.Provider
	// Now reads the clock for the snapshot's timestamp. When nil it is
	// time.Now.
	Now func() time.Time
	// RemoteLookup reads the git remote that resolves a bare Ref number.
	// When nil it is the real `git remote get-url`.
	RemoteLookup ref.RemoteLookup
	// Dir is the working directory whose git remote resolves a bare number.
	// When empty it is the process working directory.
	Dir string
	// TokenSource discovers the GitHub API token. When nil it is
	// github.DefaultTokenSource.
	TokenSource github.TokenSource
	// GitLabTokenSource discovers the GitLab API token. When nil it is
	// gitlab.DefaultTokenSource.
	GitLabTokenSource gitlab.TokenSource
	// Stdin carries selector bytes for a sole "-" argument and otherwise remains
	// the monitor's key input. When nil it is os.Stdin.
	Stdin io.Reader
	// OpenTTY opens the controlling terminal used for monitor keys after Stdin
	// carried selector bytes. When nil it opens /dev/tty read-only.
	OpenTTY func() (io.ReadCloser, error)
	// RunMonitor starts the full-screen monitor. When nil it is tui.Run.
	RunMonitor func(context.Context, tui.Options) error
	// Config is the loaded global config. When non-nil it wins outright and no
	// file is read: tests inject it, and so does any caller that has already
	// read one.
	Config *config.Config
	// ConfigPath overrides the config file location. When empty it is
	// config.DefaultPath.
	ConfigPath string
	// Env reads environment variables when resolving a Profile's credential and
	// when locating the config file. When nil it is os.Getenv. Tests inject a
	// map so no test mutates — or reads — the process environment.
	Env func(string) string
}

// loadConfig reads the run's global config, or decides not to read one at all.
//
// When Config and ConfigPath are both zero, no Profile was explicitly requested,
// AND the run serves itself — an injected Provider, or --provider fake — no
// config file is read. Both are a test or a development run, and such a run must
// never depend on whatever happens to be in the developer's home directory. An
// explicit --profile is the exception: naming one means the config must be read
// and validated even when the Provider itself is injected.
func (d Deps) loadConfig(providerName, profileName string) (config.Config, error) {
	switch {
	case d.Config != nil:
		return *d.Config, nil
	case d.ConfigPath != "":
		return config.Load(d.ConfigPath)
	case profileName != "":
		path, err := config.DefaultPath(d.Env)
		if err != nil {
			return config.Config{}, err
		}
		return config.Load(path)
	case d.Provider != nil, providerName == providerFake:
		return config.Config{}, nil
	}

	path, err := config.DefaultPath(d.Env)
	if err != nil {
		return config.Config{}, err
	}
	return config.Load(path)
}

// env returns the run's environment reader, which is what a Profile's
// credential is resolved through.
func (d Deps) env() func(string) string {
	if d.Env == nil {
		return os.Getenv
	}
	return d.Env
}

// The monitor's refresh cadence. The default is one named constant rather than
// a literal in the flag call so a config Profile has a single thing to
// override; the floor lives in internal/config, because a Profile's
// refresh_interval is held to the same one and a floor with two definitions is
// a floor with two values.
const (
	defaultRefreshInterval = 60 * time.Second
	minRefreshInterval     = config.MinRefreshInterval
)

// effectiveInterval picks the monitor's cadence: an explicit --interval beats a
// Profile's refresh_interval, which beats sitrep's default. A flag the user did
// not type must not silently outrank the Profile they wrote down, which is why
// this takes "was it set" rather than the value alone.
func effectiveInterval(flagSet bool, flagValue, profileValue time.Duration) time.Duration {
	switch {
	case flagSet:
		return flagValue
	case profileValue > 0:
		return profileValue
	default:
		return defaultRefreshInterval
	}
}

// Run executes sitrep with the given command-line arguments (excluding argv[0])
// and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	return RunWith(args, stdout, stderr, Deps{})
}

// RunWith executes sitrep with the given dependencies and returns the process
// exit code. Tests drive the whole program through this seam.
func RunWith(args []string, stdout, stderr io.Writer, deps Deps) int {
	fs := flag.NewFlagSet(buildinfo.Name, flag.ContinueOnError)
	// The flag package prints its own message and usage on both -h and a parse
	// error, in its own voice and with single-dash flag names sitrep spells
	// nowhere. Run owns every byte it writes, so the built-in printer is
	// silenced on both counts and the parse error is re-emitted below.
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	showVersion := fs.Bool("version", false, "show version information and exit")
	asJSON := fs.Bool("json", false, "print a one-shot JSON report and exit")
	asPlain := fs.Bool("plain", false, "print a one-shot Watchlist or Ticket report and exit")
	withLinks := fs.Bool("links", false, "with --json, add Actionable and unmet blockers (per-Ticket fetch)")
	providerName := fs.String("provider", defaultProviderName, "Provider to read from")
	profileName := fs.String("profile", "", "the Profile to connect with")
	fakeFixtureFlag := fs.String("fake-fixture", "", "fake fixture: blocking or no-blocking-links")
	fakeDelay := fs.Duration("fake-delay", 0, "artificial latency for fake Provider reads")
	query := fs.String("query", "", "Tracker-native Watchlist query")
	interval := fs.Duration("interval", defaultRefreshInterval, "how often the monitor refreshes")
	noMouse := fs.Bool("no-mouse", false, "start the monitor without mouse capture")

	positional, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return exitOK
		}
		return usageError(stderr, flagErrorMessage(fs, err))
	}

	if *showVersion {
		fmt.Fprintln(stdout, buildinfo.String())
		return exitOK
	}

	// Two one-shot renderers cannot both own stdout, and silently preferring
	// one would hide a scripting mistake.
	if *asJSON && *asPlain {
		return usageError(stderr, "--json and --plain are mutually exclusive")
	}

	// Unlike --interval, --links is never a default a script inherited from a
	// Profile: it is only ever present because someone typed it, and typing it
	// means "give me blocking data", which neither --plain nor the monitor will
	// produce. Failing is the honest answer; ignoring it would silently drop the
	// data the caller asked for.
	if *withLinks && !*asJSON {
		return usageError(stderr, "--links requires --json")
	}

	// --interval only means anything to the monitor. Rejecting it beside a
	// one-shot flag would fail a script for a setting the script never used;
	// checking it only on the path that honours it keeps the error honest.
	if !*asJSON && !*asPlain {
		if code, ok := checkInterval(stderr, *interval); !ok {
			return code
		}
	}

	if !knownProviderName(*providerName) {
		return usageError(stderr, fmt.Sprintf("unknown provider %q", *providerName))
	}

	fakeSettings := fakeProviderSettings{
		fixture:    fakeFixture(*fakeFixtureFlag),
		fixtureSet: isFlagSet(fs, "fake-fixture"),
		delay:      *fakeDelay,
		delaySet:   isFlagSet(fs, "fake-delay"),
	}
	if code, ok := checkFakeProviderSettings(stderr,
		isFlagSet(fs, "provider") && *providerName == providerFake, fakeSettings); !ok {
		return code
	}

	querySelected := isFlagSet(fs, "query")
	if querySelected && len(positional) != 0 {
		return usageError(stderr, `--query cannot be combined with positional Refs or "-"`)
	}
	if !querySelected && len(positional) == 0 {
		return usageError(stderr, `a Selector is required: pass one or more Refs, "-" for stdin, or --query`)
	}

	stdinSelected := false
	if !querySelected {
		stdinSelected, err = stdinSelection(positional)
		if err != nil {
			return usageError(stderr, err.Error())
		}
		if stdinSelected {
			positional, err = readStdinRefs(deps.stdin())
			if err != nil {
				return runtimeError(stderr, err)
			}
		}
	}

	// The command line was fine, so everything from here on is a runtime
	// failure rather than a usage error — starting with a config file that does
	// not parse.
	cfg, err := deps.loadConfig(*providerName, *profileName)
	if err != nil {
		return runtimeError(stderr, err)
	}

	// The run's context is cancelled on SIGINT and SIGTERM from here on, so a
	// slow pre-flight fetch — and any in-flight fetch after it — goes with a
	// ctrl+c instead of holding the process open for the Tracker's HTTP timeout.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	selection, err := deps.resolveSelection(ctx, cfg, positional, stdinSelected, querySelected, *query, *providerName, *profileName)
	if err != nil {
		return runtimeError(stderr, err)
	}
	selector := selection.selector
	r := selection.first
	prof := selection.profile

	refresh := effectiveInterval(isFlagSet(fs, "interval"), *interval, profileInterval(prof))

	p := deps.Provider
	if p == nil {
		if p, err = deps.newProvider(*providerName, selection.route, prof, cfg.Path, fakeSettings); err != nil {
			return runtimeError(stderr, err)
		}
	}
	// Every piece of tracker-controlled text crosses the Provider seam exactly
	// once, so it is cleaned of terminal control sequences exactly once, here:
	// the TUI, both one-shot renderers and the decoder path all read what comes
	// out of this call.
	p = provider.Sanitized(p)

	// One logical batched fetch, before the mode switch, because every mode needs
	// its result. For a single Ref it also decides whether the Ref named an Epic
	// or a Ticket; either Selector kind seeds the monitor without fetching again.
	snap, err := p.Resolve(ctx, selector)
	if err != nil {
		if code, ok := interrupted(ctx); ok {
			return code
		}
		if *asJSON || *asPlain {
			return runtimeError(stderr, err)
		}
		if !provider.KindOf(err).Retryable() {
			// A bad ref and a rejected credential are not going to fix
			// themselves on the next tick, and an alt-screen retry loop is a
			// worse way to read one sentence than stderr is. Note the asymmetry:
			// this is the *pre-flight* only. A monitor that is already open
			// keeps its last good reading and its footer message on any failed
			// refresh, retryable or not — that asymmetry is the monitor's
			// contract and users depend on it.
			return runtimeError(stderr, err)
		}
		// An otherwise-failed pre-flight opens the monitor anyway, unseeded, and
		// lets the TUI's own first fetch fail and draw its retry body: a monitor
		// must not exit on one bad DNS lookup, and one wasted
		// request in an already-failing situation is the right price for keeping
		// that.
		return runMonitor(ctx, stdout, stderr, deps, stdinSelected, tui.Options{
			Source:       tui.SelectorSource(p, selector, deps.clock()),
			DetailSource: tui.TicketDetailSource(p),
			DetailFanout: detailfanout.FromProvider(p),
			InitialError: err,
			Interval:     refresh,
			NoMouse:      *noMouse,
		})
	}
	snap = provider.StampSnapshot(p, snap, deps.now())

	if list, ok := selector.(provider.RefListSelector); ok && len(snap.Tickets) != len(list.Refs) {
		return runtimeError(stderr, provider.Errorf(provider.KindUnavailable,
			"%s: Ref-list Resolve returned %d Tickets for %d Refs", p.Name(), len(snap.Tickets), len(list.Refs)))
	}

	if _, epic := selector.(provider.EpicSelector); epic && decodesToTicket(snap) {
		// Blocking data is produced for a Watchlist, and this Ref resolved to
		// one Ticket. It is the same misuse as --links without --json, so it is
		// the same answer: ignoring the flag would silently drop the data the
		// caller asked for. The Ref itself resolved perfectly well, so this is
		// not a bad Ref.
		if *withLinks {
			return usageError(stderr, fmt.Sprintf(
				"--links needs a Watchlist: %s names a single Ticket", snap.Epic.Key))
		}
		if *asJSON || *asPlain {
			return runDecodedOneShot(ctx, stdout, stderr, p, snap, *asJSON)
		}
		return runDecodedMonitor(ctx, stdout, stderr, deps, p, r, snap, refresh, *noMouse)
	}

	switch {
	case *asJSON:
		blocking, linksStatus := blockingGraphFor(ctx, p, snap, *withLinks, stderr)
		// A ctrl+c during the fan-out ends the run. Emitting the half-fetched
		// document would be a lie by omission: most Tickets would say
		// links_known: false because the user pressed a key, not because the
		// Tracker could not answer.
		if code, ok := interrupted(ctx); ok {
			return code
		}
		// Durable status starts after the interrupt check: this is the one-shot
		// run's commit point. A signal after it does not turn a completed report
		// into exit 130 with a warning but no document.
		if linksStatus != nil {
			linksStatus.report()
		}
		return writeReport(stdout, stderr, func(w io.Writer) error {
			return jsonout.RenderWatchlist(w, jsonout.WatchlistDocument{
				Snapshot:     snap,
				Selector:     selector,
				ProviderName: p.Name(),
				Blocking:     blocking,
			})
		})
	case *asPlain:
		return writeReport(stdout, stderr, func(w io.Writer) error {
			return plain.RenderWatchlist(w, snap)
		})
	}

	initial := tui.ListFromWatchlistSnapshot(snap)
	return runMonitor(ctx, stdout, stderr, deps, stdinSelected, tui.Options{
		Source:       tui.SelectorSource(p, selector, deps.clock()),
		DetailSource: tui.TicketDetailSource(p),
		DetailFanout: detailfanout.FromProvider(p),
		Initial:      &initial,
		Interval:     refresh,
		NoMouse:      *noMouse,
	})
}

// blockingGraphFor reads every member's Detail and derives the Watchlist's
// blocking graph. It returns nil without issuing Detail reads when the caller
// did not request Links or the Provider does not declare blocking support.
//
// nil is what makes --json's default free: ADR-0003 still forbids a render from
// fanning Detail reads out, and Amendment 4 permits it here only because --links
// is an explicit user action. An undeclared BlockingLinks Capability leaves the
// keys absent and is explained on stderr; emitting actionable: false everywhere
// would read as a computed negative when BuildBlockingGraph in fact claims
// nothing.
//
// A failed Detail read is non-fatal and produces one aggregate stderr notice.
// The Ticket remains absent from the links map, which is exactly the
// unreadable-Links tri-state, and the document carries links_known: false.
func blockingGraphFor(ctx context.Context, p provider.Provider, snap model.WatchlistSnapshot,
	withLinks bool, stderr io.Writer,
) (*model.BlockingGraph, *linksStatus) {
	if !withLinks {
		return nil, nil
	}
	if !snap.Capabilities.BlockingLinks {
		return nil, newMissingBlockingLinksStatus(stderr)
	}

	// A one-shot run holds no Detail cache, so nothing is skipped; Plan is still
	// the way in, for its canonical order and its empty-ID skip.
	ids := detailfanout.Plan(snap.Tickets, nil)
	status := newLinksStatus(stderr, len(ids))
	details := make(map[model.TicketID]model.Detail, len(ids))
	runErr := detailfanout.Run(ctx, detailfanout.FromProvider(p), ids, func(o detailfanout.Outcome) {
		if o.Err == nil {
			details[o.ID] = o.Detail
		}
		status.record(o)
	})
	if runErr != nil && ctx.Err() == nil {
		status.recordBatchFailure()
	}
	status.complete()

	graph := model.BuildBlockingGraph(snap.Tickets, detailfanout.Links(details), snap.Capabilities)
	return &graph, status
}

// stdinSelection recognizes the transport sentinel only in argv. It must be
// exclusive so command-line and streamed membership can never be merged.
func stdinSelection(positional []string) (bool, error) {
	for _, arg := range positional {
		if arg != "-" {
			continue
		}
		if len(positional) != 1 {
			return false, errors.New(`"-" reads Refs from stdin and must be the only positional argument`)
		}
		return true, nil
	}
	return false, nil
}

const maxStdinSelectorBytes = 1 << 20

func readStdinRefs(input io.Reader) ([]string, error) {
	data, err := io.ReadAll(io.LimitReader(input, maxStdinSelectorBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading Refs from stdin: %w", err)
	}
	if len(data) > maxStdinSelectorBytes {
		return nil, fmt.Errorf("stdin Selector input exceeds the %d-byte limit", maxStdinSelectorBytes)
	}
	refs := strings.Fields(string(data))
	if len(refs) == 0 {
		return nil, errors.New("no Refs were provided on stdin")
	}
	return refs, nil
}

// runMonitor opens the live monitor and blocks until the user quits. The
// caller's Options carry the run's seams; the terminal and the clock are this
// function's to fill in, so no call site can forget one. A stdin-selected
// monitor has already consumed Stdin as selector data, so its keys come from
// the controlling terminal instead.
func runMonitor(ctx context.Context, stdout, stderr io.Writer, deps Deps, stdinSelected bool, opts tui.Options) int {
	opts.Now = deps.clock()
	opts.Input = deps.stdin()
	opts.Output = stdout

	if stdinSelected {
		input, err := deps.openTTY()
		if err != nil {
			code, failure := monitorExit(ctx, fmt.Errorf("opening controlling terminal: %w", err))
			if failure != nil {
				return runtimeError(stderr, failure)
			}
			return code
		}
		defer input.Close()
		opts.Input = input
	}

	code, failure := monitorExit(ctx, deps.runTUI(ctx, opts))
	if failure != nil {
		return runtimeError(stderr, failure)
	}
	return code
}

// monitorExit turns the monitor's outcome into an exit code, and into the one
// sentence stderr gets when there is one.
//
// A ctrl+c prints nothing and exits 130 — README's contract, and the user knows
// what they did. Raw mode delivers ctrl+c as an ordinary key press rather than
// a signal, so the monitor reports it as ErrInterrupted; a signal from outside
// arrives on the context instead, and means the same thing. Anything else is a
// real failure, and the one thing that can go wrong here is not having a
// terminal.
func monitorExit(ctx context.Context, err error) (int, error) {
	if errors.Is(err, tui.ErrInterrupted) {
		return exitInterrupted, nil
	}
	if err != nil {
		// Bubble Tea is the one thing here that knows whether it has a
		// terminal, so this is where "you are in a pipe" gets named — with the
		// way out, since both one-shot modes work anywhere.
		return exitFailure, fmt.Errorf("the monitor needs a terminal (use --plain or --json here): %w", err)
	}
	if code, ok := interrupted(ctx); ok {
		return code, nil
	}
	return exitOK, nil
}

// checkInterval rejects a refresh cadence that would hammer a Tracker. sitrep
// is read-only and polite: a sub-second poll is a way to get rate-limited, not
// a feature.
func checkInterval(stderr io.Writer, d time.Duration) (int, bool) {
	switch {
	case d <= 0:
		return usageError(stderr, "refresh interval must be positive"), false
	case d < minRefreshInterval:
		return usageError(stderr, fmt.Sprintf("refresh interval must be at least %s", minRefreshInterval)), false
	default:
		return exitOK, true
	}
}

func checkFakeProviderSettings(stderr io.Writer, explicitFake bool, settings fakeProviderSettings) (int, bool) {
	if !explicitFake {
		switch {
		case settings.fixtureSet:
			return usageError(stderr, "--fake-fixture requires --provider fake"), false
		case settings.delaySet:
			return usageError(stderr, "--fake-delay requires --provider fake"), false
		default:
			return exitOK, true
		}
	}
	if settings.fixtureSet && !settings.fixture.valid() {
		return usageError(stderr,
			`--fake-fixture must be "blocking" or "no-blocking-links"`), false
	}
	if settings.delaySet && settings.delay <= 0 {
		return usageError(stderr, "--fake-delay must be positive"), false
	}
	return exitOK, true
}

// writeReport writes one rendered report to stdout. render is the mode's
// renderer; everything before it — ref resolution, Provider construction, the
// single batched fetch, the clock stamp — is shared, which is what makes
// --plain and --json two views of one code path rather than two programs.
//
// It renders into a buffer and only then copies to stdout: a half-written
// report followed by an error would poison whatever is consuming it, whether
// that is a script reading JSON or a human reading text.
func writeReport(stdout, stderr io.Writer, render func(io.Writer) error) int {
	var buf bytes.Buffer
	if err := render(&buf); err != nil {
		return runtimeError(stderr, err)
	}
	if _, err := io.Copy(stdout, &buf); err != nil {
		return runtimeError(stderr, err)
	}
	return exitOK
}

// isFlagSet reports whether the user actually typed the named flag, as opposed
// to receiving its default. The flag package records every explicitly-set flag
// and does not forget them across the repeated Parse calls parseArgs makes,
// which is what makes this readable after parsing rather than during it.
func isFlagSet(fs *flag.FlagSet, name string) bool {
	var set bool
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// parseArgs parses flags and positional arguments in any order. The flag
// package stops at the first non-flag argument, which would make the natural
// "sitrep 111 --json" a usage error; parsing what is left after each positional
// argument accepts both orders.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
}

// flagErrorMessage rewrites the flag package's own wording into sitrep's. The
// stream and the exit code were already right; only the sentence was unowned —
// it named flags with a single dash, which no sitrep document does, and said
// "parse error" where the actual problem is that a duration needs a unit.
func flagErrorMessage(fs *flag.FlagSet, err error) string {
	msg := err.Error()
	fs.VisitAll(func(f *flag.Flag) {
		msg = strings.ReplaceAll(msg, " -"+f.Name, " --"+f.Name)
	})
	// An undeclared flag is not in that list, and the flag package strips the
	// second dash off whatever the user typed.
	msg = strings.Replace(msg, "not defined: -", "not defined: --", 1)
	if strings.Contains(msg, "--interval") || strings.Contains(msg, "--fake-delay") {
		msg += " (durations need a unit: 60s, 2m)"
	}
	return msg
}

func usageError(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "%s: %s\nRun \"%s --help\" for usage.\n", buildinfo.Name, msg, buildinfo.Name)
	return exitUsage
}

// runtimeError writes the one sentence a failed run leaves behind. The message
// is sanitized as a backstop: provider.Errorf already cleans everything a
// driver builds, and this covers anything constructed outside it that still
// quotes tracker text.
func runtimeError(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "%s: %s\n", buildinfo.Name, termtext.Line(err.Error()))
	return exitFailure
}

// now returns the run's clock reading.
func (d Deps) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

// clock returns the run's clock itself, for the monitor, which reads it on
// every frame rather than once.
func (d Deps) clock() func() time.Time {
	if d.Now == nil {
		return time.Now
	}
	return d.Now
}

// stdin returns selector input for a sole "-" argument and otherwise the
// monitor's key input.
func (d Deps) stdin() io.Reader {
	if d.Stdin == nil {
		return os.Stdin
	}
	return d.Stdin
}

func (d Deps) openTTY() (io.ReadCloser, error) {
	if d.OpenTTY != nil {
		return d.OpenTTY()
	}
	return os.OpenFile("/dev/tty", os.O_RDONLY, 0)
}

func (d Deps) runTUI(ctx context.Context, opts tui.Options) error {
	if d.RunMonitor != nil {
		return d.RunMonitor(ctx, opts)
	}
	return tui.Run(ctx, opts)
}
