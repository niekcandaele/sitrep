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
	"github.com/niekcandaele/sitrep/internal/render/jsonout"
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
  <ref>   the Epic Ref to report on

Flags:
  -h, --help              show this help and exit
      --json              print the epic as a JSON document and exit
      --provider <name>   Provider to read from (default "fake"; a development
                          and testing selector until the tracker drivers land)
      --version           show version information and exit
`

// Deps are the injectable dependencies of a sitrep run. The zero value uses
// production defaults, which is what Run passes.
type Deps struct {
	// Provider serves the run. When nil it is resolved from --provider.
	Provider provider.Provider
	// Now reads the clock for the snapshot's timestamp. When nil it is
	// time.Now.
	Now func() time.Time
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

	if len(positional) == 0 {
		return usageError(stderr, "an Epic Ref is required")
	}
	if len(positional) > 1 {
		return usageError(stderr, "only one Epic Ref may be given")
	}
	ref := positional[0]

	p := deps.Provider
	if p == nil {
		if p, err = resolveProvider(*providerName); err != nil {
			return usageError(stderr, err.Error())
		}
	}

	if !*asJSON {
		fmt.Fprintf(stderr, "%s: the terminal report is not implemented yet; use --json\n", buildinfo.Name)
		return exitUsage
	}

	return runJSON(stdout, stderr, p, ref, deps.now())
}

// runJSON produces the epic document for one ref. It renders into a buffer and
// only then copies to stdout: a half-written document followed by an error
// would poison whatever is consuming it.
func runJSON(stdout, stderr io.Writer, p provider.Provider, ref string, now time.Time) int {
	snap, err := p.FetchEpic(context.Background(), ref)
	if err != nil {
		return runtimeError(stderr, err)
	}

	snap.FetchedAt = now
	if snap.Capabilities == (model.Capabilities{}) {
		snap.Capabilities = p.Capabilities()
	}

	var buf bytes.Buffer
	if err := jsonout.RenderEpic(&buf, snap, p.Name()); err != nil {
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

// defaultProviderName is the fake Provider for now: the walking skeleton has to
// walk before any Tracker driver exists.
const defaultProviderName = "fake"

// resolveProvider turns a --provider name into a Provider. The GitHub driver
// ticket owns real resolution — auto-detecting the Tracker from the Epic Ref
// and making --provider an override; until then "fake" is the only name.
func resolveProvider(name string) (provider.Provider, error) {
	switch name {
	case "fake":
		return fake.New(), nil
	default:
		return nil, fmt.Errorf("unknown provider %q", name)
	}
}
