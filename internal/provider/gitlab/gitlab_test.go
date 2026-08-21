package gitlab_test

import (
	"context"
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
	"github.com/niekcandaele/sitrep/internal/provider/gitlab"
	"github.com/niekcandaele/sitrep/internal/ref"
	"github.com/niekcandaele/sitrep/internal/render/plain"
)

// The GitLab driver must satisfy the interface every other Provider does.
var _ provider.Provider = (*gitlab.Provider)(nil)

// The token every replayed request is expected to carry. It is obviously fake,
// so a test asserting that no credential leaks into an error message is
// asserting something visible.
const fixtureToken = "fixture-token-not-a-real-secret"

// fixtureHost is the instance the fixtures were recorded from.
const fixtureHost = "gitlab.com"

// The paths the driver reads, spelled out here so a test asserting "what was
// sent" compares against literals rather than against the driver's own path
// builders.
const (
	epicPath       = "/api/v4/groups/gitlab-org/epics/23356"
	epicIssuesPath = "/api/v4/groups/gitlab-org/epics/23356/issues"
	epicNotesPath  = "/api/v4/groups/gitlab-org/epics/5270098/notes"
	issuePath      = "/api/v4/projects/gitlab-org%2Fcli/issues/8509"
	issueNotesPath = "/api/v4/projects/gitlab-org%2Fcli/issues/8509/notes"
	issueLinksPath = "/api/v4/projects/gitlab-org%2Fcli/issues/8509/links"
)

// The Epic Ref and the TicketIDs the fixtures were written for.
var (
	epicRef = ref.Ref{
		Tracker: ref.TrackerGitLab,
		Host:    fixtureHost,
		Owner:   "gitlab-org",
		Number:  23356,
		Key:     "gitlab-org&23356",
		Raw:     "https://gitlab.com/groups/gitlab-org/-/epics/23356",
	}
	issueRef = ref.Ref{
		Tracker: ref.TrackerGitLab,
		Host:    fixtureHost,
		Owner:   "gitlab-org",
		Repo:    "cli",
		Number:  8509,
		Raw:     "https://gitlab.com/gitlab-org/cli/-/work_items/8509",
	}
)

const (
	issueTicketID model.TicketID = "issue:gitlab-org/cli#8509"
	epicTicketID  model.TicketID = "epic:gitlab-org&23356"
)

// response is one replayed answer: a status code and either a fixture file or a
// literal body, plus whatever headers the driver is meant to read.
type response struct {
	status  int
	file    string
	body    string
	headers map[string]string
}

// recordedRequest is what the replay server saw, so tests can assert the
// method, the paths and the paging parameters sitrep sends without reaching
// inside the driver.
type recordedRequest struct {
	method  string
	path    string
	query   map[string][]string
	headers http.Header
}

