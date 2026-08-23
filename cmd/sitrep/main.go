// Command sitrep is a read-only terminal ticket viewer over a resolved Watchlist.
package main

import (
	"os"

	"github.com/niekcandaele/sitrep/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
