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
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
	"github.com/niekcandaele/sitrep/internal/provider/github"
	"github.com/niekcandaele/sitrep/internal/provider/gitlab"
	"github.com/niekcandaele/sitrep/internal/provider/jira"
	"github.com/niekcandaele/sitrep/internal/ref"
	"github.com/niekcandaele/sitrep/internal/render/jsonout"
	"github.com/niekcandaele/sitrep/internal/render/plain"
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
  sitrep opens that Ticket's Detail.

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
  https://github.com/acme/widgets/issues/111  GitHub issue URL
  ABC-123                                    Jira key; its prefix matches a Profile
  https://acme.atlassian.net/browse/ABC-123   Jira browse URL
  https://gitlab.com/acme/widgets/-/issues/7  GitLab issue/work-item URL
  acme&12                                    GitLab native epic Ref
  https://gitlab.com/groups/acme/-/epics/12  GitLab native epic URL
  acme/widgets%3                             GitLab milestone Ref
  https://gitlab.com/acme/widgets/-/milestones/3
                                             GitLab milestone URL

  A bare number is resolved through the current clone's origin remote; the other
  forms work anywhere. GitLab's &N names a native Epic; %N is the milestone-as-
  Epic fallback available on GitLab Free.

Flags:
  -h, --help              show this help and exit
      --interval <dur>    how often the monitor refreshes (default 60s)
      --no-mouse          start the monitor without mouse capture
      --json              print a one-shot JSON report and exit
      --plain             print a one-shot Watchlist or Ticket report and exit
      --profile <name>    Profile from ~/.config/sitrep/config.yml (default:
                          matched from the route or Refs)
      --provider <name>   Provider to read from: "auto" (the default) detects
                          from the route or Refs; "github", "gitlab", or "jira"
                          forces that driver; "fake" serves a fixture Watchlist
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
	providerName := fs.String("provider", defaultProviderName, "Provider to read from")
	profileName := fs.String("profile", "", "the Profile to connect with")
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
		if p, err = deps.newProvider(*providerName, selection.route, prof, cfg.Path); err != nil {
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
		if *asJSON || *asPlain {
			return runDecodedOneShot(ctx, stdout, stderr, p, snap, *asJSON)
		}
		return runDecodedMonitor(ctx, stdout, stderr, deps, p, r, snap, refresh, *noMouse)
	}

	switch {
	case *asJSON:
		return writeReport(stdout, stderr, func(w io.Writer) error {
			return jsonout.RenderWatchlist(w, snap, selector, p.Name())
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
		Initial:      &initial,
		Interval:     refresh,
		NoMouse:      *noMouse,
	})
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

type connectionRoute struct {
	tracker    ref.Tracker
	host       string
	gitLabPath string
	raw        string
}

type resolvedSelection struct {
	selector provider.Selector
	first    ref.Ref
	profile  *config.Profile
	route    connectionRoute
}

// resolveSelection resolves every supplied Ref once and turns the invocation
// form into the Selector reused by preflight and every refresh. Routing stays a
// startup concern: Providers never read git remotes or Profiles. forceRefList
// preserves stdin's explicit-list meaning when it carries only one Ref.
func (d Deps) resolveSelection(ctx context.Context, cfg config.Config, rawRefs []string, forceRefList, querySelected bool,
	query, providerName, profileName string) (resolvedSelection, error) {
	if querySelected {
		return d.resolveQuerySelection(ctx, cfg, query, providerName, profileName)
	}

	refs := make([]ref.Ref, len(rawRefs))
	for i, raw := range rawRefs {
		r, err := d.resolveRef(ctx, raw, providerName)
		if err != nil {
			return resolvedSelection{}, err
		}
		r = retagProfileClaimedHost(cfg, r)
		if providerName != providerAuto && providerName != providerFake {
			if r.Tracker, err = forceTracker(providerName, r); err != nil {
				return resolvedSelection{}, err
			}
		}
		refs[i] = r
	}

	if left, right, ok := firstTrackerConflict(refs); ok {
		completed := append([]ref.Ref(nil), refs...)
		for i, r := range completed {
			if prof, err := selectProfile(cfg, r, profileName); err == nil && prof != nil {
				completed[i] = prof.Complete(r)
			}
		}
		return resolvedSelection{}, mixedTrackerError(completed[left], completed[right])
	}

	profiles := make([]*config.Profile, len(refs))
	for i, r := range refs {
		prof, err := selectProfile(cfg, r, profileName)
		if err != nil {
			return resolvedSelection{}, err
		}
		profiles[i] = prof
		if prof != nil {
			refs[i] = prof.Complete(r)
		}
	}

	if left, right, ok := firstRouteConflict(refs); ok {
		return resolvedSelection{}, routeConflictError(refs[left], refs[right])
	}
	if left, right, ok := firstProfileConflict(profiles); ok {
		//nolint:staticcheck // The specified CLI sentence starts with the domain term "Refs".
		return resolvedSelection{}, fmt.Errorf("Refs in one Watchlist resolve through different Profiles (%q and %q); pass --profile to choose one",
			profileIdentity(profiles[left]), profileIdentity(profiles[right]))
	}

	selection := resolvedSelection{
		first:   refs[0],
		profile: profiles[0],
		route:   routeFromRef(refs[0], profiles[0]),
	}
	if len(rawRefs) == 1 && !forceRefList {
		selection.selector = provider.EpicSelector{Ref: refs[0]}
		return selection, nil
	}

	unique := deduplicateRefs(refs)
	selection.first = unique[0]
	selection.route = routeFromRef(unique[0], profiles[0])
	selection.selector = provider.RefListSelector{Refs: unique}
	return selection, nil
}

func routeFromRef(r ref.Ref, prof *config.Profile) connectionRoute {
	return connectionRoute{
		tracker:    r.Tracker,
		host:       r.Host,
		gitLabPath: profileProject(prof),
		raw:        r.Raw,
	}
}

func (d Deps) resolveQuerySelection(ctx context.Context, cfg config.Config, query, providerName, profileName string) (resolvedSelection, error) {
	selection := resolvedSelection{selector: provider.QuerySelector{Query: query}}

	if profileName != "" {
		prof, ok, err := cfg.Select(ref.Ref{}, profileName)
		if err != nil {
			return resolvedSelection{}, err
		}
		if !ok {
			panic("an explicit Profile selection returned no Profile")
		}
		if providerName != providerAuto && providerName != prof.Provider {
			return resolvedSelection{}, fmt.Errorf("provider %q does not match profile %q provider %q",
				providerName, prof.Name, prof.Provider)
		}
		r := prof.Complete(ref.Ref{Raw: "--query"})
		selection.profile = &prof
		selection.route = connectionRoute{
			tracker:    ref.Tracker(prof.Provider),
			host:       r.Host,
			gitLabPath: prof.Project,
			raw:        "--query",
		}
		return selection, nil
	}

	if d.Provider != nil || providerName == providerFake {
		return selection, nil
	}

	origin, err := ref.ResolveOrigin(ctx, ref.WithRemoteLookup(d.RemoteLookup), ref.WithDir(d.Dir))
	if err != nil {
		return resolvedSelection{}, fmt.Errorf("--query needs --profile or an unambiguous git origin in the current directory: %w", err)
	}

	knownTracker := ref.HostTracker(origin.Host)
	switch providerName {
	case providerAuto:
		if knownTracker != ref.TrackerUnknown {
			origin.Tracker = knownTracker
		} else {
			matches := profilesForHost(cfg, origin.Host)
			switch len(matches) {
			case 1:
				selection.profile = &matches[0]
				origin.Tracker = ref.Tracker(matches[0].Provider)
			default:
				if len(matches) > 1 {
					names := make([]string, len(matches))
					for i := range matches {
						names[i] = matches[i].Name
					}
					return resolvedSelection{}, fmt.Errorf("profiles %s all match %s — pass --profile to choose",
						quoteJoin(names), origin.Host)
				}
				return resolvedSelection{}, fmt.Errorf("cannot tell whether %s uses GitHub or GitLab — pass --profile or --provider",
					origin.Host)
			}
		}
	case providerGitHub, providerGitLab:
		forced := ref.Tracker(providerName)
		if knownTracker != ref.TrackerUnknown && knownTracker != forced {
			return resolvedSelection{}, fmt.Errorf("git origin host %s is %s, not %s",
				origin.Host, providerDisplayName(string(knownTracker)), providerDisplayName(providerName))
		}
		origin.Tracker = forced
	default:
		return resolvedSelection{}, fmt.Errorf("--query with provider %q requires --profile", providerName)
	}

	if selection.profile == nil {
		prof, err := selectProfile(cfg, origin, "")
		if err != nil {
			return resolvedSelection{}, err
		}
		selection.profile = prof
	}
	selection.route = connectionRoute{
		tracker: origin.Tracker,
		host:    origin.Host,
		raw:     origin.Raw,
	}
	if origin.Tracker == ref.TrackerGitLab {
		selection.route.gitLabPath = strings.Trim(origin.Owner+"/"+origin.Repo, "/")
		if selection.profile != nil && selection.profile.Project != "" {
			selection.route.gitLabPath = selection.profile.Project
		}
	}
	return selection, nil
}

func profilesForHost(cfg config.Config, host string) []config.Profile {
	matches := make([]config.Profile, 0, len(cfg.Profiles))
	for _, name := range cfg.Names() {
		prof := cfg.Profiles[name]
		effectiveHost := prof.Host
		if prof.Provider == providerGitHub && effectiveHost == "" {
			effectiveHost = "github.com"
		}
		if (prof.Provider == providerGitHub || prof.Provider == providerGitLab) &&
			strings.EqualFold(strings.TrimSpace(effectiveHost), strings.TrimSpace(host)) {
			matches = append(matches, prof)
		}
	}
	return matches
}

func firstTrackerConflict(refs []ref.Ref) (int, int, bool) {
	for i := range refs {
		for j := i + 1; j < len(refs); j++ {
			if refs[i].Tracker != ref.TrackerUnknown && refs[j].Tracker != ref.TrackerUnknown &&
				refs[i].Tracker != refs[j].Tracker {
				return i, j, true
			}
		}
	}
	return 0, 0, false
}

func firstRouteConflict(refs []ref.Ref) (int, int, bool) {
	for i := range refs {
		for j := i + 1; j < len(refs); j++ {
			if refs[i].Tracker != refs[j].Tracker || !strings.EqualFold(refs[i].Host, refs[j].Host) {
				return i, j, true
			}
		}
	}
	return 0, 0, false
}

func mixedTrackerError(left, right ref.Ref) error {
	//nolint:staticcheck // The specified CLI sentence starts with the domain term "Refs".
	return fmt.Errorf("Refs in one Watchlist must use one Tracker; %q resolves to %s (%s), while %q resolves to %s (%s)",
		selectorRefLabel(left), trackerDisplayName(left.Tracker), left.Host,
		selectorRefLabel(right), trackerDisplayName(right.Tracker), right.Host)
}

func routeConflictError(left, right ref.Ref) error {
	//nolint:staticcheck // The specified CLI sentence starts with the domain term "Refs".
	return fmt.Errorf("Refs in one Watchlist must use one Tracker connection and host; %q resolves to the %s Provider at %s, while %q resolves to the %s Provider at %s",
		selectorRefLabel(left), trackerDisplayName(left.Tracker), left.Host,
		selectorRefLabel(right), trackerDisplayName(right.Tracker), right.Host)
}

func selectorRefLabel(r ref.Ref) string {
	if raw := strings.TrimSpace(r.Raw); raw != "" {
		return raw
	}
	return r.String()
}

func trackerDisplayName(tracker ref.Tracker) string {
	if tracker == ref.TrackerUnknown {
		return "Unknown"
	}
	return providerDisplayName(string(tracker))
}

func firstProfileConflict(profiles []*config.Profile) (int, int, bool) {
	for i := range profiles {
		for j := i + 1; j < len(profiles); j++ {
			if profileIdentity(profiles[i]) != profileIdentity(profiles[j]) {
				return i, j, true
			}
		}
	}
	return 0, 0, false
}

func profileIdentity(prof *config.Profile) string {
	if prof == nil {
		return "none"
	}
	return prof.Name
}

type refIdentity struct {
	tracker ref.Tracker
	host    string
	owner   string
	repo    string
	number  int
	key     string
}

func deduplicationIdentity(r ref.Ref) refIdentity {
	identity := refIdentity{
		tracker: r.Tracker,
		host:    strings.ToLower(r.Host),
		owner:   r.Owner,
		repo:    r.Repo,
		number:  r.Number,
		key:     r.Key,
	}
	if r.Tracker == ref.TrackerGitHub {
		identity.owner = strings.ToLower(identity.owner)
		identity.repo = strings.ToLower(identity.repo)
	}
	return identity
}

func deduplicateRefs(refs []ref.Ref) []ref.Ref {
	unique := make([]ref.Ref, 0, len(refs))
	seen := make(map[refIdentity]struct{}, len(refs))
	for _, r := range refs {
		identity := deduplicationIdentity(r)
		if _, ok := seen[identity]; ok {
			continue
		}
		seen[identity] = struct{}{}

		// Parsed fields may alias one bulk stdin string. The selector survives for
		// every monitor refresh, so retain only the unique Ref's own bytes.
		r.Host = strings.Clone(r.Host)
		r.Owner = strings.Clone(r.Owner)
		r.Repo = strings.Clone(r.Repo)
		r.Key = strings.Clone(r.Key)
		r.Raw = strings.Clone(r.Raw)
		unique = append(unique, r)
	}
	if !shouldCopyDeduplicatedRefs(len(unique), cap(unique)) {
		return unique[:len(unique):len(unique)]
	}
	retained := make([]ref.Ref, len(unique))
	copy(retained, unique)
	return retained
}

// A clipped near-full slice intentionally keeps its small hidden slack rather
// than paying for a second large allocation. Duplicate-heavy slices are always
// copied; shallower reductions are copied only when at least 1024 Ref slots are
// reclaimed and the copy is at most four times larger than that slack. Counting
// slots keeps this policy independent of Ref's architecture-specific byte size.
const compactRefSlackSlots = 1024

func shouldCopyDeduplicatedRefs(retained, capacity int) bool {
	slack := capacity - retained
	if slack == 0 {
		return false
	}
	return slack >= retained ||
		slack >= compactRefSlackSlots && slack*4 >= retained
}

// selectProfile finds the Profile serving this Ref, returning nil when none
// does. A Ref with no Profile is the GitHub zero-config path: `gh auth login`
// and nothing else is a supported way to run sitrep.
//
// A Ref that carries a Jira-style key and no host is the exception. Such
// a Ref names a project and nothing more; without a Profile there is no site to
// ask, so an unmatched key prefix is fatal and the error says exactly what to
// add. A Jira URL is not that case — it names its own site, and its unserved
// Tracker is reported downstream like any other.
func selectProfile(cfg config.Config, r ref.Ref, name string) (*config.Profile, error) {
	prof, ok, err := cfg.Select(r, name)
	if err != nil {
		return nil, err
	}
	if ok {
		return &prof, nil
	}
	if prefix := ref.KeyPrefix(r.Key); prefix != "" && r.Host == "" {
		return nil, unmatchedKeyPrefix(cfg, prefix)
	}
	if r.Tracker == ref.TrackerGitLab && r.Host == "" {
		return gitLabProfileForHostlessRef(cfg, r)
	}
	return nil, nil
}

// gitLabProfileForHostlessRef serves the "&12" reference form, which names a
// group and no instance. config.Select matches a non-Jira Ref on (provider,
// host), so a hostless GitLab Ref matches nothing there — this is the mirror of
// the unmatched-key-prefix rule above: a single gitlab Profile serves it,
// several are an ambiguity worth asking about, and none is fatal because there
// is no instance to read.
func gitLabProfileForHostlessRef(cfg config.Config, r ref.Ref) (*config.Profile, error) {
	// A bare "&12" or "%3" carries no path of its own, so only a Profile that
	// names a project can resolve it. A projectless gitlab Profile matched here
	// would fail downstream with "does not name a GitLab group or project",
	// which is the same problem reported later and less clearly. A milestone
	// reference like "acme/widgets%3" does carry its own path, so a projectless
	// Profile stays a valid match there.
	needsProject := r.Owner == ""

	var matches []config.Profile
	for _, name := range cfg.Names() {
		p := cfg.Profiles[name]
		if p.Provider != string(ref.TrackerGitLab) {
			continue
		}
		if needsProject && p.Project == "" {
			continue
		}
		matches = append(matches, p)
	}

	switch len(matches) {
	case 1:
		return &matches[0], nil
	case 0:
		if needsProject {
			return nil, fmt.Errorf("no Profile tells sitrep which GitLab instance %q is on, "+
				"and which group or project it names — add a gitlab profile with a host and a "+
				"project to %s, or pass the Ref's full URL",
				r.Raw, configLocation(cfg.Path))
		}
		return nil, fmt.Errorf("no Profile tells sitrep which GitLab instance %q is on — "+
			"add a gitlab profile to %s, or pass the Ref's full URL",
			r.Raw, configLocation(cfg.Path))
	default:
		names := make([]string, len(matches))
		for i, p := range matches {
			names[i] = p.Name
		}
		return nil, fmt.Errorf("profiles %s could all serve %q — pass --profile to choose",
			quoteJoin(names), r.Raw)
	}
}

// quoteJoin renders a list of names the way an ask-the-user error does.
func quoteJoin(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(quoted, " and ")
}

// retagProfileClaimedHost fixes the one Ref the grammar has to guess at.
// ref.ParseRemoteURL deliberately treats an unrecognized host as GitHub
// Enterprise, so a bare number inside a clone of a self-managed GitLab resolves
// to a GitHub Ref. A Profile that claims a host names that host's Tracker, and
// that is better evidence than the guess.
//
// Only a guessed Ref is retagged — Tracker GitHub on a host that is not
// github.com — so a URL that named its own Tracker is never overridden.
// gitlab.com needs none of this: the grammar already knows it.
func retagProfileClaimedHost(cfg config.Config, r ref.Ref) ref.Ref {
	if r.Tracker != ref.TrackerGitHub || r.Host == "" || strings.EqualFold(r.Host, "github.com") {
		return r
	}
	for _, name := range cfg.Names() {
		p := cfg.Profiles[name]
		if p.Provider == string(ref.TrackerGitLab) && strings.EqualFold(p.Host, r.Host) {
			r.Tracker = ref.TrackerGitLab
			return r
		}
	}
	return r
}

// configLocation names the config file an error tells the user to edit: the
// real path when one was resolved, and the documented default when the run read
// no file at all.
func configLocation(path string) string {
	if path == "" {
		return "~/.config/sitrep/config.yml"
	}
	return path
}

func unmatchedKeyPrefix(cfg config.Config, prefix string) error {
	msg := fmt.Sprintf("no Profile matches the key prefix %q — add one to %s",
		prefix, configLocation(cfg.Path))
	if known := cfg.KeyPrefixes(); len(known) > 0 {
		msg += " (known prefixes: " + strings.Join(known, ", ") + ")"
	}
	return errors.New(msg)
}

// profileInterval reads a Profile's refresh cadence, or zero when there is no
// Profile.
func profileInterval(prof *config.Profile) time.Duration {
	if prof == nil {
		return 0
	}
	return prof.RefreshInterval
}

// effectiveMaxTickets reads the construction-time Query budget from a selected
// Profile. A nil Profile and a hand-built zero-value Profile both use the shared
// Provider default; validated production Profiles always carry a positive value.
func effectiveMaxTickets(prof *config.Profile) int {
	if prof == nil || prof.MaxTickets <= 0 {
		return provider.DefaultMaxTickets
	}
	return prof.MaxTickets
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
	if strings.Contains(msg, "--interval") {
		msg += " (durations need a unit: 60s, 2m)"
	}
	return msg
}

func usageError(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "%s: %s\n\n", buildinfo.Name, msg)
	fmt.Fprint(stderr, usage)
	return exitUsage
}

