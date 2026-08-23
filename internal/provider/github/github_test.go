package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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

// epicRef is the Ref the fixtures were recorded for.
var epicRef = ref.Ref{
	Tracker: ref.TrackerGitHub,
	Host:    "github.com",
	Owner:   "niekcandaele",
	Repo:    "sitrep",
	Number:  2,
	Raw:     "2",
}

var refListRefs = []ref.Ref{
	{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "niekcandaele", Repo: "sitrep", Number: 5, Raw: "niekcandaele/sitrep#5"},
	{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "widgets", Number: 91, Raw: "acme/widgets#91"},
	{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "niekcandaele", Repo: "sitrep", Number: 3, Raw: "niekcandaele/sitrep#3"},
	{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "widgets", Number: 7, Raw: "acme/widgets#7"},
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// fullEpic serves the two-page fixture epic.
func fullEpic(t *testing.T) *replayServer {
	t.Helper()
	return newReplayServer(t,
		response{file: "epic_page1.json"},
		response{file: "epic_page2.json"},
	)
}

func queryMembershipPage(nodes string, hasNext bool, cursor string) response {
	return response{body: fmt.Sprintf(
		`{"data":{"search":{"pageInfo":{"hasNextPage":%t,"endCursor":%q},"nodes":[%s]}}}`,
		hasNext, cursor, nodes)}
}

const (
	queryIssue5  = `{"__typename":"Issue","number":5,"repository":{"nameWithOwner":"niekcandaele/sitrep"}}`
	queryIssue91 = `{"__typename":"Issue","number":91,"repository":{"nameWithOwner":"acme/widgets"}}`
	queryIssue3  = `{"__typename":"Issue","number":3,"repository":{"nameWithOwner":"niekcandaele/sitrep"}}`
	queryPR19    = `{"__typename":"PullRequest","number":19,"repository":{"nameWithOwner":"niekcandaele/sitrep"}}`
)

func ticketKeys(tickets []model.Ticket) []string {
	keys := make([]string, len(tickets))
	for i := range tickets {
		keys[i] = tickets[i].Key
	}
	return keys
}

func TestName(t *testing.T) {
	if p := github.New("github.com"); p.Name() != "github" {
		t.Errorf("Name() = %q, want %q", p.Name(), "github")
	}
}

func TestResolveNormalizesTheEpic(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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
	if got, wantHeader := snap.Header, provider.EpicHeader(want); got != wantHeader {
		t.Errorf("Header = %+v, want %+v", got, wantHeader)
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

func TestResolveQuerySearchesMembershipThenReadsExactTickets(t *testing.T) {
	const query = `  is:issue label:"agent ready" & σ  `
	s := newReplayServer(t,
		response{file: "query_membership.json"},
		response{file: "ref_list.json"},
	)
	p := newProvider(s)

	snap, err := p.Resolve(context.Background(), provider.QuerySelector{Query: query})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !reflect.DeepEqual(snap.Epic, model.Epic{}) || !snap.Parent.IsZero() {
		t.Errorf("Query snapshot has an outer Epic/Parent: %+v / %+v", snap.Epic, snap.Parent)
	}
	if snap.Header != provider.QueryHeader(query) {
		t.Errorf("Header = %+v, want exact Query", snap.Header)
	}
	if !snap.FetchedAt.IsZero() || snap.Capabilities != p.Capabilities() {
		t.Errorf("FetchedAt/Capabilities = %v/%+v", snap.FetchedAt, snap.Capabilities)
	}
	wantKeys := []string{"niekcandaele/sitrep#5", "acme/widgets#91", "niekcandaele/sitrep#3"}
	if len(snap.Tickets) != len(wantKeys) {
		t.Fatalf("Tickets = %d, want %d", len(snap.Tickets), len(wantKeys))
	}
	for i, want := range wantKeys {
		if snap.Tickets[i].Key != want {
			t.Errorf("Tickets[%d].Key = %q, want %q", i, snap.Tickets[i].Key, want)
		}
	}
	if got := snap.Tickets[0].Title; got != "GitHub Provider: epic fetch, Epic Ref grammar, auth" {
		t.Errorf("first Ticket title = %q, want exact-root state rather than stale search state", got)
	}

	requests := s.recorded()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want one membership search and one exact-root read", len(requests))
	}
	membership := requests[0]
	if got := membership.variables["query"]; got != query {
		t.Errorf("membership query variable = %q, want exact %q", got, query)
	}
	if got := membership.variables["first"]; got != float64(100) {
		t.Errorf("membership first variable = %v, want 100", got)
	}
	if got := membership.variables["after"]; got != nil {
		t.Errorf("membership after variable = %v, want null on page one", got)
	}
	for _, token := range []string{"search(query:$query", "type:ISSUE", "first:$first", "after:$after", "issueCount", "pageInfo", "__typename", "number", "nameWithOwner"} {
		if !strings.Contains(membership.query, token) {
			t.Errorf("membership document omits %q: %s", token, membership.query)
		}
	}
	for _, forbidden := range []string{"title", "url", "state", "assignees", "parent", "subIssues", "comments", "body"} {
		if strings.Contains(membership.query, forbidden) {
			t.Errorf("membership document includes non-identity or paging field %q: %s", forbidden, membership.query)
		}
	}
	exact := requests[1]
	wantVariables := map[string]any{
		"owner0": "niekcandaele", "repo0": "sitrep", "number0": float64(5),
		"owner1": "acme", "repo1": "widgets", "number1": float64(91),
		"owner2": "niekcandaele", "repo2": "sitrep", "number2": float64(3),
	}
	if !reflect.DeepEqual(exact.variables, wantVariables) {
		t.Errorf("exact-root variables = %#v, want %#v", exact.variables, wantVariables)
	}
	for _, forbidden := range []string{"subIssues", "body", "comments", "blockedBy", "blocking"} {
		if strings.Contains(exact.query, forbidden) {
			t.Errorf("exact-root query contains Detail/Epic field %q: %s", forbidden, exact.query)
		}
	}
}

func TestResolveQueryPaginatesBeforeAuthoritativeRead(t *testing.T) {
	const query = `  repo:acme/widgets label:"ready & waiting"  `
	s := newReplayServer(t,
		queryMembershipPage(strings.Join([]string{queryIssue5, queryPR19, queryIssue5}, ","), true, "page-two"),
		queryMembershipPage(strings.Join([]string{queryIssue91, queryIssue3}, ","), false, ""),
		response{file: "ref_list.json"},
	)
	p := newProvider(s, github.WithMaxTickets(5))

	snap, err := p.Resolve(context.Background(), provider.QuerySelector{Query: query})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.LimitReached {
		t.Error("LimitReached = true at an exhausted exact boundary")
	}
	wantKeys := []string{"niekcandaele/sitrep#5", "acme/widgets#91", "niekcandaele/sitrep#3"}
	if got := ticketKeys(snap.Tickets); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("Ticket keys = %v, want %v", got, wantKeys)
	}

	requests := s.recorded()
	if len(requests) != 3 {
		t.Fatalf("requests = %d, want two membership pages then one exact read", len(requests))
	}
	for i, want := range []struct {
		first float64
		after any
	}{{5, nil}, {2, "page-two"}} {
		if got := requests[i].variables["query"]; got != query {
			t.Errorf("page %d query = %q, want exact %q", i+1, got, query)
		}
		if got := requests[i].variables["first"]; got != want.first {
			t.Errorf("page %d first = %v, want %v", i+1, got, want.first)
		}
		if got := requests[i].variables["after"]; got != want.after {
			t.Errorf("page %d after = %v, want %v", i+1, got, want.after)
		}
		if !strings.Contains(requests[i].query, "search(query:$query") {
			t.Errorf("request %d is not the membership document", i+1)
		}
	}
	if strings.Contains(requests[2].query, "search(query:$query") {
		t.Error("authoritative exact-root read happened before membership completed")
	}
}

func TestResolveQueryCutoffAccounting(t *testing.T) {
	tests := []struct {
		name       string
		nodes      string
		hasNext    bool
		maxTickets int
		wantKeys   []string
		wantLimit  bool
	}{
		{
			name:       "exact boundary exhausted",
			nodes:      queryIssue5 + "," + queryIssue91,
			maxTickets: 2,
			wantKeys:   []string{"niekcandaele/sitrep#5", "acme/widgets#91"},
		},
		{
			name:       "exact boundary with continuation",
			nodes:      queryIssue5 + "," + queryIssue91,
			hasNext:    true,
			maxTickets: 2,
			wantKeys:   []string{"niekcandaele/sitrep#5", "acme/widgets#91"},
			wantLimit:  true,
		},
		{
			name:       "oversized response is clipped",
			nodes:      queryIssue5 + "," + queryIssue91 + "," + queryIssue3,
			maxTickets: 2,
			wantKeys:   []string{"niekcandaele/sitrep#5", "acme/widgets#91"},
			wantLimit:  true,
		},
		{
			name:       "pull request consumes native budget before filtering",
			nodes:      queryPR19 + "," + queryIssue5,
			hasNext:    true,
			maxTickets: 2,
			wantKeys:   []string{"niekcandaele/sitrep#5"},
			wantLimit:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t,
				queryMembershipPage(tt.nodes, tt.hasNext, ""),
				response{file: "ref_list.json"},
			)
			snap, err := newProvider(s, github.WithMaxTickets(tt.maxTickets)).Resolve(
				context.Background(), provider.QuerySelector{Query: "q"})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if snap.LimitReached != tt.wantLimit {
				t.Errorf("LimitReached = %t, want %t", snap.LimitReached, tt.wantLimit)
			}
			if got := ticketKeys(snap.Tickets); !reflect.DeepEqual(got, tt.wantKeys) {
				t.Errorf("Ticket keys = %v, want %v", got, tt.wantKeys)
			}
		})
	}
}

