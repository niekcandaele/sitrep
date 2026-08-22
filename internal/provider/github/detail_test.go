package github_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/github"
	"github.com/niekcandaele/sitrep/internal/provider/providertest"
)

// detailID is the node ID the Detail fixtures were recorded for. It is a
// model.TicketID because that is exactly what Resolve puts on every Ticket:
// GitHub's GraphQL node ID is the Ticket's identity for this driver.
const detailID = model.TicketID("I_kwDOT_WhQ88AAAABNqYEOA")

// fetchDetail replays one Detail payload and returns the normalized Detail.
func fetchDetail(t *testing.T, file string) (*replayServer, model.Detail) {
	t.Helper()

	s := newReplayServer(t, response{file: file})
	d, err := newProvider(s).FetchDetail(context.Background(), detailID)
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}
	return s, d
}

// A Capability declares what the driver returns today. Detail is served now, so
// Comments and BlockingLinks are on — flipped in the same change as the data.
func TestCapabilitiesIncludeDetail(t *testing.T) {
	want := model.Capabilities{Hierarchy: true, BlockingLinks: true, Comments: true, PullRequests: true}
	if got := github.New("github.com").Capabilities(); got != want {
		t.Errorf("Capabilities() = %+v, want %+v", got, want)
	}
}

// The description arrives as raw markdown, byte for byte: no rendering, no
// escaping, no normalization of the ampersand or the non-ASCII text.
func TestFetchDetailKeepsTheDescriptionVerbatim(t *testing.T) {
	_, d := fetchDetail(t, "detail_full.json")

	if d.TicketID != detailID {
		t.Errorf("TicketID = %q, want %q", d.TicketID, detailID)
	}
	for _, want := range []string{
		"## Parent",
		"hide finished work & fuzzy find",
		"Renseigner la métrique « éclair » du tableau de bord.",
		"- [ ] `d` toggles hiding Done + Cancelled tickets",
	} {
		if !strings.Contains(d.Description, want) {
			t.Errorf("Description does not contain %q:\n%s", want, d.Description)
		}
	}
	if !strings.Contains(d.Description, "\n\n") {
		t.Error("Description lost its paragraph breaks; it is stored unrendered")
	}
}

// Comments come back oldest first, with the author GitHub named, the body
// unrendered and the timestamp in UTC.
func TestFetchDetailNormalizesComments(t *testing.T) {
	_, d := fetchDetail(t, "detail_full.json")

	if len(d.Comments) != 3 {
		t.Fatalf("got %d comments, want 3", len(d.Comments))
	}

	first := d.Comments[0]
	want := model.Comment{
		ID:     "IC_kwDOT_WhQ88AAAABQAYV9w",
		Author: model.User{Login: "niekcandaele", DisplayName: "Niek Candaele", AvatarURL: "https://avatars.githubusercontent.com/u/22315101?u=5df03d047b67f831c6f771212ab6442eb8770d28&v=4"},
		Body: "_Posted by an agent (epic-runner)._\n\nDone: delivered by PR #23, " +
			"squash-merged into `epic/sitrep-v1` as 55f6059.\n",
		CreatedAt: time.Date(2026, time.August, 21, 11, 13, 40, 0, time.UTC),
		URL:       "https://github.com/niekcandaele/sitrep/issues/9#issuecomment-5369107959",
	}
	if !first.CreatedAt.Equal(want.CreatedAt) || first.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt = %v, want %v in UTC", first.CreatedAt, want.CreatedAt)
	}
	first.CreatedAt = want.CreatedAt
	if first != want {
		t.Errorf("comment 0 = %+v, want %+v", first, want)
	}

	// Oldest first is model.Detail's documented order, and comments(last:100)
	// is what keeps it true while showing the recent page.
	for i := 1; i < len(d.Comments); i++ {
		if d.Comments[i].CreatedAt.Before(d.Comments[i-1].CreatedAt) {
			t.Errorf("comment %d predates comment %d; comments must be oldest first", i, i-1)
		}
	}

	// An Actor that is not a User carries no display name, and that is not an
	// error.
	if bot := d.Comments[1].Author; bot.Login != "sitrep-bot" || bot.DisplayName != "" {
		t.Errorf("bot author = %+v, want a bare login", bot)
	}
}

