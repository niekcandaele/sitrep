package gitlab_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/providertest"
)

// fullDetail serves an issue's three drill-in answers.
func fullDetail(t *testing.T) *replayServer {
	t.Helper()
	return newReplayServer(t, map[string][]response{
		issuePath:      {{file: "issue_detail.json"}},
		issueNotesPath: {{file: "notes_page.json"}},
		issueLinksPath: {{file: "links.json"}},
	})
}

func TestFetchDetailReadsTheDescriptionVerbatim(t *testing.T) {
	detail, err := newProvider(fullDetail(t)).FetchDetail(context.Background(), issueTicketID)
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	if detail.TicketID != issueTicketID {
		t.Errorf("TicketID = %q, want %q", detail.TicketID, issueTicketID)
	}
	want := "## Plan & scope\n\nWire the « éclair » decoder through `--json`.\n\n" +
		"```go\nfmt.Println(\"a & b\")\n```\n\nSee gitlab-org/cli#8510."
	if detail.Description != want {
		t.Errorf("Description = %q, want it byte for byte:\n%q", detail.Description, want)
	}
}

// Comments arrive newest-first and must be reversed into oldest-first, GitLab's
// system notes must be dropped, and a note whose author is gone must survive.
func TestFetchDetailNormalizesTheComments(t *testing.T) {
	detail, err := newProvider(fullDetail(t)).FetchDetail(context.Background(), issueTicketID)
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	if len(detail.Comments) != 3 {
		t.Fatalf("got %d comments, want 3: four notes minus the system one", len(detail.Comments))
	}
	for _, c := range detail.Comments {
		if strings.Contains(c.Body, "changed title from") {
			t.Errorf("a system note reached the comments: %q", c.Body)
		}
	}

	first := detail.Comments[0]
	if first.Body != "First word: a & b, « éclair »." {
		t.Errorf("Comments[0].Body = %q, want the oldest comment first", first.Body)
	}
	if want := time.Date(2026, 8, 21, 7, 21, 56, 22_000_000, time.UTC); !first.CreatedAt.Equal(want) {
		t.Errorf("Comments[0].CreatedAt = %s, want %s", first.CreatedAt, want)
	}
	if first.CreatedAt.Location() != time.UTC {
		t.Errorf("Comments[0].CreatedAt is in %s, want UTC", first.CreatedAt.Location())
	}
	if first.Author.Login != "jay_mccure" || first.Author.DisplayName != "Jay McCure" {
		t.Errorf("Comments[0].Author = %+v, want the note's author", first.Author)
	}
	if want := "https://gitlab.com/gitlab-org/cli/-/work_items/8509#note_4001"; first.URL != want {
		t.Errorf("Comments[0].URL = %q, want %q", first.URL, want)
	}

	// A comment must never be dropped because its writer left.
	if got := detail.Comments[1]; got.Author != (model.User{}) {
		t.Errorf("Comments[1].Author = %+v, want the zero User", got.Author)
	}
	if got := detail.Comments[1].Body; !strings.Contains(got, "internal note") {
		t.Errorf("Comments[1].Body = %q, want the null-author note kept", got)
	}
}

// One assertion per documented link_type, on both the Kind and the label the
// renderer shows, plus GitLab's own order.
func TestFetchDetailMapsTheLinks(t *testing.T) {
	detail, err := newProvider(fullDetail(t)).FetchDetail(context.Background(), issueTicketID)
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	wants := []struct {
		kind   model.LinkKind
		native string
		key    string
	}{
		{model.LinkBlockedBy, "is blocked by", "gitlab-org/cli#201"},
		{model.LinkBlocks, "blocks", "gitlab-org/cli#202"},
		{model.LinkRelates, "relates to", "gitlab-org/platform/core#203"},
		// An invented type falls back to Relates carrying its own wording.
		{model.LinkRelates, "is caused by", "gitlab-org/cli#204"},
		{model.LinkRelates, "relates to", "gitlab-org/cli#205"},
	}
	if len(detail.Links) != len(wants) {
		t.Fatalf("got %d links, want %d", len(detail.Links), len(wants))
	}
	for i, w := range wants {
		got := detail.Links[i]
		if got.Kind != w.kind || got.NativeLabel != w.native || got.Target.Key != w.key {
			t.Errorf("Links[%d] = {%v %q %s}, want {%v %q %s}",
				i, got.Kind, got.NativeLabel, got.Target.Key, w.kind, w.native, w.key)
		}
		wantID := model.TicketID("issue:" + w.key)
		if got.Target.ID != wantID {
			t.Errorf("Links[%d].Target.ID = %q, want FetchDetail identity %q", i, got.Target.ID, wantID)
		}
	}

	// A target carries everything the links table shows, including its own
	// Native Status through the same normalizeStatus every Ticket uses.
	blocked := detail.Links[0].Target
	if blocked.Title != "Publish the wire format" ||
		blocked.URL != "https://gitlab.com/gitlab-org/cli/-/work_items/201" ||
		blocked.Status != model.StatusTodo || blocked.NativeStatus != "open" ||
		blocked.ID != "issue:gitlab-org/cli#201" {
		t.Errorf("Links[0].Target = %+v, want the full target", blocked)
	}
	if dup := detail.Links[4].Target; dup.Status != model.StatusCancelled || dup.NativeStatus != "duplicate" {
		t.Errorf("Links[4].Target = %+v, want Cancelled/duplicate", dup)
	}
}