func TestResolveQueryReportsGitHubSearchResultCeiling(t *testing.T) {
	const exposedResults = 1000
	tests := []struct {
		name          string
		reportedCount int
		wantLimit     bool
	}{
		{name: "more matches exist", reportedCount: 1001, wantLimit: true},
		{name: "exact ceiling is exhausted", reportedCount: 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			membershipRequests := 0
			exactRequests := 0

			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Query     string         `json:"query"`
					Variables map[string]any `json:"variables"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")

				if strings.Contains(request.Query, "search(query:$query") {
					first, ok := request.Variables["first"].(float64)
					if !ok || first != 100 {
						t.Errorf("membership first = %v, want 100", request.Variables["first"])
					}
					offset := 0
					if after := request.Variables["after"]; after != nil {
						cursor, ok := after.(string)
						if !ok {
							t.Errorf("membership cursor = %T, want string", after)
						} else {
							var err error
							offset, err = strconv.Atoi(cursor)
							if err != nil {
								t.Errorf("membership cursor = %q: %v", cursor, err)
							}
						}
					}
					if offset >= exposedResults {
						t.Errorf("membership request started beyond GitHub's search ceiling at %d", offset)
					}

					next := min(offset+int(first), exposedResults)
					nodes := make([]map[string]any, 0, next-offset)
					for number := offset + 1; number <= next; number++ {
						nodes = append(nodes, map[string]any{
							"__typename": "Issue",
							"number":     number,
							"repository": map[string]any{"nameWithOwner": "acme/widgets"},
						})
					}
					hasNext := next < exposedResults
					endCursor := ""
					if hasNext {
						endCursor = strconv.Itoa(next)
					}
					mu.Lock()
					membershipRequests++
					mu.Unlock()
					_ = json.NewEncoder(w).Encode(map[string]any{
						"data": map[string]any{
							"search": map[string]any{
								"issueCount": tt.reportedCount,
								"pageInfo": map[string]any{
									"hasNextPage": hasNext,
									"endCursor":   endCursor,
								},
								"nodes": nodes,
							},
						},
					})
					return
				}

				data := make(map[string]any)
				for i := 0; ; i++ {
					suffix := strconv.Itoa(i)
					rawNumber, ok := request.Variables["number"+suffix]
					if !ok {
						break
					}
					number := int(rawNumber.(float64))
					data["ref"+suffix] = map[string]any{
						"kind": map[string]any{"__typename": "Issue"},
						"issue": map[string]any{
							"id":         "issue-" + strconv.Itoa(number),
							"number":     number,
							"title":      "Exact " + strconv.Itoa(number),
							"url":        "https://github.com/acme/widgets/issues/" + strconv.Itoa(number),
							"state":      "OPEN",
							"repository": map[string]any{"nameWithOwner": "acme/widgets"},
						},
					}
				}
				mu.Lock()
				exactRequests++
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			}))
			t.Cleanup(s.Close)

			p := github.New("github.com",
				github.WithEndpoint(s.URL),
				github.WithTokenSource(func(context.Context, string) (string, error) {
					return fixtureToken, nil
				}),
				github.WithMaxTickets(1200),
			)
			snap, err := p.Resolve(context.Background(), provider.QuerySelector{Query: "is:issue"})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if snap.LimitReached != tt.wantLimit {
				t.Errorf("LimitReached = %t, want %t", snap.LimitReached, tt.wantLimit)
			}
			if len(snap.Tickets) != exposedResults {
				t.Fatalf("Tickets = %d, want %d", len(snap.Tickets), exposedResults)
			}
			if snap.Tickets[0].Key != "acme/widgets#1" ||
				snap.Tickets[exposedResults-1].Key != "acme/widgets#1000" {
				t.Errorf("Ticket boundary = %q ... %q, want #1 ... #1000",
					snap.Tickets[0].Key, snap.Tickets[exposedResults-1].Key)
			}
			if snap.Tickets[0].Title != "Exact 1" {
				t.Errorf("first Ticket title = %q, want authoritative exact-root state", snap.Tickets[0].Title)
			}

			mu.Lock()
			gotMembershipRequests := membershipRequests
			gotExactRequests := exactRequests
			mu.Unlock()
			if gotMembershipRequests != 10 {
				t.Errorf("membership requests = %d, want 10 pages and no request beyond the ceiling", gotMembershipRequests)
			}
			if gotExactRequests != 10 {
				t.Errorf("exact-root requests = %d, want 10 bounded alias batches", gotExactRequests)
			}
		})
	}
}

func TestResolveQueryRejectsNonProgressingPagination(t *testing.T) {
	tests := []struct {
		name      string
		responses []response
	}{
		{
			name: "missing first continuation cursor",
			responses: []response{
				queryMembershipPage(queryIssue5, true, ""),
			},
		},
		{
			name: "repeated continuation cursor",
			responses: []response{
				queryMembershipPage(queryIssue5, true, "same"),
				queryMembershipPage(queryIssue91, true, "same"),
			},
		},
		{
			name: "non-adjacent cursor cycle",
			responses: []response{
				queryMembershipPage(queryIssue5, true, "a"),
				queryMembershipPage(queryIssue91, true, "b"),
				queryMembershipPage(queryIssue3, true, "a"),
			},
		},
		{
			name: "empty continuation page",
			responses: []response{
				queryMembershipPage(queryIssue5, true, "next"),
				queryMembershipPage("", false, ""),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, tt.responses...)
			snap, err := newProvider(s, github.WithMaxTickets(4)).Resolve(
				context.Background(), provider.QuerySelector{Query: "q"})
			providertest.CheckError(t, "github", err, providertest.Want{
				Kind:     provider.KindUnavailable,
				Contains: []string{"query"},
				Secret:   fixtureToken,
			})
			if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
				t.Errorf("snapshot = %+v, want no partial membership", snap)
			}
			if got := len(s.recorded()); got != len(tt.responses) {
				t.Errorf("requests = %d, want %d membership-only requests", got, len(tt.responses))
			}
		})
	}
}

func TestResolveQueryPageTwoFailureReturnsNoPartialSnapshot(t *testing.T) {
	tests := []struct {
		name string
		resp response
		want providertest.Want
	}{
		{
			name: "auth",
			resp: response{status: http.StatusUnauthorized, body: `{"message":"Bad credentials"}`},
			want: providertest.Want{Kind: provider.KindAuth, Contains: []string{"authentication failed"}, Secret: fixtureToken},
		},
		{
			name: "rate limit",
			resp: response{status: http.StatusTooManyRequests, body: `{"message":"slow down"}`},
			want: providertest.Want{Kind: provider.KindRateLimit, Contains: []string{"rate limit"}, Secret: fixtureToken},
		},
		{
			name: "server failure",
			resp: response{status: http.StatusInternalServerError, body: `{"message":"down"}`},
			want: providertest.Want{Kind: provider.KindUnavailable, Contains: []string{"unexpected response", "500"}, Secret: fixtureToken},
		},
		{
			name: "decode",
			resp: response{body: `{"data": {`},
			want: providertest.Want{Kind: provider.KindUnavailable, Contains: []string{"decoding the response"}, Secret: fixtureToken},
		},
		{
			name: "malformed native query",
			resp: response{file: "query_invalid.json"},
			want: providertest.Want{Kind: provider.KindBadRef, Contains: []string{"query rejected"}, Secret: fixtureToken},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t,
				queryMembershipPage(queryIssue5, true, "next"),
				tt.resp,
			)
			snap, err := newProvider(s, github.WithMaxTickets(3)).Resolve(
				context.Background(), provider.QuerySelector{Query: "q"})
			providertest.CheckError(t, "github", err, tt.want)
			if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
				t.Errorf("snapshot = %+v, want no partial membership", snap)
			}
			if got := len(s.recorded()); got != 2 {
				t.Errorf("requests = %d, want two membership requests and no exact read", got)
			}
		})
	}
}

func TestResolveQueryEmptyMembershipNeedsNoExactRead(t *testing.T) {
	s := newReplayServer(t, response{file: "query_empty.json"})
	p := newProvider(s)
	snap, err := p.Resolve(context.Background(), provider.QuerySelector{Query: ""})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.Tickets == nil || len(snap.Tickets) != 0 {
		t.Errorf("Tickets = %#v, want non-nil empty", snap.Tickets)
	}
	if snap.Header != provider.QueryHeader("") {
		t.Errorf("Header = %+v, want explicit empty Query", snap.Header)
	}
	if got := len(s.recorded()); got != 1 {
		t.Errorf("requests = %d, want membership only", got)
	}
}

func TestResolveQueryHugeLimitDoesNotPreallocateTheBudget(t *testing.T) {
	s := newReplayServer(t, response{file: "query_empty.json"})
	p := newProvider(s, github.WithMaxTickets(int(^uint(0)>>1)))
	if _, err := p.Resolve(context.Background(), provider.QuerySelector{Query: "q"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}

func TestResolveQueryRejectsMalformedNativeQuery(t *testing.T) {
	s := newReplayServer(t, response{file: "query_invalid.json"})
	snap, err := newProvider(s).Resolve(context.Background(), provider.QuerySelector{Query: `is:issue "`})
	providertest.CheckError(t, "github", err, providertest.Want{
		Kind:     provider.KindBadRef,
		Contains: []string{"query rejected", "Unclosed quotation mark"},
		Secret:   fixtureToken,
	})
	if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
		t.Errorf("snapshot = %+v, want no partial output", snap)
	}
	if provider.KindOf(err).Retryable() {
		t.Error("a malformed native query must not be retryable")
	}
}

func TestResolveQueryClassifiesMalformedNativeQueryOnHTTP422(t *testing.T) {
	s := newReplayServer(t, response{status: http.StatusUnprocessableEntity, file: "query_invalid.json"})
	snap, err := newProvider(s).Resolve(context.Background(), provider.QuerySelector{Query: `is:issue "`})
	providertest.CheckError(t, "github", err, providertest.Want{
		Kind:     provider.KindBadRef,
		Contains: []string{"query rejected", "Unclosed quotation mark"},
		Secret:   fixtureToken,
	})
	if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
		t.Errorf("snapshot = %+v, want no partial output", snap)
	}
}

func TestResolveQueryRedactsQueryFromNativeExplanation(t *testing.T) {
	const query = "SECRET_QUERY_47"
	body := `{"errors":[{"type":"SEARCH_QUERY_ERROR","message":"Query SECRET_QUERY_47 is invalid: bad syntax","path":["search"]}]}`
	s := newReplayServer(t, response{body: body})

	_, err := newProvider(s).Resolve(context.Background(), provider.QuerySelector{Query: query})
	providertest.CheckError(t, "github", err, providertest.Want{
		Kind:     provider.KindBadRef,
		Contains: []string{"Query [query] is invalid", "bad syntax"},
		Secret:   query,
	})
	if strings.Contains(err.Error(), fixtureToken) {
		t.Errorf("error = %q, leaked credential", err)
	}
}

func TestResolveQueryDoesNotClassifyGraphQLValidationAsMalformedNativeQuery(t *testing.T) {
	body := `{"errors":[{"type":"VALIDATION","message":"Unknown field in sitrep document"}]}`
	s := newReplayServer(t, response{status: http.StatusBadRequest, body: body})
	snap, err := newProvider(s).Resolve(context.Background(), provider.QuerySelector{Query: "valid query"})
	providertest.CheckError(t, "github", err, providertest.Want{
		Kind:     provider.KindUnknown,
		Contains: []string{"API error", "Unknown field in sitrep document"},
		Secret:   fixtureToken,
	})
	if !provider.KindOf(err).Retryable() {
		t.Error("a GraphQL document validation failure must remain retryable")
	}
	if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
		t.Errorf("snapshot = %+v, want no partial output", snap)
	}
}

func TestResolveQueryPreservesMembershipStageFailureClasses(t *testing.T) {
	tests := []struct {
		name string
		resp response
		want providertest.Want
	}{
		{
			name: "auth",
			resp: response{status: http.StatusUnauthorized, body: `{"message":"Bad credentials"}`},
			want: providertest.Want{Kind: provider.KindAuth, Contains: []string{"authentication failed"}, Secret: fixtureToken},
		},
		{
			name: "rate limit",
			resp: response{status: http.StatusTooManyRequests, body: `{"message":"slow down"}`},
			want: providertest.Want{Kind: provider.KindRateLimit, Contains: []string{"rate limit"}, Secret: fixtureToken},
		},
		{
			name: "decode",
			resp: response{body: `{"data": {`},
			want: providertest.Want{Kind: provider.KindUnavailable, Contains: []string{"decoding the response"}, Secret: fixtureToken},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, tt.resp)
			snap, err := newProvider(s).Resolve(context.Background(), provider.QuerySelector{Query: "q"})
			providertest.CheckError(t, "github", err, tt.want)
			if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
				t.Errorf("snapshot = %+v, want none", snap)
			}
			if len(s.recorded()) != 1 {
				t.Errorf("requests = %d, want membership stage only", len(s.recorded()))
			}
		})
	}
}