// runtimeError writes the one sentence a failed run leaves behind. The message
// is sanitized as a backstop: provider.Errorf already cleans everything a
// driver builds, and this covers anything constructed outside it that still
// quotes tracker text.
func runtimeError(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "%s: %s\n", buildinfo.Name, provider.SanitizeLine(err.Error()))
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

// The --provider names. The default auto-detects the Tracker from the route or
// Refs; the others force one driver, which is how a development run reaches the
// fake and how a GitHub Enterprise Ref can be pinned to the GitHub driver.
const (
	providerAuto   = "auto"
	providerGitHub = "github"
	providerGitLab = "gitlab"
	providerJira   = "jira"
	providerFake   = "fake"
)

// defaultProviderName auto-detects the Provider from the Ref: sitrep can
// tell a GitHub URL from a GitLab one, so it should not make the user say.
const defaultProviderName = providerAuto

func knownProviderName(name string) bool {
	switch name {
	case providerAuto, providerGitHub, providerGitLab, providerJira, providerFake:
		return true
	default:
		return false
	}
}

// resolveRef parses the user's Ref, reading the working directory's git
// origin remote when it is a bare number.
func (d Deps) resolveRef(ctx context.Context, raw, providerName string) (ref.Ref, error) {
	r, err := ref.Parse(ctx, raw, ref.WithRemoteLookup(d.RemoteLookup), ref.WithDir(d.Dir))
	if err == nil {
		return r, nil
	}
	// The fake serves any Ref, so a Ref it cannot resolve is not fatal:
	// development runs and the golden tests must not need a git remote.
	if d.Provider != nil || providerName == providerFake {
		return ref.Ref{Raw: raw}, nil
	}
	return ref.Ref{}, err
}