// A deleted account arrives as a null author. Losing the comment with its writer
// would be worse than showing it unattributed.
func TestFetchDetailSurvivesANullAuthor(t *testing.T) {
	_, d := fetchDetail(t, "detail_full.json")

	last := d.Comments[len(d.Comments)-1]
	if last.Author != (model.User{}) {
		t.Errorf("author of the orphaned comment = %+v, want the zero User", last.Author)
	}
	if last.Body == "" {
		t.Error("the orphaned comment lost its body")
	}
}

// Links carry GitHub's own wording as the native label, blocked-by before
// blocks, with each target normalized through the same status mapping the list
// uses.
func TestFetchDetailNormalizesLinks(t *testing.T) {
	_, d := fetchDetail(t, "detail_full.json")

	want := []model.Link{
		{
			Kind:        model.LinkBlockedBy,
			NativeLabel: "is blocked by",
			Target: model.LinkTarget{
				ID:           "I_kwDOT_WhQ88AAAABNqYDsw",
				Key:          "#8",
				Title:        "TUI epic monitor: grouped list, auto-refresh",
				URL:          "https://github.com/niekcandaele/sitrep/issues/8",
				Status:       model.StatusDone,
				NativeStatus: "closed",
			},
		},
		{
			// A target outside the opened Ticket's repository is qualified, and
			// closed as not planned is Cancelled, never Done.
			Kind:        model.LinkBlockedBy,
			NativeLabel: "is blocked by",
			Target: model.LinkTarget{
				ID:           "I_kwDOACME00AAAAAB",
				Key:          "acme/widgets#91",
				Title:        "Widget adapter, abandoned",
				URL:          "https://github.com/acme/widgets/issues/91",
				Status:       model.StatusCancelled,
				NativeStatus: "not planned",
			},
		},
		{
			Kind:        model.LinkBlocks,
			NativeLabel: "blocks",
			Target: model.LinkTarget{
				ID:           "I_kwDOT_WhQ88AAAABNqYH2Q",
				Key:          "#16",
				Title:        "Errors that explain themselves + v0.1 release",
				URL:          "https://github.com/niekcandaele/sitrep/issues/16",
				Status:       model.StatusTodo,
				NativeStatus: "open",
			},
		},
	}

	if len(d.Links) != len(want) {
		t.Fatalf("got %d links, want %d: %+v", len(d.Links), len(want), d.Links)
	}
	for i := range want {
		if d.Links[i] != want[i] {
			t.Errorf("link %d = %+v, want %+v", i, d.Links[i], want[i])
		}
	}
}

// An issue with nothing on it is not an error: an empty body, no comments and no
// dependencies are the ordinary state of a freshly filed Ticket.
func TestFetchDetailOnAnEmptyIssue(t *testing.T) {
	_, d := fetchDetail(t, "detail_bare.json")

	if d.Description != "" {
		t.Errorf("Description = %q, want empty", d.Description)
	}
	if len(d.Comments) != 0 {
		t.Errorf("Comments = %+v, want none", d.Comments)
	}
	if len(d.Links) != 0 {
		t.Errorf("Links = %+v, want none", d.Links)
	}
}

// One drill-in is one request, carrying the node ID the caller passed and the
// headers GitHub needs — asserted at the seam, not inside the driver.
func TestFetchDetailIsOneRequest(t *testing.T) {
	s, _ := fetchDetail(t, "detail_full.json")

	requests := s.recorded()
	if len(requests) != 1 {
		t.Fatalf("the driver made %d requests, want exactly 1 per drill-in", len(requests))
	}

	r := requests[0]
	if got := r.variables["id"]; got != string(detailID) {
		t.Errorf("the request carried id %v, want %q", got, detailID)
	}
	if !strings.Contains(r.query, "node(id:$id)") {
		t.Errorf("the Detail request is not a node lookup: %q", r.query)
	}
	// Read-only by design (ADR-0002).
	if !strings.HasPrefix(strings.TrimSpace(r.query), "query") || strings.Contains(r.query, "mutation") {
		t.Errorf("the Detail document is not a query: %q", r.query)
	}
	if got, want := r.headers.Get("Authorization"), "bearer "+fixtureToken; got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
	if got := r.headers.Get("GraphQL-Features"); got != "sub_issues" {
		t.Errorf("GraphQL-Features = %q, want sub_issues", got)
	}
	if got := r.headers.Get("User-Agent"); got != "sitrep/test" {
		t.Errorf("User-Agent = %q, want sitrep/test", got)
	}
}

