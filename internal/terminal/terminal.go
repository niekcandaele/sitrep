// Package terminal identifies terminal-backed streams shared by the CLI and TUI.
package terminal

import "github.com/charmbracelet/x/term"

// Is reports whether v exposes a file descriptor attached to a terminal.
func Is(v any) bool {
	f, ok := v.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(f.Fd())
}