// replayServer serves recorded payloads, routed by request path — GitLab
// answers six endpoints where GitHub answered one. A path with no configured
// response fails the test loudly instead of answering 200 with nothing.
//
// Within one path the responses are served in order and the last one repeats:
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
			path:    r.URL.EscapedPath(),
			query:   r.URL.Query(),
			headers: r.Header.Clone(),
		})
		queue, ok := s.responses[r.URL.EscapedPath()]
		n := s.served[r.URL.EscapedPath()]
		s.served[r.URL.EscapedPath()]++
		s.mu.Unlock()

		if !ok || len(queue) == 0 {
			t.Errorf("the driver requested %s, which this test configured no response for", r.URL.EscapedPath())
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
// token, so no test needs a GitLab instance and none executes glab.
func newProvider(s *replayServer, opts ...gitlab.Option) *gitlab.Provider {
	base := []gitlab.Option{
		gitlab.WithBaseURL(s.URL),
		gitlab.WithUserAgent("sitrep/test"),
		gitlab.WithTokenSource(func(context.Context, string) (string, error) {
			return fixtureToken, nil
		}),
	}
	return gitlab.New(fixtureHost, append(base, opts...)...)
}

// fullEpic serves the two-page fixture epic.
func fullEpic(t *testing.T) *replayServer {
	t.Helper()
	return newReplayServer(t, map[string][]response{
		epicPath: {{file: "epic.json"}},
		epicIssuesPath: {
			{file: "epic_children_page1.json", headers: map[string]string{"x-next-page": "2"}},
			{file: "epic_children_page2.json", headers: map[string]string{"x-next-page": ""}},
		},
	})
}

func TestName(t *testing.T) {
	if p := gitlab.New(fixtureHost); p.Name() != "gitlab" {
		t.Errorf("Name() = %q, want %q", p.Name(), "gitlab")
	}
}

func TestCapabilities(t *testing.T) {
	want := model.Capabilities{Hierarchy: true, BlockingLinks: true, Comments: true, PullRequests: false}
	if got := gitlab.New(fixtureHost).Capabilities(); got != want {
		t.Errorf("Capabilities() = %+v, want %+v", got, want)
	}
}

func TestFetchEpicNormalizesTheEpic(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	want := model.Epic{
		ID:           epicTicketID,
		Key:          "gitlab-org&23356",
		Title:        "AI Clients surfaces feature parity ledger (Web Chat, IDEs, Duo CLI)",
		URL:          "https://gitlab.com/groups/gitlab-org/-/epics/23356",
		Status:       model.StatusTodo,
		NativeStatus: "open",
		Repository:   "gitlab-org",
	}
	if !reflect.DeepEqual(snap.Epic, want) {
		t.Errorf("Epic = %+v, want %+v", snap.Epic, want)
	}
	if !snap.Parent.IsZero() {
		t.Errorf("Parent = %+v, want the zero Parent: the recorded epic hangs off nothing", snap.Parent)
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
		{"gitlab-org/cli#101", model.StatusTodo, "open", "gitlab-org/cli"},
		{"gitlab-org/cli#102", model.StatusTodo, "open", "gitlab-org/cli"},
		{"gitlab-org/cli#103", model.StatusDone, "closed", "gitlab-org/cli"},
		{"gitlab-org/cli#104", model.StatusTodo, "open", "gitlab-org/cli"},
		// A child in another project identifies itself through its own path.
		{"gitlab-org/platform/core#7", model.StatusTodo, "open", "gitlab-org/platform/core"},
		{"gitlab-org/cli#105", model.StatusCancelled, "workflow::wontfix", "gitlab-org/cli"},
		{"gitlab-org/cli#106", model.StatusCancelled, "duplicate", "gitlab-org/cli"},
		{"gitlab-org/cli#107", model.StatusDone, "closed", "gitlab-org/cli"},
		{"gitlab-org/cli#108", model.StatusUnknown, "archived", "gitlab-org/cli"},
		// A payload with no references object falls back to the bare iid.
		{"#109", model.StatusTodo, "open", "gitlab-org/cli"},
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
		if got.ParentID != epicTicketID {
			t.Errorf("Tickets[%d].ParentID = %q, want %q", i, got.ParentID, epicTicketID)
		}
		if !strings.HasPrefix(string(got.ID), "issue:") {
			t.Errorf("Tickets[%d].ID = %q, want an issue TicketID", i, got.ID)
		}
	}

	// A title with an ampersand and non-ASCII survives verbatim.
	if got := snap.Tickets[1].Title; got != "Filtering & fuzzy find, « éclair » included" {
		t.Errorf("Tickets[1].Title = %q, want it unmangled", got)
	}
	// GitLab has many assignees per issue, in its own order, and the deprecated
	// singular `assignee` is ignored.
	if got := snap.Tickets[1].Assignees; len(got) != 2 ||
		got[0].Login != "jay_mccure" || got[0].DisplayName != "Jay McCure" ||
		got[1].Login != "aelhusseiny" {
		t.Errorf("Tickets[1].Assignees = %+v, want both, in GitLab's order", got)
	}
	if got := snap.Tickets[0].Assignees; got != nil {
		t.Errorf("Tickets[0].Assignees = %+v, want nil for an unassigned issue", got)
	}
	// GitLab's own web_url is authoritative and already carries the work-item
	// form it serves today.
	if got := snap.Tickets[0].URL; got != "https://gitlab.com/gitlab-org/cli/-/work_items/101" {
		t.Errorf("Tickets[0].URL = %q, want GitLab's own web_url", got)
	}
}

// ADR-0003 with teeth: GitLab sends every child's description on the polled
// path whether sitrep wants it or not, and none of it may reach a model.Ticket.
func TestFetchEpicPutsNoDescriptionOnTheHotPath(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	const leak = "polled path"
	for i, ticket := range snap.Tickets {
		for _, field := range []string{ticket.Title, ticket.NativeStatus, ticket.Key, ticket.Repository} {
			if strings.Contains(field, leak) {
				t.Errorf("Tickets[%d] carries description text in %q", i, field)
			}
		}
	}
}

// ADR-0002 with teeth.
func TestEveryRequestIsAGet(t *testing.T) {
	s := fullEpic(t)
	p := newProvider(s)

	if _, err := p.FetchEpic(context.Background(), epicRef); err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	recorded := s.recorded()
	if len(recorded) == 0 {
		t.Fatal("no requests were recorded")
	}
	for _, r := range recorded {
		if r.method != http.MethodGet {
			t.Errorf("the driver sent %s %s; this Tracker driver is read-only", r.method, r.path)
		}
	}
}

func TestFetchEpicSendsExactlyWhatItNeeds(t *testing.T) {
	s := fullEpic(t)
	p := newProvider(s)

	if _, err := p.FetchEpic(context.Background(), epicRef); err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	if n := len(s.requestsTo(epicPath)); n != 1 {
		t.Errorf("%d requests for the epic, want 1", n)
	}
	pages := s.requestsTo(epicIssuesPath)
	if len(pages) != 2 {
		t.Fatalf("%d children requests, want 2", len(pages))
	}
	for i, page := range pages {
		if got := page.query["per_page"]; len(got) != 1 || got[0] != "100" {
			t.Errorf("children request %d carried per_page=%v, want GitLab's maximum", i, got)
		}
	}
	if got := pages[0].query["page"]; len(got) != 1 || got[0] != "1" {
		t.Errorf("the first children request carried page=%v, want 1", got)
	}
	if got := pages[1].query["page"]; len(got) != 1 || got[0] != "2" {
		t.Errorf("the second children request carried page=%v, want the header's next page", got)
	}

	// The hot path fetches nothing expensive.
	for _, r := range s.recorded() {
		if strings.Contains(r.path, "/notes") || strings.Contains(r.path, "/links") {
			t.Errorf("the polled path requested %s; that belongs to FetchDetail", r.path)
		}
		if strings.Contains(r.path, "merge_request") {
			t.Errorf("the driver requested %s; merge requests are #15's", r.path)
		}
		if got := r.headers.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want application/json", got)
		}
		if got := r.headers.Get("User-Agent"); got != "sitrep/test" {
			t.Errorf("User-Agent = %q, want sitrep's", got)
		}
		if got := r.headers.Get("Authorization"); got != "Bearer "+fixtureToken {
			t.Errorf("Authorization = %q, want the Bearer form", got)
		}
	}
}

