package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/github"
	"github.com/niekcandaele/sitrep/internal/provider/providertest"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// The GitHub driver must satisfy the interface every other Provider does.
var _ provider.Provider = (*github.Provider)(nil)

// fixtureToken is the token every replayed request is expected to carry. It is
// obviously fake so that a test asserting no token leaks into an error message
// is asserting something visible.
const fixtureToken = "fixture-token-not-a-real-secret"

// epicRef is the Epic Ref the fixtures were recorded for.
var epicRef = ref.Ref{
	Tracker: ref.TrackerGitHub,
	Host:    "github.com",
	Owner:   "niekcandaele",
	Repo:    "sitrep",
	Number:  2,
	Raw:     "2",
}

// response is one replayed answer: a status code and either a fixture file or a
// literal body.
type response struct {
	status  int
	file    string
	body    string
	headers map[string]string
}

// recordedRequest is what the replay server saw, so tests can assert the
// headers and cursors sitrep sends without reaching inside the driver.
type recordedRequest struct {
	method    string
	headers   http.Header
	query     string
	variables map[string]any
}

// replayServer serves recorded GraphQL payloads, one per request, in order. It
// is the driver's only test seam: everything below it is the driver's business
// and everything above it is asserted on the normalized model.
type replayServer struct {
	*httptest.Server

	mu        sync.Mutex
	responses []response
	requests  []recordedRequest
}

func newReplayServer(t *testing.T, responses ...response) *replayServer {
	t.Helper()

	s := &replayServer{responses: responses}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("request method = %q, want POST", r.Method)
		}

		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding the request body: %v", err)
		}

		s.mu.Lock()
		n := len(s.requests)
		s.requests = append(s.requests, recordedRequest{
			method:    r.Method,
			headers:   r.Header.Clone(),
			query:     body.Query,
			variables: body.Variables,
		})
		var resp response
		if n < len(s.responses) {
			resp = s.responses[n]
		} else {
			resp = response{status: http.StatusInternalServerError, body: `{"message":"unexpected extra request"}`}
		}
		s.mu.Unlock()

		for k, v := range resp.headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		status := resp.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)

		if resp.file != "" {
			payload, err := os.ReadFile(filepath.Join("testdata", resp.file))
			if err != nil {
				t.Errorf("reading fixture %s: %v", resp.file, err)
				return
			}
			_, _ = w.Write(payload)
			return
		}
		_, _ = w.Write([]byte(resp.body))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *replayServer) recorded() []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recordedRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

// newProvider points a real Provider at the replay server with a stub token
// source, so no test needs gh or a network.
func newProvider(s *replayServer, opts ...github.Option) *github.Provider {
	base := []github.Option{
		github.WithEndpoint(s.URL),
		github.WithTokenSource(func(context.Context, string) (string, error) { return fixtureToken, nil }),
		github.WithUserAgent("sitrep/test"),
	}
	return github.New("github.com", append(base, opts...)...)
}

// fullEpic serves the two-page fixture epic.
func fullEpic(t *testing.T) *replayServer {
	t.Helper()
	return newReplayServer(t,
		response{file: "epic_page1.json"},
		response{file: "epic_page2.json"},
	)
}

func TestName(t *testing.T) {
	if p := github.New("github.com"); p.Name() != "github" {
		t.Errorf("Name() = %q, want %q", p.Name(), "github")
	}
}

func TestFetchEpicNormalizesTheEpic(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	want := model.Epic{
		ID:           "I_kwDOT_WhQ88AAAABNqX_5Q",
		Key:          "niekcandaele/sitrep#2",
		Title:        "Epic: sitrep v1 — cross-tracker epic monitor",
		URL:          "https://github.com/niekcandaele/sitrep/issues/2",
		Status:       model.StatusTodo,
		NativeStatus: "open",
		Repository:   "niekcandaele/sitrep",
	}
	if !reflect.DeepEqual(snap.Epic, want) {
		t.Errorf("Epic = %+v, want %+v", snap.Epic, want)
	}
	// The recorded epic hangs off nothing, and that is an ordinary state.
	if !snap.Parent.IsZero() {
		t.Errorf("Parent = %+v, want the zero Parent", snap.Parent)
	}

	// FetchedAt belongs to the caller's clock; a Provider leaves it zero.
	if !snap.FetchedAt.IsZero() {
		t.Errorf("FetchedAt = %v, want the zero time", snap.FetchedAt)
	}
	if got, want := snap.Capabilities, p.Capabilities(); got != want {
		t.Errorf("snapshot Capabilities = %+v, want %+v", got, want)
	}
}

