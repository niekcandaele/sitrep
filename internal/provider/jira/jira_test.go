package jira_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/niekcandaele/sitrep/internal/provider/jira"
	"github.com/niekcandaele/sitrep/internal/provider/providertest"
	"github.com/niekcandaele/sitrep/internal/ref"
	"github.com/niekcandaele/sitrep/internal/render/plain"
)

// The Jira driver must satisfy the interface every other Provider does.
var _ provider.Provider = (*jira.Provider)(nil)

// The credential every replayed request is expected to carry. Both halves are
// obviously fake, so a test asserting that no credential leaks into an error
// message is asserting something visible.
const (
	fixtureEmail = "fixture@example.test"
	fixtureToken = "fixture-token-not-a-real-secret"
)

// fixtureHost is the site the fixtures were written for.
const fixtureHost = "acme.atlassian.net"

// The paths the driver reads, spelled out here so a test asserting "what was
// sent" compares against literals rather than against the driver's own
// constants.
const (
	epicPath      = "/rest/api/2/issue/ABC-1"
	ticketPath    = "/rest/api/2/issue/ABC-12"
	commentsPath  = "/rest/api/2/issue/ABC-12/comment"
	searchPath    = "/rest/api/2/search/jql"
	linkTypePath  = "/rest/api/2/issueLinkType"
	bulkFetchPath = "/rest/api/3/issue/bulkfetch"
)

// epicRef is the Ref the fixtures were written for.
var epicRef = ref.Ref{
	Tracker: ref.TrackerJira,
	Host:    fixtureHost,
	Key:     "ABC-1",
	Raw:     "ABC-1",
}