// A namespaced path is escaped whole, which is GitLab's documented addressing
// for a group or project id.
func TestNamespacedPathsAreEscapedWhole(t *testing.T) {
	const path = "/api/v4/groups/acme%2Fplatform/epics/12"
	s := newReplayServer(t, map[string][]response{
		path:             {{file: "epic.json"}},
		path + "/issues": {{file: "epic_children_empty.json"}},
	})
	p := newProvider(s)

	r := ref.Ref{Tracker: ref.TrackerGitLab, Host: fixtureHost,
		Owner: "acme", Repo: "platform", Number: 12, Key: "acme/platform&12", Raw: "acme/platform&12"}
	if _, err := p.FetchEpic(context.Background(), r); err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	if n := len(s.requestsTo(path)); n != 1 {
		t.Errorf("%d requests to the escaped epic path, want 1", n)
	}
}

// An epic someone pointed sitrep at that has no children is a Ticket, not an
// error — and its Tickets slice is empty rather than null.
func TestFetchEpicWithNoChildren(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		epicPath:       {{file: "epic.json"}},
		epicIssuesPath: {{file: "epic_children_empty.json"}},
	})

	snap, err := newProvider(s).FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	if snap.Tickets == nil {
		t.Fatal("Tickets is nil; an epic with no children renders as none, not as null")
	}
	if len(snap.Tickets) != 0 {
		t.Errorf("got %d Tickets, want none", len(snap.Tickets))
	}
}

