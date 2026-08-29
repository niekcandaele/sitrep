package tui

import (
	"fmt"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/termtext"
)

// detailHeaderHeight is what renderDetailHeader always draws: the breadcrumb,
// the Ticket's identity, its meta line, and a blank spacer. It is fixed so the
// body's arithmetic does not have to ask whether a Ticket happens to have
// assignees.
const detailHeaderHeight = 4

// detailDocument is the complete, ephemeral Detail body for one frame. LinkRows
// is produced while Lines is assembled, so keyboard and mouse navigation use
// the same geometry the renderer drew rather than reverse-engineering strings.
type detailDocument struct {
	Lines    []string
	LinkRows []detailLinkRow
}

// detailMarkdownSections is the expensive, width- and theme-bound part of a
// loaded Detail document. Link rows stay outside it so focus and navigation
// geometry can be recomposed on every frame without invoking Goldmark or
// Glamour.
type detailMarkdownSections struct {
	description []string
	comments    []string
	valid       bool

	sourceDescription string
	sourceComments    []detailMarkdownCommentSource
	ticketURL         string
	commentsEnabled   bool
	width             int
	theme             markdownTheme
	glamourStyle      string
}

type detailMarkdownCommentSource struct {
	body      string
	author    string
	createdAt string
}

func (sections detailMarkdownSections) matches(
	in DetailInput, width int, markdown detailMarkdownRenderers,
) bool {
	if !sections.valid || sections.sourceDescription != in.Detail.Description ||
		sections.ticketURL != in.Ticket.URL || sections.commentsEnabled != in.Capabilities.Comments ||
		sections.width != width || sections.theme != markdown.theme ||
		sections.glamourStyle != os.Getenv("GLAMOUR_STYLE") ||
		len(sections.sourceComments) != len(in.Detail.Comments) {
		return false
	}
	for i, comment := range in.Detail.Comments {
		source := sections.sourceComments[i]
		if source.body != comment.Body || source.author != comment.Author.Login ||
			source.createdAt != comment.CreatedAt.UTC().Format(commentTimeLayout) {
			return false
		}
	}
	return true
}

func renderDetailMarkdownSections(
	in DetailInput, width int, s Styles, markdown detailMarkdownRenderers,
) detailMarkdownSections {
	comments := make([]detailMarkdownCommentSource, len(in.Detail.Comments))
	for i, comment := range in.Detail.Comments {
		comments[i] = detailMarkdownCommentSource{
			body:      comment.Body,
			author:    comment.Author.Login,
			createdAt: comment.CreatedAt.UTC().Format(commentTimeLayout),
		}
	}
	return detailMarkdownSections{
		description:       describeDetail(in, width, s, markdown.description),
		comments:          commentLines(in, width, s, markdown.comment),
		valid:             true,
		sourceDescription: in.Detail.Description,
		sourceComments:    comments,
		ticketURL:         in.Ticket.URL,
		commentsEnabled:   in.Capabilities.Comments,
		width:             width,
		theme:             markdown.theme,
		glamourStyle:      os.Getenv("GLAMOUR_STYLE"),
	}
}

// detailLinkIdentity identifies one displayed relationship across re-reads and
// reflow. Mutable target display fields are deliberately absent. Occurrence
// disambiguates otherwise identical relationships without collapsing them.
type detailLinkIdentity struct {
	TargetID    model.TicketID
	Kind        model.LinkKind
	NativeLabel string
	Occurrence  int
}

type detailLinkRow struct {
	Line     int
	Identity detailLinkIdentity
	Link     model.Link
}

// commentIndent is the two columns a comment body is inset by, so a wrapped
// comment reads as one block under its author rather than running into the next.
const commentIndent = "  "

// commentTimeLayout formats a comment's timestamp. It is absolute, in UTC, and
// deliberately not read through the injected clock: a comment's time is a fact
// about the Tracker, and time.Local in a golden is a test that passes in
// Amsterdam and fails in CI.
const commentTimeLayout = "2006-01-02 15:04"

