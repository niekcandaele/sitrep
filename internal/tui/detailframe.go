package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/render/plain"
)

// detailHeaderHeight is what renderDetailHeader always draws: the breadcrumb,
// the Ticket's identity, its meta line, and a blank spacer. It is fixed so the
// body's arithmetic does not have to ask whether a Ticket happens to have
// assignees.
const detailHeaderHeight = 4

// commentIndent is the two columns a comment body is inset by, so a wrapped
// comment reads as one block under its author rather than running into the next.
const commentIndent = "  "

// commentTimeLayout formats a comment's timestamp. It is absolute, in UTC, and
// deliberately not read through the injected clock: a comment's time is a fact
// about the Tracker, and time.Local in a golden is a test that passes in
// Amsterdam and fails in CI.
const commentTimeLayout = "2006-01-02 15:04"

// renderDetailHeader draws the block above the Detail body: the breadcrumb of
// the collection this Ticket was reached through, the Ticket's own identity, and
// the same meta line the list row shows for it.
//
// It is drawn in every state — loading, failed, loaded — because a screen that
// cannot say which Ticket it is reading is worse than the list it replaced.
func renderDetailHeader(in DetailInput, staleness string, width int, s Styles) string {
	return strings.Join([]string{
		pairLine(renderBreadcrumb(in.Parent, width, s), s.Staleness.Render(staleness), width),
		headerIdentity(Header{Key: in.Ticket.Key, Title: in.Ticket.Title, URL: in.Ticket.URL}, width, s),
		truncateLine(ticketMeta(detailMetaTicket(in.Ticket), in.Capabilities, s), width),
		"",
	}, "\n")
}

// renderBreadcrumb draws the collection a Ticket was reached through. The zero
// Header draws nothing at all: a Ticket opened without a list behind it has no
// parent to name, and an empty breadcrumb is the honest way to say so.
func renderBreadcrumb(parent Header, width int, s Styles) string {
	switch {
	case parent.Key == "" && parent.Title == "":
		return ""
	case parent.Key == "":
		return truncateLine(s.Breadcrumb.Render(plain.Truncate(parent.Title, width)), width)
	case parent.Title == "":
		return truncateLine(s.Breadcrumb.Render(parent.Key), width)
	}
	return truncateLine(s.Breadcrumb.Render(parent.Key+separator+plain.Truncate(parent.Title, width)), width)
}

// detailMetaTicket rebuilds the Ticket fields ticketMeta reads. The Detail
// screen shares that renderer rather than wording things itself: a Ticket must
// not describe itself differently one keystroke apart.
func detailMetaTicket(h DetailHeader) model.Ticket {
	return model.Ticket{
		Key:          h.Key,
		Title:        h.Title,
		URL:          h.URL,
		Status:       h.Status,
		NativeStatus: h.NativeStatus,
		Assignees:    h.Assignees,
		PullRequests: h.PullRequests,
		Repository:   h.Repository,
	}
}

// detailLines renders a DetailInput into the terminal lines its body occupies,
// wrapped to width. It is the whole Detail document, not the visible window:
// scrolling is choosing a slice of it, which is what makes the window arithmetic
// testable without a terminal.
//
// The body is wrapped here rather than handed to bubbles/viewport on purpose.
// The wrapping and the height arithmetic have to exist in this package anyway —
// #8 reached the same conclusion for the list — viewport carries its own KeyMap
// and mutable state into a package whose renderers are pure, and a golden of a
// viewport is a golden of somebody else's scrollbar.
func detailLines(in DetailInput, width int, s Styles) []string {
	lines := describeDetail(in, width, s)
	if comments := commentLines(in, width, s); len(comments) > 0 {
		lines = append(append(lines, ""), comments...)
	}
	if links := linkLines(in, width, s); len(links) > 0 {
		lines = append(append(lines, ""), links...)
	}
	return lines
}

// describeDetail renders the description section, which is always drawn: an
// empty description is a fact about the Ticket, and a screen that answers a
// drill-in with nothing at all reads as a bug.
func describeDetail(in DetailInput, width int, s Styles) []string {
	lines := []string{s.SectionHeader.Render("DESCRIPTION"), ""}
	if strings.TrimSpace(in.Detail.Description) == "" {
		return append(lines, s.Muted.Render("No description."))
	}
	for _, line := range wrapText(in.Detail.Description, width) {
		lines = append(lines, s.Body.Render(line))
	}
	return lines
}

// commentLines renders the comments section, or nothing at all when the Provider
// does not declare the Comments Capability. That silence is the point: an
// undeclared Capability is absent, never an error and never a placeholder. With
// the Capability and no comments there is something to say, so it is said.
func commentLines(in DetailInput, width int, s Styles) []string {
	if !in.Capabilities.Comments {
		return nil
	}
	if len(in.Detail.Comments) == 0 {
		return []string{s.SectionHeader.Render("COMMENTS"), "", s.Muted.Render("No comments yet.")}
	}

	// The heading counts what arrived. A discussion longer than the Provider's
	// own cap shows its most recent page; model.Detail has nowhere to carry the
	// Tracker's total, and inventing one here would be a guess.
	lines := []string{s.SectionHeader.Render(fmt.Sprintf("COMMENTS (%d)", len(in.Detail.Comments))), ""}
	for i, c := range in.Detail.Comments {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, truncateLine(s.CommentAuthor.Render(commentByline(c)), width))
		for _, line := range wrapText(c.Body, width-len(commentIndent)) {
			lines = append(lines, commentIndent+s.Body.Render(line))
		}
	}
	return lines
}