func TestFetchEpicNormalizesEveryTicket(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	type want struct {
		key    string
		status model.StatusCategory
		native string
		repo   string
	}
	// API order, preserved across both pages: ordering is a rendering concern
	// and the driver never sorts.
	//
	// Several open Tickets read as InProgress because a pull request is moving
	// them; Native Status stays GitHub's own "open" throughout.
	wants := []want{
		{"#3", model.StatusDone, "closed", "niekcandaele/sitrep"},
		{"#4", model.StatusDone, "closed", "niekcandaele/sitrep"},
		{"#5", model.StatusInProgress, "open", "niekcandaele/sitrep"},
		{"#6", model.StatusInProgress, "open", "niekcandaele/sitrep"},
		{"#7", model.StatusTodo, "open", "niekcandaele/sitrep"},
		{"#90", model.StatusInProgress, "open", "niekcandaele/sitrep"},
		{"acme/widgets#91", model.StatusCancelled, "not planned", "acme/widgets"},
		{"#92", model.StatusCancelled, "duplicate", "niekcandaele/sitrep"},
		{"#93", model.StatusDone, "reason from the future", "niekcandaele/sitrep"},
		{"#94", model.StatusDone, "closed", "niekcandaele/sitrep"},
		{"#95", model.StatusInProgress, "open", "niekcandaele/sitrep"},
		{"#96", model.StatusInProgress, "open", "niekcandaele/sitrep"},
		{"#97", model.StatusTodo, "open", "niekcandaele/sitrep"},
		{"#98", model.StatusInProgress, "open", "niekcandaele/sitrep"},
		{"#99", model.StatusInProgress, "open", "niekcandaele/sitrep"},
		{"#100", model.StatusInProgress, "open", "niekcandaele/sitrep"},
		{"#101", model.StatusTodo, "open", "niekcandaele/sitrep"},
	}

	if len(snap.Tickets) != len(wants) {
		t.Fatalf("got %d Tickets, want %d", len(snap.Tickets), len(wants))
	}
	for i, w := range wants {
		got := snap.Tickets[i]
		if got.Key != w.key || got.Status != w.status || got.NativeStatus != w.native || got.Repository != w.repo {
			t.Errorf("Ticket %d = {%s %v %q %s}, want {%s %v %q %s}",
				i, got.Key, got.Status, got.NativeStatus, got.Repository,
				w.key, w.status, w.native, w.repo)
		}
		if got.ID == "" {
			t.Errorf("Ticket %s has no ID; the node ID is the Ticket's identity", got.Key)
		}
		// ADR-0003: one level of the sub-issue graph, so nothing has a parent.
		if got.ParentID != "" {
			t.Errorf("Ticket %s has ParentID %q, want empty", got.Key, got.ParentID)
		}
	}
}