// renderDetailHeader draws the block above the Detail body: the root Watchlist
// and prior Trail Tickets as a breadcrumb, the current Ticket's identity, and a
// meta line using the same field renderer as list rows.
//
// It is drawn in every state — loading, failed, loaded — because a screen that
// cannot say which Ticket it is reading is worse than the list it replaced.
func renderDetailHeader(in DetailInput, staleness string, width int, s Styles, trail []detailTrailEntry) string {
	return strings.Join([]string{
		renderDetailTopLine(in.Parent, trail, staleness, width, s),
		headerIdentity(Header{Key: in.Ticket.Key, Title: in.Ticket.Title, URL: in.Ticket.URL}, width, s, true),
		truncateLine(ticketMeta(detailMetaTicket(in.Ticket), in.Capabilities, s), width),
		"",
	}, "\n")
}

type detailBreadcrumbCrumb struct {
	text string
	url  string
}

const (
	breadcrumbDivider  = " › "
	breadcrumbCollapse = "… › "
)

// renderDetailTopLine reserves the current Detail's age before fitting the
// newest complete Trail crumbs into the remaining cells. This keeps the fixed
// header stable even when a cyclic Trail grows without bound.
func renderDetailTopLine(parent Header, trail []detailTrailEntry, staleness string, width int, s Styles) string {
	if width <= 0 {
		return ""
	}
	right := s.Staleness.Render(staleness)
	if lipgloss.Width(right) >= width {
		right = ansi.Truncate(right, width, "")
		return strings.Repeat(" ", max(width-lipgloss.Width(right), 0)) + right
	}

	crumbWidth := width - lipgloss.Width(right) - 1
	left := renderBreadcrumbCrumbs(detailBreadcrumbs(parent, trail), crumbWidth, s)
	return pairLine(left, right, width)
}

func detailBreadcrumbs(parent Header, trail []detailTrailEntry) []detailBreadcrumbCrumb {
	crumbs := make([]detailBreadcrumbCrumb, 0, len(trail)+1)
	if text := breadcrumbHeaderText(parent); text != "" {
		crumbs = append(crumbs, detailBreadcrumbCrumb{text: text, url: parent.URL})
	}
	for _, entry := range trail {
		text := entry.ticket.Key
		if text == "" {
			text = entry.ticket.Title
		}
		if text != "" {
			crumbs = append(crumbs, detailBreadcrumbCrumb{text: text, url: entry.ticket.URL})
		}
	}
	return crumbs
}

func breadcrumbHeaderText(parent Header) string {
	switch {
	case parent.Key == "":
		return parent.Title
	case parent.Title == "":
		return parent.Key
	default:
		return parent.Key + separator + parent.Title
	}
}

func renderBreadcrumbCrumbs(crumbs []detailBreadcrumbCrumb, width int, s Styles) string {
	if width <= 0 || len(crumbs) == 0 {
		return ""
	}

	rendered := make([]string, len(crumbs))
	for i, crumb := range crumbs {
		rendered[i] = renderHyperlink(s.Breadcrumb, crumb.text, crumb.url)
	}
	all := strings.Join(rendered, breadcrumbDivider)
	if lipgloss.Width(all) <= width {
		return all
	}

	newest := rendered[len(rendered)-1]
	used := lipgloss.Width(newest)
	if used > width {
		return ansi.Truncate(newest, width, "")
	}

	first := len(rendered) - 1
	for i := first - 1; i >= 0; i-- {
		candidate := lipgloss.Width(rendered[i]) + lipgloss.Width(breadcrumbDivider)
		prefix := 0
		if i > 0 {
			prefix = lipgloss.Width(breadcrumbCollapse)
		}
		if used+candidate+prefix > width {
			break
		}
		used += candidate
		first = i
	}

	left := strings.Join(rendered[first:], breadcrumbDivider)
	if first > 0 && lipgloss.Width(breadcrumbCollapse)+lipgloss.Width(left) <= width {
		left = breadcrumbCollapse + left
	}
	return left
}

// renderBreadcrumb is the root-only compatibility seam used by focused tests.
func renderBreadcrumb(parent Header, width int, s Styles) string {
	return renderBreadcrumbCrumbs(detailBreadcrumbs(parent, nil), width, s)
}