func TestResolveQueryTransportErrorDoesNotExposeQuery(t *testing.T) {
	const query = "SECRET_QUERY_47"
	transportErr := errors.New("dial failed after reading SECRET_QUERY_47")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})}
	p := github.New("github.com",
		github.WithTokenSource(func(context.Context, string) (string, error) { return fixtureToken, nil }),
		github.WithHTTPClient(client),
	)

	snap, err := p.Resolve(context.Background(), provider.QuerySelector{Query: query})
	providertest.CheckError(t, "github", err, providertest.Want{
		Kind:     provider.KindUnavailable,
		Contains: []string{"transport failure"},
		Secret:   query,
	})
	if !errors.Is(err, transportErr) {
		t.Errorf("errors.Is(%v, transportErr) = false, want true", err)
	}
	if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
		t.Errorf("snapshot = %+v, want none", snap)
	}
}

func TestResolveQueryExactReadFailureReturnsNoPartialSnapshot(t *testing.T) {
	s := newReplayServer(t,
		response{file: "query_membership.json"},
		response{file: "ref_list_missing.json"},
	)
	snap, err := newProvider(s).Resolve(context.Background(), provider.QuerySelector{Query: "q"})
	providertest.CheckError(t, "github", err, providertest.Want{
		Kind:     provider.KindBadRef,
		Contains: []string{"acme/widgets#91", "not found"},
		Secret:   fixtureToken,
	})
	if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
		t.Errorf("snapshot = %+v, want no partial membership", snap)
	}
	if len(s.recorded()) != 2 {
		t.Errorf("requests = %d, want membership plus failed exact-root read", len(s.recorded()))
	}
}