// The acceptance criterion made executable: an issue closed as not planned is
// Cancelled, and a cancelled Ticket is excluded from the progress denominator
// rather than holding the epic permanently short of 100%. The fixture's
// not-planned Ticket carries a closed pull request, so this also proves pull
// request correlation cannot promote a Ticket back out of Cancelled.
func TestNotPlannedIsCancelledAndLeavesTheDenominator(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	var cancelled []string
	for _, ticket := range snap.Tickets {
		if ticket.Status == model.StatusCancelled {
			cancelled = append(cancelled, ticket.Key)
		}
	}
	if len(cancelled) != 2 {
		t.Fatalf("cancelled Tickets = %v, want the not-planned and duplicate children", cancelled)
	}

	progress := model.ComputeProgress(snap.Tickets)
	if progress.Cancelled != 2 {
		t.Errorf("Progress.Cancelled = %d, want 2", progress.Cancelled)
	}
	if progress.Total != 17 {
		t.Errorf("Progress.Total = %d, want 17", progress.Total)
	}
	if progress.Denominator != 15 {
		t.Errorf("Progress.Denominator = %d, want 15: cancelled work cannot still be finished", progress.Denominator)
	}
}

// A cross-repo child is an ordinary node with a different repository. It must
// survive the fetch and be attributed to where it actually lives.
func TestCrossRepoChildKeepsItsOwnRepository(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	var found *model.Ticket
	for i := range snap.Tickets {
		if snap.Tickets[i].Repository == "acme/widgets" {
			found = &snap.Tickets[i]
		}
	}
	if found == nil {
		t.Fatal("the cross-repo child was dropped")
	}
	if found.Key != "acme/widgets#91" {
		t.Errorf("Key = %q, want a repo-qualified key", found.Key)
	}

	// The display key of a cross-repo child is a working Epic Ref: a human can
	// paste it straight back into sitrep.
	back, err := ref.Parse(context.Background(), found.Key)
	if err != nil {
		t.Fatalf("the cross-repo Key does not parse as an Epic Ref: %v", err)
	}
	if back.Owner != "acme" || back.Repo != "widgets" || back.Number != 91 {
		t.Errorf("Key round-tripped to %+v, want acme/widgets#91", back)
	}
}

// ticketsByKey indexes a fetched epic so a test can name the Ticket it means.
func ticketsByKey(t *testing.T, snap model.EpicSnapshot) map[string]model.Ticket {
	t.Helper()
	byKey := make(map[string]model.Ticket, len(snap.Tickets))
	for _, ticket := range snap.Tickets {
		byKey[ticket.Key] = ticket
	}
	return byKey
}