// newProvider chooses the driver that serves this Ref and constructs it from
// the Profile, when there is one. The Ref's Tracker is the only input: an
// explicit --provider has already been applied to it by forceTracker, so there
// is one answer to "which driver is this" rather than two that can disagree.
// The fake is the exception, because it serves any Ref and belongs to no
// Tracker.
//
// This is where a Profile is consumed and where it stops existing: everything
// past this call sees a Provider and a Ref.
func (d Deps) newProvider(name string, route connectionRoute, prof *config.Profile, configPath string) (provider.Provider, error) {
	maxTickets := effectiveMaxTickets(prof)
	if name == providerFake {
		return fake.New(fake.WithMaxTickets(maxTickets)), nil
	}

	switch route.tracker {
	case ref.TrackerGitHub:
		return d.newGitHub(route.host, prof, maxTickets), nil
	case ref.TrackerJira:
		return d.newJira(route.host, prof, configPath, maxTickets)
	case ref.TrackerGitLab:
		return d.newGitLab(route.host, route.gitLabPath, prof, maxTickets)
	default:
		return nil, fmt.Errorf("cannot tell which tracker serves %q", route.raw)
	}
}

// forceTracker applies an explicit --provider to a Ref, which is the one thing
// the flag exists for: telling sitrep which driver serves an unrecognized host.
// A self-managed GitLab URL with no "/-/" in it, or a bare number inside such a
// clone, parses as GitHub Enterprise — the grammar's documented guess — and
// --provider is how a user corrects it.
//
// It overrides a guess, never a fact. A ref whose host names its own Tracker,
// or which carries a Jira-style key, said what it is; contradicting that is a
// mistake worth reporting rather than obeying.
func forceTracker(name string, r ref.Ref) (ref.Tracker, error) {
	forced := ref.Tracker(name)
	if r.Tracker == forced {
		return forced, nil
	}

	known := ref.HostTracker(r.Host)
	keyed := ref.KeyPrefix(r.Key) != "" && forced != ref.TrackerJira
	if (known != ref.TrackerUnknown && known != forced) || keyed {
		return "", fmt.Errorf("%q is not a %s Ref", r.Raw, providerDisplayName(name))
	}
	return forced, nil
}

