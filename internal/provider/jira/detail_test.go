package jira_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/providertest"
)

// fullDetail serves the drill-in fixtures: the catalogue, the issue and its
// comments.
func fullDetail(t *testing.T) *replayServer {
	t.Helper()
	return newReplayServer(t, map[string][]response{
		linkTypePath: {{file: "issue_link_types.json"}},
		ticketPath:   {{file: "detail_full.json"}},
		commentsPath: {{file: "comments_page.json"}},
	})
}

// The description Jira returns for ABC-12, byte for byte. It is spelled out
// here rather than read from the fixture because "verbatim" is the claim under
// test: on REST v2 this is wiki markup, and sitrep stores it unrendered.
const wantDescription = "h2. Decoding a Ref\n\n" +
	"A Ref with children is an Epic & one without is a Ticket.\n\n" +
	"{code:go}\nfunc decodesToTicket(snap model.Epic" + "Snapshot) bool { return len(snap.Tickets) == 0 }\n{code}\n\n" +
	"See « éclair » for the naming discussion."

func TestFetchDetailReadsTheDescriptionVerbatim(t *testing.T) {
	p := newProvider(fullDetail(t))

	detail, err := p.FetchDetail(context.Background(), "ABC-12")
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	if detail.TicketID != "ABC-12" {
		t.Errorf("TicketID = %q, want the issue key", detail.TicketID)
	}
	if detail.Description != wantDescription {
		t.Errorf("Description = %q,\nwant %q", detail.Description, wantDescription)
	}
}

// Comments arrive newest-first because the request orders by -created, and
// model.Detail requires oldest-first: the reversal is the whole point.
func TestFetchDetailOrdersCommentsOldestFirst(t *testing.T) {
	p := newProvider(fullDetail(t))

	detail, err := p.FetchDetail(context.Background(), "ABC-12")
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	if len(detail.Comments) != 3 {
		t.Fatalf("got %d comments, want 3", len(detail.Comments))
	}
	for i, want := range []string{"30001", "30002", "30003"} {
		if detail.Comments[i].ID != want {
			t.Errorf("Comments[%d].ID = %q, want %q (oldest first)", i, detail.Comments[i].ID, want)
		}
	}

	// Jira's own timestamp format carries no colon in its offset, so it is not
	// RFC3339 — and the instant has to survive the conversion to UTC.
	first := detail.Comments[0]
	want := time.Date(2026, 1, 12, 8, 14, 33, 123_000_000, time.UTC)
	if !first.CreatedAt.Equal(want) {
		t.Errorf("Comments[0].CreatedAt = %s, want %s", first.CreatedAt, want)
	}
	if first.CreatedAt.Location() != time.UTC {
		t.Errorf("Comments[0].CreatedAt is in %s, want UTC", first.CreatedAt.Location())
	}
	if first.Author.DisplayName != "Ada Lovelace" {
		t.Errorf("Comments[0].Author = %+v, want Ada Lovelace", first.Author)
	}
	if got, want := first.URL, "https://acme.atlassian.net/browse/ABC-12?focusedCommentId=30001"; got != want {
		t.Errorf("Comments[0].URL = %q, want %q", got, want)
	}

	// A comment must never be dropped because its writer left the company.
	if got := detail.Comments[1].Author; got != (model.User{}) {
		t.Errorf("Comments[1].Author = %+v, want the zero User for a null author", got)
	}
	if detail.Comments[1].Body == "" {
		t.Error("the null-author comment lost its body")
	}
}

// An empty description, no comments and no links are the ordinary state of a
// freshly filed Ticket, and must never read as an error.
func TestFetchDetailOnABareTicket(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		linkTypePath:                       {{file: "issue_link_types.json"}},
		"/rest/api/2/issue/ABC-13":         {{file: "detail_bare.json"}},
		"/rest/api/2/issue/ABC-13/comment": {{file: "comments_empty.json"}},
	})
	p := newProvider(s)

	detail, err := p.FetchDetail(context.Background(), "ABC-13")
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}
	if detail.Description != "" {
		t.Errorf("Description = %q, want empty for a null description", detail.Description)
	}
	if len(detail.Comments) != 0 {
		t.Errorf("got %d comments, want none", len(detail.Comments))
	}
	if len(detail.Links) != 0 {
		t.Errorf("got %d links, want none", len(detail.Links))
	}
}

