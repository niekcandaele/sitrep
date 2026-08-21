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
            acme&12                                    a GitLab epic reference
            https://gitlab.com/groups/acme/-/epics/12  a GitLab group epic URL
            acme/widgets%3                             a GitLab milestone
                                                       reference — a milestone is
                                                       how GitLab Free spells an
                                                       Epic
            https://gitlab.com/acme/widgets/-/milestones/3
                                                       a GitLab milestone URL
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
                          "gitlab" the GitLab driver, "jira" the Jira driver,
                          "fake" serves a built-in fixture epic
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
	// GitLabTokenSource discovers the GitLab API token. When nil it is
	// gitlab.DefaultTokenSource.
	GitLabTokenSource gitlab.TokenSource
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

	// A Profile claiming the Ref's host names that host's Tracker, which is the
	// only way a bare number inside a self-managed GitLab clone can be told
	// apart from a GitHub Enterprise one.
	r = retagProfileClaimedHost(cfg, r)

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
			// refresh, retryable or not — that is #8's contract and users depend
			// on it.
			return runtimeError(stderr, err)
		}
		// An otherwise-failed pre-flight opens the monitor anyway, unseeded, and
		// lets the TUI's own first fetch fail and draw its retry body: #8 decided
		// that a monitor must not exit on one bad DNS lookup, and one wasted
		// request in an already-failing situation is the right price for keeping
		// that.
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
	var matches []config.Profile
	for _, name := range cfg.Names() {
		if p := cfg.Profiles[name]; p.Provider == string(ref.TrackerGitLab) {
			matches = append(matches, p)
		}
	}

	switch len(matches) {
	case 1:
		return &matches[0], nil
	case 0:
		return nil, fmt.Errorf("no Profile tells sitrep which GitLab instance %q is on — "+
			"add a gitlab profile to %s, or pass the epic's full URL",
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
	providerGitLab = "gitlab"
	providerJira   = "jira"
	providerFake   = "fake"
)

// defaultProviderName auto-detects the Provider from the Epic Ref: sitrep can
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
	case providerGitLab:
		if r.Tracker != ref.TrackerGitLab {
			return nil, fmt.Errorf("%q is not a GitLab Epic Ref", r.Raw)
		}
		return d.newGitLab(r, prof)
	}

	switch r.Tracker {
	case ref.TrackerGitHub:
		return d.newGitHub(r, prof), nil
	case ref.TrackerJira:
		return d.newJira(r, prof, configPath)
	case ref.TrackerGitLab:
		return d.newGitLab(r, prof)
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

// newGitLab constructs the GitLab driver from the Profile that matched this
// Ref, when there is one.
//
// Unlike Jira, a GitLab Ref needs no Profile: a user with `glab auth login`
// done is a supported zero-config setup, exactly as on GitHub. What a Profile
// adds is the instance's default group or project path — which is what makes
// the bare "&12" reference form typeable — and a named token variable.
//
// Translating a config.Credential into the driver's own token happens here,
// deliberately: it is what keeps internal/provider/gitlab free of any knowledge
// of the config file.
func (d Deps) newGitLab(r ref.Ref, prof *config.Profile) (provider.Provider, error) {
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
	return gitlab.New(r.Host,
		gitlab.WithPath(profileProject(prof)),
		gitlab.WithTokenSource(d.gitLabTokenSource(prof))), nil
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