// An epic's parent breadcrumb is built from parent_iid, with no Title: the
// payload does not carry one and the polled path will not spend a request per
// refresh on it.
func TestFetchEpicReportsItsOwnParent(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		epicPath:       {{file: "epic_with_parent.json"}},
		epicIssuesPath: {{file: "epic_children_empty.json"}},
	})

	snap, err := newProvider(s).FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	want := model.Parent{
		ID:  "epic:gitlab-org&4200",
		Key: "gitlab-org&4200",
		URL: "https://gitlab.com/groups/gitlab-org/-/epics/4200",
	}
	if snap.Parent != want {
		t.Errorf("Parent = %+v, want %+v", snap.Parent, want)
	}
}

// The decoder's input, asserted at this seam: a project issue Ref comes back
// with no Tickets, the issue's own identity on Epic, and its epic on Parent.
func TestFetchEpicOnAnIssueRef(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		issuePath: {{file: "issue_with_epic.json"}},
	})

	snap, err := newProvider(s).FetchEpic(context.Background(), issueRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	if len(snap.Tickets) != 0 {
		t.Errorf("got %d Tickets, want none: an issue Ref names one node", len(snap.Tickets))
	}
	wantEpic := model.Epic{
		ID:           issueTicketID,
		Key:          "gitlab-org/cli#8509",
		Title:        "Telemetry: record root-command invocations as 'glab' instead of null",
		URL:          "https://gitlab.com/gitlab-org/cli/-/work_items/8509",
		Status:       model.StatusTodo,
		NativeStatus: "open",
		Assignees: []model.User{{
			Login:       "jay_mccure",
			DisplayName: "Jay McCure",
			AvatarURL:   "https://avatar.example/redacted",
		}},
		Repository: "gitlab-org/cli",
	}
	if !reflect.DeepEqual(snap.Epic, wantEpic) {
		t.Errorf("Epic = %+v, want %+v", snap.Epic, wantEpic)
	}

	// The embedded epic object is a complete breadcrumb for no extra request.
	wantParent := model.Parent{
		ID:    epicTicketID,
		Key:   "gitlab-org&23356",
		Title: "AI Clients surfaces feature parity ledger (Web Chat, IDEs, Duo CLI)",
		URL:   "https://gitlab.com/groups/gitlab-org/-/epics/23356",
	}
	if snap.Parent != wantParent {
		t.Errorf("Parent = %+v, want %+v", snap.Parent, wantParent)
	}
}

