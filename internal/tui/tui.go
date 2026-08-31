// Package tui is sitrep's live monitor: the full-screen Bubble Tea program
// `sitrep <ref>` opens when neither --json nor --plain is given.
//
// # The list-view contract
//
// The screen consumes a ListInput — a Watchlist of Tickets plus a Header, the
// Capabilities that decide what may be shown, and when the reading was taken —
// not an Epic. ListFromWatchlistSnapshot is the only function in this package
// allowed to mention model.Epic; nothing downstream of it takes an Epic
// parameter. That is deliberate: a future ad-hoc Ticket set or query result
// feeds the same screen by producing a ListInput, without the screen learning
// a second shape. Where those Tickets come from is the Source seam, which the
// TUI holds as a plain function so it knows nothing about Refs, auth or
// GraphQL.
//
// # The terminal-text boundary
//
// Model data enters this package through exactly four funnels — Options.Initial,
// Options.Open, Options.Source and Options.DetailSource — and none of them has
// to come from a Provider: a Source is any closure the caller wrote. So the
// screens do not trust their caller. Everything entering Model state crosses
// intake.go, which applies internal/termtext to whole model values, and the
// renderers below are therefore free of sanitizing calls (ADR-0006).
//
// A new screen inherits that by consuming a ListInput or a DetailInput: both
// can only be seated through those funnels or through DetailFromTicket, which
// crosses the same boundary. A screen shows a Key, never a model.TicketID —
// identity is deliberately outside the boundary because it is never drawn.
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
// # Detail seats and Trail share a second mode
//
// Enter on the list changes mode and seats the selected Ticket's Detail. Inside
// Detail, following a focused Link archives the current seat in the session-local
// Trail and seats the target without leaving Detail mode. esc first pops one Trail
// seat; only an untrailed root returns to its armed list or quits when a decoded
// Ticket has no list behind it. u clears the Trail and jumps to the root Watchlist.
//
// Trail seat pushes and pops leave the hidden list state untouched. Root esc
// and u instead resume or arm the list through its normal reconciliation, which
// may clamp the selection and scroll offset to the current rows before drawing.
//
// A Ticket's Detail is fetched only when a seated Ticket misses the session cache
// or r explicitly re-reads it: re-opening a cached Ticket costs the Tracker
// nothing, and no list refresh — automatic or forced — ever calls FetchDetail
// (ADR-0003). A cached Detail carries its own "read Ns ago" stamp so a stale
// reading is visible rather than looking as fresh as the list beside it.
//
// The screen consumes a DetailInput — a Ticket header, an optional parent
// breadcrumb and the Detail itself — the same way the list consumes a ListInput.
// DetailFromTicket is the adapter this package needs today; a Ticket opened
// without a list behind it, a bare Ticket Ref decoded straight into Detail,
// fills the same fields and needs no new entry point here.
//
// # The Frontier is a second rendering of the same Watchlist
//
// v draws the current Watchlist as nodes and BlockedBy/Blocks edges, answering
// which Tickets can be picked up right now. It is a mode toggle rather than a
// navigation frame: membership is exactly the Watchlist's, the selection
// survives the toggle in both directions, and a Ticket opened from it returns
// to it — detailReturn is what records that, and the Trail stays what CONTEXT.md
// says it is, the path of Tickets followed through explicit Links in Detail.
//
// Filters are deliberately ignored here. Hiding a node deletes an edge, and a
// deleted edge can make a blocked Ticket look Actionable, which is the precise
// failure Actionable exists to prevent — so the screen draws the whole
// Watchlist and says so in the footer whenever a filter is on.
//
// The screen consumes a FrontierInput, adapted by FrontierFromList and cleaned
// at intake like every other seat. Everything derived — the model.BlockingGraph
// and the canvas — is recomputed from it, and the layout in frontierframe.go is
// a pure function of data with no Model, no clock and no style in it.
//
// # The Frontier's bulk fan-out is the one exception ADR-0003 allows
//
// Actionable is computed from BlockedBy Links, and Links live in Detail, so the
// Frontier reads every member's Detail once. That is a fan-out, and it is
// legitimate only because an explicit key press asked for it: never a refresh,
// never a poll, never a render (ADR-0003 Amendment 4). The policy — canonical
// order, cache skipping, bounded concurrency, and the rule that only a
// successful read is recorded — lives in internal/detailfanout, which the
// one-shot renderers share. Results land in the same per-Ticket Detail cache a
// drill-in uses, so a Ticket already opened this session costs nothing. Each
// explicit fan-out has a generation-scoped child context: leaving or reseating
// the Frontier, abandoning it with u, or quitting cancels its Provider reads.
// Opening a node keeps that lifetime alive behind Detail because root esc returns
// to the same Frontier seat. A successful read still warms the shared cache even
// when it races with cancellation.
//
// Emphasis is withheld until every planned read has answered. Fail-closed plus
// a progressive fetch means a half-loaded Frontier would give wrong answers to
// anyone glancing at it, so the cards draw in Status Category colours with no
// badge and the header counts the reads still outstanding. Generation checks
// remain the correctness guard against Providers that race with or ignore
// cancellation; contexts stop the actual work rather than merely dropping its
// eventual answer.
//
// # The program can start in Detail, and can be seeded
//
// Options.Open starts the program on one Ticket's Detail — the decoder entry for
// a Ref that named a Ticket rather than a Watchlist — with the parent
// breadcrumb the Detail screen already had a seat for, and u to open that parent
// in the monitor. Until the user presses it the list is never fetched at all.
// Options.Initial seats a reading the caller already took, so a monitor draws
// data on its first frame rather than re-fetching what its caller just fetched.
//
// Neither teaches this package about Refs: the walk-up is a Source the
// caller built, and the breadcrumb is a Header the caller filled.
//
// The body is wrapped into lines once and scrolled by slicing them, for the same
// reason the list window is hand-rolled: the arithmetic has to exist anyway, and
// a golden of bubbles/viewport is a golden of somebody else's scrollbar.
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

	"github.com/niekcandaele/sitrep/internal/terminal"
)