// refListRefs deliberately differs from Jira's ascending-id response order.
var refListRefs = []ref.Ref{
	{Tracker: ref.TrackerJira, Host: fixtureHost, Key: "DEF-4", Raw: "DEF-4"},
	{Tracker: ref.TrackerJira, Host: fixtureHost, Key: "ABC-7", Raw: "ABC-7"},
	{Tracker: ref.TrackerJira, Host: fixtureHost, Key: "ABC-1", Raw: "ABC-1"},
	{Tracker: ref.TrackerJira, Host: fixtureHost, Key: "ABC-3", Raw: "ABC-3"},
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
// method, the fields and the cursors sitrep sends without reaching inside the
// driver.
type recordedRequest struct {
	method  string
	path    string
	query   map[string][]string
	headers http.Header
	body    []byte
}

// replayServer serves recorded payloads, routed by request path. Jira answers
// several endpoints where GitHub answered one, so this routes rather than
// serving a flat sequence — and a path with no configured response fails the
// test loudly instead of answering 200 with nothing.
//
// Within one path the responses are served in order, and the last one repeats:
// that is what lets a paged fetch be two files and a polled one be the same
// file twice.
type replayServer struct {
	*httptest.Server

	mu        sync.Mutex
	responses map[string][]response
	served    map[string]int
	requests  []recordedRequest
}

func newReplayServer(t *testing.T, responses map[string][]response) *replayServer {
	t.Helper()

	s := &replayServer{responses: responses, served: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		s.mu.Lock()
		s.requests = append(s.requests, recordedRequest{
			method:  r.Method,
			path:    r.URL.Path,
			query:   r.URL.Query(),
			headers: r.Header.Clone(),
			body:    body,
		})
		queue, ok := s.responses[r.URL.Path]
		n := s.served[r.URL.Path]
		s.served[r.URL.Path]++
		s.mu.Unlock()

		if !ok || len(queue) == 0 {
			t.Errorf("the driver requested %s, which this test configured no response for", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		resp := queue[min(n, len(queue)-1)]

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

// requestsTo returns every recorded request for one path.
func (s *replayServer) requestsTo(path string) []recordedRequest {
	var out []recordedRequest
	for _, r := range s.recorded() {
		if r.path == path {
			out = append(out, r)
		}
	}
	return out
}

// newProvider points a real Provider at the replay server with the fixture
// credential, so no test needs a Jira site.
func newProvider(s *replayServer, opts ...jira.Option) *jira.Provider {
	base := []jira.Option{
		jira.WithBaseURL(s.URL),
		jira.WithCredentials(jira.Credentials{Email: fixtureEmail, Token: fixtureToken}),
		jira.WithUserAgent("sitrep/test"),
	}
	return jira.New(fixtureHost, append(base, opts...)...)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// fullEpic serves the two-page fixture epic.
func fullEpic(t *testing.T) *replayServer {
	t.Helper()
	return newReplayServer(t, map[string][]response{
		epicPath:   {{file: "epic_issue.json"}},
		searchPath: {{file: "epic_children_page1.json"}, {file: "epic_children_page2.json"}},
	})
}

func jiraQueryPage(keys []string, isLast bool, token string) response {
	issues := make([]string, len(keys))
	for i, key := range keys {
		issues[i] = fmt.Sprintf(`{"key":%q}`, key)
	}
	return response{body: fmt.Sprintf(
		`{"issues":[%s],"isLast":%t,"nextPageToken":%q}`,
		strings.Join(issues, ","), isLast, token)}
}

func ticketKeys(tickets []model.Ticket) []string {
	keys := make([]string, len(tickets))
	for i := range tickets {
		keys[i] = tickets[i].Key
	}
	return keys
}

func TestName(t *testing.T) {
	if p := jira.New(fixtureHost); p.Name() != "jira" {
		t.Errorf("Name() = %q, want %q", p.Name(), "jira")
	}
}

func TestResolveNormalizesTheEpic(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	want := model.Epic{
		ID:           "ABC-1",
		Key:          "ABC-1",
		Title:        "Epic: sitrep v1 — cross-tracker epic monitor",
		URL:          "https://acme.atlassian.net/browse/ABC-1",
		Status:       model.StatusTodo,
		NativeStatus: "To Do",
		Assignees: []model.User{{
			Login:       "Ada Lovelace",
			DisplayName: "Ada Lovelace",
			AvatarURL:   "https://avatar.example/ada/48",
		}},
		Repository: "ABC",
	}
	if !reflect.DeepEqual(snap.Epic, want) {
		t.Errorf("Epic = %+v, want %+v", snap.Epic, want)
	}
	if got, headerWant := snap.Header, provider.EpicHeader(want); got != headerWant {
		t.Errorf("Header = %+v, want the Epic identity %+v", got, headerWant)
	}
	if !snap.Parent.IsZero() {
		t.Errorf("Parent = %+v, want the zero Parent: the fixture epic hangs off nothing", snap.Parent)
	}
	if snap.Capabilities != p.Capabilities() {
		t.Errorf("Capabilities = %+v, want the Provider's %+v", snap.Capabilities, p.Capabilities())
	}
	// The caller stamps the clock; a Provider must leave it zero.
	if !snap.FetchedAt.IsZero() {
		t.Errorf("FetchedAt = %s, want the zero time", snap.FetchedAt)
	}
}

func TestResolveReturnsEveryChildAcrossBothPages(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	type want struct {
		key        string
		status     model.StatusCategory
		native     string
		repository string
	}
	wants := []want{
		{"ABC-2", model.StatusTodo, "To Do", "ABC"},
		{"ABC-3", model.StatusInProgress, "In Review", "ABC"},
		{"ABC-4", model.StatusDone, "Done", "ABC"},
		{"ABC-5", model.StatusTodo, "Selected for Development", "ABC"},
		{"ABC-6", model.StatusInProgress, "In Progress", "ABC"},
		{"ABC-7", model.StatusCancelled, "Won't Do", "ABC"},
		{"ABC-8", model.StatusCancelled, "Duplicate", "ABC"},
		{"ABC-9", model.StatusDone, "Done", "ABC"},
		{"ABC-10", model.StatusUnknown, "Icebox", "ABC"},
		// A cross-project child identifies itself through its own project key.
		{"DEF-4", model.StatusInProgress, "In Progress", "DEF"},
	}

	if len(snap.Tickets) != len(wants) {
		t.Fatalf("got %d Tickets, want %d", len(snap.Tickets), len(wants))
	}
	for i, w := range wants {
		got := snap.Tickets[i]
		if got.Key != w.key || got.Status != w.status ||
			got.NativeStatus != w.native || got.Repository != w.repository {
			t.Errorf("Tickets[%d] = {%s %v %q %s}, want {%s %v %q %s}",
				i, got.Key, got.Status, got.NativeStatus, got.Repository,
				w.key, w.status, w.native, w.repository)
		}
		if got.URL != "https://acme.atlassian.net/browse/"+w.key {
			t.Errorf("Tickets[%d].URL = %q, want the browse URL", i, got.URL)
		}
		// Every child here hangs directly off the fetched Epic, which
		// model.Ticket documents as an empty ParentID — the same shape the
		// GitHub driver produces for the same situation.
		if got.ParentID != "" {
			t.Errorf("Tickets[%d].ParentID = %q, want it empty for a direct child", i, got.ParentID)
		}
	}

	// A title with an ampersand and non-ASCII survives verbatim.
	if got := snap.Tickets[4].Title; got != "Filtering & fuzzy find, « éclair » included" {
		t.Errorf("Tickets[4].Title = %q, want it unmangled", got)
	}
	// Jira has one assignee per issue, so the slice holds zero or one User.
	if got := snap.Tickets[1].Assignees; len(got) != 1 || got[0].DisplayName != "Grace Hopper" {
		t.Errorf("Tickets[1].Assignees = %+v, want one User named Grace Hopper", got)
	}
	if got := snap.Tickets[3].Assignees; got != nil {
		t.Errorf("Tickets[3].Assignees = %+v, want nil for an unassigned issue", got)
	}
}

func TestResolveSendsExactlyWhatItNeeds(t *testing.T) {
	s := fullEpic(t)
	p := newProvider(s)

	if _, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if n := len(s.requestsTo(epicPath)); n != 1 {
		t.Errorf("%d requests for the epic issue, want 1", n)
	}
	searches := s.requestsTo(searchPath)
	if len(searches) != 2 {
		t.Fatalf("%d children searches, want 2", len(searches))
	}

	jql := searches[0].query["jql"][0]
	for _, want := range []string{`parent = "ABC-1"`, "ORDER BY created ASC"} {
		if !strings.Contains(jql, want) {
			t.Errorf("jql = %q, want it to contain %q", jql, want)
		}
	}
	// The modern parent field, never the deprecated Epic Link.
	for _, unwanted := range []string{"Epic Link", "customfield_", "epic link"} {
		if strings.Contains(jql, unwanted) {
			t.Errorf("jql = %q, want it to say nothing about %q", jql, unwanted)
		}
	}
	if got := searches[0].query["fields"][0]; got != "summary,status,resolution,assignee,parent,project" {
		t.Errorf("fields = %q, want the polled field selection", got)
	}
	if got := searches[0].query["nextPageToken"]; got != nil {
		t.Errorf("the first search carried nextPageToken=%v, want none", got)
	}
	if got := searches[1].query["nextPageToken"]; len(got) != 1 || got[0] != "CAEaAggD" {
		t.Errorf("the second search carried nextPageToken=%v, want the first page's cursor", got)
	}

	for _, r := range s.recorded() {
		if got := r.headers.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want application/json", got)
		}
		if got := r.headers.Get("User-Agent"); got != "sitrep/test" {
			t.Errorf("User-Agent = %q, want sitrep's", got)
		}
		// The documented Atlassian flow is Basic email:token, asserted rather
		// than assumed.
		auth := r.headers.Get("Authorization")
		encoded, ok := strings.CutPrefix(auth, "Basic ")
		if !ok {
			t.Fatalf("Authorization = %q, want a Basic credential", auth)
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("decoding the Authorization header: %v", err)
		}
		if string(decoded) != fixtureEmail+":"+fixtureToken {
			t.Errorf("the Basic credential is not the email:token pair")
		}
	}
}

func TestResolveQuerySearchesMembershipThenBulkFetchesExactTickets(t *testing.T) {
	const query = `  project in (ABC, DEF) AND labels = "agent ready" & text ~ "σ"  `
	s := newReplayServer(t, map[string][]response{
		searchPath:    {{file: "query_membership.json"}},
		bulkFetchPath: {{file: "ref_list.json"}},
	})
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
	wantKeys := []string{"DEF-4", "ABC-7", "ABC-1"}
	if len(snap.Tickets) != len(wantKeys) {
		t.Fatalf("Tickets = %d, want %d", len(snap.Tickets), len(wantKeys))
	}
	for i, want := range wantKeys {
		if snap.Tickets[i].Key != want {
			t.Errorf("Tickets[%d].Key = %q, want %q", i, snap.Tickets[i].Key, want)
		}
	}
	if got := snap.Tickets[0].Title; got != "A child living in another project" {
		t.Errorf("first Ticket title = %q, want bulk-fetch state rather than stale search state", got)
	}

	searches := s.requestsTo(searchPath)
	if len(searches) != 1 {
		t.Fatalf("membership searches = %d, want exactly one first page", len(searches))
	}
	membership := searches[0]
	if membership.method != http.MethodGet {
		t.Errorf("membership method = %s, want GET", membership.method)
	}
	if got := membership.query["jql"]; len(got) != 1 || got[0] != query {
		t.Errorf("jql = %q, want exact %q", got, query)
	}
	if got := membership.query["fields"]; len(got) != 1 || got[0] != "key" {
		t.Errorf("membership fields = %q, want identity only", got)
	}
	if got := membership.query["maxResults"]; len(got) != 1 || got[0] != "100" {
		t.Errorf("maxResults = %q, want maximal first page", got)
	}
	if len(membership.query) != 3 || membership.query["nextPageToken"] != nil || membership.query["startAt"] != nil {
		t.Errorf("membership query params = %v, want only jql, fields and maxResults", membership.query)
	}

	bulk := s.requestsTo(bulkFetchPath)
	if len(bulk) != 1 {
		t.Fatalf("bulk reads = %d, want one exact-root read", len(bulk))
	}
	var body struct {
		IssueIDsOrKeys []string `json:"issueIdsOrKeys"`
		Fields         []string `json:"fields"`
	}
	if err := json.Unmarshal(bulk[0].body, &body); err != nil {
		t.Fatalf("decoding bulk body: %v", err)
	}
	if !reflect.DeepEqual(body.IssueIDsOrKeys, wantKeys) {
		t.Errorf("bulk issue keys = %v, want search order/de-duplication %v", body.IssueIDsOrKeys, wantKeys)
	}
	wantFields := []string{"summary", "status", "resolution", "assignee", "parent", "project"}
	if !reflect.DeepEqual(body.Fields, wantFields) {
		t.Errorf("bulk fields = %v, want thin exact-root fields %v", body.Fields, wantFields)
	}
	if got := len(s.recorded()); got != 2 {
		t.Errorf("all requests = %d, want membership plus exact-root only", got)
	}
}

func TestResolveQueryPaginatesBeforeBulkFetch(t *testing.T) {
	const query = `  project = ABC AND text ~ "ready & waiting"  `
	s := newReplayServer(t, map[string][]response{
		searchPath: {
			jiraQueryPage([]string{"DEF-4", "ABC-7", "DEF-4"}, false, "page-two"),
			jiraQueryPage([]string{"ABC-1"}, true, ""),
		},
		bulkFetchPath: {{file: "ref_list.json"}},
	})
	p := newProvider(s, jira.WithMaxTickets(5))

	snap, err := p.Resolve(context.Background(), provider.QuerySelector{Query: query})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.LimitReached {
		t.Error("LimitReached = true below an exhausted budget")
	}
	wantKeys := []string{"DEF-4", "ABC-7", "ABC-1"}
	if got := ticketKeys(snap.Tickets); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("Ticket keys = %v, want %v", got, wantKeys)
	}

	searches := s.requestsTo(searchPath)
	if len(searches) != 2 {
		t.Fatalf("membership searches = %d, want two", len(searches))
	}
	for i, want := range []struct {
		maxResults string
		token      []string
	}{{"5", nil}, {"2", []string{"page-two"}}} {
		if got := searches[i].query["jql"]; len(got) != 1 || got[0] != query {
			t.Errorf("page %d jql = %q, want exact %q", i+1, got, query)
		}
		if got := searches[i].query["maxResults"]; len(got) != 1 || got[0] != want.maxResults {
			t.Errorf("page %d maxResults = %q, want %q", i+1, got, want.maxResults)
		}
		if got := searches[i].query["nextPageToken"]; !reflect.DeepEqual(got, want.token) {
			t.Errorf("page %d nextPageToken = %q, want %q", i+1, got, want.token)
		}
	}
	bulk := s.requestsTo(bulkFetchPath)
	if len(bulk) != 1 {
		t.Fatalf("bulk reads = %d, want one after all membership pages", len(bulk))
	}
	all := s.recorded()
	if len(all) != 3 || all[0].path != searchPath || all[1].path != searchPath || all[2].path != bulkFetchPath {
		t.Fatalf("request order = %v, want membership page 1, membership page 2, then bulk read", all)
	}
	var body struct {
		IssueIDsOrKeys []string `json:"issueIdsOrKeys"`
	}
	if err := json.Unmarshal(bulk[0].body, &body); err != nil {
		t.Fatalf("decoding bulk body: %v", err)
	}
	if !reflect.DeepEqual(body.IssueIDsOrKeys, wantKeys) {
		t.Errorf("bulk keys = %v, want stable membership order %v", body.IssueIDsOrKeys, wantKeys)
	}
}

func TestResolveQueryCutoffAccounting(t *testing.T) {
	tests := []struct {
		name       string
		keys       []string
		isLast     bool
		maxTickets int
		wantKeys   []string
		wantLimit  bool
	}{
		{
			name:       "exact boundary exhausted",
			keys:       []string{"DEF-4", "ABC-7"},
			isLast:     true,
			maxTickets: 2,
			wantKeys:   []string{"DEF-4", "ABC-7"},
		},
		{
			name:       "exact boundary with continuation",
			keys:       []string{"DEF-4", "ABC-7"},
			maxTickets: 2,
			wantKeys:   []string{"DEF-4", "ABC-7"},
			wantLimit:  true,
		},
		{
			name:       "oversized response is clipped",
			keys:       []string{"DEF-4", "ABC-7", "ABC-1"},
			isLast:     true,
			maxTickets: 2,
			wantKeys:   []string{"DEF-4", "ABC-7"},
			wantLimit:  true,
		},
		{
			name:       "duplicate consumes native budget before de-duplication",
			keys:       []string{"DEF-4", "DEF-4"},
			maxTickets: 2,
			wantKeys:   []string{"DEF-4"},
			wantLimit:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{
				searchPath:    {jiraQueryPage(tt.keys, tt.isLast, "")},
				bulkFetchPath: {{file: "ref_list.json"}},
			})
			snap, err := newProvider(s, jira.WithMaxTickets(tt.maxTickets)).Resolve(
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

func TestResolveQueryRejectsNonProgressingPagination(t *testing.T) {
	tests := []struct {
		name  string
		pages []response
	}{
		{
			name:  "missing first continuation token",
			pages: []response{jiraQueryPage([]string{"DEF-4"}, false, "")},
		},
		{
			name: "repeated continuation token",
			pages: []response{
				jiraQueryPage([]string{"DEF-4"}, false, "same"),
				jiraQueryPage([]string{"ABC-7"}, false, "same"),
			},
		},
		{
			name: "non-adjacent token cycle",
			pages: []response{
				jiraQueryPage([]string{"DEF-4"}, false, "a"),
				jiraQueryPage([]string{"ABC-7"}, false, "b"),
				jiraQueryPage([]string{"ABC-1"}, false, "a"),
			},
		},
		{
			name: "empty continuation page",
			pages: []response{
				jiraQueryPage([]string{"DEF-4"}, false, "next"),
				jiraQueryPage(nil, true, ""),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{searchPath: tt.pages})
			snap, err := newProvider(s, jira.WithMaxTickets(4)).Resolve(
				context.Background(), provider.QuerySelector{Query: "q"})
			providertest.CheckError(t, "jira", err, providertest.Want{
				Kind:     provider.KindUnavailable,
				Contains: []string{"query"},
				Secret:   fixtureToken,
			})
			if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
				t.Errorf("snapshot = %+v, want no partial membership", snap)
			}
			if got := len(s.requestsTo(searchPath)); got != len(tt.pages) {
				t.Errorf("membership requests = %d, want %d", got, len(tt.pages))
			}
			if got := len(s.requestsTo(bulkFetchPath)); got != 0 {
				t.Errorf("bulk reads = %d, want none", got)
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
			resp: response{status: http.StatusUnauthorized, body: `{"message":"unauthorized"}`},
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
			resp: response{body: `{"issues": [`},
			want: providertest.Want{Kind: provider.KindUnavailable, Contains: []string{"decoding"}, Secret: fixtureToken},
		},
		{
			name: "malformed native query",
			resp: response{status: http.StatusBadRequest, body: `{"errors":{"jql":"bad query"}}`},
			want: providertest.Want{Kind: provider.KindBadRef, Contains: []string{"query rejected"}, Secret: fixtureToken},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{
				searchPath: {
					jiraQueryPage([]string{"DEF-4"}, false, "next"),
					tt.resp,
				},
			})
			snap, err := newProvider(s, jira.WithMaxTickets(3)).Resolve(
				context.Background(), provider.QuerySelector{Query: "q"})
			providertest.CheckError(t, "jira", err, tt.want)
			if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
				t.Errorf("snapshot = %+v, want no partial membership", snap)
			}
			if got := len(s.requestsTo(searchPath)); got != 2 {
				t.Errorf("membership requests = %d, want 2", got)
			}
			if got := len(s.requestsTo(bulkFetchPath)); got != 0 {
				t.Errorf("bulk reads = %d, want none", got)
			}
		})
	}
}

func TestResolveQueryEmptyMembershipNeedsNoBulkRead(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		searchPath: {{file: "query_empty.json"}},
	})
	snap, err := newProvider(s).Resolve(context.Background(), provider.QuerySelector{Query: ""})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.Tickets == nil || len(snap.Tickets) != 0 {
		t.Errorf("Tickets = %#v, want non-nil empty", snap.Tickets)
	}
	if snap.Header != provider.QueryHeader("") {
		t.Errorf("Header = %+v, want explicit empty Query", snap.Header)
	}
	if len(s.recorded()) != 1 || len(s.requestsTo(bulkFetchPath)) != 0 {
		t.Errorf("requests = %+v, want membership only", s.recorded())
	}
}

func TestResolveQueryHugeLimitDoesNotPreallocateTheBudget(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		searchPath: {{file: "query_empty.json"}},
	})
	p := newProvider(s, jira.WithMaxTickets(int(^uint(0)>>1)))
	if _, err := p.Resolve(context.Background(), provider.QuerySelector{Query: "q"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}

func TestResolveQueryRejectsMalformedJQLWithNativeExplanation(t *testing.T) {
	const query = `project in (ABC`
	s := newReplayServer(t, map[string][]response{
		searchPath: {{
			status: http.StatusBadRequest,
			body: `{"errorMessages":["The query project in (ABC is invalid."],` +
				`"errors":{"jql":"Expected ')' before end of query.","zfield":"Unknown field."}}`,
		}},
	})
	snap, err := newProvider(s).Resolve(context.Background(), provider.QuerySelector{Query: query})
	providertest.CheckError(t, "jira", err, providertest.Want{
		Kind: provider.KindBadRef,
		Contains: []string{
			"query rejected",
			"The query [query] is invalid.",
			"jql: Expected ')' before end of query.",
			"zfield: Unknown field.",
		},
		Secret: query,
	})
	if strings.Contains(err.Error(), fixtureToken) {
		t.Errorf("error = %q, leaked credential", err)
	}
	if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
		t.Errorf("snapshot = %+v, want no partial output", snap)
	}
	if provider.KindOf(err).Retryable() {
		t.Error("malformed JQL must not be retryable")
	}
}

func TestResolveQueryPreservesMembershipFailureClasses(t *testing.T) {
	tests := []struct {
		name string
		resp response
		want providertest.Want
	}{
		{
			name: "auth",
			resp: response{status: http.StatusUnauthorized, file: "errors_auth.json"},
			want: providertest.Want{Kind: provider.KindAuth, Contains: []string{"authentication failed"}, Secret: fixtureToken},
		},
		{
			name: "rate limit",
			resp: response{status: http.StatusTooManyRequests, body: `{"errorMessages":["Rate limit exceeded"]}`, headers: map[string]string{"Retry-After": "30"}},
			want: providertest.Want{Kind: provider.KindRateLimit, Contains: []string{"rate limit", "30"}, Secret: fixtureToken},
		},
		{
			name: "decode",
			resp: response{body: `{"issues": [`},
			want: providertest.Want{Kind: provider.KindUnavailable, Contains: []string{"decoding the response"}, Secret: fixtureToken},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{searchPath: {tt.resp}})
			snap, err := newProvider(s).Resolve(context.Background(), provider.QuerySelector{Query: "q"})
			providertest.CheckError(t, "jira", err, tt.want)
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
	transportErr := errors.New("dial failed")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})}
	p := jira.New(fixtureHost,
		jira.WithCredentials(jira.Credentials{Email: fixtureEmail, Token: fixtureToken}),
		jira.WithHTTPClient(client),
	)

	snap, err := p.Resolve(context.Background(), provider.QuerySelector{Query: query})
	providertest.CheckError(t, "jira", err, providertest.Want{
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

func TestResolveQueryBulkFailureReturnsNoPartialSnapshot(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		searchPath:    {{file: "query_membership.json"}},
		bulkFetchPath: {{file: "ref_list_error.json"}},
	})
	snap, err := newProvider(s).Resolve(context.Background(), provider.QuerySelector{Query: "q"})
	providertest.CheckError(t, "jira", err, providertest.Want{
		Kind:     provider.KindBadRef,
		Contains: []string{"ABC-7 not found"},
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
	s := newReplayServer(t, map[string][]response{
		searchPath: {{body: `{"issues":[{"key":"not a key"}],"isLast":true}`}},
	})
	snap, err := newProvider(s).Resolve(context.Background(), provider.QuerySelector{Query: "q"})
	providertest.CheckError(t, "jira", err, providertest.Want{
		Kind:     provider.KindUnavailable,
		Contains: []string{"invalid key", "not a key"},
		Secret:   fixtureToken,
	})
	if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
		t.Errorf("snapshot = %+v, want none", snap)
	}
}

func TestResolveRefListUsesAuthoritativeBulkFetch(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		bulkFetchPath: {{file: "ref_list.json"}},
	})
	p := newProvider(s)

	snap, err := p.Resolve(context.Background(), provider.RefListSelector{Refs: refListRefs})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if snap.Tickets == nil {
		t.Fatal("Tickets = nil, want a successful non-nil Ticket slice")
	}
	wantKeys := []string{"DEF-4", "ABC-7", "ABC-1", "ABC-3"}
	if len(snap.Tickets) != len(wantKeys) {
		t.Fatalf("got %d Tickets, want %d", len(snap.Tickets), len(wantKeys))
	}
	wantParents := []model.TicketID{"ABC-1", "ABC-1", "", "ABC-1"}
	wantRepositories := []string{"DEF", "ABC", "ABC", "ABC"}
	wantStatuses := []model.StatusCategory{
		model.StatusInProgress, model.StatusCancelled, model.StatusTodo, model.StatusInProgress,
	}
	for i, key := range wantKeys {
		got := snap.Tickets[i]
		if got.Key != key || got.ParentID != wantParents[i] ||
			got.Repository != wantRepositories[i] || got.Status != wantStatuses[i] {
			t.Errorf("Tickets[%d] = {%s parent=%s project=%s status=%s}, want {%s parent=%s project=%s status=%s}",
				i, got.Key, got.ParentID, got.Repository, got.Status,
				key, wantParents[i], wantRepositories[i], wantStatuses[i])
		}
	}
	if got, want := snap.Header, provider.RefListHeader(4); got != want {
		t.Errorf("Header = %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(snap.Epic, model.Epic{}) {
		t.Errorf("Epic = %+v, want the zero Epic", snap.Epic)
	}
	if !snap.Parent.IsZero() {
		t.Errorf("Parent = %+v, want the zero outer Parent", snap.Parent)
	}
	if snap.Capabilities != p.Capabilities() {
		t.Errorf("Capabilities = %+v, want %+v", snap.Capabilities, p.Capabilities())
	}
	if !snap.FetchedAt.IsZero() {
		t.Errorf("FetchedAt = %s, want the zero time", snap.FetchedAt)
	}

	requests := s.recorded()
	if len(requests) != 1 {
		t.Fatalf("got %d requests, want exactly one bulk fetch", len(requests))
	}
	request := requests[0]
	if request.path != bulkFetchPath || request.method != http.MethodPost {
		t.Errorf("request = %s %s, want POST %s", request.method, request.path, bulkFetchPath)
	}
	if len(request.query) != 0 {
		t.Errorf("bulk fetch query = %v, want none", request.query)
	}
	var rawBody map[string]json.RawMessage
	if err := json.Unmarshal(request.body, &rawBody); err != nil {
		t.Fatalf("decoding the bulk fetch request body: %v", err)
	}
	if len(rawBody) != 2 || rawBody["issueIdsOrKeys"] == nil || rawBody["fields"] == nil {
		t.Errorf("bulk fetch body keys = %v, want exactly issueIdsOrKeys and fields", reflect.ValueOf(rawBody).MapKeys())
	}
	var body struct {
		IssueIDsOrKeys []string `json:"issueIdsOrKeys"`
		Fields         []string `json:"fields"`
	}
	if err := json.Unmarshal(request.body, &body); err != nil {
		t.Fatalf("decoding the bulk fetch request body fields: %v", err)
	}
	if !reflect.DeepEqual(body.IssueIDsOrKeys, wantKeys) {
		t.Errorf("issueIdsOrKeys = %v, want %v", body.IssueIDsOrKeys, wantKeys)
	}
	wantFields := []string{"summary", "status", "resolution", "assignee", "parent", "project"}
	if !reflect.DeepEqual(body.Fields, wantFields) {
		t.Errorf("fields = %v, want exactly %v", body.Fields, wantFields)
	}
	if got := request.headers.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := request.headers.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q, want application/json", got)
	}
	if got := request.headers.Get("User-Agent"); got != "sitrep/test" {
		t.Errorf("User-Agent = %q, want sitrep/test", got)
	}
	wantAuthorization := "Basic " + base64.StdEncoding.EncodeToString(
		[]byte(fixtureEmail+":"+fixtureToken))
	if got := request.headers.Get("Authorization"); got != wantAuthorization {
		t.Error("Authorization does not carry the configured Basic email:token credential")
	}
}