// detailMetaTicket rebuilds the Ticket fields ticketMeta reads. The Detail
// screen shares that renderer rather than wording things itself: a Ticket must
// not describe itself differently one keystroke apart.
func detailMetaTicket(h DetailHeader) model.Ticket {
	return model.Ticket{
		Key:              h.Key,
		Title:            h.Title,
		URL:              h.URL,
		Status:           h.Status,
		NativeStatus:     h.NativeStatus,
		Assignees:        h.Assignees,
		PullRequests:     h.PullRequests,
		PullRequestTotal: h.PullRequestTotal,
		Repository:       h.Repository,
	}
}

// detailLines is the compatibility seam for callers that only need the body.
// composeDetailDocument is authoritative for both rendered lines and Link rows.
func detailLines(in DetailInput, width int, s Styles) []string {
	return composeDetailDocument(in, width, s, detailLinkIdentity{}, false).Lines
}

// composeDetailDocument is the compatibility seam for focused rendering tests.
// The live Model supplies its cached, width-bound renderers instead.
func composeDetailDocument(in DetailInput, width int, s Styles, focused detailLinkIdentity, hasFocus bool) detailDocument {
	markdown := newDetailMarkdownRenderers(width, markdownDark)
	return composeDetailDocumentWithMarkdown(in, width, s, focused, hasFocus, markdown)
}

// composeDetailDocumentWithMarkdown renders a DetailInput and records the exact
// document line occupied by every capability-visible Link. Description and
// comment rendering never creates Link rows: only linkDocument owns that
// navigation metadata.
func composeDetailDocumentWithMarkdown(in DetailInput, width int, s Styles, focused detailLinkIdentity,
	hasFocus bool, markdown detailMarkdownRenderers) detailDocument {
	sections := renderDetailMarkdownSections(in, width, s, markdown)
	return composeDetailDocumentWithSections(in, width, s, focused, hasFocus, sections)
}

func composeDetailDocumentWithSections(in DetailInput, width int, s Styles, focused detailLinkIdentity,
	hasFocus bool, sections detailMarkdownSections) detailDocument {
	doc := detailDocument{Lines: append([]string(nil), sections.description...)}
	if len(sections.comments) > 0 {
		doc.Lines = append(append(doc.Lines, ""), sections.comments...)
	}
	links, rows := linkDocument(in, width, s, focused, hasFocus)
	if len(links) > 0 {
		doc.Lines = append(doc.Lines, "")
		base := len(doc.Lines)
		doc.Lines = append(doc.Lines, links...)
		for i := range rows {
			rows[i].Line += base
		}
		doc.LinkRows = rows
	}
	doc.Lines = truncateDocumentLines(doc.Lines, width)
	return doc
}

func truncateDocumentLines(lines []string, width int) []string {
	for i := range lines {
		lines[i] = truncateLine(lines[i], width)
	}
	return lines
}

// describeDetail renders the description section, which is always drawn: an
// empty description is a fact about the Ticket, and a screen that answers a
// drill-in with nothing at all reads as a bug.
func describeDetail(in DetailInput, width int, s Styles, renderer markdownRenderer) []string {
	lines := []string{s.SectionHeader.Render("DESCRIPTION"), ""}
	body := in.Detail.Description
	if strings.TrimSpace(body) == "" {
		return append(lines, s.Muted.Render("No description."))
	}
	return append(lines, renderMarkdownBody(body, in.Ticket.URL, width, s, renderer)...)
}

func renderMarkdownBody(body, ticketURL string, width int, s Styles, renderer markdownRenderer) []string {
	lines, err := renderer.render(body, ticketURL)
	if err == nil {
		return lines
	}

	// The renderer's own prose is not model data — it is a message from a
	// dependency — so it does not arrive through intake and is cleaned here.
	message := "Could not render Markdown: " + termtext.Line(err.Error())
	fallback := []string{s.Error.Render(truncateLine(message, width))}
	for _, line := range wrapText(body, width) {
		fallback = append(fallback, s.Body.Render(line))
	}
	return fallback
}

