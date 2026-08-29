package tui

import (
	"charm.land/lipgloss/v2"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/termtext"
)

// Styles is every Lip Gloss style the monitor draws with, in one place, so a
// test can substitute a blank set and later work extends one file.
//
// Nothing here branches on a Native Status string. Colour is chosen from the
// Status Category and the pull request's own enums; the Tracker's label is
// printed, never interpreted. And nothing here is load-bearing: emphasis is
// carried by words ("ci FAIL"), case (uppercase headings) and a glyph ("▸"),
// so a monochrome terminal or a pipe still reads.
type Styles struct {
	// HeaderKey styles the Watchlist's key.
	HeaderKey lipgloss.Style
	// HeaderTitle styles the Watchlist's title.
	HeaderTitle lipgloss.Style
	// HeaderURL styles the Watchlist's URL.
	HeaderURL lipgloss.Style
	// BarFilled styles the completed part of the progress bar.
	BarFilled lipgloss.Style
	// BarEmpty styles the remaining part of the progress bar.
	BarEmpty lipgloss.Style
	// Counts styles the "3/9 done · 1 cancelled · 33%" line.
	Counts lipgloss.Style
	// Staleness styles the "updated 12s ago" indicator.
	Staleness lipgloss.Style
	// GroupHeader styles a Status Category heading, by category.
	GroupHeader map[model.StatusCategory]lipgloss.Style
	// TicketKey styles a Ticket's key.
	TicketKey lipgloss.Style
	// TicketTitle styles a Ticket's title.
	TicketTitle lipgloss.Style
	// Selected styles the selected Ticket's title line.
	Selected lipgloss.Style
	// NativeStatus styles the Tracker's own status label.
	NativeStatus lipgloss.Style
	// Assignees styles the @-handles.
	Assignees lipgloss.Style
	// PullRequest styles a pull request fragment whose state is unremarkable.
	PullRequest lipgloss.Style
	// PullRequestGood styles a fragment whose checks pass and review approves.
	PullRequestGood lipgloss.Style
	// PullRequestBad styles a fragment whose checks fail or review blocks.
	PullRequestBad lipgloss.Style
	// PullRequestBusy styles a fragment still waiting on checks or review.
	PullRequestBusy lipgloss.Style
	// FilterLine styles the footer's filter state and its Ticket count.
	FilterLine lipgloss.Style
	// SearchBox styles the find box while it holds the keyboard.
	SearchBox lipgloss.Style
	// EmptyFilter styles the notice that stands in for a list a filter has
	// emptied.
	EmptyFilter lipgloss.Style
	// Breadcrumb styles the root Watchlist and prior Trail Tickets in Detail.
	Breadcrumb lipgloss.Style
	// SectionHeader styles a Detail section heading, e.g. "COMMENTS (3)".
	SectionHeader lipgloss.Style
	// CommentAuthor styles a comment's "@login · time" byline.
	CommentAuthor lipgloss.Style
	// LinkLabel styles a Link's native label. The label carries the meaning; the
	// colour carries none.
	LinkLabel lipgloss.Style
	// Body styles the Detail screen's wrapped prose.
	Body lipgloss.Style
	// Error styles the refresh error line.
	Error lipgloss.Style
	// Muted styles secondary prose, such as the empty Watchlist notice.
	Muted lipgloss.Style
	// FrontierBold is the loud half of the Frontier's emphasis channel: an
	// Actionable node, a node on a cycle, and the focus marker. Emphasis is
	// weight rather than a second hue, because colour there carries Status
	// Category and nothing else.
	FrontierBold lipgloss.Style
	// FrontierFaint is the quiet half of that channel: a node that is blocked.
	FrontierFaint lipgloss.Style
}

