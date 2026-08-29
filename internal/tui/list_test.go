package tui

import (
	"fmt"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/render/plain"
)

func ticket(key string, status model.StatusCategory) model.Ticket {
	return model.Ticket{ID: model.TicketID(key), Key: key, Title: key, Status: status}
}

// BuildRows only flattens: grouping, display order and Provider order inside a
// group are model.GroupByCategory's contract, and re-deriving any of them here
// would be a second opinion the two could disagree on.
func TestBuildRows(t *testing.T) {
	tests := []struct {
		name    string
		tickets []model.Ticket
		want    []string
	}{
		{
			name: "no tickets produce no rows",
		},
		{
			name:    "one category yields one heading and its tickets",
			tickets: []model.Ticket{ticket("#1", model.StatusTodo), ticket("#2", model.StatusTodo)},
			want:    []string{"Todo(2)", "#1", "#2"},
		},
		{
			name: "categories come back in display order",
			tickets: []model.Ticket{
				ticket("#1", model.StatusDone),
				ticket("#2", model.StatusTodo),
				ticket("#3", model.StatusInProgress),
			},
			want: []string{"In Progress(1)", "#3", "Todo(1)", "#2", "Done(1)", "#1"},
		},
		{
			name: "provider order inside a group is preserved",
			tickets: []model.Ticket{
				ticket("#9", model.StatusTodo),
				ticket("#1", model.StatusTodo),
				ticket("#5", model.StatusTodo),
			},
			want: []string{"Todo(3)", "#9", "#1", "#5"},
		},
		{
			name: "an unmapped status lands in its own group at the end",
			tickets: []model.Ticket{
				ticket("#1", model.StatusDone),
				ticket("#2", model.StatusCategory(42)),
			},
			want: []string{"Done(1)", "#1", "Unknown(1)", "#2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeRows(BuildRows(tt.tickets))

			if len(got) != len(tt.want) {
				t.Fatalf("rows = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("row %d = %q, want %q (all: %v)", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}

// Only Tickets are selectable: the cursor never rests on a heading.
func TestGroupHeadersAreNotSelectable(t *testing.T) {
	rows := BuildRows([]model.Ticket{ticket("#1", model.StatusTodo)})

	if rows[0].Selectable() {
		t.Error("a group heading is selectable")
	}
	if !rows[1].Selectable() {
		t.Error("a Ticket row is not selectable")
	}
}

// ListFromWatchlistSnapshot copies the generic Header without deriving an
// identity from the snapshot's Epic.
func TestListFromWatchlistSnapshot(t *testing.T) {
	snap := model.WatchlistSnapshot{
		Header:       model.WatchlistHeader{Title: "4 tickets"},
		Epic:         model.Epic{Key: "ignored", Title: "ignored", URL: "ignored"},
		Tickets:      []model.Ticket{ticket("#1", model.StatusTodo)},
		LimitReached: true,
		Capabilities: model.Capabilities{PullRequests: true},
	}

	in := ListFromWatchlistSnapshot(snap)

	if in.Header.Key != "" || in.Header.Title != "4 tickets" || in.Header.URL != "" {
		t.Errorf("header = %+v, want the snapshot Header", in.Header)
	}
	if len(in.Tickets) != 1 || !in.LimitReached || !in.Capabilities.PullRequests {
		t.Errorf("input = %+v, want Tickets, LimitReached, and Capabilities", in)
	}
}

// describeRows renders a row list as short strings a table test can read.
func describeRows(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Kind == RowGroupHeader {
			out = append(out, fmt.Sprintf("%s(%d)", plain.CategoryLabel(r.Category), r.Count))
			continue
		}
		out = append(out, r.Ticket.Key)
	}
	return out
}