// Options are the monitor's injectable dependencies. Every zero value that has
// a sensible production default takes it.
type Options struct {
	// Source produces one reading of the Watchlist on every refresh. It may be
	// nil when Open is set and the decoded Ticket has no Watchlist behind it:
	// there is then nothing to monitor, and the walk-up key is not offered.
	Source Source
	// DetailSource reads one seated Ticket's Detail on a session-cache miss and
	// when the user explicitly re-reads it. A monitor without one still lists
	// Tickets; enter then says why it cannot open them.
	DetailSource DetailSource
	// Open, when non-nil, starts the program on one Ticket's Detail instead of
	// the list: the decoder entry point for a Ref that named a Ticket. The
	// Detail itself is read from DetailSource on the first frame, the same way a
	// drill-in reads it, and the list is not fetched at all until the user walks
	// up into it.
	Open *OpenTicket
	// Initial, when non-nil, seats a reading the caller already took, so the
	// program draws data on its first frame instead of re-fetching what the
	// caller just fetched. The refresh clock starts from the reading's own
	// FetchedAt, not from startup.
	Initial *ListInput
	// InitialError carries a retryable pre-flight rate-limit refusal into the
	// monitor so its policy can hold the first TUI fetch until the known reset.
	InitialError error
	// Interval is how long the monitor waits between automatic refreshes.
	Interval time.Duration
	// Now reads the clock the staleness indicator and the refresh schedule are
	// measured against. When nil it is time.Now.
	Now func() time.Time
	// Heartbeat schedules the next monitor heartbeat. Nil uses the production
	// one-second tick; tests can inject a no-op command and deliver beats directly.
	Heartbeat tea.Cmd
	// NoMouse starts the monitor without terminal mouse capture. The user can
	// enable it at runtime with m.
	NoMouse bool
	// Input is the terminal the monitor reads keys from.
	Input io.Reader
	// Output is the terminal the monitor draws to.
	Output io.Writer
}

// ErrNoTerminal reports that the monitor was handed something other than a
// terminal on either end. It is the one failure a caller acts on: the way out
// is a one-shot mode, which works in a pipe.
var ErrNoTerminal = errors.New("standard input and output must both be a terminal")

// ErrInterrupted reports that the user ended the session with ctrl+c. It is not
// a failure and carries no message a caller should print: the user knows what
// they did. It exists because raw mode delivers ctrl+c as an ordinary key press
// rather than a signal, so the exit code sitrep documents for it — 130 — has to
// be carried out of the program by hand.
var ErrInterrupted = errors.New("interrupted")

// Run opens the monitor and blocks until the user quits or ctx is cancelled.
//
// A cancelled context is a clean exit, not a failure: it is how a SIGINT
// reaches the program. A ctrl+c typed at the monitor is the same event arriving
// as a key press, and is reported as ErrInterrupted.
func Run(ctx context.Context, opts Options) error {
	// Bubble Tea does not refuse a pipe: it starts, draws escape sequences
	// into whatever it was given, and waits forever for a keystroke that can
	// never arrive. So the question "am I interactive?" is answered here, once,
	// before the program exists — rather than left to a caller that would have
	// to ask it again.
	if !terminal.Is(opts.Input) || !terminal.Is(opts.Output) {
		return ErrNoTerminal
	}

	p := tea.NewProgram(
		New(ctx, opts),
		tea.WithContext(ctx),
		tea.WithInput(opts.Input),
		tea.WithOutput(opts.Output),
	)
	final, err := p.Run()
	if err != nil && ctx.Err() == nil {
		return err
	}
	if m, ok := final.(Model); ok && m.Interrupted() {
		return ErrInterrupted
	}
	return nil
}