// The heart of pull request correlation: every situation the overseer wants to
// tell apart — the agent is coding, it is waiting on review, the checks are red
// — arrives on the normalized model, per Ticket, from one fetch.
func TestPullRequestsAreCorrelatedToTickets(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	byKey := ticketsByKey(t, snap)

	tests := []struct {
		ticket string
		lead   model.PullRequest
		count  int
	}{
		{
			ticket: "#6",
			lead: model.PullRequest{
				Number: 20, Title: "GitHub PR correlation",
				URL:        "https://github.com/niekcandaele/sitrep/pull/20",
				Repository: "niekcandaele/sitrep",
				State:      model.PRDraft, Review: model.ReviewNone, Checks: model.ChecksPending,
			},
			count: 1,
		},
		{
			ticket: "#5",
			lead: model.PullRequest{
				Number: 19, Title: "GitHub driver: epic fetch, Epic Ref grammar, auth",
				URL:        "https://github.com/niekcandaele/sitrep/pull/19",
				Repository: "niekcandaele/sitrep",
				State:      model.PROpen, Review: model.ReviewPending, Checks: model.ChecksPassing,
			},
			count: 1,
		},
		{
			ticket: "#95",
			lead: model.PullRequest{
				Number: 104, Title: "Rate limit backoff",
				URL:        "https://github.com/niekcandaele/sitrep/pull/104",
				Repository: "niekcandaele/sitrep",
				State:      model.PROpen, Review: model.ReviewChangesRequested, Checks: model.ChecksFailing,
			},
			count: 1,
		},
		{
			// GitHub's ERROR rollup is a red pipeline as surely as FAILURE is.
			ticket: "#96",
			lead: model.PullRequest{
				Number: 105, Title: "Approved work with an infrastructure error",
				URL:        "https://github.com/niekcandaele/sitrep/pull/105",
				Repository: "niekcandaele/sitrep",
				State:      model.PROpen, Review: model.ReviewApproved, Checks: model.ChecksFailing,
			},
			count: 1,
		},
		{
			ticket: "#3",
			lead: model.PullRequest{
				Number: 17, Title: "Add engineering rails: build, CI gates, release pipeline",
				URL:        "https://github.com/niekcandaele/sitrep/pull/17",
				Repository: "niekcandaele/sitrep",
				State:      model.PRMerged, Review: model.ReviewApproved, Checks: model.ChecksPassing,
			},
			count: 1,
		},
		{
			// includeClosedPrs earns its place here: without it "the agent's
			// work was turned down" would look like "no work started".
			ticket: "#97",
			lead: model.PullRequest{
				Number: 106, Title: "Rejected approach",
				URL:        "https://github.com/niekcandaele/sitrep/pull/106",
				Repository: "niekcandaele/sitrep",
				// Closed beats the draft flag.
				State: model.PRClosed, Review: model.ReviewChangesRequested, Checks: model.ChecksPassing,
			},
			count: 1,
		},
		{
			// A pull request in another repository is reported as living there.
			ticket: "#99",
			lead: model.PullRequest{
				Number: 110, Title: "Cross-repo fix for the sitrep epic",
				URL:        "https://github.com/acme/widgets/pull/110",
				Repository: "acme/widgets",
				State:      model.PROpen, Review: model.ReviewPending, Checks: model.ChecksPassing,
			},
			count: 1,
		},
		{
			// A head commit that did not come back is "no CI", not "green".
			ticket: "#100",
			lead: model.PullRequest{
				Number: 112, Title: "A pull request with no status check rollup",
				URL:        "https://github.com/niekcandaele/sitrep/pull/112",
				Repository: "niekcandaele/sitrep",
				State:      model.PROpen, Review: model.ReviewNone, Checks: model.ChecksNone,
			},
			count: 2,
		},
		{
			// Enum values sitrep has never heard of never read as green.
			ticket: "#101",
			lead: model.PullRequest{
				Number: 113, Title: "A state and a rollup sitrep has never heard of",
				URL:   "https://github.com/niekcandaele/sitrep/pull/113",
				State: model.PRUnknown, Review: model.ReviewNone, Checks: model.ChecksPending,
			},
			count: 1,
		},
		{
			ticket: "acme/widgets#91",
			lead: model.PullRequest{
				Number: 7, Title: "Widget adapter, abandoned",
				URL:        "https://github.com/acme/widgets/pull/7",
				Repository: "acme/widgets",
				State:      model.PRClosed, Review: model.ReviewNone, Checks: model.ChecksFailing,
			},
			count: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.ticket, func(t *testing.T) {
			ticket, ok := byKey[tt.ticket]
			if !ok {
				t.Fatalf("the fixture epic has no Ticket %s", tt.ticket)
			}
			if len(ticket.PullRequests) != tt.count {
				t.Fatalf("Ticket %s has %d pull requests, want %d: none may be dropped",
					tt.ticket, len(ticket.PullRequests), tt.count)
			}
			if got := ticket.PullRequests[0]; got != tt.lead {
				t.Errorf("lead pull request = %+v, want %+v", got, tt.lead)
			}
		})
	}
}

// A Ticket nothing is working on says so with nil, the model's documented
// "none" — including the shapes GitHub can answer with instead of an empty
// list.
func TestTicketsWithNoPullRequests(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	byKey := ticketsByKey(t, snap)

	for _, key := range []string{"#7", "#92", "#93", "#94"} {
		if got := byKey[key].PullRequests; got != nil {
			t.Errorf("Ticket %s has PullRequests %+v, want nil", key, got)
		}
	}
}

