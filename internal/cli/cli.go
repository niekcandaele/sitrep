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
	"github.com/niekcandaele/sitrep/internal/provider/jira"
	"github.com/niekcandaele/sitrep/internal/ref"
	"github.com/niekcandaele/sitrep/internal/render/jsonout"
	"github.com/niekcandaele/sitrep/internal/render/plain"
	"github.com/niekcandaele/sitrep/internal/tui"
)

// Exit codes returned by Run. They are a contract: 0 means the report was
// produced, 1 means sitrep tried and failed, 2 means the command line was
// wrong.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

const usage = `sitrep - a read-only terminal situation report on a delegated epic.

Usage:
  sitrep [flags] <ref>

Arguments:
  <ref>   the Epic Ref to report on, in any of these forms:
            111                                        a bare issue number
            acme/widgets#111                           owner, repository, number
            https://github.com/acme/widgets/issues/111  a full issue URL
            ABC-123                                    a Jira-style key, matched
                                                       to a Profile by its key
                                                       prefix
          A bare number is resolved through the origin remote of the current
          directory's git clone; the other forms work anywhere.
          A Ref with sub-tickets is an Epic; one without is a Ticket, which
          sitrep reports on directly — press u there to open its Epic.

Flags:
  -h, --help              show this help and exit
      --interval <dur>    how often the monitor refreshes (default 60s)
      --json              print the epic as a JSON document and exit
      --plain             print a one-shot text snapshot of the epic and exit
      --profile <name>    the Profile to connect with, from
                          ~/.config/sitrep/config.yml (default: matched from the
                          ref)
      --provider <name>   Provider to read from: "auto" (the default) picks one
                          from the Epic Ref, "github" forces the GitHub driver,
                          "jira" forces the Jira driver, "fake" serves a
                          built-in fixture epic
      --version           show version information and exit
`

