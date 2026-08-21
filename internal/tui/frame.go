package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/render/plain"
)

// Layout constants. The bar is width-adaptive within these bounds so the
// monitor reads on an 80-column SSH session and on a wide one, without the bar
// eating the counts beside it.
const (
	minBarWidth = 10
	maxBarWidth = 28
	// selectionGutter is the two columns "▸ " occupies in front of a Ticket.
	selectionGutter = 2
	// separator joins the parts of the counts line.
	separator = " · "
	// selectedMarker flags the selected Ticket without relying on colour.
	selectedMarker = "▸ "
	// unselectedMarker keeps unselected Tickets in the same column.
	unselectedMarker = "  "
)

// The progress bar's runes, shared with the --plain report so the two views
// draw the same bar.
const (
	barFilledRune = "█"
	barEmptyRune  = "░"
)

// renderHeader draws the block above the list: the collection's identity, then
// its progress bar, counts and staleness indicator.
//
// The progress it reports is computed from every Ticket in the reading, never
// from the subset the list happens to be showing. Filtering the view must not
// move the progress bar: an Epic is 3/9 done whether or not the screen is
// hiding the done ones.
// Before the first reading lands there is no progress to report, so the bar is
// left out rather than drawn as a truthful-looking "0/0 done · 0%".
func renderHeader(in ListInput, staleness string, hasData bool, width int, s Styles) string {
	progress := rightAlign(s.Staleness.Render(staleness), width)
	if hasData {
		progress = headerProgress(model.ComputeProgress(in.Tickets), staleness, width, s)
	}
	return strings.Join([]string{headerIdentity(in.Header, width, s), progress, ""}, "\n")
}

// rightAlign pushes a fragment to the right-hand edge of the terminal.
func rightAlign(s string, width int) string {
	if pad := width - lipgloss.Width(s); pad > 0 {
		return strings.Repeat(" ", pad) + s
	}
	return truncateLine(s, width)
}

// headerIdentity draws "#111  Widget sync v2 …" with the collection's URL
// right-aligned when there is room for it. The URL is a convenience, not
// load-bearing: it is the first thing dropped on a narrow terminal.
func headerIdentity(h Header, width int, s Styles) string {
	left := s.HeaderKey.Render(h.Key)
	if h.Key != "" && h.Title != "" {
		left += "  "
	}
	left += s.HeaderTitle.Render(plain.Truncate(h.Title, width))

	if h.URL == "" {
		return truncateLine(left, width)
	}
	right := s.HeaderURL.Render(h.URL)
	if lipgloss.Width(left)+2+lipgloss.Width(right) > width {
		return truncateLine(left, width)
	}
	return left + strings.Repeat(" ", width-lipgloss.Width(left)-lipgloss.Width(right)) + right
}

// headerProgress draws the bar, the counts and the staleness indicator. The
// Cancelled count appears only when there is one: "0 cancelled" is noise on the
// nine epics out of ten that have none.
func headerProgress(p model.Progress, staleness string, width int, s Styles) string {
	counts := fmt.Sprintf("%d/%d done", p.Done, p.Denominator)
	if p.Cancelled > 0 {
		counts += fmt.Sprintf("%s%d cancelled", separator, p.Cancelled)
	}
	counts += fmt.Sprintf("%s%d%%", separator, p.PercentDone)

	right := s.Staleness.Render(staleness)
	// The bar takes what is left after the counts and the indicator, within
	// its bounds.
	barWidth := width - lipgloss.Width(counts) - lipgloss.Width(right) - 6
	barWidth = min(max(barWidth, minBarWidth), maxBarWidth)

	left := renderBar(p, barWidth, s) + "  " + s.Counts.Render(counts)
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return truncateLine(left+" "+right, width)
	}
	return left + strings.Repeat(" ", gap) + right
}

// renderBar draws the completion bar. The fill count comes from plain.BarFill
// so the monitor and the --plain report round identically.
func renderBar(p model.Progress, width int, s Styles) string {
	filled := plain.BarFill(p, width)
	return s.BarFilled.Render(strings.Repeat(barFilledRune, filled)) +
		s.BarEmpty.Render(strings.Repeat(barEmptyRune, width-filled))
}

// rowLines renders one row into the terminal lines it occupies. A group
// heading takes a blank spacer line plus its heading, except at the very top
// of the list where the spacer would only waste a row. A Ticket takes its
// key/title line plus a meta line, and only one line when the meta would be
// empty.
func rowLines(rows []Row, i, keyColumn, width int, selected bool, caps model.Capabilities, s Styles) []string {
	r := rows[i]
	if r.Kind == RowGroupHeader {
		heading := s.groupHeader(r.Category).Render(
			fmt.Sprintf("%s (%d)", strings.ToUpper(plain.CategoryLabel(r.Category)), r.Count))
		if i == 0 {
			return []string{heading}
		}
		return []string{"", heading}
	}

	marker := unselectedMarker
	titleStyle := s.TicketTitle
	if selected {
		marker = selectedMarker
		titleStyle = s.Selected
	}

	titleWidth := width - selectionGutter - keyColumn
	title := titleStyle.Render(plain.Truncate(r.Ticket.Title, titleWidth))
	lines := []string{marker + s.TicketKey.Render(plain.PadKey(r.Ticket.Key, keyColumn)) + title}

	if meta := ticketMeta(r.Ticket, caps, s); meta != "" {
		lines = append(lines, unselectedMarker+strings.Repeat(" ", keyColumn)+meta)
	}
	return lines
}

