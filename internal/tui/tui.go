// Package tui is sitrep's live monitor: the full-screen Bubble Tea program
// `sitrep <ref>` opens when neither --json nor --plain is given.
//
// # The list-view contract
//
// The screen consumes a ListInput — a collection of Tickets plus a Header, the
// Capabilities that decide what may be shown, and when the reading was taken —
// not an Epic. ListFromEpicSnapshot is the only function in this package
// allowed to mention model.Epic; nothing downstream of it takes an Epic
// parameter. That is deliberate: a future ad-hoc Ticket set or query result
// feeds the same screen by producing a ListInput, without the screen learning
// a second shape. Where those Tickets come from is the Source seam, which the
// TUI holds as a plain function so it knows nothing about Epic Refs, auth or
// GraphQL.
//
// # Progress is computed from every Ticket
//
// The header's progress bar is derived from the whole reading, never from the
// subset the list is showing. Filtering the view must not move the bar. The
// per-group counts, by contrast, come from the rows actually built. The single
// call site those rows are derived through is Model.visibleTickets.
//
// # Filtering is a pure function at one call site
//
// The hide-finished toggle and the fuzzy find are one Filter, applied by
// Model.visibleTickets to Tickets already in memory. Nothing else in the row
// pipeline knows a filter exists: BuildRows, renderRows and the Source are all
// unchanged by it, and no filter change reaches a Provider — clearing a filter
// restores every Ticket with no refetch, because m.input was never touched.
//
// The find matches a Ticket's key and title and nothing else. Native Status is
// excluded deliberately: CONTEXT.md has it displayed as-is and never filtered
// on, because every Tracker words it differently.
//
// # The esc ladder
//
// esc escapes one thing at a time: the find box, then the filter, then the
// program. q and ctrl+c still quit unconditionally from the list, so a filter
// can never trap anyone. Inside the find box every other key is text — q types
// a q — because a find box that quits when you search for "queue" is worse
// than no find box.
//
// # Why the list is hand-rolled
//
// ADR-0001 names bubbles/list and bubbles/viewport as available, not
// mandatory, and neither fits this screen. bubbles/list owns its own title
// chrome, pagination and filtering, and has no concept of a non-selectable
// section heading, so a grouped list costs more to bend it into than to write;
// its filtering would also have to be wrestled away from sitrep's own.
// bubbles/viewport has no cursor to keep visible, which means computing the
// window anyway — so the window arithmetic lives here, in renderRows and
// ensureVisible, as pure functions with direct tests. Only bubbles/key and
// bubbles/help are used. Do not "fix" this by reaching for list.
//
// # One clock, one timer
//
// Model.now is the only clock in the package: nothing else may call time.Now,
// because the staleness indicator is the one place a clock reaches the screen
// and golden frames need it fixed. A single one-second heartbeat drives both
// that indicator and the decision to refresh, so the label and the refresh can
// never disagree about how old the data is.
package tui

import (
	"context"
	"errors"
	"io"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
)

// Options are the monitor's injectable dependencies. Every zero value that has
// a sensible production default takes it.
type Options struct {
	// Source produces one reading of the collection on every refresh.
	Source Source
	// Interval is how long the monitor waits between automatic refreshes.
	Interval time.Duration
	// Now reads the clock the staleness indicator and the refresh schedule are
	// measured against. When nil it is time.Now.
	Now func() time.Time
	// Input is the terminal the monitor reads keys from.
	Input io.Reader
	// Output is the terminal the monitor draws to.
	Output io.Writer
}

// ErrNoTerminal reports that the monitor was handed something other than a
// terminal on either end. It is the one failure a caller acts on: the way out
// is a one-shot mode, which works in a pipe.
var ErrNoTerminal = errors.New("standard input and output must both be a terminal")

// Run opens the monitor and blocks until the user quits or ctx is cancelled.
//
// A cancelled context is a clean exit, not a failure: it is how a SIGINT
// reaches the program.
func Run(ctx context.Context, opts Options) error {
	// Bubble Tea does not refuse a pipe: it starts, draws escape sequences
	// into whatever it was given, and waits forever for a keystroke that can
	// never arrive. So the question "am I interactive?" is answered here, once,
	// before the program exists — rather than left to a caller that would have
	// to ask it again.
	if !isTerminal(opts.Input) || !isTerminal(opts.Output) {
		return ErrNoTerminal
	}

	p := tea.NewProgram(
		New(ctx, opts),
		tea.WithContext(ctx),
		tea.WithInput(opts.Input),
		tea.WithOutput(opts.Output),
	)
	if _, err := p.Run(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// isTerminal reports whether v is a real terminal. Anything without a file
// descriptor — a buffer, a pipe, a test's reader — is not.
func isTerminal(v any) bool {
	f, ok := v.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(f.Fd())
}
