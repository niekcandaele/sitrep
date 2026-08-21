// Package cli is the testable seam between the sitrep process and its
// behaviour: everything the binary does is reachable through Run, which takes
// its arguments and writers as parameters and returns an exit code instead of
// calling os.Exit.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/niekcandaele/sitrep/internal/buildinfo"
)

// Exit codes returned by Run.
const (
	exitOK    = 0
	exitUsage = 2
)

const usage = `sitrep - a read-only terminal situation report on a delegated epic.

Usage:
  sitrep [flags] <ref>

Arguments:
  <ref>   the Epic Ref to report on

Flags:
  -h, --help      show this help and exit
      --version   show version information and exit
`

// Run executes sitrep with the given command-line arguments (excluding argv[0])
// and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(buildinfo.Name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	// The flag package prints its own usage on both -h and a parse error; Run
	// decides where each of those goes, so the built-in printer stays silent.
	fs.Usage = func() {}

	showVersion := fs.Bool("version", false, "show version information and exit")

	if err := fs.Parse(args); err != nil {
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

	if fs.NArg() == 0 {
		fmt.Fprintf(stderr, "%s: an Epic Ref is required\n\n", buildinfo.Name)
		fmt.Fprint(stderr, usage)
		return exitUsage
	}

	fmt.Fprintf(stderr, "%s: reporting on a ref is not implemented yet\n", buildinfo.Name)
	return exitUsage
}