func TestFetchEpicOnAnIssueWithNoEpic(t *testing.T) {
	s := newReplayServer(t, map[string][]response{issuePath: {{file: "issue.json"}}})

	snap, err := newProvider(s).FetchEpic(context.Background(), issueRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	if !snap.Parent.IsZero() {
		t.Errorf("Parent = %+v, want the zero Parent: the recorded issue has epic: null", snap.Parent)
	}
}

// The capability-off acceptance criterion, executable: no merge request
// information anywhere, and no error about its absence.
func TestNoMergeRequestInformationIsServed(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	if snap.Capabilities.PullRequests {
		t.Error("Capabilities.PullRequests = true; merge request correlation is #15's")
	}
	for i, ticket := range snap.Tickets {
		if ticket.PullRequests != nil {
			t.Errorf("Tickets[%d].PullRequests = %+v, want nil", i, ticket.PullRequests)
		}
	}

	var buf strings.Builder
	if err := plain.RenderEpic(&buf, snap); err != nil {
		t.Fatalf("RenderEpic: %v", err)
	}
	if strings.Contains(strings.ToLower(buf.String()), "pull request") ||
		strings.Contains(strings.ToLower(buf.String()), "merge request") {
		t.Errorf("the report mentions merge requests:\n%s", buf.String())
	}
}

// A Ref this driver cannot serve is rejected before any request leaves the
// process.
func TestBadRefsFailBeforeAnyRequest(t *testing.T) {
	tests := []struct {
		name string
		r    ref.Ref
		want string
	}{
		{
			name: "a GitHub Ref",
			r:    ref.Ref{Tracker: ref.TrackerGitHub, Owner: "acme", Repo: "widgets", Number: 7, Raw: "acme/widgets#7"},
			want: "is not a GitLab Epic Ref",
		},
		{
			name: "no path and no Profile project",
			r:    ref.Ref{Tracker: ref.TrackerGitLab, Number: 12, Key: "&12", Raw: "&12"},
			want: "does not name a GitLab group or project",
		},
		{
			name: "an iid of zero",
			r:    ref.Ref{Tracker: ref.TrackerGitLab, Owner: "acme", Repo: "widgets", Raw: "acme/widgets"},
			want: "does not name a GitLab epic or issue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{})
			_, err := newProvider(s).FetchEpic(context.Background(), tt.r)
			if err == nil {
				t.Fatal("FetchEpic succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q, want it to mention %q", err, tt.want)
			}
			if n := len(s.recorded()); n != 0 {
				t.Errorf("%d requests were sent; a bad Ref must fail before any of them", n)
			}
		})
	}
}

func TestFetchEpicFailures(t *testing.T) {
	tests := []struct {
		name    string
		resp    response
		want    []string
		notWant []string
	}{
		{
			name: "401",
			resp: response{status: http.StatusUnauthorized, body: `{"message":"401 Unauthorized"}`},
			want: []string{"gitlab:", "authentication failed (401)", "glab auth status", "GITLAB_TOKEN"},
		},
		{
			// Epics are Premium/Ultimate, so a 403 on an epic path is a tier
			// problem and says so.
			name: "403 on an epic path",
			resp: response{status: http.StatusForbidden, file: "error_forbidden.json"},
			want: []string{"gitlab:", "GitLab Premium or Ultimate (403)", "gitlab-org&23356"},
		},
		{
			name: "404",
			resp: response{status: http.StatusNotFound, body: `{"message":"404 Group Not Found"}`},
			want: []string{"gitlab:", "gitlab-org&23356", "not found (or you lack access)"},
		},
		{
			name: "429 with Retry-After",
			resp: response{status: http.StatusTooManyRequests, body: `{}`,
				headers: map[string]string{"Retry-After": "60"}},
			want: []string{"rate limit exceeded", "1m0s"},
		},
		{
			name: "429 with only a ratelimit-reset",
			resp: response{status: http.StatusTooManyRequests, body: `{}`,
				headers: map[string]string{"ratelimit-reset": "1755000000"}},
			want: []string{"rate limit exceeded", "retry after"},
		},
		{
			name: "429 with nothing to say",
			resp: response{status: http.StatusTooManyRequests, body: `{}`},
			want: []string{"rate limit exceeded", "an unknown time"},
		},
		{
			name: "an error payload",
			resp: response{status: http.StatusInternalServerError, file: "error_message.json"},
			want: []string{"gitlab: API error:", "500 Internal Server Error"},
		},
		{
			name: "a status with nothing to say",
			resp: response{status: http.StatusBadGateway, body: `<html>bad gateway</html>`},
			want: []string{"unexpected response 502"},
		},
		{
			name: "malformed JSON",
			resp: response{body: `{"iid": `},
			want: []string{"decoding the response from"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{epicPath: {tt.resp}})

			_, err := newProvider(s).FetchEpic(context.Background(), epicRef)
			if err == nil {
				t.Fatal("FetchEpic succeeded, want an error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q, want it to mention %q", err, want)
				}
			}
			// A credential never reaches an error message.
			if strings.Contains(err.Error(), fixtureToken) {
				t.Error("the token reached an error message")
			}
		})
	}
}