// Index 0 is the Provider's contract with renderers showing one pull request
// per row, and it follows the predecessor's rule: merged if the work landed,
// otherwise the newest open or draft one.
func TestTheLeadPullRequestComesFirst(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	byKey := ticketsByKey(t, snap)

	tests := map[string][]int{
		// A closed attempt, the merged one that landed, and a newer open
		// follow-up: the open one leads — it is what the Ticket's In Progress
		// grouping is reading — and the rest keep GitHub's order.
		"#90": {103, 101, 102},
		// Two open ones: the newest leads.
		"#98": {109, 107},
		// Neither has a rollup; the newest still leads.
		"#100": {112, 111},
	}

	for key, want := range tests {
		t.Run(key, func(t *testing.T) {
			var got []int
			for _, pr := range byKey[key].PullRequests {
				got = append(got, pr.Number)
			}
			if len(got) != len(want) {
				t.Fatalf("Ticket %s pull requests = %v, want %v", key, got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("Ticket %s pull requests = %v, want %v", key, got, want)
				}
			}
		})
	}
}

// The acceptance criterion made executable: GitHub has no in-progress state, so
// an open Ticket with an open or draft pull request is what "the agent is
// working on it right now" looks like in the progress arithmetic.
func TestOpenPullRequestsMakeTicketsInProgress(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	progress := model.ComputeProgress(snap.Tickets)
	if progress.InProgress != 8 {
		t.Errorf("Progress.InProgress = %d, want 8", progress.InProgress)
	}
	if progress.Todo != 3 {
		t.Errorf("Progress.Todo = %d, want 3: a merged, closed or unknown pull request promotes nothing", progress.Todo)
	}
	if progress.Cancelled != 2 || progress.Denominator != 15 {
		t.Errorf("Progress = %+v, want cancelled work still outside the denominator", progress)
	}

	// Native Status means the Tracker's own label. sitrep does not invent
	// "in review" and put it in a field defined as GitHub's wording.
	byKey := ticketsByKey(t, snap)
	for _, key := range []string{"#5", "#6", "#90"} {
		if got := byKey[key].NativeStatus; got != "open" {
			t.Errorf("Ticket %s NativeStatus = %q, want %q", key, got, "open")
		}
	}
}

// ADR-0003: pull request data rides on the same request as the sub-issues. One
// logical fetch per refresh, whatever the epic contains.
func TestPullRequestsRideOnTheEpicQuery(t *testing.T) {
	s := fullEpic(t)

	if _, err := newProvider(s).FetchEpic(context.Background(), epicRef); err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	requests := s.recorded()
	if len(requests) != 2 {
		t.Fatalf("the driver made %d requests, want 2: one per sub-issue page and none per Ticket", len(requests))
	}
	for i, r := range requests {
		if !strings.Contains(r.query, "closedByPullRequestsReferences") {
			t.Errorf("request %d does not ask for pull requests: %q", i, r.query)
		}
	}
}

func TestAssigneesAreMapped(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	byKey := map[string]model.Ticket{}
	for _, ticket := range snap.Tickets {
		byKey[ticket.Key] = ticket
	}

	if got := byKey["#3"].Assignees; got != nil {
		t.Errorf("Assignees of an unassigned Ticket = %+v, want nil", got)
	}
	if got := byKey["acme/widgets#91"].Assignees; len(got) != 1 || got[0].Login != "dana" {
		t.Errorf("Assignees of #91 = %+v, want one user dana", got)
	}

	three := byKey["#90"].Assignees
	if len(three) != 3 {
		t.Fatalf("Assignees of #90 = %+v, want three", three)
	}
	want := []model.User{
		{Login: "alice", DisplayName: "Alice Ansel", AvatarURL: "https://avatars.githubusercontent.com/u/1?v=4"},
		{Login: "bob", DisplayName: "", AvatarURL: "https://avatars.githubusercontent.com/u/2?v=4"},
		{Login: "carol", DisplayName: "Carol Chen", AvatarURL: "https://avatars.githubusercontent.com/u/3?v=4"},
	}
	for i, w := range want {
		if three[i] != w {
			t.Errorf("assignee %d = %+v, want %+v", i, three[i], w)
		}
	}
}

