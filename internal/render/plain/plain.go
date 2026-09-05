// Package plain has two responsibilities: it owns sitrep's one-shot --plain
// reports through RenderWatchlist and RenderTicket, and it provides shared,
// renderer-independent terminal-shaping vocabulary. The TUI consumes applicable
// text and layout shaping, measurement, and display-policy members; the two
// report entry points are not shared rendering APIs.
//
// Display-policy callers supply their frame context. ShowsNativeStatus applies
// only to Ticket rows under a Status Category heading; heading-less Ticket
// frames use StatusField. Link-target rows are exempt because they have no such
// heading. This is terminal presentation policy, not model state or a Provider
// or JSON contract. plain may depend on model values and terminal-text utilities,
// but never on TUI screens, styles, input, geometry, or lifecycle.
//
// The report is plain text and nothing else. It contains no ANSI escape
// sequence of any kind, never switches to the alternate screen, and never
// probes the terminal — not its width, not whether it is a TTY, not $TERM or
// $NO_COLOR. That is the point of the mode: it has to survive a dumb terminal
// over SSH, `| tee report.txt`, and an agent reading it as text. Emphasis is
// carried by words and layout ("ci FAIL") rather than colour, because a red
// "ci ok" does not survive a pipe. Colour and styling arrive with the TUI,
// where Lip Gloss is the decided stack (ADR-0001); do not add either here.
//
// Everything the renderer prints is a pure function of the model.WatchlistSnapshot
// it is given: no clock, no environment, no map iteration reaching the output,
// no sorting beyond the display order the model already defines. That purity
// is what makes a golden test at the terminal seam a total test of the
// renderer, and it is why RenderWatchlist takes a snapshot and an io.Writer and
// nothing else. RenderTicket, the report a Ref that named a Ticket
// produces, obeys the same rules on the same terms.
package plain

import (
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/termtext"
)

// Layout widths are fixed constants, not terminal-derived: a one-shot report
// that renders identically on a 200-column monitor and in a pipe is a feature.
const (
	// barWidth is the rune width of the progress bar.
	barWidth = 28
	// maxTitleWidth is the rune length a Ticket title is truncated to.
	maxTitleWidth = 92
	// minKeyColumn is the floor for the computed key column: the gutter the
	// meta line indents to even when every key is short.
	minKeyColumn = 9
	// ticketIndent prefixes every Ticket line inside a group.
	ticketIndent = "  "
	// keyColumnPadding separates the longest key from the title column.
	keyColumnPadding = 2
)

// The progress bar's runes. They are UTF-8 rather than ASCII: "dumb terminal"
// means no ANSI and no alternate screen, not no Unicode, and Ticket titles are
// already UTF-8.
const (
	barFilledRune = "█"
	barEmptyRune  = "░"
	// ellipsis terminates a truncated title.
	ellipsis = "…"
)

