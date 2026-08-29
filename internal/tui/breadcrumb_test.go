package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/niekcandaele/sitrep/internal/model"
)

func breadcrumbTrail(tickets ...model.Ticket) []detailTrailEntry {
	trail := make([]detailTrailEntry, len(tickets))
	for i, ticket := range tickets {
		trail[i].ticket = ticket
	}
	return trail
}

func TestDetailBreadcrumbCrumbsUseRootThenPriorTickets(t *testing.T) {
	parent := Header{Key: "ROOT", Title: "Root watchlist", URL: "https://example.test/root"}
	trail := breadcrumbTrail(
		model.Ticket{Key: "A-1", Title: "ignored while key exists"},
		model.Ticket{Title: "Title-only prior"},
		model.Ticket{},
	)
	got := detailBreadcrumbs(parent, trail)
	want := []detailBreadcrumbCrumb{
		{text: "ROOT" + separator + "Root watchlist", url: parent.URL},
		{text: "A-1"},
		{text: "Title-only prior"},
	}
	if len(got) != len(want) {
		t.Fatalf("crumb count = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("crumb[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestRenderBreadcrumbCrumbsFitsAndCollapsesFromTheLeft(t *testing.T) {
	crumbs := []detailBreadcrumbCrumb{{text: "ROOT"}, {text: "A-1"}, {text: "B-2"}, {text: "A-1"}}
	full := "ROOT › A-1 › B-2 › A-1"
	fullWidth := lipgloss.Width(full)

	tests := []struct {
		name  string
		width int
		want  string
	}{
		{name: "exact fit", width: fullWidth, want: full},
		{name: "one cell less drops oldest complete crumb", width: fullWidth - 1, want: "… › A-1 › B-2 › A-1"},
		{name: "retains newest suffix", width: 13, want: "… › B-2 › A-1"},
		{name: "retains newest", width: 7, want: "… › A-1"},
		{name: "no room", width: 0, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ansi.Strip(renderBreadcrumbCrumbs(crumbs, tt.width, Styles{}))
			if got != tt.want {
				t.Errorf("render = %q, want %q", got, tt.want)
			}
			if lipgloss.Width(got) > tt.width {
				t.Errorf("render width = %d, budget %d", lipgloss.Width(got), tt.width)
			}
		})
	}
}

func TestRenderBreadcrumbCrumbsPreservesNewestAcrossCollapseBoundary(t *testing.T) {
	tests := []struct {
		name   string
		newest string
		width  int
		want   string
	}{
		{name: "one less truncates newest", newest: "A-1", width: 2, want: "A-"},
		{name: "newest exact fit", newest: "A-1", width: 3, want: "A-1"},
		{name: "between newest and marker", newest: "A-1", width: 5, want: "A-1"},
		{name: "one less than marker plus newest", newest: "A-1", width: 6, want: "A-1"},
		{name: "marker plus newest exact fit", newest: "A-1", width: 7, want: "… › A-1"},
		{name: "Unicode one less", newest: "界-1", width: 3, want: "界-"},
		{name: "Unicode exact fit", newest: "界-1", width: 4, want: "界-1"},
		{name: "Unicode before marker boundary", newest: "界-1", width: 7, want: "界-1"},
		{name: "Unicode marker boundary", newest: "界-1", width: 8, want: "… › 界-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crumbs := []detailBreadcrumbCrumb{{text: "ROOT"}, {text: tt.newest}}
			got := ansi.Strip(renderBreadcrumbCrumbs(crumbs, tt.width, Styles{}))
			if got != tt.want {
				t.Errorf("render = %q, want %q", got, tt.want)
			}
			if lipgloss.Width(got) > tt.width {
				t.Errorf("render width = %d, budget %d", lipgloss.Width(got), tt.width)
			}
		})
	}
}

func TestRenderBreadcrumbCrumbsTruncatesOnlyAnOversizedNewestCrumb(t *testing.T) {
	const newest = "界面-wide-newest"
	crumbs := []detailBreadcrumbCrumb{{text: "OLD"}, {text: newest}}
	for _, width := range []int{1, 4, 5, 8, 12} {
		got := ansi.Strip(renderBreadcrumbCrumbs(crumbs, width, Styles{}))
		want := ansi.Truncate(newest, width, "")
		if got != want {
			t.Errorf("width %d render = %q, want newest-only truncation %q", width, got, want)
		}
		if lipgloss.Width(got) > width {
			t.Errorf("width %d produced %q at %d cells", width, got, lipgloss.Width(got))
		}
	}
}

func TestDetailTopLineAlwaysReservesAndRightAlignsStaleness(t *testing.T) {
	parent := Header{Key: "ROOT", Title: "A very long parent title"}
	trail := breadcrumbTrail(model.Ticket{Key: "A-1"}, model.Ticket{Key: "B-2"})

	for _, width := range []int{1, 4, 12, 20, 40} {
		got := ansi.Strip(renderDetailTopLine(parent, trail, "read 4m ago", width, Styles{}))
		if lipgloss.Width(got) > width {
			t.Errorf("width %d produced %q at %d cells", width, got, lipgloss.Width(got))
		}
		if lipgloss.Width(got) != width {
			t.Errorf("width %d top line occupies %d cells: %q", width, lipgloss.Width(got), got)
		}
	}

	narrow := ansi.Strip(renderDetailTopLine(parent, trail, "never read", 4, Styles{}))
	if narrow != "neve" {
		t.Errorf("narrow staleness = %q, want %q", narrow, "neve")
	}

	wide := ansi.Strip(renderDetailTopLine(parent, trail, "read 4m ago", 20, Styles{}))
	if !strings.HasSuffix(wide, "read 4m ago") || !strings.Contains(wide, "B-2") {
		t.Errorf("top line did not retain newest crumb and staleness: %q", wide)
	}
}

func TestDetailHeaderKeepsFourLinesAndOmitsCurrentTicketFromBreadcrumb(t *testing.T) {
	in := DetailInput{
		Ticket: DetailHeader{Key: "CURRENT-9", Title: "Current ticket"},
		Parent: Header{Key: "ROOT", Title: "Watchlist"},
	}
	trail := breadcrumbTrail(model.Ticket{Key: "A-1"}, model.Ticket{Key: "B-2"})
	for _, width := range []int{8, 20, 60} {
		header := ansi.Strip(renderDetailHeader(in, "read just now", width, Styles{}, trail))
		lines := strings.Split(header, "\n")
		if len(lines) != detailHeaderHeight {
			t.Fatalf("width %d header lines = %d, want %d: %q", width, len(lines), detailHeaderHeight, header)
		}
		if strings.Contains(lines[0], "CURRENT-9") {
			t.Errorf("width %d breadcrumb repeats current Ticket: %q", width, lines[0])
		}
		if width >= lipgloss.Width("CURRENT-9") && !strings.Contains(lines[1], "CURRENT-9") {
			t.Errorf("width %d identity omits current Ticket: %q", width, lines[1])
		}
		for i, line := range lines {
			if lipgloss.Width(line) > width {
				t.Errorf("width %d line %d occupies %d cells: %q", width, i, lipgloss.Width(line), line)
			}
		}
	}
}