func TestResolveRefListIssueErrorPreventsPartialOutput(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		bulkFetchPath: {{file: "ref_list_error.json"}},
	})

	snap, err := newProvider(s).Resolve(
		context.Background(), provider.RefListSelector{Refs: refListRefs})
	providertest.CheckError(t, "jira", err, providertest.Want{
		Kind:     provider.KindBadRef,
		Contains: []string{"ABC-7 not found (or you lack access)"},
		Secret:   fixtureToken,
	})
	if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
		t.Errorf("Resolve returned a partial snapshot %+v, want the zero snapshot", snap)
	}
	if n := len(s.requestsTo(bulkFetchPath)); n != 1 {
		t.Errorf("got %d bulk requests, want 1", n)
	}
}

func TestResolveRefListValidatesEveryKeyBeforeIO(t *testing.T) {
	s := newReplayServer(t, map[string][]response{})
	refs := append([]ref.Ref(nil), refListRefs[:1]...)
	refs = append(refs, ref.Ref{
		Tracker: ref.TrackerJira,
		Host:    fixtureHost,
		Key:     `ABC-7" OR "1`,
		Raw:     `ABC-7" OR "1`,
	})

	_, err := newProvider(s).Resolve(
		context.Background(), provider.RefListSelector{Refs: refs})
	providertest.CheckError(t, "jira", err, providertest.Want{
		Kind:     provider.KindBadRef,
		Contains: []string{"does not name a Jira issue"},
	})
	if n := len(s.recorded()); n != 0 {
		t.Errorf("%d requests reached the server; every key must validate before I/O", n)
	}
}