func TestPaginationFollowsTheCursor(t *testing.T) {
	s := fullEpic(t)

	if _, err := newProvider(s).FetchEpic(context.Background(), epicRef); err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	requests := s.recorded()
	if len(requests) != 2 {
		t.Fatalf("the driver made %d requests, want 2: one per sub-issue page", len(requests))
	}
	if got, ok := requests[0].variables["cursor"]; ok {
		t.Errorf("the first request carried cursor %v, want none", got)
	}
	if got := requests[1].variables["cursor"]; got != "Y3Vyc29yOjEwMA==" {
		t.Errorf("the second request carried cursor %v, want page 1's endCursor", got)
	}
	for i, r := range requests {
		if got := r.variables["owner"]; got != "niekcandaele" {
			t.Errorf("request %d owner = %v, want niekcandaele", i, got)
		}
		if got := r.variables["number"]; got != float64(2) {
			t.Errorf("request %d number = %v, want 2", i, got)
		}
	}
}

func TestEveryRequestCarriesTheHeadersGitHubNeeds(t *testing.T) {
	s := fullEpic(t)

	if _, err := newProvider(s).FetchEpic(context.Background(), epicRef); err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	requests := s.recorded()
	if len(requests) != 2 {
		t.Fatalf("the driver made %d requests, want 2", len(requests))
	}
	for i, r := range requests {
		if got, want := r.headers.Get("Authorization"), "bearer "+fixtureToken; got != want {
			t.Errorf("request %d Authorization = %q, want %q", i, got, want)
		}
		// Without this header some endpoints answer with an error about an
		// unknown field subIssues, which reads like a query bug and is not one.
		if got := r.headers.Get("GraphQL-Features"); got != "sub_issues" {
			t.Errorf("request %d GraphQL-Features = %q, want sub_issues", i, got)
		}
		if got := r.headers.Get("User-Agent"); got != "sitrep/test" {
			t.Errorf("request %d User-Agent = %q, want sitrep/test", i, got)
		}
		if got := r.headers.Get("Content-Type"); got != "application/json" {
			t.Errorf("request %d Content-Type = %q, want application/json", i, got)
		}
	}
}

// Read-only by design (ADR-0002): every document sitrep sends is a query.
func TestTheDocumentSentIsAlwaysAQuery(t *testing.T) {
	s := fullEpic(t)

	if _, err := newProvider(s).FetchEpic(context.Background(), epicRef); err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	for i, r := range s.recorded() {
		if !strings.HasPrefix(strings.TrimSpace(r.query), "query") {
			t.Errorf("request %d sent a document that is not a query: %q", i, r.query)
		}
		if strings.Contains(r.query, "mutation") {
			t.Errorf("request %d sent a mutation: %q", i, r.query)
		}
	}
}

// An issue with no sub-issues is not an error: it is a Ticket someone pointed
// sitrep at, and the ticket decoder needs it to come back cleanly.
func TestEmptyEpic(t *testing.T) {
	p := newProvider(newReplayServer(t, response{file: "epic_empty.json"}))

	snap, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	if snap.Tickets == nil {
		t.Error("Tickets is nil, want an empty slice")
	}
	if len(snap.Tickets) != 0 {
		t.Errorf("Tickets = %+v, want none", snap.Tickets)
	}
}

// epicFailure is one replayed failure and the error contract it must satisfy.
type epicFailure struct {
	name     string
	response response
	want     providertest.Want
}

