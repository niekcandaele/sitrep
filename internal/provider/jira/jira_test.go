package jira_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
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
	epicPath     = "/rest/api/2/issue/ABC-1"
	ticketPath   = "/rest/api/2/issue/ABC-12"
	commentsPath = "/rest/api/2/issue/ABC-12/comment"
	searchPath   = "/rest/api/2/search/jql"
	linkTypePath = "/rest/api/2/issueLinkType"
)

// epicRef is the Epic Ref the fixtures were written for.
var epicRef = ref.Ref{
	Tracker: ref.TrackerJira,
	Host:    fixtureHost,
	Key:     "ABC-1",
	Raw:     "ABC-1",
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
		s.mu.Lock()
		s.requests = append(s.requests, recordedRequest{
			method:  r.Method,
			path:    r.URL.Path,
			query:   r.URL.Query(),
			headers: r.Header.Clone(),
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

// fullEpic serves the two-page fixture epic.
func fullEpic(t *testing.T) *replayServer {
	t.Helper()
	return newReplayServer(t, map[string][]response{
		epicPath:   {{file: "epic_issue.json"}},
		searchPath: {{file: "epic_children_page1.json"}, {file: "epic_children_page2.json"}},
	})
}

func TestName(t *testing.T) {
	if p := jira.New(fixtureHost); p.Name() != "jira" {
		t.Errorf("Name() = %q, want %q", p.Name(), "jira")
	}
}

func TestFetchEpicNormalizesTheEpic(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
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

func TestFetchEpicReturnsEveryChildAcrossBothPages(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
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
		if got.ParentID != "ABC-1" {
			t.Errorf("Tickets[%d].ParentID = %q, want ABC-1", i, got.ParentID)
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

func TestFetchEpicSendsExactlyWhatItNeeds(t *testing.T) {
	s := fullEpic(t)
	p := newProvider(s)

	if _, err := p.FetchEpic(context.Background(), epicRef); err != nil {
		t.Fatalf("FetchEpic: %v", err)
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

// ADR-0002 with teeth: this driver reads and never writes.
func TestEveryRequestIsAGet(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		epicPath:     {{file: "epic_issue.json"}},
		searchPath:   {{file: "epic_children_page1.json"}, {file: "epic_children_page2.json"}},
		ticketPath:   {{file: "detail_full.json"}},
		commentsPath: {{file: "comments_page.json"}},
		linkTypePath: {{file: "issue_link_types.json"}},
	})
	p := newProvider(s)

	if _, err := p.FetchEpic(context.Background(), epicRef); err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	if _, err := p.FetchDetail(context.Background(), "ABC-12"); err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	recorded := s.recorded()
	if len(recorded) == 0 {
		t.Fatal("no requests were recorded")
	}
	for _, r := range recorded {
		if r.method != http.MethodGet {
			t.Errorf("a %s went to %s; every request this driver sends is a GET", r.method, r.path)
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

	snap, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
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
	if err := plain.RenderEpic(&buf, snap); err != nil {
		t.Fatalf("RenderEpic: %v", err)
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
func TestFetchEpicOnARefThatNamesAPlainTicket(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		ticketPath: {{file: "ticket_with_parent.json"}},
		searchPath: {{file: "epic_children_empty.json"}},
	})
	p := newProvider(s)

	r := ref.Ref{Tracker: ref.TrackerJira, Host: fixtureHost, Key: "ABC-12", Raw: "ABC-12"}
	snap, err := p.FetchEpic(context.Background(), r)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
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

func TestFetchEpicRejectsBadRefsBeforeAnyRequest(t *testing.T) {
	tests := []struct {
		name string
		r    ref.Ref
		want string
	}{
		{
			name: "a GitHub Ref",
			r:    ref.Ref{Tracker: ref.TrackerGitHub, Owner: "acme", Repo: "widgets", Number: 1, Raw: "acme/widgets#1"},
			want: "is not a Jira Epic Ref",
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

			_, err := p.FetchEpic(context.Background(), tt.r)
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
// iterates it so that TestFetchEpicFailuresCoverTheNamedClasses can assert what
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

func TestFetchEpicFailures(t *testing.T) {
	for _, tt := range epicFailures() {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{epicPath: {tt.resp}})

			_, err := newProvider(s).FetchEpic(context.Background(), epicRef)
			providertest.CheckError(t, "jira", err, tt.want)
		})
	}
}

// The ticket's promise is per-driver: a bad ref, an auth failure and rate
// limiting each explain themselves on this Tracker. This asserts the table
// above actually exercises all three.
func TestFetchEpicFailuresCoverTheNamedClasses(t *testing.T) {
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

			_, err := p.FetchEpic(context.Background(), epicRef)
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
	for _, rendered := range []string{fmt.Sprint(c), fmt.Sprintf("%v", c), c.String()} {
		if strings.Contains(rendered, fixtureToken) {
			t.Errorf("Credentials rendered as %q, which contains the token", rendered)
		}
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

		_, err := p.FetchEpic(context.Background(), epicRef)
		if err == nil {
			t.Fatal("FetchEpic succeeded, want an error about the repeated cursor")
		}
		if !strings.Contains(err.Error(), "no new page cursor") {
			t.Errorf("error = %q, want it to name the cursor problem", err)
		}
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

		_, err := p.FetchEpic(context.Background(), epicRef)
		if err == nil {
			t.Fatal("FetchEpic succeeded, want an error about refusing to keep paging")
		}
		if !strings.Contains(err.Error(), "refusing to keep paging") {
			t.Errorf("error = %q, want it to say it stopped paging", err)
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

	first, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("the first FetchEpic: %v", err)
	}
	second, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("the second FetchEpic: %v", err)
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

	if _, err := p.FetchEpic(context.Background(), epicRef); err != nil {
		t.Fatalf("FetchEpic: %v", err)
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
