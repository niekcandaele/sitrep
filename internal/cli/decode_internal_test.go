package cli

import (
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
	"github.com/niekcandaele/sitrep/internal/ref"
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

// The pure half of the decoded monitor: what it opens on, and whether the
// walk-up key has anything behind it. runMonitor needs a terminal; this does
// not, and this is where the branches are.
func TestDecodedMonitorOptions(t *testing.T) {
	child := ref.Ref{
		Tracker: ref.TrackerGitHub,
		Host:    "github.com",
		Owner:   "acme",
		Repo:    "widgets",
		Number:  112,
		Raw:     "acme/widgets#112",
	}
	ticket := model.Epic{ID: "t1", Key: "acme/widgets#112", Title: "Add the widget"}

	tests := []struct {
		name       string
		parent     model.Parent
		wantKey    string
		wantSource bool
	}{
		{
			name: "a parent the grammar can re-parse offers a walk-up",
			parent: model.Parent{
				ID:    "e1",
				Key:   "acme/widgets#111",
				Title: "Widget sync",
				URL:   "https://github.com/acme/widgets/issues/111",
			},
			wantKey:    "acme/widgets#111",
			wantSource: true,
		},
		{
			// The breadcrumb still says what the Ticket belongs to; there is
			// just nothing to open, so the key is not offered rather than
			// offered and broken.
			name:    "a parent that does not parse still draws a breadcrumb",
			parent:  model.Parent{ID: "e1", Key: "not a ref", Title: "Somewhere"},
			wantKey: "not a ref",
		},
		{
			name: "no parent at all",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := model.EpicSnapshot{
				Epic:         ticket,
				Parent:       tt.parent,
				Capabilities: model.Capabilities{Comments: true},
			}

			open, source := decodedMonitorOptions(fake.New(), child, snap, time.Now)

			if open.Ticket.ID != ticket.ID || open.Ticket.Title != ticket.Title {
				t.Errorf("Ticket = %+v, want the decoded %+v", open.Ticket, ticket)
			}
			if open.Capabilities != snap.Capabilities {
				t.Errorf("Capabilities = %+v, want %+v", open.Capabilities, snap.Capabilities)
			}
			if open.Parent.Key != tt.wantKey {
				t.Errorf("Parent.Key = %q, want %q", open.Parent.Key, tt.wantKey)
			}
			if (source != nil) != tt.wantSource {
				t.Errorf("Source present = %v, want %v", source != nil, tt.wantSource)
			}
		})
	}
}