// providerDisplayName is how an error spells a provider: the Trackers' own
// capitalisation, because that is how the user reads them everywhere else.
func providerDisplayName(name string) string {
	switch name {
	case providerGitHub:
		return "GitHub"
	case providerGitLab:
		return "GitLab"
	case providerJira:
		return "Jira"
	default:
		return name
	}
}

// newJira constructs the Jira driver from the Profile that matched this Ref.
//
// A Jira Ref always needs a Profile, including the /browse/ URL form that names
// its own site: the site is not the problem, the credential is, and a Profile is
// the only place an Atlassian email and a token reference come from. Saying so
// here is more useful than letting the driver fail at its first request.
func (d Deps) newJira(
	host string,
	prof *config.Profile,
	configPath string,
	maxTickets int,
) (provider.Provider, error) {
	if prof == nil {
		return nil, fmt.Errorf("jira: reading %s needs a Profile — add one to %s with your "+
			"Atlassian email and the environment variable holding your API token",
			host, configLocation(configPath))
	}
	cred, err := prof.Credential(d.env())
	if err != nil {
		return nil, err
	}
	// Translating a config.Credential into the driver's own Credentials happens
	// here, deliberately: it is what keeps internal/provider/jira free of any
	// knowledge of the config file.
	return jira.New(host,
		jira.WithCredentials(jira.Credentials{
			Email: cred.User,
			Token: cred.Token,
		}),
		jira.WithMaxTickets(maxTickets)), nil
}