func TestResolveQueryRejectsInvalidSearchIdentity(t *testing.T) {
	s := newReplayServer(t, response{body: `{"data":{"search":{"nodes":[{"__typename":"Issue","number":0,"repository":{"nameWithOwner":"acme/widgets"}}]}}}`})
	snap, err := newProvider(s).Resolve(context.Background(), provider.QuerySelector{Query: "q"})
	providertest.CheckError(t, "github", err, providertest.Want{
		Kind:     provider.KindUnavailable,
		Contains: []string{"invalid identity"},
		Secret:   fixtureToken,
	})
	if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
		t.Errorf("snapshot = %+v, want none", snap)
	}
}

func TestResolveRefListReadsExactTicketsInSelectorOrder(t *testing.T) {
	s := newReplayServer(t, response{file: "ref_list.json"})
	p := newProvider(s)

	snap, err := p.Resolve(context.Background(), provider.RefListSelector{Refs: refListRefs})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if !reflect.DeepEqual(snap.Epic, model.Epic{}) {
		t.Errorf("Epic = %+v, want the zero Epic", snap.Epic)
	}
	if !snap.Parent.IsZero() {
		t.Errorf("Parent = %+v, want the zero Parent", snap.Parent)
	}
	if got, want := snap.Header, provider.RefListHeader(4); got != want {
		t.Errorf("Header = %+v, want %+v", got, want)
	}
	if snap.Tickets == nil {
		t.Fatal("Tickets is nil, want the four explicitly named Tickets")
	}
	if !snap.FetchedAt.IsZero() {
		t.Errorf("FetchedAt = %v, want the zero time", snap.FetchedAt)
	}
	if got, want := snap.Capabilities, p.Capabilities(); got != want {
		t.Errorf("Capabilities = %+v, want %+v", got, want)
	}

	wants := []struct {
		key    string
		status model.StatusCategory
		repo   string
	}{
		{"niekcandaele/sitrep#5", model.StatusInProgress, "niekcandaele/sitrep"},
		{"acme/widgets#91", model.StatusCancelled, "acme/widgets"},
		{"niekcandaele/sitrep#3", model.StatusDone, "niekcandaele/sitrep"},
		{"acme/widgets#7", model.StatusTodo, "acme/widgets"},
	}
	if len(snap.Tickets) != len(wants) {
		t.Fatalf("Tickets = %d, want %d", len(snap.Tickets), len(wants))
	}
	for i, want := range wants {
		got := snap.Tickets[i]
		if got.Key != want.key || got.Status != want.status || got.Repository != want.repo {
			t.Errorf("Ticket %d = {%q %s %q}, want {%q %s %q}",
				i, got.Key, got.Status, got.Repository, want.key, want.status, want.repo)
		}
		if got.ID == "" {
			t.Errorf("Ticket %d has no authoritative node ID", i)
		}
	}
	if got := snap.Tickets[0].PullRequests; len(got) != 1 || got[0].Number != 19 {
		t.Errorf("first Ticket PullRequests = %+v, want recorded pull request #19", got)
	}

	requests := s.recorded()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want one aliased GraphQL POST for four Refs", len(requests))
	}
	request := requests[0]
	if !strings.HasPrefix(strings.TrimSpace(request.query), "query") || strings.Contains(request.query, "mutation") {
		t.Errorf("Ref-list document is not a read-only query: %s", request.query)
	}
	wantVariables := map[string]any{
		"owner0": "niekcandaele", "repo0": "sitrep", "number0": float64(5),
		"owner1": "acme", "repo1": "widgets", "number1": float64(91),
		"owner2": "niekcandaele", "repo2": "sitrep", "number2": float64(3),
		"owner3": "acme", "repo3": "widgets", "number3": float64(7),
	}
	if !reflect.DeepEqual(request.variables, wantVariables) {
		t.Errorf("variables = %#v, want %#v", request.variables, wantVariables)
	}
	for i := range refListRefs {
		suffix := fmt.Sprint(i)
		for _, token := range []string{"ref" + suffix + ": repository", "$owner" + suffix, "$repo" + suffix, "$number" + suffix} {
			if !strings.Contains(request.query, token) {
				t.Errorf("query does not contain %q: %s", token, request.query)
			}
		}
	}
	for _, value := range []string{"niekcandaele", "sitrep", "acme", "widgets"} {
		if strings.Contains(request.query, value) {
			t.Errorf("query interpolated Ref value %q instead of using variables: %s", value, request.query)
		}
	}
	for _, field := range []string{"issueOrPullRequest", "id number title url state stateReason", "repository", "assignees", "closedByPullRequestsReferences", "statusCheckRollup"} {
		if !strings.Contains(request.query, field) {
			t.Errorf("query omits thin Ticket field %q: %s", field, request.query)
		}
	}
	for _, forbidden := range []string{"subIssues", "parent", "body", "comments", "blockedBy", "blocking"} {
		if strings.Contains(request.query, forbidden) {
			t.Errorf("Ref-list query contains %q, want neither Epic expansion nor Detail fields: %s", forbidden, request.query)
		}
	}
}