// A 403 away from an epic endpoint is a permission problem, not a tier one.
func TestForbiddenOnANonEpicPath(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		issuePath: {{status: http.StatusForbidden, body: `{"message":"403 Forbidden"}`}},
	})

	_, err := newProvider(s).FetchEpic(context.Background(), issueRef)
	if err == nil {
		t.Fatal("FetchEpic succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "access denied (403)") {
		t.Errorf("error %q, want the plain access-denied wording", err)
	}
	if strings.Contains(err.Error(), "Premium") {
		t.Errorf("error %q, want no tier claim about a project endpoint", err)
	}
}

// A paging API that contradicts itself is an error, not a loop: following a
// next page that is not after this one is how a polling tool spins forever.
func TestPaginationRefusesToLoop(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		epicPath: {{file: "epic.json"}},
		epicIssuesPath: {
			{file: "epic_children_page1.json", headers: map[string]string{"x-next-page": "1"}},
		},
	})

	_, err := newProvider(s).FetchEpic(context.Background(), epicRef)
	if err == nil {
		t.Fatal("FetchEpic succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "which is not after page 1") {
		t.Errorf("error %q, want it to name the contradiction", err)
	}
}

func TestPaginationIsBounded(t *testing.T) {
	// A server that always promises one more page: without a bound this fetch
	// never returns.
	page := 0
	s := newReplayServer(t, map[string][]response{
		epicPath:       {{file: "epic.json"}},
		epicIssuesPath: nil,
	})
	s.responses[epicIssuesPath] = func() []response {
		out := make([]response, 0, 200)
		for i := 0; i < 200; i++ {
			page++
			out = append(out, response{
				file:    "epic_children_empty.json",
				headers: map[string]string{"x-next-page": fmt.Sprint(page + 1)},
			})
		}
		return out
	}()

	_, err := newProvider(s).FetchEpic(context.Background(), epicRef)
	if err == nil {
		t.Fatal("FetchEpic succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "refusing to keep paging") {
		t.Errorf("error %q, want the bound to name itself", err)
	}
}

// The polled path: two consecutive fetches are equal, re-issue the same
// requests, and resolve the token exactly once.
func TestPollingIsStableAndResolvesTheTokenOnce(t *testing.T) {
	// One page, so that the replay server's "the last response repeats" rule
	// serves the same epic to both fetches rather than advancing through pages.
	s := newReplayServer(t, map[string][]response{
		epicPath:       {{file: "epic.json"}},
		epicIssuesPath: {{file: "epic_children_page1.json"}},
	})

	var tokens int
	p := newProvider(s, gitlab.WithTokenSource(func(context.Context, string) (string, error) {
		tokens++
		return fixtureToken, nil
	}))

	first, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("first FetchEpic: %v", err)
	}
	second, err := p.FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("second FetchEpic: %v", err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Error("two consecutive snapshots of an unchanged epic differ")
	}
	if n := len(s.requestsTo(epicPath)); n != 2 {
		t.Errorf("%d epic requests across two fetches, want 2", n)
	}
	if tokens != 1 {
		t.Errorf("the token was resolved %d times; a 60s poll must not re-resolve forever", tokens)
	}
}

// A token that cannot be found fails the fetch before any request, with an
// error naming both fixes and no credential.
func TestNoTokenFailsWithoutARequest(t *testing.T) {
	s := newReplayServer(t, map[string][]response{})
	p := gitlab.New(fixtureHost,
		gitlab.WithBaseURL(s.URL),
		gitlab.WithTokenSource(func(context.Context, string) (string, error) {
			return "", fmt.Errorf(`no GitLab token found: run "glab auth login" or set GITLAB_TOKEN`)
		}))

	_, err := p.FetchEpic(context.Background(), epicRef)
	if err == nil {
		t.Fatal("FetchEpic succeeded with no token, want an error")
	}
	for _, want := range []string{"gitlab:", "glab auth login", "GITLAB_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q, want it to mention %q", err, want)
		}
	}
	if n := len(s.recorded()); n != 0 {
		t.Errorf("%d requests were sent without a token", n)
	}
}
