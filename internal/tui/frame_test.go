package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/niekcandaele/sitrep/internal/model"
)

// ensureVisible is the one piece of list arithmetic in the monitor, and it is
// much cheaper to pin here than through frames. Rows are not uniform height,
// so the window is measured in lines.
func TestEnsureVisible(t *testing.T) {
	tests := []struct {
		name     string
		heights  []int
		selected int
		offset   int
		height   int
		want     int
	}{
		{
			name:   "an empty list scrolls nowhere",
			height: 10,
			want:   0,
		},
		{
			name:     "a selection already inside the window does not scroll",
			heights:  []int{1, 2, 2, 2},
			selected: 1,
			offset:   0,
			height:   10,
			want:     0,
		},
		{
			name:     "a selection above the window scrolls up to it",
			heights:  []int{1, 2, 2, 2},
			selected: 0,
			offset:   2,
			height:   4,
			want:     0,
		},
		{
			name:     "a selection below the window scrolls just far enough",
			heights:  []int{2, 2, 2, 2},
			selected: 3,
			offset:   0,
			height:   4,
			want:     2,
		},
		{
			name:     "a window taller than the list shows all of it",
			heights:  []int{2, 2},
			selected: 1,
			offset:   0,
			height:   100,
			want:     0,
		},
		{
			name:     "a one-line window puts the selection at the top",
			heights:  []int{2, 2, 2},
			selected: 2,
			offset:   0,
			height:   1,
			want:     2,
		},
		{
			name:     "an out-of-range selection is clamped rather than panicking",
			heights:  []int{1, 1},
			selected: 99,
			offset:   0,
			height:   10,
			want:     0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ensureVisible(tt.heights, tt.selected, tt.offset, tt.height); got != tt.want {
				t.Errorf("ensureVisible(%v, %d, %d, %d) = %d, want %d",
					tt.heights, tt.selected, tt.offset, tt.height, got, tt.want)
			}
		})
	}
}

func TestRowAt(t *testing.T) {
	tests := []struct {
		name    string
		heights []int
		offset  int
		y       int
		want    int
		ok      bool
	}{
		{name: "empty height map", y: 0},
		{name: "negative line", heights: []int{1}, y: -1},
		{name: "negative offset", heights: []int{1}, offset: -1, y: 0},
		{name: "offset below map", heights: []int{1}, offset: 1, y: 0},
		{name: "first group heading", heights: []int{1, 2, 2, 1}, y: 0, want: 0, ok: true},
		{name: "ticket title", heights: []int{1, 2, 2, 1}, y: 1, want: 1, ok: true},
		{name: "ticket meta", heights: []int{1, 2, 2, 1}, y: 2, want: 1, ok: true},
		{name: "later group spacer", heights: []int{1, 2, 2, 1}, y: 3, want: 2, ok: true},
		{name: "later group heading", heights: []int{1, 2, 2, 1}, y: 4, want: 2, ok: true},
		{name: "one-line ticket boundary", heights: []int{1, 2, 2, 1}, y: 5, want: 3, ok: true},
		{name: "padding below rows", heights: []int{1, 2, 2, 1}, y: 6},
		{name: "nonzero offset starts at its row", heights: []int{1, 2, 2, 1}, offset: 2, y: 0, want: 2, ok: true},
		{name: "nonzero offset second row", heights: []int{1, 2, 2, 1}, offset: 2, y: 2, want: 3, ok: true},
		{name: "nonzero offset padding", heights: []int{1, 2, 2, 1}, offset: 2, y: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := rowAt(tt.heights, tt.offset, tt.y)
			if got != tt.want || ok != tt.ok {
				t.Errorf("rowAt(%v, %d, %d) = (%d, %t), want (%d, %t)",
					tt.heights, tt.offset, tt.y, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// A meta line cut mid-word reads as a complete answer about the wrong thing:
// "ci FAIL" clipped to "ci FA" is a truncated CI verdict that looks whole. The
// ellipsis is the difference between a short answer and a wrong one.
func TestMetaLineTruncatesWithAnEllipsis(t *testing.T) {
	t3 := ticket("#1", model.StatusTodo)
	t3.NativeStatus = "Selected for Development"
	t3.Assignees = []model.User{{Login: "mara-vos"}, {Login: "tobias"}}
	t3.PullRequests = []model.PullRequest{{
		Number: 501, State: model.PROpen,
		Checks: model.ChecksFailing, Review: model.ReviewChangesRequested,
	}}

	const (
		width     = 60
		keyColumn = 6
	)
	budget := width - selectionGutter - keyColumn

	lines := rowLines([]Row{{Kind: RowTicket, Ticket: t3}}, 0, keyColumn, width,
		false, model.Capabilities{PullRequests: true}, Styles{})
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want a title and a meta line: %q", len(lines), lines)
	}

	meta := strings.TrimRight(lines[1], " ")
	if !strings.HasSuffix(meta, "…") {
		t.Errorf("meta line %q does not end in an ellipsis; a clipped verdict reads as a whole one", meta)
	}
	if w := lipgloss.Width(meta) - selectionGutter - keyColumn; w > budget {
		t.Errorf("meta line is %d cells of content, want at most the title's budget of %d", w, budget)
	}
}

// renderRows pads its output so the footer sits at the bottom of the screen
// rather than floating under a short list, and never emits a line wider than
// the terminal — a wrapped line would silently break the arithmetic above.
func TestRenderRowsFillsTheWindowExactly(t *testing.T) {
	rows := BuildRows([]model.Ticket{
		ticket("#1", model.StatusTodo),
		ticket("#2", model.StatusTodo),
	})

	got := renderRows(rows, 1, 0, 12, 40, model.Capabilities{}, Styles{})

	if n := len(strings.Split(got, "\n")); n != 12 {
		t.Errorf("body is %d lines, want exactly the 12 it was given", n)
	}
	for _, line := range strings.Split(got, "\n") {
		if w := lipgloss.Width(line); w > 40 {
			t.Errorf("line %q is %d cells wide, want at most 40", line, w)
		}
	}
}