// newGitLab constructs the GitLab driver from the Profile that matched this
// Ref, when there is one.
//
// Unlike Jira, a GitLab Ref needs no Profile: a user with `glab auth login`
// done is a supported zero-config setup, exactly as on GitHub. What a Profile
// adds is a default path — which is what makes the bare "&12" reference form
// typeable — and a named token variable. The path declares its own scope in its
// spelling, which only the driver reads: the Profile's project is passed through
// verbatim.
//
// Translating a config.Credential into the driver's own token happens here,
// deliberately: it is what keeps internal/provider/gitlab free of any knowledge
// of the config file.
func (d Deps) newGitLab(host, path string, prof *config.Profile, maxTickets int) (provider.Provider, error) {
	// The Profile's credential is resolved first so that a Profile naming a
	// variable nobody set reports *that* rather than a vaguer failure later.
	//
	// An unset auth.token_env is not one of those cases on GitLab: it falls
	// through to glab (see gitLabTokenSource), so demanding it here would break
	// the very setup this driver supports. Blanking token_env is how this asks
	// config for exactly the other half — auth.user_env — whose absence is a
	// real error whichever way the token arrives.
	if prof != nil {
		identityOnly := *prof
		identityOnly.Auth.TokenEnv = ""
		if _, err := identityOnly.Credential(d.env()); err != nil {
			return nil, err
		}
	}
	return gitlab.New(host,
		gitlab.WithPath(path),
		gitlab.WithTokenSource(d.gitLabTokenSource(prof)),
		gitlab.WithMaxTickets(maxTickets)), nil
}