// commentByline renders "@login · 2026-01-12 09:14 UTC". A comment whose author
// has no login — a deleted account — is shown as @unknown rather than dropped.
func commentByline(c model.Comment) string {
	login := c.Author.Login
	if login == "" {
		login = "unknown"
	}
	return "@" + login + separator + c.CreatedAt.UTC().Format(commentTimeLayout) + " UTC"
}

// linkLines renders the links section as aligned columns: the Tracker's own
// label, the target's key, its title, and its Native Status. The label is the
// meaning — it is displayed and never interpreted — so nothing here branches on
// the Kind except to pick a colour.
func linkLines(in DetailInput, width int, s Styles) []string {
	links := model.VisibleLinks(in.Detail.Links, in.Capabilities)
	if len(links) == 0 {
		return nil
	}

	labels := make([]string, len(links))
	labelWidth, keyWidth, titleWidth, statusWidth := 0, 0, 0, 0
	for i, l := range links {
		labels[i] = linkLabel(l)
		labelWidth = max(labelWidth, lipgloss.Width(labels[i]))
		keyWidth = max(keyWidth, lipgloss.Width(l.Target.Key))
		titleWidth = max(titleWidth, lipgloss.Width(l.Target.Title))
		statusWidth = max(statusWidth, lipgloss.Width(nativeStatusTag(l.Target.NativeStatus)))
	}
	// The title is what gives on a narrow terminal: the label carries the
	// relationship and the key is how a human finds the Ticket again.
	titleWidth = min(titleWidth, max(width-labelWidth-keyWidth-statusWidth-6, minLinkTitleWidth))

	lines := []string{s.SectionHeader.Render(fmt.Sprintf("LINKS (%d)", len(links))), ""}
	for i, l := range links {
		line := column(s.LinkLabel.Render(labels[i]), labels[i], labelWidth) +
			column(s.TicketKey.Render(l.Target.Key), l.Target.Key, keyWidth)

		title := plain.Truncate(l.Target.Title, titleWidth)
		if tag := nativeStatusTag(l.Target.NativeStatus); tag != "" {
			line += column(s.TicketTitle.Render(title), title, titleWidth) + s.NativeStatus.Render(tag)
		} else {
			line += s.TicketTitle.Render(title)
		}
		lines = append(lines, truncateLine(line, width))
	}
	return lines
}

// minLinkTitleWidth is how much of a link target's title survives on a terminal
// too narrow for the whole row.
const minLinkTitleWidth = 12

// linkLabel is the Tracker's own wording for a relationship, falling back to the
// LinkKind's own token when the Tracker supplied none — spelled with spaces,
// because "blocked_by" is a wire token and this is a sentence a human reads.
func linkLabel(l model.Link) string {
	if l.NativeLabel != "" {
		return l.NativeLabel
	}
	return strings.ReplaceAll(l.Kind.String(), "_", " ")
}

// nativeStatusTag renders a link target's Native Status the way a list row does,
// or nothing when the Tracker supplied none.
func nativeStatusTag(native string) string {
	if native == "" {
		return ""
	}
	return "[" + native + "]"
}

// column pads a styled cell to width plus a two-space gutter. The padding is
// measured on the unstyled text because a rendered cell carries escape
// sequences that occupy no columns.
func column(styled, raw string, width int) string {
	return styled + strings.Repeat(" ", max(width-lipgloss.Width(raw), 0)+2)
}

// wrapText wraps a block of text to width, preserving its blank lines.
//
// It wraps rather than truncates because a description cut off at column 120 is
// useless, and it wraps line by line so that the paragraph breaks in a markdown
// body survive. Nothing here renders the markdown: the source is what the
// Tracker returned and what model.Detail stores.
func wrapText(text string, width int) []string {
	if width < 1 {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		// ansi.Wrap breaks on word boundaries and falls back to a hard break for
		// a token longer than the line, which is what keeps a bare URL from
		// overflowing the terminal.
		out = append(out, strings.Split(ansi.Wrap(line, width, ""), "\n")...)
	}
	return out
}

// clampDetailOffset keeps a scroll offset inside a document that may have just
// been re-wrapped by a resize or replaced by a re-fetch.
func clampDetailOffset(offset, lineCount, height int) int {
	return min(max(offset, 0), max(lineCount-max(height, 1), 0))
}

// renderDetailBody returns exactly height lines starting at offset, padding the
// remainder so the footer sits at the bottom of the screen. It is the same shape
// as the list's renderRows, minus the cursor.
func renderDetailBody(lines []string, offset, height, width int) string {
	offset = clampDetailOffset(offset, len(lines), height)

	out := make([]string, 0, max(height, 0))
	for i := offset; i < len(lines) && len(out) < height; i++ {
		out = append(out, truncateLine(lines[i], width))
	}
	for len(out) < height {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

// scrollIndicator reports how far through the document the window sits, or
// nothing at all when the whole Detail fits on screen and there is nothing to
// report.
func scrollIndicator(offset, lineCount, height int) string {
	span := lineCount - height
	if span <= 0 {
		return ""
	}
	return fmt.Sprintf("%d%%", clampDetailOffset(offset, lineCount, height)*100/span)
}