func TestResolveRefListRejectsAnyBadMemberWithoutPartialOutput(t *testing.T) {
	tests := []struct {
		name     string
		fixture  string
		contains []string
	}{
		{name: "missing repository", fixture: "ref_list_missing.json", contains: []string{"acme/widgets#91", "not found"}},
		{name: "pull request", fixture: "ref_list_pull_request.json", contains: []string{"acme/widgets#91", "is a pull request, not a Ticket"}},
		{name: "GraphQL error path names the member", fixture: "ref_list_errors.json", contains: []string{"acme/widgets#91", "not found"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, response{file: tt.fixture})
			snap, err := newProvider(s).Resolve(context.Background(), provider.RefListSelector{Refs: refListRefs[:2]})
			if err == nil {
				t.Fatalf("Resolve = %+v, want an error", snap)
			}
			providertest.CheckError(t, "github", err, providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: tt.contains,
				Secret:   fixtureToken,
			})
			if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
				t.Errorf("snapshot = %+v, want no partial output", snap)
			}
			if got := len(s.recorded()); got != 1 {
				t.Errorf("requests = %d, want one batch request", got)
			}
		})
	}
}

func TestResolveRefListRejectsInvalidSelectorsBeforeIO(t *testing.T) {
	tests := []struct {
		name     string
		selector provider.Selector
		contains string
	}{
		{name: "empty Ref list", selector: provider.RefListSelector{}, contains: "at least one Ref"},
		{name: "unknown selector", selector: nil, contains: "unsupported Watchlist selector"},
		{name: "pointer selector", selector: &provider.EpicSelector{Ref: epicRef}, contains: "unsupported Watchlist selector"},
		{
			name: "invalid later Ref",
			selector: provider.RefListSelector{Refs: []ref.Ref{
				refListRefs[0],
				{Tracker: ref.TrackerGitLab, Raw: "https://gitlab.com/acme/widgets/-/issues/9"},
			}},
			contains: "not a GitHub Ticket Ref",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t)
			tokenCalls := 0
			p := github.New("github.com",
				github.WithEndpoint(s.URL),
				github.WithTokenSource(func(context.Context, string) (string, error) {
					tokenCalls++
					return fixtureToken, nil
				}),
			)

			_, err := p.Resolve(context.Background(), tt.selector)
			providertest.CheckError(t, "github", err, providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{tt.contains},
			})
			if tokenCalls != 0 {
				t.Errorf("token source calls = %d, want no I/O", tokenCalls)
			}
			if got := len(s.recorded()); got != 0 {
				t.Errorf("requests = %d, want no I/O", got)
			}
		})
	}
}