// Deps are the injectable dependencies of a sitrep run. Every field's zero
// value is the production behaviour, which is what Run passes.
type Deps struct {
	// Provider serves the run. When nil it is resolved from the Epic Ref and
	// --provider; when set it wins outright and nothing is constructed.
	Provider provider.Provider
	// Now reads the clock for the snapshot's timestamp. When nil it is
	// time.Now.
	Now func() time.Time
	// RemoteLookup reads the git remote that resolves a bare Epic Ref number.
	// When nil it is the real `git remote get-url`.
	RemoteLookup ref.RemoteLookup
	// Dir is the working directory whose git remote resolves a bare number.
	// When empty it is the process working directory.
	Dir string
	// TokenSource discovers the GitHub API token. When nil it is
	// github.DefaultTokenSource.
	TokenSource github.TokenSource
	// Stdin is the monitor's input. When nil it is os.Stdin.
	Stdin io.Reader
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
// When Config and ConfigPath are both zero AND the run serves itself — an
// injected Provider, or --provider fake — no config file is read. Both are a
// test or a development run, neither can be served by a Profile, and such a run
// must never depend on — or be broken by — whatever happens to be in the
// developer's home directory. Do not delete this branch: without it every
// golden in this package becomes a function of the machine it runs on.
func (d Deps) loadConfig(providerName string) (config.Config, error) {
	switch {
	case d.Config != nil:
		return *d.Config, nil
	case d.ConfigPath != "":
		return config.Load(d.ConfigPath)
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
	fs.SetOutput(stderr)
	// The flag package prints its own usage on both -h and a parse error; Run
	// decides where each of those goes, so the built-in printer stays silent.
	fs.Usage = func() {}

	showVersion := fs.Bool("version", false, "show version information and exit")
	asJSON := fs.Bool("json", false, "print the epic as a JSON document and exit")
	asPlain := fs.Bool("plain", false, "print a one-shot text snapshot of the epic and exit")
	providerName := fs.String("provider", defaultProviderName, "Provider to read from")
	profileName := fs.String("profile", "", "the Profile to connect with")
	interval := fs.Duration("interval", defaultRefreshInterval, "how often the monitor refreshes")

	positional, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return exitOK
		}
		fmt.Fprint(stderr, usage)
		return exitUsage
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

	if len(positional) == 0 {
		return usageError(stderr, "an Epic Ref is required")
	}
	if len(positional) > 1 {
		return usageError(stderr, "only one Epic Ref may be given")
	}
	rawRef := positional[0]

	if !knownProviderName(*providerName) {
		return usageError(stderr, fmt.Sprintf("unknown provider %q", *providerName))
	}

	// The command line was fine, so everything from here on is a runtime
	// failure rather than a usage error — starting with a config file that does
	// not parse.
	cfg, err := deps.loadConfig(*providerName)
	if err != nil {
		return runtimeError(stderr, err)
	}

	// The run's context is cancelled on SIGINT and SIGTERM from here on, so a
	// slow pre-flight fetch — and any in-flight fetch after it — goes with a
	// ctrl+c instead of holding the process open for the Tracker's HTTP timeout.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The Epic Ref is resolved once, here, before a Provider exists: the
	// Provider is chosen from what the Ref points at, and FetchEpic is polled,
	// so re-resolving a bare number there would re-run git forever.
	r, err := deps.resolveRef(ctx, rawRef, *providerName)
	if err != nil {
		return runtimeError(stderr, err)
	}

	// A Profile is resolved once, here, between the Epic Ref and the Provider,
	// and is consumed entirely at Provider-construction time. Nothing
	// downstream — not the pre-flight fetch, not the renderers, not the TUI —
	// knows a Profile exists.
	prof, err := selectProfile(cfg, r, *profileName)
	if err != nil {
		return runtimeError(stderr, err)
	}
	if prof != nil {
		r = prof.Complete(r)
	}

	refresh := effectiveInterval(isFlagSet(fs, "interval"), *interval, profileInterval(prof))

	p := deps.Provider
	if p == nil {
		if p, err = deps.newProvider(*providerName, r, prof, cfg.Path); err != nil {
			return runtimeError(stderr, err)
		}
	}

	// One batched fetch, before the mode switch, because every mode needs its
	// result: it is what decides whether this Ref named an Epic or a Ticket, and
	// the monitor is seeded with it rather than fetching it again.
	snap, err := p.FetchEpic(ctx, r)
	if err != nil {
		if *asJSON || *asPlain {
			return runtimeError(stderr, err)
		}
		// A failed pre-flight opens the monitor anyway, unseeded, and lets the
		// TUI's own first fetch fail and draw its retry body: #8 decided that a
		// monitor must not exit on one bad DNS lookup, and one wasted request in
		// an already-failing situation is the right price for keeping that.
		return runMonitor(ctx, stdout, stderr, deps, tui.Options{
			Source:       tui.EpicSource(p, r, deps.clock()),
			DetailSource: tui.TicketDetailSource(p),
			Interval:     refresh,
		})
	}
	snap = provider.StampSnapshot(p, snap, deps.now())

	if decodesToTicket(snap) {
		if *asJSON || *asPlain {
			return runDecodedOneShot(ctx, stdout, stderr, p, snap, *asJSON)
		}
		return runDecodedMonitor(ctx, stdout, stderr, deps, p, r, snap, refresh)
	}

	switch {
	case *asJSON:
		return writeReport(stdout, stderr, func(w io.Writer) error {
			return jsonout.RenderEpic(w, snap, p.Name())
		})
	case *asPlain:
		return writeReport(stdout, stderr, func(w io.Writer) error {
			return plain.RenderEpic(w, snap)
		})
	}

	initial := tui.ListFromEpicSnapshot(snap)
	return runMonitor(ctx, stdout, stderr, deps, tui.Options{
		Source:       tui.EpicSource(p, r, deps.clock()),
		DetailSource: tui.TicketDetailSource(p),
		Initial:      &initial,
		Interval:     refresh,
	})
}

// runMonitor opens the live monitor and blocks until the user quits. The
// caller's Options carry the run's seams; the terminal and the clock are this
// function's to fill in, so no call site can forget one.
func runMonitor(ctx context.Context, stdout, stderr io.Writer, deps Deps, opts tui.Options) int {
	opts.Now = deps.clock()
	opts.Input = deps.stdin()
	opts.Output = stdout

	if err := tui.Run(ctx, opts); err != nil {
		// Bubble Tea is the one thing here that knows whether it has a
		// terminal, so this is where "you are in a pipe" gets named — with the
		// way out, since both one-shot modes work anywhere.
		return runtimeError(stderr, fmt.Errorf("the monitor needs a terminal (use --plain or --json here): %w", err))
	}
	return exitOK
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

// selectProfile finds the Profile serving this Ref, returning nil when none
// does. A Ref with no Profile is the GitHub zero-config path: `gh auth login`
// and nothing else is a supported way to run sitrep.
//
// An Epic Ref that carries a Jira-style key and no host is the exception. Such
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
	return nil, nil
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

func usageError(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "%s: %s\n\n", buildinfo.Name, msg)
	fmt.Fprint(stderr, usage)
	return exitUsage
}

func runtimeError(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "%s: %v\n", buildinfo.Name, err)
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

// stdin returns the monitor's input.
func (d Deps) stdin() io.Reader {
	if d.Stdin == nil {
		return os.Stdin
	}
	return d.Stdin
}

// The --provider names. The default auto-detects the Tracker from the Epic
// Ref; the others force one driver, which is how a development run reaches the
// fake and how a GitHub Enterprise ref can be pinned to the GitHub driver.
const (
	providerAuto   = "auto"
	providerGitHub = "github"
	providerJira   = "jira"
	providerFake   = "fake"
)

// defaultProviderName auto-detects the Provider from the Epic Ref: sitrep can
// tell a GitHub URL from a GitLab one, so it should not make the user say.
const defaultProviderName = providerAuto

func knownProviderName(name string) bool {
	switch name {
	case providerAuto, providerGitHub, providerJira, providerFake:
		return true
	default:
		return false
	}
}

// resolveRef parses the user's Epic Ref, reading the working directory's git
// origin remote when it is a bare number.
func (d Deps) resolveRef(ctx context.Context, raw, providerName string) (ref.Ref, error) {
	r, err := ref.Parse(ctx, raw, ref.WithRemoteLookup(d.RemoteLookup), ref.WithDir(d.Dir))
	if err == nil {
		return r, nil
	}
	// The fake serves any Epic Ref, so a Ref it cannot resolve is not fatal:
	// development runs and the golden tests must not need a git remote.
	if d.Provider != nil || providerName == providerFake {
		return ref.Ref{Raw: raw}, nil
	}
	return ref.Ref{}, err
}

// newProvider chooses the driver that serves this Ref and constructs it from
// the Profile, when there is one. --provider is an override, not the normal
// path.
//
// This is where a Profile is consumed and where it stops existing: everything
// past this call sees a Provider and a Ref.
func (d Deps) newProvider(name string, r ref.Ref, prof *config.Profile, configPath string) (provider.Provider, error) {
	switch name {
	case providerFake:
		return fake.New(), nil
	case providerGitHub:
		if r.Tracker != ref.TrackerGitHub {
			return nil, fmt.Errorf("%q is not a GitHub Epic Ref", r.Raw)
		}
		return d.newGitHub(r, prof), nil
	case providerJira:
		if r.Tracker != ref.TrackerJira {
			return nil, fmt.Errorf("%q is not a Jira Epic Ref", r.Raw)
		}
		return d.newJira(r, prof, configPath)
	}

	switch r.Tracker {
	case ref.TrackerGitHub:
		return d.newGitHub(r, prof), nil
	case ref.TrackerJira:
		return d.newJira(r, prof, configPath)
	case ref.TrackerGitLab:
		return nil, notSupportedYet(r.Tracker, prof, d.env())
	default:
		return nil, fmt.Errorf("cannot tell which tracker serves %q", r.Raw)
	}
}

// newJira constructs the Jira driver from the Profile that matched this Ref.
//
// A Jira Ref always needs a Profile, including the /browse/ URL form that names
// its own site: the site is not the problem, the credential is, and a Profile is
// the only place an Atlassian email and a token reference come from. Saying so
// here is more useful than letting the driver fail at its first request.
func (d Deps) newJira(r ref.Ref, prof *config.Profile, configPath string) (provider.Provider, error) {
	if prof == nil {
		return nil, fmt.Errorf("jira: reading %s needs a Profile — add one to %s with your "+
			"Atlassian email and the environment variable holding your API token",
			r.Host, configLocation(configPath))
	}
	cred, err := prof.Credential(d.env())
	if err != nil {
		return nil, err
	}
	// Translating a config.Credential into the driver's own Credentials happens
	// here, deliberately: it is what keeps internal/provider/jira free of any
	// knowledge of the config file.
	return jira.New(r.Host, jira.WithCredentials(jira.Credentials{
		Email: cred.User,
		Token: cred.Token,
	})), nil
}

// notSupportedYet is the seam #14 (GitLab) plugs into: it replaces this error
// with a driver constructed from the Profile's Host, Project and Credential,
// the way newJira does. Everything it needs is already resolved here.
//
// The credential is resolved first, so a Profile that names an unset variable
// reports that instead of the generic message: the fix for "I wrote a Profile
// and nothing happened" is more useful than the fact that the driver is not
// written yet.
func notSupportedYet(tracker ref.Tracker, prof *config.Profile, env func(string) string) error {
	if prof == nil {
		return fmt.Errorf("%s is not supported yet", tracker)
	}
	if _, err := prof.Credential(env); err != nil {
		return err
	}
	return fmt.Errorf("%s is not supported yet (profile %q)", tracker, prof.Name)
}

func (d Deps) newGitHub(r ref.Ref, prof *config.Profile) provider.Provider {
	return github.New(r.Host, github.WithTokenSource(d.gitHubTokenSource(prof)))
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

// profileTokenSource is gitHubTokenSource with its fallback named, so a test can
// prove the fallback is reached without shelling out to `gh`. A nil result means
// "the driver's own default", which is the zero-config path unchanged.
func profileTokenSource(injected github.TokenSource, prof *config.Profile,
	env func(string) string, fallback github.TokenSource) github.TokenSource {
	if injected != nil {
		return injected
	}
	if prof == nil || prof.Auth.TokenEnv == "" {
		return nil
	}

	name := prof.Auth.TokenEnv
	return func(ctx context.Context, host string) (string, error) {
		if token := strings.TrimSpace(env(name)); token != "" {
			return token, nil
		}
		return fallback(ctx, host)
	}
}