// epicFailures is the driver's failure table, hoisted out of the test that
// iterates it so that TestFetchEpicFailuresCoverTheNamedClasses can assert what
// it covers rather than trusting a comment.
func epicFailures() []epicFailure {
	return []epicFailure{
		{
			name:     "bad token",
			response: response{status: http.StatusUnauthorized, body: `{"message":"Bad credentials"}`},
			want: providertest.Want{
				Kind:     provider.KindAuth,
				Contains: []string{"authentication failed (401)", "gh auth status", "GITHUB_TOKEN"},
				Secret:   fixtureToken,
			},
		},
		{
			// GitHub spends 403 on SAML SSO enforcement too, and the header is
			// the only thing that says so.
			name: "403 from SAML SSO enforcement",
			response: response{
				status:  http.StatusForbidden,
				body:    `{"message":"Resource protected by organization SAML enforcement"}`,
				headers: map[string]string{"x-github-sso": "required; url=https://github.com/orgs/acme/sso"},
			},
			want: providertest.Want{
				Kind:     provider.KindAuth,
				Contains: []string{"access denied (403)", "SAML SSO", "https://github.com/orgs/acme/sso"},
				Secret:   fixtureToken,
			},
		},
		{
			name:     "403 from a token missing a scope",
			response: response{status: http.StatusForbidden, body: `{"message":"Forbidden"}`},
			want: providertest.Want{
				Kind:     provider.KindAuth,
				Contains: []string{"access denied (403)", "scopes", "gh auth refresh"},
				Secret:   fixtureToken,
			},
		},
		{
			name: "the primary rate limit is exhausted",
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
			// A secondary limit is a burst guard: 403 or 429 plus a retry-after,
			// and no x-ratelimit-remaining at all.
			name: "a secondary rate limit with a retry-after",
			response: response{
				status:  http.StatusForbidden,
				body:    `{"message":"You have exceeded a secondary rate limit"}`,
				headers: map[string]string{"retry-after": "30"},
			},
			want: providertest.Want{
				Kind:     provider.KindRateLimit,
				Contains: []string{"secondary rate limit", "retry after 30s"},
				Secret:   fixtureToken,
			},
		},
		{
			name:     "a 429 with nothing to say",
			response: response{status: http.StatusTooManyRequests, body: `{"message":"Too many requests"}`},
			want: providertest.Want{
				Kind:     provider.KindRateLimit,
				Contains: []string{"secondary rate limit", "an unknown time"},
				Secret:   fixtureToken,
			},
		},
		{
			// The GraphQL point budget is reported on a *200*, so only the
			// errors[] entry and the headers give it away.
			name: "the GraphQL point budget is exhausted",
			response: response{
				file:    "errors_rate_limited.json",
				headers: map[string]string{"x-ratelimit-reset": "1767225600"},
			},
			want: providertest.Want{
				Kind:     provider.KindRateLimit,
				Contains: []string{"rate limit exceeded", "resets at"},
				Secret:   fixtureToken,
			},
		},
		{
			name:     "issue missing",
			response: response{file: "issue_null.json"},
			want: providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{"niekcandaele/sitrep#2", "not found"},
				Secret:   fixtureToken,
			},
		},
		{
			// GitHub shares one number namespace between issues and pull
			// requests, so pasting a pull request link is a common mistake —
			// and "not found" is a false claim about a page the user is looking
			// at.
			name:     "the number names a pull request",
			response: response{file: "issue_is_a_pull_request.json"},
			want: providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{"niekcandaele/sitrep#2", "is a pull request, not a Ticket"},
				Secret:   fixtureToken,
			},
		},
		{
			name:     "repository missing, reported as a GraphQL error",
			response: response{file: "errors_not_found.json"},
			want: providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{"niekcandaele/sitrep#2", "not found"},
				Secret:   fixtureToken,
			},
		},
		{
			name:     "other GraphQL errors",
			response: response{file: "errors_query.json"},
			want: providertest.Want{
				// FORBIDDEN entries keep GitHub's own joined wording; only the
				// classification is added, so the monitor does not poll a scope
				// problem forever.
				Kind:     provider.KindAuth,
				Contains: []string{"API error", "Resource not accessible by integration", ";"},
				Secret:   fixtureToken,
			},
		},
		{
			name:     "server error",
			response: response{status: http.StatusInternalServerError, body: `{"message":"oops"}`},
			want: providertest.Want{
				Kind:     provider.KindUnavailable,
				Contains: []string{"unexpected response 500"},
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
}

func TestFetchEpicErrors(t *testing.T) {
	for _, tt := range epicFailures() {
		t.Run(tt.name, func(t *testing.T) {
			p := newProvider(newReplayServer(t, tt.response))

			snap, err := p.FetchEpic(context.Background(), epicRef)
			if err == nil {
				t.Fatalf("FetchEpic = %+v, want an error", snap)
			}
			providertest.CheckError(t, "github", err, tt.want)
		})
	}
}

// The ticket's promise is per-driver: a bad ref, an auth failure and rate
// limiting each explain themselves on this Tracker. This asserts the table
// above actually exercises all three, so deleting the only rate-limit row is
// loud rather than quiet.
func TestFetchEpicFailuresCoverTheNamedClasses(t *testing.T) {
	kinds := []provider.Kind{}
	for _, tt := range epicFailures() {
		kinds = append(kinds, tt.want.Kind)
	}
	providertest.CheckCoversTheNamedClasses(t, "github", kinds)
}

// A user with no token gets one line naming both ways to fix it, and sitrep
// does not waste a request finding out.
func TestFetchEpicWithoutAToken(t *testing.T) {
	sources := map[string]github.TokenSource{
		"the source finds nothing": func(context.Context, string) (string, error) {
			return "", nil
		},
		"the source fails": func(context.Context, string) (string, error) {
			return "", errors.New(`no GitHub token found: run "gh auth login" or set GITHUB_TOKEN`)
		},
	}

	for name, source := range sources {
		t.Run(name, func(t *testing.T) {
			s := fullEpic(t)
			p := github.New("github.com", github.WithEndpoint(s.URL), github.WithTokenSource(source))

			_, err := p.FetchEpic(context.Background(), epicRef)
			providertest.CheckError(t, "github", err, providertest.Want{
				Kind:     provider.KindAuth,
				Contains: []string{"gh auth login", "GITHUB_TOKEN"},
			})
			if n := len(s.recorded()); n != 0 {
				t.Errorf("the driver made %d requests without a token, want none", n)
			}
		})
	}
}

// The token is resolved once per Provider: a polled hot path must not fork a gh
// subprocess on every refresh.
func TestTheTokenIsResolvedOnce(t *testing.T) {
	var calls int
	s := newReplayServer(t,
		response{file: "epic_empty.json"},
		response{file: "epic_empty.json"},
	)
	p := github.New("github.com",
		github.WithEndpoint(s.URL),
		github.WithTokenSource(func(context.Context, string) (string, error) {
			calls++
			return fixtureToken, nil
		}),
	)

	for i := range 2 {
		if _, err := p.FetchEpic(context.Background(), epicRef); err != nil {
			t.Fatalf("FetchEpic %d: %v", i, err)
		}
	}
	if calls != 1 {
		t.Errorf("the token source was consulted %d times, want 1", calls)
	}
}

func TestFetchEpicRejectsANonGitHubRef(t *testing.T) {
	p := newProvider(fullEpic(t))

	_, err := p.FetchEpic(context.Background(), ref.Ref{Tracker: ref.TrackerGitLab, Raw: "https://gitlab.com/a/b/-/issues/1"})
	if err == nil {
		t.Fatal("FetchEpic accepted a GitLab Ref, want an error")
	}
	// A Ref this driver cannot serve is a bad ref, and sitrep says so before it
	// spends a request finding out.
	providertest.CheckError(t, "github", err, providertest.Want{
		Kind:     provider.KindBadRef,
		Contains: []string{"not a GitHub Epic Ref", "https://gitlab.com/a/b/-/issues/1"},
	})
}

func TestFetchEpicHonoursContextCancellation(t *testing.T) {
	blocked := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-blocked
	}))
	defer func() {
		close(blocked)
		s.Close()
	}()

	p := github.New("github.com",
		github.WithEndpoint(s.URL),
		github.WithTokenSource(func(context.Context, string) (string, error) { return fixtureToken, nil }),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.FetchEpic(ctx, epicRef)
		done <- err
	}()
	cancel()

	if err := <-done; err == nil {
		t.Fatal("FetchEpic returned no error after its context was cancelled")
	}
}