// A freshly filed Ticket is the ordinary case, not an error.
func TestFetchDetailOnABareTicket(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		issuePath:      {{file: "issue_detail_bare.json"}},
		issueNotesPath: {{file: "notes_empty.json"}},
		issueLinksPath: {{file: "links_empty.json"}},
	})

	detail, err := newProvider(s).FetchDetail(context.Background(), issueTicketID)
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}
	if detail.Description != "" {
		t.Errorf("Description = %q, want empty", detail.Description)
	}
	if detail.Comments != nil {
		t.Errorf("Comments = %+v, want nil", detail.Comments)
	}
	if detail.Links != nil {
		t.Errorf("Links = %+v, want nil", detail.Links)
	}
}

func TestFetchDetailSendsExactlyWhatItNeeds(t *testing.T) {
	s := fullDetail(t)

	if _, err := newProvider(s).FetchDetail(context.Background(), issueTicketID); err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	notes := s.requestsTo(issueNotesPath)
	if len(notes) != 1 {
		t.Fatalf("%d notes requests, want 1: nothing paginates", len(notes))
	}
	if got := notes[0].query["sort"]; len(got) != 1 || got[0] != "desc" {
		t.Errorf("notes sort=%v, want desc: the most recent hundred, not the most ancient", got)
	}
	if got := notes[0].query["order_by"]; len(got) != 1 || got[0] != "created_at" {
		t.Errorf("notes order_by=%v, want created_at", got)
	}
	if got := notes[0].query["per_page"]; len(got) != 1 || got[0] != "100" {
		t.Errorf("notes per_page=%v, want 100", got)
	}
	if n := len(s.requestsTo(issueLinksPath)); n != 1 {
		t.Errorf("%d links requests, want 1", n)
	}
	for _, r := range s.recorded() {
		if r.method != http.MethodGet {
			t.Errorf("the driver sent %s %s; this Tracker driver is read-only", r.method, r.path)
		}
	}
}

// The epic Detail path, and its trap: GitLab's epic notes endpoint is addressed
// by the epic's database id, not its iid, which is why the epic is read first.
func TestFetchDetailOnAnEpic(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		epicPath:      {{file: "epic.json"}},
		epicNotesPath: {{file: "notes_page.json"}},
	})

	detail, err := newProvider(s).FetchDetail(context.Background(), epicTicketID)
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	if detail.TicketID != epicTicketID {
		t.Errorf("TicketID = %q, want %q", detail.TicketID, epicTicketID)
	}
	if !strings.Contains(detail.Description, "## Why") {
		t.Errorf("Description = %q, want the recorded epic's description", detail.Description)
	}
	if len(detail.Comments) != 3 {
		t.Errorf("got %d comments, want 3", len(detail.Comments))
	}
	// The linked-epics API is separately deprecated and epic-to-epic blocking is
	// out of scope, so an epic Detail carries no links and that is not an error.
	if detail.Links != nil {
		t.Errorf("Links = %+v, want nil for an epic", detail.Links)
	}

	if n := len(s.requestsTo(epicNotesPath)); n != 1 {
		t.Errorf("%d requests to %s, want 1 — the notes path must use the epic's id, not its iid",
			n, epicNotesPath)
	}
	for _, r := range s.recorded() {
		if strings.Contains(r.path, "/links") {
			t.Errorf("the driver requested %s; an epic Detail reads no links", r.path)
		}
	}
}

func TestFetchDetailRejectsABadTicketID(t *testing.T) {
	ids := []model.TicketID{"", "   ", "gitlab-org/cli#8509", "issue:", "ABC-12", "epic:gitlab-org"}

	for _, id := range ids {
		t.Run(string(id), func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{})
			_, err := newProvider(s).FetchDetail(context.Background(), id)
			providertest.CheckError(t, "gitlab", err, providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{"does not name a GitLab epic, issue or milestone"},
			})
			if n := len(s.recorded()); n != 0 {
				t.Errorf("%d requests were sent for a malformed id", n)
			}
		})
	}
}

func TestFetchDetailFailures(t *testing.T) {
	tests := []struct {
		name string
		resp response
		want providertest.Want
	}{
		{
			name: "not found",
			resp: response{status: http.StatusNotFound, body: `{"message":"404 Not found"}`},
			want: providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{"gitlab-org/cli#8509", "not found (or you lack access)"},
				Secret:   fixtureToken,
			},
		},
		{
			// A drill-in classifies exactly as the polled path does: a 401 on
			// Enter is the same auth failure it is on a refresh.
			name: "unauthorized",
			resp: response{status: http.StatusUnauthorized, body: `{"message":"401 Unauthorized"}`},
			want: providertest.Want{
				Kind:     provider.KindAuth,
				Contains: []string{"authentication failed (401)"},
				Secret:   fixtureToken,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{issuePath: {tt.resp}})

			_, err := newProvider(s).FetchDetail(context.Background(), issueTicketID)
			providertest.CheckError(t, "gitlab", err, tt.want)
		})
	}
}