// RenderWatchlist writes the one-shot text snapshot of snap: a header block
// with the Watchlist identity and progress, then its Tickets grouped by Status
// Category in the model's display order.
func RenderWatchlist(w io.Writer, snap model.WatchlistSnapshot) error {
	var b strings.Builder

	progress := model.ComputeProgress(snap.Tickets)
	writeHeader(&b, snap.Header, progress, snap.LimitReached, len(snap.Tickets))

	if len(snap.Tickets) == 0 {
		if snap.Header.Key == "" {
			b.WriteString("This Watchlist has no Tickets.\n\n")
		} else {
			b.WriteString("This Epic has no Tickets.\n\n")
		}
		_, err := io.WriteString(w, b.String())
		return err
	}

	keyColumn := KeyColumnWidth(snap.Tickets)
	for _, group := range model.GroupByCategory(snap.Tickets) {
		writeGroup(&b, group, keyColumn, snap.Capabilities)
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// LimitNotice renders the shared plain/TUI cutoff sentence. The count is the
// number of authoritative Tickets actually shown, never a guessed total.
func LimitNotice(ticketCount int) string {
	noun := "tickets"
	if ticketCount == 1 {
		noun = "ticket"
	}
	return fmt.Sprintf("Limit reached — showing %d %s.", ticketCount, noun)
}

// CategoryLabel returns the human display label for a Status Category
// ("In Progress", "Todo", "Done", "Cancelled", "Unknown"). The model's own
// String is the wire token ("in_progress"); this is the reading version.
//
// It is total: a Status Category outside the set returns something visibly
// diagnostic rather than an empty string, so a broken Provider shows up
// instead of hiding.
func CategoryLabel(c model.StatusCategory) string {
	switch c {
	case model.StatusTodo:
		return "Todo"
	case model.StatusInProgress:
		return "In Progress"
	case model.StatusDone:
		return "Done"
	case model.StatusCancelled:
		return "Cancelled"
	case model.StatusUnknown:
		return "Unknown"
	default:
		return c.String()
	}
}

// BarFill returns how many of width cells a progress bar should draw as
// filled: Done over Denominator, rounded to nearest, clamped into [0, width].
// A zero Denominator fills nothing rather than dividing by zero.
//
// It is exported so the TUI's width-adaptive coloured bar and this package's
// fixed-width monochrome one share a single rounding rule: two renderers that
// disagree about where the bar ends is a bug a reader would have to diff two
// packages to find.
func BarFill(p model.Progress, width int) int {
	if width <= 0 {
		return 0
	}
	filled := 0
	if p.Denominator > 0 {
		filled = int(math.Round(float64(p.Done) / float64(p.Denominator) * float64(width)))
	}
	// A Provider reporting more Done than Denominator must not produce a
	// negative repeat count and panic.
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return filled
}

// ProgressBar renders a fixed-width completion bar for a Watchlist's Progress.
// Cancelled Tickets are already out of Progress.Denominator, so the bar shows
// real finished work rather than a total that can never be reached. The result
// is always exactly width runes wide, including when the Denominator is zero.
func ProgressBar(p model.Progress, width int) string {
	if width <= 0 {
		return ""
	}
	filled := BarFill(p, width)
	return strings.Repeat(barFilledRune, filled) + strings.Repeat(barEmptyRune, width-filled)
}

// KeyColumnWidth computes the column the Ticket titles start in, in runes,
// over every Ticket rather than per group, so keys line up across the whole
// report. It is exported so the TUI's list aligns identically: a user who has
// seen one view recognises the other.
func KeyColumnWidth(tickets []model.Ticket) int {
	width := minKeyColumn
	for _, t := range tickets {
		if n := len([]rune(t.Key)) + keyColumnPadding; n > width {
			width = n
		}
	}
	return width
}

// PadKey pads a Ticket key out to the key column, measured in runes.
func PadKey(key string, width int) string {
	if pad := width - len([]rune(key)); pad > 0 {
		return key + strings.Repeat(" ", pad)
	}
	return key
}

// Truncate shortens s to at most width runes, ending in an ellipsis when it
// had to cut. It counts runes rather than bytes: a byte-slice truncation
// corrupts a multi-byte title such as "Renseigner la métrique « éclair »".
//
// A cut is re-balanced for the same reason: the terminator that closes a
// bidirectional scope can be among the runes dropped, and a renderer may not
// re-create at the cut a defect the boundary already removed. This is not a
// second policy about Tracker text — the policy is termtext.Balance's, and
// this only declines to undo it.
func Truncate(s string, width int) string {
	runes := []rune(s)
	if len(runes) <= width || width <= 0 {
		return s
	}
	return termtext.Balance(string(runes[:width-1]) + ellipsis)
}

// PullRequestSummary renders one pull request as a single line fragment:
// identity, state, and — for a live pull request only — its checks and review
// posture. A merged or closed pull request's CI result and review are history,
// so they are suppressed. ticketRepository is the Ticket's own repository; the
// pull request's is prefixed to its number only when it differs, because a
// pull request can live somewhere else than the Ticket it closes.
func PullRequestSummary(pr model.PullRequest, ticketRepository string) string {
	parts := []string{pullRequestIdentity(pr, ticketRepository), pullRequestState(pr.State)}

	if pr.State == model.PROpen || pr.State == model.PRDraft {
		if s := checksWord(pr.Checks); s != "" {
			parts = append(parts, s)
		}
		if s := reviewWord(pr.Review); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

// writeHeader writes the Watchlist identity, progress bar, optional URL and
// Query cutoff notice.
func writeHeader(b *strings.Builder, header model.WatchlistHeader, p model.Progress, limitReached bool, ticketCount int) {
	// Output that starts flush against a shell prompt is hard to read.
	b.WriteString("\n")
	if header.Key == "" {
		fmt.Fprintf(b, "Watchlist  %s\n", header.Title)
	} else {
		fmt.Fprintf(b, "Epic %s  %s\n", header.Key, header.Title)
	}

	progress := fmt.Sprintf("%s  %d/%d done", ProgressBar(p, barWidth), p.Done, p.Denominator)
	if p.Cancelled > 0 {
		progress += fmt.Sprintf("  %d cancelled", p.Cancelled)
	}
	fmt.Fprintf(b, "%s  %d%%\n", progress, p.PercentDone)

	if header.URL != "" {
		fmt.Fprintf(b, "%s\n", header.URL)
	}
	if limitReached {
		fmt.Fprintf(b, "%s\n", LimitNotice(ticketCount))
	}
	b.WriteString("\n")
}

// writeGroup writes one Status Category section: its uppercased label and
// count, then its Tickets in Provider order, then a blank line. Uppercase is
// how a section announces itself without colour.
func writeGroup(b *strings.Builder, g model.Group, keyColumn int, caps model.Capabilities) {
	fmt.Fprintf(b, "%s (%d)\n", strings.ToUpper(CategoryLabel(g.Category)), len(g.Tickets))
	for _, t := range g.Tickets {
		writeTicket(b, t, keyColumn, caps)
	}
	b.WriteString("\n")
}

// writeTicket writes one Ticket as up to two lines: key and title, then the
// meta line with Native Status, assignees and the lead pull request. The meta
// line is omitted when it would be empty.
func writeTicket(b *strings.Builder, t model.Ticket, keyColumn int, caps model.Capabilities) {
	// Ticket.Key is printed verbatim: the Provider already qualifies cross-repo
	// children, so re-deriving a prefix from Ticket.Repository would double the
	// qualification on GitHub and guess wrong on the next Tracker.
	fmt.Fprintf(b, "%s%s%s\n", ticketIndent, PadKey(t.Key, keyColumn), Truncate(t.Title, maxTitleWidth))

	meta := ticketMeta(t, caps)
	if meta == "" {
		return
	}
	fmt.Fprintf(b, "%s%s%s\n", ticketIndent, strings.Repeat(" ", keyColumn), meta)
}

// ticketMeta joins the non-empty parts of a grouped row's second line with two
// spaces: its Native Status, its assignees, and its lead pull request. The row
// sits under a Status Category heading, so a Native Status that only restates
// that heading is dropped.
func ticketMeta(t model.Ticket, caps model.Capabilities) string {
	var parts []string

	// The word is printed exactly as the Tracker wrote it and never branched
	// on for meaning; ShowsNativeStatus decides only whether it is printed at
	// all, by reading the Status Category the row already sits under.
	if ShowsNativeStatus(t) {
		parts = append(parts, "["+t.NativeStatus+"]")
	}
	return strings.Join(append(parts, ticketMetaTail(t, caps)...), "  ")
}

// ticketMetaTail is the part of a meta line that does not depend on whether a
// Status Category heading stands above the Ticket: assignees, then the lead
// pull request.
func ticketMetaTail(t model.Ticket, caps model.Capabilities) []string {
	var parts []string
	if s := assigneeList(t.Assignees); s != "" {
		parts = append(parts, s)
	}
	if s := pullRequests(t, caps); s != "" {
		parts = append(parts, s)
	}
	return parts
}

// assigneeList renders the assignees as @-prefixed logins in Provider order.
// The login rather than the display name: the handle is what you type at an
// agent. No assignees renders nothing at all, not an "unassigned" placeholder.
func assigneeList(users []model.User) string {
	if len(users) == 0 {
		return ""
	}
	handles := make([]string, 0, len(users))
	for _, u := range users {
		handles = append(handles, "@"+u.Login)
	}
	return strings.Join(handles, " ")
}

// pullRequests renders the Ticket's lead pull request, with a count of the
// rest so nothing is silently hidden — including the ones the Provider never
// fetched, when it can say how many there are. The Capability is the authority:
// when the snapshot does not declare PullRequests nothing is emitted, even if
// the Ticket somehow carries pull requests.
func pullRequests(t model.Ticket, caps model.Capabilities) string {
	if !caps.PullRequests || len(t.PullRequests) == 0 {
		return ""
	}
	// Providers list the lead pull request first: the one that best represents
	// the Ticket's current state.
	summary := PullRequestSummary(t.PullRequests[0], t.Repository)
	if overflow := PullRequestOverflow(len(t.PullRequests), t.PullRequestTotal); overflow != "" {
		summary += " " + overflow
	}
	return summary
}

// pullRequestIdentity renders "#18", qualified as "acme/gadgets#18" when the
// pull request lives outside the Ticket's own repository.
func pullRequestIdentity(pr model.PullRequest, ticketRepository string) string {
	if pr.Repository != "" && pr.Repository != ticketRepository {
		return fmt.Sprintf("%s#%d", pr.Repository, pr.Number)
	}
	return fmt.Sprintf("#%d", pr.Number)
}

// pullRequestState renders the pull request's lifecycle state. An unmapped
// state says so rather than being guessed into "open".
func pullRequestState(s model.PRState) string {
	switch s {
	case model.PRDraft:
		return "draft"
	case model.PROpen:
		return "open"
	case model.PRMerged:
		return "merged"
	case model.PRClosed:
		return "closed"
	default:
		return "state unknown"
	}
}

// checksWord renders the CI result. ChecksNone renders nothing: no CI
// configured is not news. FAIL is uppercase because that is how a monochrome
// report shouts.
func checksWord(s model.CheckState) string {
	switch s {
	case model.ChecksPassing:
		return "ci ok"
	case model.ChecksFailing:
		return "ci FAIL"
	case model.ChecksPending:
		return "ci ..."
	default:
		return ""
	}
}

// reviewWord renders the review posture. ReviewNone renders nothing; pending
// is rendered because "waiting on review" is one of the states the report
// exists to distinguish from "an agent is still coding".
func reviewWord(s model.ReviewState) string {
	switch s {
	case model.ReviewApproved:
		return "approved"
	case model.ReviewChangesRequested:
		return "changes req"
	case model.ReviewPending:
		return "review pending"
	default:
		return ""
	}
}
