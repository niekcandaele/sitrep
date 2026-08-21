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
	"time"

	"github.com/niekcandaele/sitrep/internal/buildinfo"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
	"github.com/niekcandaele/sitrep/internal/provider/github"
	"github.com/niekcandaele/sitrep/internal/ref"
	"github.com/niekcandaele/sitrep/internal/render/jsonout"
	"github.com/niekcandaele/sitrep/internal/render/plain"
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
          A bare number is resolved through the origin remote of the current
          directory's git clone; the other forms work anywhere.

Flags:
  -h, --help              show this help and exit
      --json              print the epic as a JSON document and exit
      --plain             print a one-shot text snapshot of the epic and exit
      --provider <name>   Provider to read from: "auto" (the default) picks one
                          from the Epic Ref, "github" forces the GitHub driver,
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

	// The Epic Ref is resolved once, here, before a Provider exists: the
	// Provider is chosen from what the Ref points at, and FetchEpic is polled,
	// so re-resolving a bare number there would re-run git forever.
	ctx := context.Background()
	r, err := deps.resolveRef(ctx, rawRef, *providerName)
	if err != nil {
		return runtimeError(stderr, err)
	}

	p := deps.Provider
	if p == nil {
		if p, err = deps.newProvider(*providerName, r); err != nil {
			return runtimeError(stderr, err)
		}
	}

	switch {
	case *asJSON:
		render := func(w io.Writer, snap model.EpicSnapshot) error {
			return jsonout.RenderEpic(w, snap, p.Name())
		}
		return runOneShot(ctx, stdout, stderr, p, r, deps.now(), render)
	case *asPlain:
		return runOneShot(ctx, stdout, stderr, p, r, deps.now(), plain.RenderEpic)
	}

	fmt.Fprintf(stderr, "%s: the terminal report is not implemented yet; use --json or --plain\n", buildinfo.Name)
	return exitUsage
}

// runOneShot fetches the Epic once and writes one rendered report to stdout.
// render is the mode's renderer; everything before it — ref resolution,
// Provider construction, the single batched fetch, the clock stamp — is shared,
// which is what makes --plain and --json two views of one code path rather than
// two programs.
//
// It renders into a buffer and only then copies to stdout: a half-written
// report followed by an error would poison whatever is consuming it, whether
// that is a script reading JSON or a human reading text.
func runOneShot(ctx context.Context, stdout, stderr io.Writer, p provider.Provider, r ref.Ref,
	now time.Time, render func(io.Writer, model.EpicSnapshot) error) int {
	snap, err := p.FetchEpic(ctx, r)
	if err != nil {
		return runtimeError(stderr, err)
	}

	snap.FetchedAt = now
	if snap.Capabilities == (model.Capabilities{}) {
		snap.Capabilities = p.Capabilities()
	}

	var buf bytes.Buffer
	if err := render(&buf, snap); err != nil {
		return runtimeError(stderr, err)
	}
	if _, err := io.Copy(stdout, &buf); err != nil {
		return runtimeError(stderr, err)
	}
	return exitOK
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

// The --provider names. The default auto-detects the Tracker from the Epic
// Ref; the others force one driver, which is how a development run reaches the
// fake and how a GitHub Enterprise ref can be pinned to the GitHub driver.
const (
	providerAuto   = "auto"
	providerGitHub = "github"
	providerFake   = "fake"
)

// defaultProviderName auto-detects the Provider from the Epic Ref: sitrep can
// tell a GitHub URL from a GitLab one, so it should not make the user say.
const defaultProviderName = providerAuto

func knownProviderName(name string) bool {
	switch name {
	case providerAuto, providerGitHub, providerFake:
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

// newProvider chooses the driver that serves this Ref. --provider is an
// override, not the normal path.
func (d Deps) newProvider(name string, r ref.Ref) (provider.Provider, error) {
	switch name {
	case providerFake:
		return fake.New(), nil
	case providerGitHub:
		if r.Tracker != ref.TrackerGitHub {
			return nil, fmt.Errorf("%q is not a GitHub Epic Ref", r.Raw)
		}
		return d.newGitHub(r), nil
	}

	switch r.Tracker {
	case ref.TrackerGitHub:
		return d.newGitHub(r), nil
	case ref.TrackerGitLab, ref.TrackerJira:
		return nil, fmt.Errorf("%s is not supported yet", r.Tracker)
	default:
		return nil, fmt.Errorf("cannot tell which tracker serves %q", r.Raw)
	}
}

func (d Deps) newGitHub(r ref.Ref) provider.Provider {
	return github.New(r.Host, github.WithTokenSource(d.TokenSource))
}
