// Command sitrep prints a read-only situation report on a delegated epic.
package main

import (
	"os"

	"github.com/niekcandaele/sitrep/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