func TestResolveRefListChunksAtTheAliasBoundAndPreservesGlobalOrder(t *testing.T) {
	var (
		mu         sync.Mutex
		chunkSizes []int
	)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		count := len(body.Variables) / 3
		mu.Lock()
		chunkSizes = append(chunkSizes, count)
		mu.Unlock()

		data := make(map[string]any, count)
		for i := range count {
			suffix := fmt.Sprint(i)
			number := int(body.Variables["number"+suffix].(float64))
			data["ref"+suffix] = map[string]any{
				"kind": map[string]any{"__typename": "Issue"},
				"issue": map[string]any{
					"id":     fmt.Sprintf("issue-%d", number),
					"number": number,
					"title":  fmt.Sprintf("Issue %d", number),
					"url":    fmt.Sprintf("https://github.com/acme/widgets/issues/%d", number),
					"state":  "OPEN",
					"repository": map[string]any{
						"nameWithOwner": "acme/widgets",
					},
					"assignees": map[string]any{"nodes": []any{}},
					"closedByPullRequestsReferences": map[string]any{
						"totalCount": 0,
						"nodes":      []any{},
					},
				},
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"data": data}); err != nil {
			t.Errorf("encoding response: %v", err)
		}
	}))
	t.Cleanup(s.Close)

	refs := make([]ref.Ref, 101)
	for i := range refs {
		refs[i] = ref.Ref{
			Tracker: ref.TrackerGitHub,
			Host:    "github.com",
			Owner:   "acme",
			Repo:    "widgets",
			Number:  i + 1,
			Raw:     fmt.Sprintf("acme/widgets#%d", i+1),
		}
	}
	p := github.New("github.com",
		github.WithEndpoint(s.URL),
		github.WithTokenSource(func(context.Context, string) (string, error) { return fixtureToken, nil }))

	snap, err := p.Resolve(context.Background(), provider.RefListSelector{Refs: refs})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(snap.Tickets) != len(refs) {
		t.Fatalf("Tickets = %d, want %d", len(snap.Tickets), len(refs))
	}
	for i, ticket := range snap.Tickets {
		if want := fmt.Sprintf("acme/widgets#%d", i+1); ticket.Key != want {
			t.Errorf("Tickets[%d].Key = %q, want %q", i, ticket.Key, want)
		}
	}
	mu.Lock()
	gotSizes := append([]int(nil), chunkSizes...)
	mu.Unlock()
	if want := []int{100, 1}; !reflect.DeepEqual(gotSizes, want) {
		t.Errorf("chunk sizes = %v, want %v", gotSizes, want)
	}
}

