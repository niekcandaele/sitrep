package github_test

import (
	"context"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// ticketRef is the Ref a decoding run resolves: it names a plain Ticket
// rather than a collection, which the caller can only learn from the answer.
var ticketRef = ref.Ref{
	Tracker: ref.TrackerGitHub,
	Host:    "github.com",
	Owner:   "niekcandaele",
	Repo:    "sitrep",
	Number:  11,
	Raw:     "11",
}

// A Ref naming a Ticket comes back as a snapshot with no Tickets, whose Epic
// carries that Ticket's own identity and whose Parent carries the collection it
// belongs to. The driver reports it and decides nothing.
func TestResolveReportsTheFetchedIssuesParent(t *testing.T) {
	s := newReplayServer(t, response{file: "ticket_with_parent.json"})

	snap, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: ticketRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(snap.Tickets) != 0 {
		t.Errorf("got %d Tickets, want none: this Ref names a Ticket", len(snap.Tickets))
	}
	want := model.Parent{
		ID:    "I_kwDOT_WhQ88AAAABNqX_5Q",
		Key:   "#2",
		Title: "Epic: sitrep v1 — cross-tracker epic monitor",
		URL:   "https://github.com/niekcandaele/sitrep/issues/2",
	}
	if snap.Parent != want {
		t.Errorf("Parent = %+v, want %+v", snap.Parent, want)
	}
	if snap.Epic.Key != "niekcandaele/sitrep#11" {
		t.Errorf("Epic.Key = %q, want the fetched issue's own identity", snap.Epic.Key)
	}

	// One request, and no second one to discover the parent (ADR-0003).
	if n := len(s.recorded()); n != 1 {
		t.Errorf("the driver made %d requests, want 1", n)
	}
}

// The root issue's assignees and pull requests ride on the same batched call,
// because the decoded Ticket's Detail header is built from them.
func TestResolveReadsTheRootIssuesAssigneesAndPullRequests(t *testing.T) {
	s := newReplayServer(t, response{file: "ticket_with_parent.json"})

	snap, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: ticketRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got := snap.Epic.Repository; got != "niekcandaele/sitrep" {
		t.Errorf("Epic.Repository = %q, want niekcandaele/sitrep", got)
	}
	if got := snap.Epic.Assignees; len(got) != 1 || got[0].Login != "niekcandaele" {
		t.Errorf("Epic.Assignees = %+v, want one user niekcandaele", got)
	}
	if got := snap.Epic.PullRequests; len(got) != 1 || got[0].Number != 25 {
		t.Fatalf("Epic.PullRequests = %+v, want the one open pull request", got)
	}
	// The pull-request rule for in-progress runs on the fetched issue exactly as
	// it does on a child: one node must not describe itself two different ways
	// depending on which Ref reached it.
	if snap.Epic.Status != model.StatusInProgress {
		t.Errorf("Epic.Status = %v, want in_progress: an open pull request is moving it", snap.Epic.Status)
	}
	if snap.Epic.NativeStatus != "open" {
		t.Errorf("Epic.NativeStatus = %q, want GitHub's own word", snap.Epic.NativeStatus)
	}
}

// A parent in another repository is qualified against the fetched issue's own
// repository — the rule the children and the Detail link targets already use.
func TestResolveQualifiesACrossRepoParent(t *testing.T) {
	s := newReplayServer(t, response{file: "ticket_cross_repo_parent.json"})

	snap, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: ticketRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got := snap.Parent.Key; got != "acme/widgets#111" {
		t.Errorf("Parent.Key = %q, want it qualified acme/widgets#111", got)
	}
	// A root issue with no pull requests at all is empty, not a panic.
	if got := snap.Epic.PullRequests; got != nil {
		t.Errorf("Epic.PullRequests = %+v, want nil", got)
	}
	if got := snap.Epic.Assignees; got != nil {
		t.Errorf("Epic.Assignees = %+v, want nil", got)
	}
}

// A Ticket that hangs off nothing is an ordinary state, not an error.
func TestResolveWithoutAParent(t *testing.T) {
	s := newReplayServer(t, response{file: "ticket_no_parent.json"})

	snap, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: ticketRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if !snap.Parent.IsZero() {
		t.Errorf("Parent = %+v, want the zero Parent", snap.Parent)
	}
	if len(snap.Tickets) != 0 {
		t.Errorf("got %d Tickets, want none", len(snap.Tickets))
	}
}

// ADR-0003 in executable form: the polled document asks for the parent, which is
// O(1) per fetch, and still never for a body or comments, which are O(N).
func TestEpicQueryStaysTheCheapDocument(t *testing.T) {
	s := newReplayServer(t, response{file: "ticket_with_parent.json"})

	if _, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: ticketRef}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	query := s.recorded()[0].query
	if !strings.Contains(query, "parent {") {
		t.Errorf("the epic query does not ask for the fetched issue's parent: %q", query)
	}
	for _, forbidden := range []string{"body", "comments"} {
		if strings.Contains(query, forbidden) {
			t.Errorf("the polled epic query asks for %q, which belongs to FetchDetail: %q", forbidden, query)
		}
	}
}
