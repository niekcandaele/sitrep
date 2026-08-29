package termtext_test

import (
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/termtext"
	"github.com/niekcandaele/sitrep/internal/termtext/termtexttest"
)

// TestWalkersCleanEveryModelField is this boundary's structural guarantee.
//
// If you are reading this because it failed after adding a string field to a
// model type: that field is drawn on a terminal and nothing cleans it yet.
// Give it a policy in internal/termtext/model.go — termtext.Line for a
// one-line field, termtext.Body for one whose newlines are content. Exempting
// it is for identities that are never drawn, and costs a line in the allowlist
// below saying why.
//
// The filler reaches every string field by reflection, so a field added
// tomorrow is covered without anyone remembering to cover it.
func TestWalkersCleanEveryModelField(t *testing.T) {
	tests := []struct {
		name string
		// hostile returns a freshly filled value, and walk returns what the
		// walker makes of one. They are separate calls because the slice
		// walkers clean in place: the guard below needs a value no walker has
		// touched.
		hostile func() any
		walk    func() any
		opts    []termtexttest.Option
	}{
		{
			name:    "WatchlistSnapshot",
			hostile: func() any { return filledSnapshot() },
			walk:    func() any { return termtext.Snapshot(filledSnapshot()) },
			// Identity is never drawn — a screen shows a Key, never an ID — and
			// cleaning it would corrupt the Detail cache key and Provider re-reads.
			opts: []termtexttest.Option{termtexttest.Exempt(
				"Epic.ID", "Parent.ID", "Tickets[].ID", "Tickets[].ParentID",
			)},
		},
		{
			name:    "Ticket",
			hostile: func() any { return filledTicket() },
			walk:    func() any { return termtext.Ticket(filledTicket()) },
			// Identity, never drawn.
			opts: []termtexttest.Option{termtexttest.Exempt("ID", "ParentID")},
		},
		{
			name:    "Tickets",
			hostile: func() any { return filledTickets() },
			walk:    func() any { return termtext.Tickets(filledTickets()) },
			// Identity, never drawn.
			opts: []termtexttest.Option{termtexttest.Exempt("[].ID", "[].ParentID")},
		},
		{
			name:    "Epic",
			hostile: func() any { return filled[model.Epic]() },
			walk:    func() any { return termtext.Epic(filled[model.Epic]()) },
			// Identity, never drawn.
			opts: []termtexttest.Option{termtexttest.Exempt("ID")},
		},
		{
			name:    "Parent",
			hostile: func() any { return filled[model.Parent]() },
			walk:    func() any { return termtext.Parent(filled[model.Parent]()) },
			// Identity, never drawn.
			opts: []termtexttest.Option{termtexttest.Exempt("ID")},
		},
		{
			name:    "WatchlistHeader",
			hostile: func() any { return filled[model.WatchlistHeader]() },
			walk:    func() any { return termtext.Header(filled[model.WatchlistHeader]()) },
		},
		{
			name:    "User",
			hostile: func() any { return filled[model.User]() },
			walk:    func() any { return termtext.User(filled[model.User]()) },
		},
		{
			name:    "PullRequests",
			hostile: func() any { return filledSlice[model.PullRequest]() },
			walk:    func() any { return termtext.PullRequests(filledSlice[model.PullRequest]()) },
		},
		{
			name:    "Links",
			hostile: func() any { return filledSlice[model.Link]() },
			walk:    func() any { return termtext.Links(filledSlice[model.Link]()) },
			// Identity, never drawn.
			opts: []termtexttest.Option{termtexttest.Exempt("[].Target.ID")},
		},
		{
			name:    "Detail",
			hostile: func() any { return filled[model.Detail]() },
			walk:    func() any { return termtext.Detail(filled[model.Detail]()) },
			opts: []termtexttest.Option{
				// A description and a comment body are laid out, not one line.
				termtexttest.Multiline("Description", "Comments[].Body"),
				// Identity, never drawn.
				termtexttest.Exempt("TicketID", "Links[].Target.ID"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A filler that reached nothing would make the assertion below
			// vacuously true, so the hostile value has to fail it first.
			if termtexttest.IsClean(tt.hostile(), tt.opts...) {
				t.Fatalf("the filled %s carried nothing hostile, so this test proves nothing", tt.name)
			}
			termtexttest.AssertClean(t, tt.name, tt.walk(), tt.opts...)
		})
	}
}

// The exemptions above are a decision about display, not about content: an
// identity crosses the boundary unchanged so the Detail cache and the
// Provider's re-reads still find the Ticket the Tracker named.
func TestWalkersLeaveIdentityUntouched(t *testing.T) {
	ticket := filledTicket()
	walked := termtext.Ticket(ticket)

	if walked.ID != ticket.ID || walked.ParentID != ticket.ParentID {
		t.Errorf("identity was rewritten: ID %q -> %q, ParentID %q -> %q",
			ticket.ID, walked.ID, ticket.ParentID, walked.ParentID)
	}
}

func filled[T any]() T {
	var v T
	termtexttest.Fill(&v)
	return v
}

func filledSlice[T any]() []T {
	s := make([]T, 2)
	termtexttest.Fill(&s)
	return s
}

func filledTicket() model.Ticket    { return filled[model.Ticket]() }
func filledTickets() []model.Ticket { return filledSlice[model.Ticket]() }
func filledSnapshot() model.WatchlistSnapshot {
	return filled[model.WatchlistSnapshot]()
}