// DefaultStyles returns the monitor's palette for a dark or light terminal.
// The two variants are chosen per colour rather than per style so the two
// backgrounds stay in step; isDark comes from the terminal itself
// (tea.BackgroundColorMsg), not from an environment guess.
func DefaultStyles(isDark bool) Styles {
	c := lipgloss.LightDark(isDark)

	var (
		text     = c(lipgloss.Color("#1c1c1c"), lipgloss.Color("#dcdcdc"))
		dim      = c(lipgloss.Color("#6c6c6c"), lipgloss.Color("#8a8a8a"))
		faint    = c(lipgloss.Color("#b0b0b0"), lipgloss.Color("#3a3a3a"))
		accent   = c(lipgloss.Color("#005f87"), lipgloss.Color("#6fc3df"))
		good     = c(lipgloss.Color("#116611"), lipgloss.Color("#7bc47b"))
		bad      = c(lipgloss.Color("#a30000"), lipgloss.Color("#ff8f8f"))
		busy     = c(lipgloss.Color("#8a6d00"), lipgloss.Color("#e0c46c"))
		selected = c(lipgloss.Color("#000000"), lipgloss.Color("#ffffff"))
	)

	base := lipgloss.NewStyle()
	return Styles{
		HeaderKey:   base.Foreground(accent).Bold(true),
		HeaderTitle: base.Foreground(text).Bold(true),
		HeaderURL:   base.Foreground(dim),
		BarFilled:   base.Foreground(accent),
		BarEmpty:    base.Foreground(faint),
		Counts:      base.Foreground(text),
		Staleness:   base.Foreground(dim),
		GroupHeader: map[model.StatusCategory]lipgloss.Style{
			model.StatusInProgress: base.Foreground(busy).Bold(true),
			model.StatusTodo:       base.Foreground(accent).Bold(true),
			model.StatusDone:       base.Foreground(good).Bold(true),
			model.StatusCancelled:  base.Foreground(dim).Bold(true),
			model.StatusUnknown:    base.Foreground(bad).Bold(true),
		},
		TicketKey:       base.Foreground(dim),
		TicketTitle:     base.Foreground(text),
		Selected:        base.Foreground(selected).Bold(true),
		NativeStatus:    base.Foreground(dim),
		Assignees:       base.Foreground(accent),
		PullRequest:     base.Foreground(dim),
		PullRequestGood: base.Foreground(good),
		PullRequestBad:  base.Foreground(bad),
		PullRequestBusy: base.Foreground(busy),
		FilterLine:      base.Foreground(dim),
		SearchBox:       base.Foreground(accent).Bold(true),
		EmptyFilter:     base.Foreground(dim),
		Breadcrumb:      base.Foreground(dim),
		SectionHeader:   base.Foreground(accent).Bold(true),
		CommentAuthor:   base.Foreground(accent),
		LinkLabel:       base.Foreground(dim),
		Body:            base.Foreground(text),
		Error:           base.Foreground(bad).Bold(true),
		Muted:           base.Foreground(dim),
		FrontierBold:    base.Foreground(text).Bold(true),
		FrontierFaint:   base.Foreground(dim).Faint(true),
	}
}

// renderHyperlink is the TUI's only OSC 8 creation seam.
//
// It cleans its own text and URI, and that is not a second boundary: model
// fields reaching it are already clean from intake.go, so these calls are
// idempotent no-ops in practice. They are scope integrity for the one sequence
// this function writes. An unbalanced or control-carrying URI breaks the
// hyperlink scope, which then swallows the rest of the screen — and this
// function is also handed strings the package composed itself, such as a
// plain.PullRequestSummary, which never crossed intake as a model field.
func renderHyperlink(style lipgloss.Style, text, url string) string {
	text = termtext.Line(text)
	url = termtext.Line(url)
	if url == "" {
		return style.Render(text)
	}
	return style.Hyperlink(url).Render(text)
}

// groupHeader returns the style for a Status Category heading, falling back to
// the broken-Provider style for a category outside the set.
func (s Styles) groupHeader(c model.StatusCategory) lipgloss.Style {
	if st, ok := s.GroupHeader[c]; ok {
		return st
	}
	return s.GroupHeader[model.StatusUnknown]
}

// pullRequest picks the fragment's style from the pull request's own enums: a
// failing check or a blocked review reads as bad, a green live pull request as
// good, anything still moving as busy. History — merged and closed — is muted.
func (s Styles) pullRequest(pr model.PullRequest) lipgloss.Style {
	if pr.State != model.PROpen && pr.State != model.PRDraft {
		return s.PullRequest
	}
	switch {
	case pr.Checks == model.ChecksFailing || pr.Review == model.ReviewChangesRequested:
		return s.PullRequestBad
	case pr.Checks == model.ChecksPending || pr.Review == model.ReviewPending:
		return s.PullRequestBusy
	case pr.Checks == model.ChecksPassing || pr.Review == model.ReviewApproved:
		return s.PullRequestGood
	default:
		return s.PullRequest
	}
}