// ADR-0003, in its executable form: the polled document must never learn to
// fetch what only a drill-in needs.
func TestTheEpicQueryCarriesNoDetailSelection(t *testing.T) {
	s := fullEpic(t)

	if _, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: epicRef}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	requests := s.recorded()
	if len(requests) == 0 {
		t.Fatal("the driver made no requests")
	}
	for i, r := range requests {
		for _, forbidden := range []string{"body", "comments", "blockedBy", "blocking"} {
			if strings.Contains(r.query, forbidden) {
				t.Errorf("epic request %d selects %q. The epic document is polled every "+
					"interval and Detail is read once, on drill-in (ADR-0003): a description "+
					"or a comment page on this query multiplies the cost of every refresh. "+
					"Put it in detailQuery instead.", i, forbidden)
			}
		}
	}
}

// The two shapes of "that Ticket did not come back" say the same thing, name the
// id, and never panic on the missing node.
func TestFetchDetailErrors(t *testing.T) {
	tests := []struct {
		name     string
		response response
		want     providertest.Want
	}{
		{
			name:     "the node is null",
			response: response{file: "detail_node_null.json"},
			want: providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{"no ticket found", string(detailID)},
				Secret:   fixtureToken,
			},
		},
		{
			name:     "the node is not an Issue",
			response: response{file: "detail_not_an_issue.json"},
			want: providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{"no ticket found", string(detailID)},
				Secret:   fixtureToken,
			},
		},
		{
			name:     "a NOT_FOUND error entry",
			response: response{file: "errors_not_found.json"},
			want: providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{"no ticket found", string(detailID)},
				Secret:   fixtureToken,
			},
		},
		{
			name:     "other GraphQL errors",
			response: response{file: "errors_query.json"},
			want: providertest.Want{
				Kind:     provider.KindAuth,
				Contains: []string{"API error", "Resource not accessible by integration"},
				Secret:   fixtureToken,
			},
		},
		{
			// A drill-in classifies exactly as the polled path does: a 401 on
			// Enter is the same auth failure it is on a refresh.
			name:     "bad token",
			response: response{status: http.StatusUnauthorized, body: `{"message":"Bad credentials"}`},
			want: providertest.Want{
				Kind:     provider.KindAuth,
				Contains: []string{"authentication failed (401)", "gh auth status", "GITHUB_TOKEN"},
				Secret:   fixtureToken,
			},
		},
		{
			name: "rate limited",
			response: response{
				status:  http.StatusForbidden,
				body:    `{"message":"API rate limit exceeded"}`,
				headers: map[string]string{"x-ratelimit-remaining": "0", "x-ratelimit-reset": "1767225600"},
			},
			want: providertest.Want{
				Kind:     provider.KindRateLimit,
				Contains: []string{"rate limit exceeded", "resets at"},
				Secret:   fixtureToken,
			},
		},
		{
			name:     "malformed JSON",
			response: response{body: `{"data": {`},
			want: providertest.Want{
				Kind:     provider.KindUnavailable,
				Contains: []string{"decoding the response"},
				Secret:   fixtureToken,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newProvider(newReplayServer(t, tt.response))

			_, err := p.FetchDetail(context.Background(), detailID)
			providertest.CheckError(t, "github", err, tt.want)
		})
	}
}

// An empty id is a caller bug, and sitrep does not spend a request finding out.
func TestFetchDetailWithoutAnID(t *testing.T) {
	s := newReplayServer(t)

	if _, err := newProvider(s).FetchDetail(context.Background(), ""); err == nil {
		t.Fatal("FetchDetail accepted an empty ticket id")
	}
	if n := len(s.recorded()); n != 0 {
		t.Errorf("the driver made %d requests for an empty id, want none", n)
	}
}
