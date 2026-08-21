package cli

import (
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

// The decode rule has one name and one test: children mean a collection, no
// children mean the Ref named a Ticket. Everything else in this ticket follows
// from this line, so it is asserted here rather than through six layers.
func TestDecodesToTicket(t *testing.T) {
	tests := []struct {
		name string
		snap model.EpicSnapshot
		want bool
	}{
		{"the fixture Epic", fake.FixtureSnapshot(), false},
		{"a Ref that named a Ticket", fake.FixtureTicketSnapshot(), true},
		{"a Ticket with no parent", fake.FixtureOrphanTicketSnapshot(), true},
		// §7.1's flagged consequence, asserted rather than discovered: an Epic
		// with no Tickets yet opens as its own Detail.
		{"a collection with nothing in it yet", model.EpicSnapshot{Epic: model.Epic{Key: "#900"}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decodesToTicket(tt.snap); got != tt.want {
				t.Errorf("decodesToTicket = %v, want %v", got, tt.want)
			}
		})
	}
}

// The decoded Ticket carries everything its Detail header draws, so a decoded
// Ticket reads exactly the way its row in a list would.
func TestDecodedTicket(t *testing.T) {
	snap := fake.FixtureTicketSnapshot()

	got := decodedTicket(snap)

	if got.ID != snap.Epic.ID || got.Key != snap.Epic.Key || got.Title != snap.Epic.Title {
		t.Errorf("identity = %+v, want the fetched node's own", got)
	}
	if got.Status != model.StatusInProgress || got.NativeStatus != "In Review" {
		t.Errorf("status = %v/%q, want in_progress/In Review", got.Status, got.NativeStatus)
	}
	if len(got.Assignees) != 1 || got.Repository != "acme/widgets" || len(got.PullRequests) != 1 {
		t.Errorf("the Detail header's fields did not survive the decode: %+v", got)
	}
}