// gitLabTokenSource layers a Profile's auth reference on top of the GitLab
// driver's own token discovery, the same way gitHubTokenSource does. An unset
// named variable falls through to gitlab.DefaultTokenSource rather than being
// fatal, because a user with `glab auth login` done is a supported
// zero-token-variable setup.
func (d Deps) gitLabTokenSource(prof *config.Profile) gitlab.TokenSource {
	return profileTokenSource(d.GitLabTokenSource, prof, d.env(), gitlab.DefaultTokenSource)
}

// profileProject is a Profile's project path, or "" when there is no Profile.
func profileProject(prof *config.Profile) string {
	if prof == nil {
		return ""
	}
	return prof.Project
}

func (d Deps) newGitHub(host string, prof *config.Profile, maxTickets int) provider.Provider {
	return github.New(host,
		github.WithTokenSource(d.gitHubTokenSource(prof)),
		github.WithMaxTickets(maxTickets))
}

// gitHubTokenSource layers a Profile's auth reference on top of the GitHub
// driver's own token discovery. Naming a variable is a preference, not a
// demand: a GitHub user whose named variable happens to be unset still gets
// `gh auth token`, so writing a Profile to set a host or a refresh interval can
// never break a working GitHub setup. For Jira and GitLab an unset named
// variable is an error, because there is no other way in.
func (d Deps) gitHubTokenSource(prof *config.Profile) github.TokenSource {
	return profileTokenSource(d.TokenSource, prof, d.env(), github.DefaultTokenSource)
}

// profileTokenSource is gitHubTokenSource and gitLabTokenSource with their
// fallback named, so a test can prove the fallback is reached without shelling
// out to `gh` or `glab`. A nil result means "the driver's own default", which is
// the zero-config path unchanged.
//
// It is generic over the two drivers' token source types, which are the same
// function shape under two names, because the layering rule is one rule: an
// injected source wins, then a Profile's named variable, then the driver's own
// discovery.
func profileTokenSource[T ~func(context.Context, string) (string, error)](injected T,
	prof *config.Profile, env func(string) string, fallback T) T {
	if injected != nil {
		return injected
	}
	if prof == nil || prof.Auth.TokenEnv == "" {
		return nil
	}

	name := prof.Auth.TokenEnv
	return T(func(ctx context.Context, host string) (string, error) {
		if token := strings.TrimSpace(env(name)); token != "" {
			return token, nil
		}
		return fallback(ctx, host)
	})
}