func TestResolveRejectsEmptyAndUnknownSelectorsBeforeIO(t *testing.T) {
	tests := []struct {
		name     string
		selector provider.Selector
		want     string
	}{
		{name: "empty Ref list", selector: provider.RefListSelector{}, want: "must name at least one"},
		{name: "nil Selector", selector: nil, want: "unsupported Watchlist selector"},
		{name: "pointer Selector", selector: &provider.EpicSelector{Ref: epicRef}, want: "unsupported Watchlist selector"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{})
			_, err := newProvider(s).Resolve(context.Background(), tt.selector)
			providertest.CheckError(t, "jira", err, providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{tt.want},
			})
			if n := len(s.recorded()); n != 0 {
				t.Errorf("%d requests reached the server; an unsupported Selector must fail immediately", n)
			}
		})
	}
}

func TestResolveRefListChunksOnlyAtTheBulkFetchBound(t *testing.T) {
	var (
		mu         sync.Mutex
		chunkSizes []int
	)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != bulkFetchPath {
			t.Errorf("request = %s %s, want POST %s", r.Method, r.URL.Path, bulkFetchPath)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			IssueIDsOrKeys []string `json:"issueIdsOrKeys"`
			Fields         []string `json:"fields"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		chunkSizes = append(chunkSizes, len(body.IssueIDsOrKeys))
		mu.Unlock()

		issues := make([]map[string]any, 0, len(body.IssueIDsOrKeys))
		for _, key := range body.IssueIDsOrKeys {
			issues = append(issues, map[string]any{
				"key": key,
				"fields": map[string]any{
					"summary":    "Bulk member " + key,
					"status":     map[string]any{"name": "To Do", "statusCategory": map[string]any{"key": "new"}},
					"resolution": nil,
					"assignee":   nil,
					"parent":     nil,
					"project":    map[string]any{"key": "ABC"},
				},
			})
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"issues": issues, "issueErrors": []any{},
		}); err != nil {
			t.Errorf("encoding response: %v", err)
		}
	}))
	t.Cleanup(s.Close)

	refs := make([]ref.Ref, 1001)
	for i := range refs {
		key := fmt.Sprintf("ABC-%d", i+1)
		refs[i] = ref.Ref{Tracker: ref.TrackerJira, Host: fixtureHost, Key: key, Raw: key}
	}
	p := jira.New(fixtureHost,
		jira.WithBaseURL(s.URL),
		jira.WithCredentials(jira.Credentials{Email: fixtureEmail, Token: fixtureToken}))

	snap, err := p.Resolve(context.Background(), provider.RefListSelector{Refs: refs})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(snap.Tickets) != len(refs) {
		t.Errorf("got %d Tickets, want %d", len(snap.Tickets), len(refs))
	}
	mu.Lock()
	gotSizes := append([]int(nil), chunkSizes...)
	mu.Unlock()
	if want := []int{1000, 1}; !reflect.DeepEqual(gotSizes, want) {
		t.Errorf("bulk request sizes = %v, want %v", gotSizes, want)
	}
}

// ADR-0002 with teeth: every operation reads. Jira requires POST for the bulk
// read body, but no request may target a mutation endpoint.
func TestEveryRequestIsARead(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		epicPath:      {{file: "epic_issue.json"}},
		searchPath:    {{file: "epic_children_page1.json"}, {file: "epic_children_page2.json"}},
		bulkFetchPath: {{file: "ref_list.json"}},
		ticketPath:    {{file: "detail_full.json"}},
		commentsPath:  {{file: "comments_page.json"}},
		linkTypePath:  {{file: "issue_link_types.json"}},
	})
	p := newProvider(s)

	if _, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef}); err != nil {
		t.Fatalf("resolving the Epic: %v", err)
	}
	if _, err := p.Resolve(context.Background(), provider.RefListSelector{Refs: refListRefs}); err != nil {
		t.Fatalf("resolving the Ref list: %v", err)
	}
	if _, err := p.FetchDetail(context.Background(), "ABC-12"); err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	recorded := s.recorded()
	if len(recorded) == 0 {
		t.Fatal("no requests were recorded")
	}
	for _, r := range recorded {
		wantMethod := http.MethodGet
		if r.path == bulkFetchPath {
			wantMethod = http.MethodPost
		}
		if r.method != wantMethod {
			t.Errorf("request to %s used %s, want read method %s", r.path, r.method, wantMethod)
		}
		switch r.method {
		case http.MethodPut, http.MethodPatch, http.MethodDelete:
			t.Errorf("a mutation method %s reached %s", r.method, r.path)
		}
	}
}

// The capability-differences user story, executable: Jira declares no pull
// requests, so the section is silently absent and nothing errors.
func TestNoPullRequestsAnywhere(t *testing.T) {
	p := newProvider(fullEpic(t))

	if p.Capabilities().PullRequests {
		t.Error("Capabilities().PullRequests = true, want false")
	}

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.Epic.PullRequests != nil {
		t.Errorf("Epic.PullRequests = %+v, want nil", snap.Epic.PullRequests)
	}
	for _, ticket := range snap.Tickets {
		if ticket.PullRequests != nil {
			t.Errorf("%s carries pull requests, which this driver does not serve", ticket.Key)
		}
	}

	var buf bytes.Buffer
	if err := plain.RenderWatchlist(&buf, snap); err != nil {
		t.Fatalf("RenderWatchlist: %v", err)
	}
	for _, unwanted := range []string{"pull request", "PR ", "#pr"} {
		if strings.Contains(strings.ToLower(buf.String()), strings.ToLower(unwanted)) {
			t.Errorf("the rendered report mentions %q; an undeclared Capability is silently absent", unwanted)
		}
	}
}

// A Ref that named a plain Ticket: no children, the fetched issue's identity on
// Epic and its own parent on Parent. Which screen that opens is internal/cli's
// decision, asserted at this seam rather than through the CLI.
func TestResolveOnARefThatNamesAPlainTicket(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		ticketPath: {{file: "ticket_with_parent.json"}},
		searchPath: {{file: "epic_children_empty.json"}},
	})
	p := newProvider(s)

	r := ref.Ref{Tracker: ref.TrackerJira, Host: fixtureHost, Key: "ABC-12", Raw: "ABC-12"}
	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: r})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if snap.Tickets == nil {
		t.Error("Tickets = nil, want an empty non-nil slice so an Epic with no children renders as none")
	}
	if len(snap.Tickets) != 0 {
		t.Errorf("got %d Tickets, want none", len(snap.Tickets))
	}
	if snap.Epic.Key != "ABC-12" {
		t.Errorf("Epic.Key = %q, want the fetched issue's own key", snap.Epic.Key)
	}
	want := model.Parent{
		ID:    "ABC-1",
		Key:   "ABC-1",
		Title: "Epic: sitrep v1 — cross-tracker epic monitor",
		URL:   "https://acme.atlassian.net/browse/ABC-1",
	}
	if snap.Parent != want {
		t.Errorf("Parent = %+v, want %+v", snap.Parent, want)
	}
}

func TestResolveRejectsBadRefsBeforeAnyRequest(t *testing.T) {
	tests := []struct {
		name string
		r    ref.Ref
		want string
	}{
		{
			name: "a GitHub Ref",
			r:    ref.Ref{Tracker: ref.TrackerGitHub, Owner: "acme", Repo: "widgets", Number: 1, Raw: "acme/widgets#1"},
			want: "is not a Jira Ref",
		},
		{
			name: "an empty key",
			r:    ref.Ref{Tracker: ref.TrackerJira, Host: fixtureHost, Raw: ""},
			want: "does not name a Jira issue",
		},
		{
			name: "a key carrying a JQL metacharacter",
			r:    ref.Ref{Tracker: ref.TrackerJira, Host: fixtureHost, Key: `ABC-1" OR "1`, Raw: `ABC-1" OR "1`},
			want: "does not name a Jira issue",
		},
		{
			name: "a key carrying a path traversal",
			r:    ref.Ref{Tracker: ref.TrackerJira, Host: fixtureHost, Key: "../../ABC-1", Raw: "../../ABC-1"},
			want: "does not name a Jira issue",
		},
		{
			name: "a Ref with no host",
			r:    ref.Ref{Tracker: ref.TrackerJira, Key: "ABC-1", Raw: "ABC-1"},
			want: "Profile is missing a host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{})
			p := newProvider(s)

			_, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: tt.r})
			providertest.CheckError(t, "jira", err, providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{tt.want},
			})
			if n := len(s.recorded()); n != 0 {
				t.Errorf("%d requests reached the server; a bad Ref is rejected before any of them", n)
			}
		})
	}
}

// epicFailure is one replayed failure and the error contract it must satisfy.
type epicFailure struct {
	name string
	resp response
	want providertest.Want
}

// epicFailures is the driver's failure table, hoisted out of the test that
// iterates it so that TestResolveFailuresCoverTheNamedClasses can assert what
// it covers rather than trusting a comment.
func epicFailures() []epicFailure {
	return []epicFailure{
		{
			name: "unauthorized",
			resp: response{status: http.StatusUnauthorized, file: "errors_auth.json"},
			want: providertest.Want{
				Kind:     provider.KindAuth,
				Contains: []string{"authentication failed (401)", "email", "API token"},
				Secret:   fixtureToken,
			},
		},
		{
			name: "forbidden",
			resp: response{status: http.StatusForbidden, body: `{"errorMessages":[],"errors":{}}`},
			want: providertest.Want{
				Kind:     provider.KindAuth,
				Contains: []string{"access denied (403)", "ABC-1"},
				Secret:   fixtureToken,
			},
		},
		{
			// Atlassian raises this after repeated failed logins, and no
			// credential change clears it — only a browser does.
			name: "forbidden by a CAPTCHA challenge",
			resp: response{
				status: http.StatusForbidden,
				body:   `{"errorMessages":[],"errors":{}}`,
				headers: map[string]string{
					"X-Authentication-Denied-Reason": "CAPTCHA_CHALLENGE; login-url=https://acme.atlassian.net/login.jsp",
				},
			},
			want: providertest.Want{
				Kind:     provider.KindAuth,
				Contains: []string{"CAPTCHA", "acme.atlassian.net", "browser"},
				Secret:   fixtureToken,
			},
		},
		{
			name: "not found",
			resp: response{status: http.StatusNotFound, file: "errors_not_found.json"},
			want: providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{"ABC-1 not found (or you lack access)"},
				Secret:   fixtureToken,
			},
		},
		{
			name: "rate limited with a Retry-After",
			resp: response{
				status:  http.StatusTooManyRequests,
				body:    `{}`,
				headers: map[string]string{"Retry-After": "30"},
			},
			want: providertest.Want{
				Kind:     provider.KindRateLimit,
				Contains: []string{"rate limit exceeded", "30s"},
				Secret:   fixtureToken,
			},
		},
		{
			// Jira Cloud sends this instead of Retry-After on some 429s. It is a
			// moment, not a duration, so it renders as a local time.
			name: "rate limited with only an X-RateLimit-Reset",
			resp: response{
				status:  http.StatusTooManyRequests,
				body:    `{}`,
				headers: map[string]string{"X-RateLimit-Reset": "2026-08-21T13:32:00Z"},
			},
			want: providertest.Want{
				Kind:     provider.KindRateLimit,
				Contains: []string{"rate limit exceeded", "retry after", "2026"},
				Secret:   fixtureToken,
			},
		},
		{
			name: "rate limited with nothing to say",
			resp: response{status: http.StatusTooManyRequests, body: `{}`},
			want: providertest.Want{
				Kind:     provider.KindRateLimit,
				Contains: []string{"rate limit exceeded", "an unknown time"},
				Secret:   fixtureToken,
			},
		},
		{
			name: "a server error",
			resp: response{status: http.StatusInternalServerError, body: `<html>oh no</html>`},
			want: providertest.Want{
				Kind:     provider.KindUnavailable,
				Contains: []string{"unexpected response 500"},
				Secret:   fixtureToken,
			},
		},
		{
			name: "an error payload with no status wording of its own",
			resp: response{status: http.StatusBadRequest, file: "errors_not_found.json"},
			want: providertest.Want{
				Kind:     provider.KindUnavailable,
				Contains: []string{"API error:", "Issue does not exist"},
				Secret:   fixtureToken,
			},
		},
		{
			name: "a non-Query request ignores field errors",
			resp: response{status: http.StatusBadRequest,
				body: `{"errorMessages":[],"errors":{"jql":"query-field-only-message"}}`},
			want: providertest.Want{
				Kind:     provider.KindUnavailable,
				Contains: []string{"unexpected response 400"},
				Secret:   "query-field-only-message",
			},
		},
		{
			name: "a malformed body",
			resp: response{body: `{"key": "ABC-1", "fields": `},
			want: providertest.Want{
				Kind:     provider.KindUnavailable,
				Contains: []string{"decoding the response"},
				Secret:   fixtureToken,
			},
		},
	}
}

func TestResolveFailures(t *testing.T) {
	for _, tt := range epicFailures() {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{epicPath: {tt.resp}})

			_, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
			providertest.CheckError(t, "jira", err, tt.want)
		})
	}
}

// The ticket's promise is per-driver: a bad ref, an auth failure and rate
// limiting each explain themselves on this Tracker. This asserts the table
// above actually exercises all three.
func TestResolveFailuresCoverTheNamedClasses(t *testing.T) {
	kinds := []provider.Kind{}
	for _, tt := range epicFailures() {
		kinds = append(kinds, tt.want.Kind)
	}
	providertest.CheckCoversTheNamedClasses(t, "jira", kinds)
}

// A missing half of the credential is reported at the first request, naming the
// half that is missing and never the one that is present.
func TestMissingCredentials(t *testing.T) {
	tests := []struct {
		name        string
		credentials jira.Credentials
		want        []string
	}{
		{
			name:        "no token",
			credentials: jira.Credentials{Email: fixtureEmail},
			want:        []string{"no API token for acme.atlassian.net", "auth.token_env"},
		},
		{
			name:        "no email",
			credentials: jira.Credentials{Token: fixtureToken},
			want:        []string{"no Atlassian account email", "auth.user"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{epicPath: {{file: "epic_issue.json"}}})
			p := jira.New(fixtureHost, jira.WithBaseURL(s.URL), jira.WithCredentials(tt.credentials))

			_, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
			providertest.CheckError(t, "jira", err, providertest.Want{
				Kind:     provider.KindAuth,
				Contains: tt.want,
				Secret:   fixtureToken,
			})
			if n := len(s.recorded()); n != 0 {
				t.Errorf("%d requests were sent without a complete credential", n)
			}
		})
	}
}

// Credentials never render, however they are printed.
func TestCredentialsNeverRenderTheirToken(t *testing.T) {
	c := jira.Credentials{Email: fixtureEmail, Token: fixtureToken}

	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	// %#v and encoding/json both read the exported Token field and would walk
	// straight past String.
	renderings := []string{fmt.Sprint(c), c.String(), string(encoded)}
	// The verbs are formatted through a variable so that this stays a test of
	// what each one prints rather than of what a linter thinks it should be
	// rewritten to.
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		renderings = append(renderings, fmt.Sprintf(verb, c))
	}
	for _, rendered := range renderings {
		if strings.Contains(rendered, fixtureToken) {
			t.Errorf("Credentials rendered as %q, which contains the token", rendered)
		}
		// A substring is a leak too: half a token still narrows the search.
		if half := fixtureToken[:len(fixtureToken)/2]; strings.Contains(rendered, half) {
			t.Errorf("Credentials rendered as %q, which contains part of the token", rendered)
		}
		if !strings.Contains(rendered, "REDACTED") {
			t.Errorf("Credentials rendered as %q, want it to say REDACTED", rendered)
		}
	}
}

// An empty ParentID means "hangs directly off the Epic". A child whose parent
// is something else still says so, so the field keeps meaning what
// model.Ticket documents.
func TestParentIDNamesOnlyANonEpicParent(t *testing.T) {
	const children = `{"isLast":true,"issues":[
		{"key":"ABC-2","fields":{"summary":"Direct","status":{"name":"To Do",
		 "statusCategory":{"key":"new"}},"parent":{"key":"ABC-1"},
		 "project":{"key":"ABC"}}},
		{"key":"ABC-3","fields":{"summary":"Nested","status":{"name":"To Do",
		 "statusCategory":{"key":"new"}},"parent":{"key":"ABC-2"},
		 "project":{"key":"ABC"}}}]}`

	s := newReplayServer(t, map[string][]response{
		epicPath:   {{file: "epic_issue.json"}},
		searchPath: {{body: children}},
	})

	snap, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(snap.Tickets) != 2 {
		t.Fatalf("got %d Tickets, want 2", len(snap.Tickets))
	}
	if got := snap.Tickets[0].ParentID; got != "" {
		t.Errorf("ABC-2.ParentID = %q, want it empty: its parent is the fetched Epic", got)
	}
	if got := snap.Tickets[1].ParentID; got != "ABC-2" {
		t.Errorf("ABC-3.ParentID = %q, want ABC-2", got)
	}
}

func TestPaginationSafety(t *testing.T) {
	t.Run("a repeated cursor is an error, not a loop", func(t *testing.T) {
		s := newReplayServer(t, map[string][]response{
			epicPath: {{file: "epic_issue.json"}},
			searchPath: {{body: `{"isLast":false,"nextPageToken":"CAEaAggD","issues":[]}`},
				{body: `{"isLast":false,"nextPageToken":"CAEaAggD","issues":[]}`}},
		})
		p := newProvider(s)

		_, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
		if err == nil {
			t.Fatal("Resolve succeeded, want an error about the repeated cursor")
		}
		// A server that reports more and hands over the same cursor is
		// misbehaving, which is what KindUnavailable means — and it stays
		// retryable, on the same terms as a 500.
		providertest.CheckError(t, "jira", err, providertest.Want{
			Kind:     provider.KindUnavailable,
			Contains: []string{"no new page cursor"},
			Secret:   fixtureToken,
		})
		if !provider.KindOf(err).Retryable() {
			t.Error("a server fault must stay retryable; the monitor recovers when the server does")
		}
	})

	// "there is more" with no cursor is an inconsistent answer, and the GitHub
	// and GitLab drivers both refuse one. A situation report silently missing
	// children is worse than one that failed.
	t.Run("a truncated child list is an error, not a short epic", func(t *testing.T) {
		s := newReplayServer(t, map[string][]response{
			epicPath:   {{file: "epic_issue.json"}},
			searchPath: {{file: "epic_children_truncated.json"}},
		})
		p := newProvider(s)

		snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
		if err == nil {
			t.Fatalf("Resolve returned %d children and no error; want an error",
				len(snap.Tickets))
		}
		providertest.CheckError(t, "jira", err, providertest.Want{
			Kind:     provider.KindUnavailable,
			Contains: []string{"ABC-1", "no page cursor"},
			Secret:   fixtureToken,
		})
	})

	t.Run("an endless epic stops paging", func(t *testing.T) {
		var mu sync.Mutex
		var searches int
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == epicPath {
				payload, err := os.ReadFile(filepath.Join("testdata", "epic_issue.json"))
				if err != nil {
					t.Errorf("reading the epic fixture: %v", err)
				}
				_, _ = w.Write(payload)
				return
			}
			mu.Lock()
			searches++
			n := searches
			mu.Unlock()
			fmt.Fprintf(w, `{"isLast":false,"nextPageToken":"cursor-%d","issues":[]}`, n)
		}))
		t.Cleanup(s.Close)

		p := jira.New(fixtureHost,
			jira.WithBaseURL(s.URL),
			jira.WithCredentials(jira.Credentials{Email: fixtureEmail, Token: fixtureToken}))

		_, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
		if err == nil {
			t.Fatal("Resolve succeeded, want an error about refusing to keep paging")
		}
		// A collection past the cap is a stable property of the ref, so the
		// monitor prints one line and exits rather than retrying forever.
		providertest.CheckError(t, "jira", err, providertest.Want{
			Kind:     provider.KindBadRef,
			Contains: []string{"refusing to keep paging"},
			Secret:   fixtureToken,
		})
		if provider.KindOf(err).Retryable() {
			t.Error("an over-cap collection is as large on the next tick; retrying buys nothing")
		}
	})
}

// The polled path is polled: two consecutive fetches return the same snapshot
// and re-issue the same requests, because nothing about an Epic may be cached.
func TestPollingRefetches(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		epicPath:   {{file: "epic_issue.json"}},
		searchPath: {{file: "epic_children_empty.json"}},
	})

	var resolved int
	p := jira.New(fixtureHost,
		jira.WithBaseURL(s.URL),
		jira.WithUserAgent("sitrep/test"),
		jira.WithCredentialSource(func(context.Context, string) (jira.Credentials, error) {
			resolved++
			return jira.Credentials{Email: fixtureEmail, Token: fixtureToken}, nil
		}))

	first, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("the first Resolve: %v", err)
	}
	second, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("the second Resolve: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Error("two fetches of an unchanged epic returned different snapshots")
	}
	if n := len(s.requestsTo(epicPath)); n != 2 {
		t.Errorf("%d epic reads for two fetches, want 2", n)
	}
	if n := len(s.requestsTo(searchPath)); n != 2 {
		t.Errorf("%d children searches for two fetches, want 2", n)
	}
	// A 60s poll must not re-resolve the credential forever.
	if resolved != 1 {
		t.Errorf("the credential was resolved %d times, want once per Provider", resolved)
	}
}

// ADR-0003 made testable: the polled path never reaches for Detail data. This
// is the assertion that stops the next person optimising Detail into the list
// model.
func TestTheHotPathStaysCold(t *testing.T) {
	s := fullEpic(t)
	p := newProvider(s)

	if _, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	for _, r := range s.recorded() {
		if r.path == linkTypePath {
			t.Error("the polled path requested the issue link types")
		}
		if strings.HasSuffix(r.path, "/comment") {
			t.Error("the polled path requested comments")
		}
		for _, fields := range r.query["fields"] {
			for _, unwanted := range []string{"description", "issuelinks"} {
				if strings.Contains(fields, unwanted) {
					t.Errorf("the polled path asked for %q", unwanted)
				}
			}
		}
	}
}