// commentLines renders the comments section, or nothing at all when the Provider
// does not declare the Comments Capability. That silence is the point: an
// undeclared Capability is absent, never an error and never a placeholder. With
// the Capability and no comments there is something to say, so it is said.
func commentLines(in DetailInput, width int, s Styles, renderer markdownRenderer) []string {
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
		for _, line := range renderMarkdownBody(c.Body, in.Ticket.URL,
			width-lipgloss.Width(commentIndent), s, renderer) {
			lines = append(lines, commentIndent+line)
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

// linkDocument renders the links section and records each relationship at the
// final zero-based line it occupies inside the returned section.
func linkDocument(in DetailInput, width int, s Styles, focused detailLinkIdentity, hasFocus bool) ([]string, []detailLinkRow) {
	links := model.VisibleLinks(in.Detail.Links, in.Capabilities)
	if len(links) == 0 {
		return nil, nil
	}

	labels := make([]string, len(links))
	statuses := make([]string, len(links))
	identities := make([]detailLinkIdentity, len(links))
	seen := make(map[detailLinkIdentity]int, len(links))
	labelWidth, keyWidth, titleWidth, statusWidth := 0, 0, 0, 0
	for i, l := range links {
		labels[i] = linkLabel(l)
		statuses[i] = nativeStatusTag(l.Target.NativeStatus)
		labelWidth = max(labelWidth, lipgloss.Width(labels[i]))
		keyWidth = max(keyWidth, lipgloss.Width(l.Target.Key))
		titleWidth = max(titleWidth, lipgloss.Width(l.Target.Title))
		statusWidth = max(statusWidth, lipgloss.Width(statuses[i]))

		identity := detailLinkIdentity{TargetID: l.Target.ID, Kind: l.Kind, NativeLabel: l.NativeLabel}
		occurrence := seen[identity]
		seen[identity] = occurrence + 1
		identity.Occurrence = occurrence
		identities[i] = identity
	}
	// The relationship label gives first on a narrow terminal. Reserve both
	// inter-column gutters, the target key, and a useful title before assigning its
	// width; a trailing Native Status can then disappear under the row's final
	// truncation without hiding the target identity. The fixed focus gutter is
	// removed before either column budget is calculated.
	contentWidth := max(width-selectionGutter, 0)
	labelWidth = min(labelWidth, max(contentWidth-keyWidth-minLinkTitleWidth-4, 0))
	titleWidth = min(titleWidth, max(contentWidth-labelWidth-keyWidth-statusWidth-6, minLinkTitleWidth))

	lines := []string{s.SectionHeader.Render(fmt.Sprintf("LINKS (%d)", len(links))), ""}
	rows := make([]detailLinkRow, 0, len(links))
	for i, l := range links {
		label := ansi.Truncate(labels[i], labelWidth, "…")
		line := column(s.LinkLabel.Render(label), label, labelWidth) +
			column(renderHyperlink(s.TicketKey, l.Target.Key, l.Target.URL), l.Target.Key, keyWidth)

		title := ansi.Truncate(l.Target.Title, titleWidth, "…")
		styledTitle := renderHyperlink(s.TicketTitle, title, l.Target.URL)
		if tag := statuses[i]; tag != "" {
			line += column(styledTitle, title, titleWidth) + s.NativeStatus.Render(tag)
		} else {
			line += styledTitle
		}

		marker := unselectedMarker
		if hasFocus && identities[i] == focused {
			marker = selectedMarker
		}
		rows = append(rows, detailLinkRow{Line: len(lines), Identity: identities[i], Link: l})
		lines = append(lines, truncateLine(marker+line, width))
	}
	return lines, rows
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
//
// The LINKS table is deliberately exempt from plain.ShowsNativeStatus: no
// Status Category heading groups these rows, so a link target's Native Status
// is the only status signal the reader gets. Do not "fix" the inconsistency
// with the list and Detail header meta lines; it is the rule working.
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

// ensureDocumentLineVisible moves a line-oriented window only when target is
// above or below it. It leaves an already-visible target and its reader-chosen
// offset untouched.
func ensureDocumentLineVisible(target, offset, lineCount, height int) int {
	offset = clampDetailOffset(offset, lineCount, height)
	height = max(height, 1)
	if target < offset {
		return clampDetailOffset(target, lineCount, height)
	}
	if target >= offset+height {
		return clampDetailOffset(target-height+1, lineCount, height)
	}
	return offset
}

func detailLinkRowByIdentity(doc detailDocument, identity detailLinkIdentity) (detailLinkRow, bool) {
	for _, row := range doc.LinkRows {
		if row.Identity == identity {
			return row, true
		}
	}
	return detailLinkRow{}, false
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