func TestFetchDetailSendsThreeRequestsAndDiscoversLinkTypesOnce(t *testing.T) {
	s := fullDetail(t)
	p := newProvider(s)

	for i := range 2 {
		if _, err := p.FetchDetail(context.Background(), "ABC-12"); err != nil {
			t.Fatalf("FetchDetail %d: %v", i+1, err)
		}
	}

	if n := len(s.requestsTo(ticketPath)); n != 2 {
		t.Errorf("%d issue reads for two drill-ins, want 2", n)
	}
	if n := len(s.requestsTo(commentsPath)); n != 2 {
		t.Errorf("%d comment reads for two drill-ins, want 2", n)
	}
	// The catalogue is discovered once per process, before the first link is
	// mapped, and never again.
	if n := len(s.requestsTo(linkTypePath)); n != 1 {
		t.Errorf("%d link type discoveries, want exactly 1", n)
	}

	issue := s.requestsTo(ticketPath)[0]
	if got := issue.query["fields"][0]; got != "description,issuelinks,summary,status,resolution,project" {
		t.Errorf("fields = %q, want the drill-in field selection", got)
	}
	comments := s.requestsTo(commentsPath)[0]
	if got := comments.query["orderBy"]; len(got) != 1 || got[0] != "-created" {
		t.Errorf("orderBy = %v, want -created", got)
	}
	if got := comments.query["maxResults"]; len(got) != 1 || got[0] != "100" {
		t.Errorf("maxResults = %v, want 100", got)
	}
}

func TestFetchDetailsUsesTheSingularFallback(t *testing.T) {
	s := fullDetail(t)
	details, err := newProvider(s).FetchDetails(t.Context(), []model.TicketID{"", "ABC-12", "ABC-12"})
	if err != nil {
		t.Fatalf("FetchDetails: %v", err)
	}
	if len(details) != 1 || details["ABC-12"].TicketID != "ABC-12" {
		t.Errorf("Details = %+v, want one canonical ABC-12 result", details)
	}
	if got := len(s.requestsTo(ticketPath)); got != 1 {
		t.Errorf("issue reads = %d, want one singular Detail read", got)
	}
	if got := len(s.requestsTo(commentsPath)); got != 1 {
		t.Errorf("comment reads = %d, want one singular Detail read", got)
	}
	if got := len(s.requestsTo(linkTypePath)); got != 1 {
		t.Errorf("link type discoveries = %d, want one", got)
	}
}

func TestFetchDetailFailures(t *testing.T) {
	tests := []struct {
		name string
		id   model.TicketID
		resp *response
		want providertest.Want
	}{
		{
			name: "an empty id",
			id:   "",
			want: providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{"does not name a Jira issue"},
			},
		},
		{
			name: "a malformed id",
			id:   "not a key",
			want: providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{"does not name a Jira issue"},
			},
		},
		{
			name: "an unknown issue",
			id:   "ABC-12",
			resp: &response{status: http.StatusNotFound, file: "errors_not_found.json"},
			want: providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{"ABC-12 not found (or you lack access)"},
				Secret:   fixtureToken,
			},
		},
		{
			// A drill-in classifies exactly as the polled path does: a 401 on
			// Enter is the same auth failure it is on a refresh.
			name: "an unauthorized read",
			id:   "ABC-12",
			resp: &response{status: http.StatusUnauthorized, file: "errors_auth.json"},
			want: providertest.Want{
				Kind:     provider.KindAuth,
				Contains: []string{"authentication failed (401)"},
				Secret:   fixtureToken,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			responses := map[string][]response{linkTypePath: {{file: "issue_link_types.json"}}}
			if tt.resp != nil {
				responses[ticketPath] = []response{*tt.resp}
			}
			s := newReplayServer(t, responses)
			p := newProvider(s)

			_, err := p.FetchDetail(context.Background(), tt.id)
			providertest.CheckError(t, "jira", err, tt.want)
			if tt.resp == nil && len(s.recorded()) != 0 {
				t.Error("a malformed ticket id reached the network")
			}
		})
	}
}