func TestResolveRefListLaterChunkFailureReturnsNoPartialSnapshot(t *testing.T) {
	var calls int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 2 {
			_, _ = w.Write([]byte(`{"data":{"ref0":null},"errors":[{"type":"NOT_FOUND","message":"not found","path":["ref0"]}]}`))
			return
		}

		var body struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request: %v", err)
			return
		}
		data := make(map[string]any, len(body.Variables)/3)
		for i := range len(body.Variables) / 3 {
			suffix := fmt.Sprint(i)
			number := int(body.Variables["number"+suffix].(float64))
			data["ref"+suffix] = map[string]any{
				"kind": map[string]any{"__typename": "Issue"},
				"issue": map[string]any{
					"id": fmt.Sprintf("issue-%d", number), "number": number, "title": "issue", "url": "https://example.test",
					"state": "OPEN", "repository": map[string]any{"nameWithOwner": "acme/widgets"},
					"assignees":                      map[string]any{"nodes": []any{}},
					"closedByPullRequestsReferences": map[string]any{"totalCount": 0, "nodes": []any{}},
				},
			}
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"data": data}); err != nil {
			t.Errorf("encoding response: %v", err)
		}
	}))
	t.Cleanup(s.Close)

	refs := make([]ref.Ref, 101)
	for i := range refs {
		refs[i] = ref.Ref{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "widgets", Number: i + 1}
	}
	p := github.New("github.com",
		github.WithEndpoint(s.URL),
		github.WithTokenSource(func(context.Context, string) (string, error) { return fixtureToken, nil }))

	snap, err := p.Resolve(context.Background(), provider.RefListSelector{Refs: refs})
	providertest.CheckError(t, "github", err, providertest.Want{
		Kind:     provider.KindBadRef,
		Contains: []string{"acme/widgets#101", "not found"},
	})
	if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
		t.Errorf("snapshot = %+v, want no partial first chunk", snap)
	}
	if calls != 2 {
		t.Errorf("requests = %d, want two chunks", calls)
	}
}

func TestResolveNormalizesEveryTicket(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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

	// The display key of a cross-repo child is a working Ref: a human can
	// paste it straight back into sitrep.
	back, err := ref.Parse(context.Background(), found.Key)
	if err != nil {
		t.Fatalf("the cross-repo Key does not parse as a Ref: %v", err)
	}
	if back.Owner != "acme" || back.Repo != "widgets" || back.Number != 91 {
		t.Errorf("Key round-tripped to %+v, want acme/widgets#91", back)
	}
}