// hasMeta reports whether a Ticket's second line would carry anything, which
// is what decides whether the Ticket occupies one line or two. It answers the
// same question as ticketMeta returning "", without building the string, so
// the window arithmetic does not depend on a style set.
func hasMeta(t model.Ticket, caps model.Capabilities) bool {
	return t.NativeStatus != "" || len(t.Assignees) > 0 ||
		(caps.PullRequests && len(t.PullRequests) > 0)
}

// ticketMeta builds a Ticket's second line: its Native Status, its assignees
// and its lead pull request, in the same order and with the same words the
// --plain report uses. Native Status is printed as the Tracker wrote it and
// never branched on.
func ticketMeta(t model.Ticket, caps model.Capabilities, s Styles) string {
	var parts []string

	if t.NativeStatus != "" {
		parts = append(parts, s.NativeStatus.Render("["+t.NativeStatus+"]"))
	}
	if handles := assignees(t.Assignees); handles != "" {
		parts = append(parts, s.Assignees.Render(handles))
	}
	if pr := pullRequest(t, caps, s); pr != "" {
		parts = append(parts, pr)
	}
	return strings.Join(parts, "  ")
}

// assignees renders the @-prefixed logins in Provider order. No assignees
// renders nothing at all, not an "unassigned" placeholder.
func assignees(users []model.User) string {
	if len(users) == 0 {
		return ""
	}
	handles := make([]string, 0, len(users))
	for _, u := range users {
		handles = append(handles, "@"+u.Login)
	}
	return strings.Join(handles, " ")
}

// pullRequest renders the Ticket's lead pull request, with a count of the rest
// so nothing is silently hidden. The Capability is the authority: without it
// nothing is emitted at all — no error, no placeholder.
func pullRequest(t model.Ticket, caps model.Capabilities, s Styles) string {
	if !caps.PullRequests || len(t.PullRequests) == 0 {
		return ""
	}
	lead := t.PullRequests[0]
	summary := plain.PullRequestSummary(lead, t.Repository)
	if rest := len(t.PullRequests) - 1; rest > 0 {
		summary += fmt.Sprintf(" +%d more", rest)
	}
	return s.pullRequest(lead).Render(summary)
}

// rowHeights returns how many terminal lines each row occupies, which is what
// the scroll window arithmetic is measured in: rows are not uniform, so a
// window counted in rows would overflow the body.
func rowHeights(rows []Row, caps model.Capabilities) []int {
	heights := make([]int, len(rows))
	for i, r := range rows {
		switch {
		case r.Kind == RowGroupHeader && i == 0:
			heights[i] = 1
		case r.Kind == RowGroupHeader:
			heights[i] = 2
		case !hasMeta(r.Ticket, caps):
			heights[i] = 1
		default:
			heights[i] = 2
		}
	}
	return heights
}

// ensureVisible returns the offset that keeps the selected row inside a window
// of height lines, scrolling as little as possible: up to reach a selection
// above the window, down just far enough to bring one below it into view.
func ensureVisible(heights []int, selected, offset, height int) int {
	if len(heights) == 0 {
		return 0
	}
	selected = min(max(selected, 0), len(heights)-1)
	offset = min(max(offset, 0), len(heights)-1)

	if selected < offset {
		return selected
	}
	used := 0
	for i := offset; i <= selected; i++ {
		used += heights[i]
	}
	for used > height && offset < selected {
		used -= heights[offset]
		offset++
	}
	return offset
}

// renderRows draws the window of rows starting at offset that fits in height
// lines, marking the selected row and padding the remainder so the footer sits
// at the bottom of the screen rather than floating under a short list. It is
// pure: same inputs, same bytes.
func renderRows(rows []Row, selected, offset, height, width int, caps model.Capabilities, s Styles) string {
	lines := make([]string, 0, height)
	keyColumn := keyColumnWidth(rows)

	for i := offset; i < len(rows) && len(lines) < height; i++ {
		for _, line := range rowLines(rows, i, keyColumn, width, i == selected, caps, s) {
			if len(lines) == height {
				break
			}
			lines = append(lines, truncateLine(line, width))
		}
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// keyColumnWidth computes the Ticket key column over every row, so keys line up
// across groups and not merely within one.
func keyColumnWidth(rows []Row) int {
	tickets := make([]model.Ticket, 0, len(rows))
	for _, r := range rows {
		if r.Kind == RowTicket {
			tickets = append(tickets, r.Ticket)
		}
	}
	return plain.KeyColumnWidth(tickets)
}

// truncateBlock clips every line of a multi-line block to the terminal width.
// truncateLine cannot do this: it measures a string as one run of cells, so a
// block handed to it whole survives only as far as its first width cells and
// the rest of the block goes with them.
func truncateBlock(block string, width int) string {
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		lines[i] = truncateLine(line, width)
	}
	return strings.Join(lines, "\n")
}

// truncateLine clips a rendered line to the terminal width. It is ANSI-aware:
// the line already carries styling, and cutting it by bytes would leave a
// dangling escape sequence that bleeds colour down the screen. Nothing this
// package emits may be wider than the terminal — a line that wraps would
// silently break the window arithmetic above it.
func truncateLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(line, width, "")
}