// ticketsByKey indexes a fetched epic so a test can name the Ticket it means.
func ticketsByKey(t *testing.T, snap model.WatchlistSnapshot) map[string]model.Ticket {
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

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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

	if _, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: epicRef}); err != nil {
		t.Fatalf("Resolve: %v", err)
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

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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

// Both pagination guards, which the happy path never reaches. The failure mode
// they prevent is an unbounded request loop in a tool that polls, so the error
// text is asserted rather than only the failure.
func TestPaginationSafety(t *testing.T) {
	// endlessPage is one sub-issue page that always promises another. cursor is
	// what it hands back, which is the whole difference between the two cases.
	endlessPage := func(cursor string) string {
		return `{"data":{"repository":{"issue":{
			"id":"I_1","number":2,"title":"Epic","url":"https://github.com/acme/widgets/issues/2",
			"state":"OPEN","repository":{"nameWithOwner":"acme/widgets"},
			"subIssues":{"totalCount":1,"pageInfo":{"hasNextPage":true,"endCursor":"` + cursor + `"},
			"nodes":[]}}}}}`
	}

	t.Run("a repeated cursor is an error, not a loop", func(t *testing.T) {
		s := newReplayServer(t,
			response{body: endlessPage("same")},
			response{body: endlessPage("same")},
		)

		_, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
		if err == nil {
			t.Fatal("Resolve succeeded, want an error about the repeated cursor")
		}
		// A server that reports a next page and hands over no usable cursor is
		// misbehaving, which is what KindUnavailable means — and it stays
		// retryable, on the same terms as a 500.
		providertest.CheckError(t, "github", err, providertest.Want{
			Kind:     provider.KindUnavailable,
			Contains: []string{"no new page cursor"},
			Secret:   fixtureToken,
		})
		if !provider.KindOf(err).Retryable() {
			t.Error("a server fault must stay retryable; the monitor recovers when the server does")
		}
	})

	t.Run("an endless epic stops paging", func(t *testing.T) {
		var mu sync.Mutex
		var pages int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			pages++
			n := pages
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(endlessPage(fmt.Sprintf("cursor-%d", n))))
		}))
		t.Cleanup(srv.Close)

		p := github.New("github.com",
			github.WithEndpoint(srv.URL),
			github.WithTokenSource(func(context.Context, string) (string, error) { return fixtureToken, nil }))

		_, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
		if err == nil {
			t.Fatal("Resolve succeeded, want an error about refusing to keep paging")
		}
		// A collection past the cap is a stable property of the ref, so the
		// monitor prints one line and exits rather than retrying forever.
		providertest.CheckError(t, "github", err, providertest.Want{
			Kind:     provider.KindBadRef,
			Contains: []string{"refusing to keep paging"},
			Secret:   fixtureToken,
		})
		if provider.KindOf(err).Retryable() {
			t.Error("an over-cap collection is as large on the next tick; retrying buys nothing")
		}
		mu.Lock()
		defer mu.Unlock()
		if pages > 60 {
			t.Errorf("the driver made %d requests; the bound is meant to stop it far sooner", pages)
		}
	})
}

func TestPaginationFollowsTheCursor(t *testing.T) {
	s := fullEpic(t)

	if _, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: epicRef}); err != nil {
		t.Fatalf("Resolve: %v", err)
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

	if _, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: epicRef}); err != nil {
		t.Fatalf("Resolve: %v", err)
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

	if _, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: epicRef}); err != nil {
		t.Fatalf("Resolve: %v", err)
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

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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
// iterates it so that TestResolveFailuresCoverTheNamedClasses can assert what
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

func TestResolveErrors(t *testing.T) {
	for _, tt := range epicFailures() {
		t.Run(tt.name, func(t *testing.T) {
			p := newProvider(newReplayServer(t, tt.response))

			snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
			if err == nil {
				t.Fatalf("Resolve = %+v, want an error", snap)
			}
			providertest.CheckError(t, "github", err, tt.want)
		})
	}
}

// The ticket's promise is per-driver: a bad ref, an auth failure and rate
// limiting each explain themselves on this Tracker. This asserts the table
// above actually exercises all three, so deleting the only rate-limit row is
// loud rather than quiet.
func TestResolveFailuresCoverTheNamedClasses(t *testing.T) {
	kinds := []provider.Kind{}
	for _, tt := range epicFailures() {
		kinds = append(kinds, tt.want.Kind)
	}
	providertest.CheckCoversTheNamedClasses(t, "github", kinds)
}

// A user with no token gets one line naming both ways to fix it, and sitrep
// does not waste a request finding out.
func TestResolveWithoutAToken(t *testing.T) {
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

			_, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
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
		if _, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef}); err != nil {
			t.Fatalf("Resolve %d: %v", i, err)
		}
	}
	if calls != 1 {
		t.Errorf("the token source was consulted %d times, want 1", calls)
	}
}

// `gh auth token` reads a keyring that can be locked, so token discovery can
// fail transiently. Caching that failure for the process lifetime means an open
// monitor never recovers, however healthy the machine gets.
func TestATransientTokenFailureIsNotCached(t *testing.T) {
	var calls int
	s := newReplayServer(t, response{file: "epic_empty.json"})
	p := github.New("github.com",
		github.WithEndpoint(s.URL),
		github.WithTokenSource(func(context.Context, string) (string, error) {
			calls++
			if calls == 1 {
				return "", errors.New("the keyring is locked")
			}
			return fixtureToken, nil
		}),
	)

	if _, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef}); err == nil {
		t.Fatal("the first Resolve succeeded, want the token failure")
	}
	if _, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef}); err != nil {
		t.Fatalf("the second Resolve: %v; a transient token failure must not be cached", err)
	}
	if calls != 2 {
		t.Errorf("the token source was called %d times, want 2: the failure must be retried", calls)
	}
}

func TestResolveRejectsANonGitHubRef(t *testing.T) {
	p := newProvider(fullEpic(t))

	_, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: ref.Ref{Tracker: ref.TrackerGitLab, Raw: "https://gitlab.com/a/b/-/issues/1"}})
	if err == nil {
		t.Fatal("Resolve accepted a GitLab Ref, want an error")
	}
	// A Ref this driver cannot serve is a bad ref, and sitrep says so before it
	// spends a request finding out.
	providertest.CheckError(t, "github", err, providertest.Want{
		Kind:     provider.KindBadRef,
		Contains: []string{"not a GitHub Ref", "https://gitlab.com/a/b/-/issues/1"},
	})
}

func TestResolveHonoursContextCancellation(t *testing.T) {
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
		_, err := p.Resolve(ctx, provider.EpicSelector{Ref: epicRef})
		done <- err
	}()
	cancel()

	if err := <-done; err == nil {
		t.Fatal("Resolve returned no error after its context was cancelled")
	}
}
