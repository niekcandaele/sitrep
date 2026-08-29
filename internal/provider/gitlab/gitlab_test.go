package gitlab_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/gitlab"
	"github.com/niekcandaele/sitrep/internal/provider/providertest"
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
	issueMRsPath   = issuePath + "/closed_by"

	// The milestone endpoints, in both scopes. The lookup is the *list* path
	// with an iids[] filter; the issues path carries the resolved database id,
	// which is §2.1's whole point.
	projectMilestonesPath     = "/api/v4/projects/gitlab-org%2Fcli/milestones"
	projectMilestoneIssues    = projectMilestonesPath + "/6239395/issues"
	groupMilestonesPath       = "/api/v4/groups/gitlab-org/milestones"
	groupMilestoneIssuesPath  = groupMilestonesPath + "/6239400/issues"
	mergeRequestApprovalsPath = "/api/v4/projects/gitlab-org%2Fcli/merge_requests/3761/approvals"
)

// mergeRequestsPath is the closing-linkage endpoint for one issue: the one that
// decides which merge requests are moving a Ticket.
func mergeRequestsPath(project string, iid int) string {
	return fmt.Sprintf("/api/v4/projects/%s/issues/%d/closed_by",
		url.PathEscape(project), iid)
}

// relatedSuffix is the wider list correlate reads for head_pipeline alone. The
// replay server answers it from the closed_by queue for the same issue, because
// the fixtures are one payload in one shape and configuring both by hand would
// be two copies to drift.
const relatedSuffix = "/related_merge_requests"

// relatedMergeRequestsPath is that wider list's path for one issue, for the
// tests that configure the two endpoints separately.
func relatedMergeRequestsPath(project string, iid int) string {
	return fmt.Sprintf("/api/v4/projects/%s/issues/%d%s",
		url.PathEscape(project), iid, relatedSuffix)
}

// closedBySibling maps a related_merge_requests path onto the closed_by path
// for the same issue, or "" for any other path.
func closedBySibling(path string) string {
	if !strings.HasSuffix(path, relatedSuffix) {
		return ""
	}
	return strings.TrimSuffix(path, relatedSuffix) + "/closed_by"
}

// approvalsFallback maps any merge request's approvals path onto the one a test
// configured. Which merge request leads is the lead rule's business and several
// fixtures hold three of them, so a test about approvals says what the answer is
// and not which iid it hangs off.
func approvalsFallback(path string) string {
	if !strings.HasSuffix(path, "/approvals") {
		return ""
	}
	return mergeRequestApprovalsPath
}

// noMergeRequests answers every listed issue's correlation request with an empty
// array, which is what a test about something other than merge requests wants.
func noMergeRequests(responses map[string][]response, project string, iids ...int) map[string][]response {
	for _, iid := range iids {
		responses[mergeRequestsPath(project, iid)] = []response{{file: "closed_by_empty.json"}}
	}
	return responses
}

// The Ref and the TicketIDs the fixtures were written for.
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
	method   string
	path     string
	rawQuery string
	query    map[string][]string
	headers  http.Header
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

	// onRequest, when set, runs as each request arrives. It is how a
	// cancellation test cancels at a real moment in the fan-out rather than
	// guessing one with a sleep.
	onRequest func(path string)
}

func newReplayServer(t *testing.T, responses map[string][]response) *replayServer {
	t.Helper()

	s := &replayServer{responses: responses, served: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, recordedRequest{
			method:   r.Method,
			path:     r.URL.EscapedPath(),
			rawQuery: r.URL.RawQuery,
			query:    r.URL.Query(),
			headers:  r.Header.Clone(),
		})
		path := r.URL.EscapedPath()
		if _, configured := s.responses[path]; !configured {
			for _, fallback := range []string{closedBySibling(path), approvalsFallback(path)} {
				if fallback == "" {
					continue
				}
				if _, ok := s.responses[fallback]; ok {
					path = fallback
					break
				}
			}
		}
		queue, ok := s.responses[path]
		n := s.served[path]
		s.served[path]++
		hook := s.onRequest
		s.mu.Unlock()

		if hook != nil {
			hook(r.URL.EscapedPath())
		}

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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// fullEpic serves the two-page fixture epic, with every child correlating to no
// merge requests. A test about merge requests configures its own answers.
func fullEpic(t *testing.T) *replayServer {
	t.Helper()
	responses := map[string][]response{
		epicPath: {{file: "epic.json"}},
		epicIssuesPath: {
			{file: "epic_children_page1.json", headers: map[string]string{"x-next-page": "2"}},
			{file: "epic_children_page2.json", headers: map[string]string{"x-next-page": ""}},
		},
	}
	noMergeRequests(responses, "gitlab-org/cli", 101, 102, 103, 104, 105, 106, 107, 108, 109)
	noMergeRequests(responses, "gitlab-org/platform/core", 7)
	// Whichever merge request a test's fixture makes the lead, its approvals
	// come from here; approvalsFallback routes every iid to this one path.
	responses[mergeRequestApprovalsPath] = []response{{file: "approvals_pending.json"}}
	return newReplayServer(t, responses)
}

const (
	queryGlobalIssues  = "/api/v4/issues"
	queryCLI101Path    = "/api/v4/projects/gitlab-org%2Fcli/issues/101"
	queryPlatform7Path = "/api/v4/projects/gitlab-org%2Fplatform%2Fcore/issues/7"
	queryCLI101MRs     = queryCLI101Path + "/closed_by"
	queryPlatform7MRs  = queryPlatform7Path + "/closed_by"

	queryIssueCLI101 = `{"iid":101,"project_id":34675721,"references":{"full":"gitlab-org/cli#101"}}`
	queryIssueCore7  = `{"iid":7,"project_id":51000123,"references":{"full":"gitlab-org/platform/core#7"}}`
)

func queryPage(issues string, next string) response {
	return response{
		body:    "[" + issues + "]",
		headers: map[string]string{"X-Next-Page": next},
	}
}

func queryResponses(pages ...response) map[string][]response {
	return map[string][]response{
		queryGlobalIssues:  pages,
		queryCLI101Path:    {{file: "query_issue_cli_101.json"}},
		queryPlatform7Path: {{file: "query_issue_core_7.json"}},
		queryCLI101MRs:     {{file: "closed_by_empty.json"}},
		queryPlatform7MRs:  {{file: "closed_by_empty.json"}},
	}
}

func ticketKeys(tickets []model.Ticket) []string {
	keys := make([]string, len(tickets))
	for i := range tickets {
		keys[i] = tickets[i].Key
	}
	return keys
}

func syntheticGitLabIssue(iid int) map[string]any {
	return map[string]any{
		"id":         100000 + iid,
		"iid":        iid,
		"project_id": 7001,
		"title":      "Exact " + strconv.Itoa(iid),
		"state":      "opened",
		"labels":     []string{},
		"assignees":  []any{},
		"web_url":    "https://gitlab.com/acme/widgets/-/issues/" + strconv.Itoa(iid),
		"references": map[string]any{
			"short":    "#" + strconv.Itoa(iid),
			"relative": "#" + strconv.Itoa(iid),
			"full":     "acme/widgets#" + strconv.Itoa(iid),
		},
	}
}

func syntheticGitLabRef(iid int) ref.Ref {
	return ref.Ref{
		Tracker: ref.TrackerGitLab,
		Host:    fixtureHost,
		Owner:   "acme",
		Repo:    "widgets",
		Number:  iid,
		Raw:     "acme/widgets#" + strconv.Itoa(iid),
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func providerAtGitLabServer(rawURL string, opts ...gitlab.Option) *gitlab.Provider {
	base := []gitlab.Option{
		gitlab.WithBaseURL(rawURL),
		gitlab.WithUserAgent("sitrep/test"),
		gitlab.WithTokenSource(func(context.Context, string) (string, error) {
			return fixtureToken, nil
		}),
	}
	return gitlab.New(fixtureHost, append(base, opts...)...)
}

func receiveWithin[T any](t *testing.T, ch <-chan T, event string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", event)
		var zero T
		return zero
	}
}

func exactIssueIID(path string) (int, bool) {
	const prefix = "/api/v4/projects/acme%2Fwidgets/issues/"
	if !strings.HasPrefix(path, prefix) {
		return 0, false
	}
	suffix := strings.TrimPrefix(path, prefix)
	if strings.Contains(suffix, "/") {
		return 0, false
	}
	iid, err := strconv.Atoi(suffix)
	return iid, err == nil
}

func TestName(t *testing.T) {
	if p := gitlab.New(fixtureHost); p.Name() != "gitlab" {
		t.Errorf("Name() = %q, want %q", p.Name(), "gitlab")
	}
}

func TestCapabilities(t *testing.T) {
	want := model.Capabilities{
		Hierarchy: true, BlockingLinks: true, Comments: true, PullRequests: true,
		Selectors: model.SelectorCapabilities{Epic: true, RefList: true, Query: true},
	}
	if got := gitlab.New(fixtureHost).Capabilities(); got != want {
		t.Errorf("Capabilities() = %+v, want %+v", got, want)
	}
}

func TestResolveNormalizesTheEpic(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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
	if snap.Header != provider.EpicHeader(want) {
		t.Errorf("Header = %+v, want the Epic display identity", snap.Header)
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
		// A child reached through the epic-issues endpoint hangs directly off
		// the Epic, which model.Ticket documents as an empty ParentID — the same
		// shape the GitHub driver produces for the same situation.
		if got.ParentID != "" {
			t.Errorf("Tickets[%d].ParentID = %q, want it empty for a direct child", i, got.ParentID)
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
func TestResolvePutsNoDescriptionOnTheHotPath(t *testing.T) {
	p := newProvider(fullEpic(t))

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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

// ADR-0002 with teeth, across every endpoint this driver reads: the epic, its
// children, a milestone, its children, related merge requests and approvals.
func TestEveryRequestIsAGetIncludingMilestonesAndMergeRequests(t *testing.T) {
	epic := fullEpic(t)
	epic.responses[mergeRequestsPath("gitlab-org/cli", 101)] = []response{{file: "closed_by.json"}}
	epic.responses[mergeRequestApprovalsPath] = []response{{file: "approvals_pending.json"}}
	milestone := fullProjectMilestone(t)

	if _, err := newProvider(epic).Resolve(context.Background(), provider.EpicSelector{Ref: epicRef}); err != nil {
		t.Fatalf("Resolve on the epic: %v", err)
	}
	if _, err := newProvider(milestone).Resolve(context.Background(), provider.EpicSelector{Ref: projectMilestoneRef}); err != nil {
		t.Fatalf("Resolve on the milestone: %v", err)
	}

	var seen int
	for _, s := range []*replayServer{epic, milestone} {
		for _, r := range s.recorded() {
			seen++
			if r.method != http.MethodGet {
				t.Errorf("the driver sent %s %s; this Tracker driver is read-only", r.method, r.path)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no requests were recorded")
	}
	// The endpoints this ticket added are among the ones just proven read-only.
	for _, want := range []string{"/milestones", "/closed_by", "/approvals"} {
		var found bool
		for _, s := range []*replayServer{epic, milestone} {
			for _, r := range s.recorded() {
				found = found || strings.Contains(r.path, want)
			}
		}
		if !found {
			t.Errorf("no request to %s was recorded; this test proves nothing about it", want)
		}
	}
}

func TestResolveSendsExactlyWhatItNeeds(t *testing.T) {
	s := fullEpic(t)
	p := newProvider(s)

	if _, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef}); err != nil {
		t.Fatalf("Resolve: %v", err)
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
	if _, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: r}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if n := len(s.requestsTo(path)); n != 1 {
		t.Errorf("%d requests to the escaped epic path, want 1", n)
	}
}

// An epic someone pointed sitrep at that has no children is a Ticket, not an
// error — and its Tickets slice is empty rather than null.
func TestResolveWithNoChildren(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		epicPath:       {{file: "epic.json"}},
		epicIssuesPath: {{file: "epic_children_empty.json"}},
	})

	snap, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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
func TestResolveReportsItsOwnParent(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		epicPath:       {{file: "epic_with_parent.json"}},
		epicIssuesPath: {{file: "epic_children_empty.json"}},
	})

	snap, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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
func TestResolveOnAnIssueRef(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		issuePath:    {{file: "issue_with_epic.json"}},
		issueMRsPath: {{file: "closed_by_empty.json"}},
	})

	snap, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: issueRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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
	if snap.Header != provider.EpicHeader(wantEpic) {
		t.Errorf("Header = %+v, want the decoded issue's display identity", snap.Header)
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

func TestResolveOnAnIssueWithNoEpic(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		issuePath:    {{file: "issue.json"}},
		issueMRsPath: {{file: "closed_by_empty.json"}},
	})

	snap, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: issueRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !snap.Parent.IsZero() {
		t.Errorf("Parent = %+v, want the zero Parent: the recorded issue has epic: null", snap.Parent)
	}
}

// The PullRequests Capability contract is executable: the declared Capability,
// served merge-request data, and renderer-visible output agree.
func TestMergeRequestInformationIsServed(t *testing.T) {
	responses := map[string][]response{
		epicPath:       {{file: "epic.json"}},
		epicIssuesPath: {{file: "epic_children_page1.json"}},
	}
	noMergeRequests(responses, "gitlab-org/cli", 102, 103, 104)
	noMergeRequests(responses, "gitlab-org/platform/core", 7)
	responses[mergeRequestsPath("gitlab-org/cli", 101)] = []response{{file: "closed_by.json"}}
	responses[mergeRequestApprovalsPath] = []response{{file: "approvals_approved.json"}}
	s := newReplayServer(t, responses)
	p := newProvider(s)

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !snap.Capabilities.PullRequests {
		t.Error("Capabilities.PullRequests = false; this driver correlates merge requests")
	}

	prs := snap.Tickets[0].PullRequests
	if len(prs) != 1 {
		t.Fatalf("Tickets[0].PullRequests = %+v, want the one related merge request", prs)
	}
	want := model.PullRequest{
		Number:     3761,
		Title:      "fix(telemetry): attribute events to the executed command",
		URL:        "https://gitlab.com/gitlab-org/cli/-/merge_requests/3761",
		Repository: "gitlab-org/cli",
		State:      model.PROpen,
		Review:     model.ReviewApproved,
		Checks:     model.ChecksPassing,
	}
	if prs[0] != want {
		t.Errorf("PullRequests[0] = %+v, want %+v", prs[0], want)
	}
	// An open Ticket with an open merge request is being worked on right now.
	if snap.Tickets[0].Status != model.StatusInProgress {
		t.Errorf("Tickets[0].Status = %v, want InProgress", snap.Tickets[0].Status)
	}
	// A Ticket with no related merge requests carries nil, not an empty slice.
	if snap.Tickets[1].PullRequests != nil {
		t.Errorf("Tickets[1].PullRequests = %+v, want nil", snap.Tickets[1].PullRequests)
	}

	var buf strings.Builder
	if err := plain.RenderWatchlist(&buf, snap); err != nil {
		t.Fatalf("RenderWatchlist: %v", err)
	}
	// The renderer is #7's and spells a pull request "#3761" whichever Tracker
	// served it; the driver's job is only to put the data where it looks.
	for _, want := range []string{"#3761", "open", "ci ok"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the report does not mention %q:\n%s", want, buf.String())
		}
	}
}

func TestResolveQuerySearchesMembershipThenReadsExactIssues(t *testing.T) {
	const (
		query         = "state=opened&labels=agent%20ready&scope=all&per_page=7"
		globalIssues  = "/api/v4/issues"
		cli101Path    = "/api/v4/projects/gitlab-org%2Fcli/issues/101"
		platform7Path = "/api/v4/projects/gitlab-org%2Fplatform%2Fcore/issues/7"
		cli101MRs     = cli101Path + "/closed_by"
		platform7MRs  = platform7Path + "/closed_by"
	)
	responses := map[string][]response{
		globalIssues:  {{file: "query_membership.json"}},
		cli101Path:    {{file: "query_issue_cli_101.json"}},
		platform7Path: {{file: "query_issue_core_7.json"}},
		cli101MRs:     {{file: "closed_by_empty.json"}},
		platform7MRs:  {{file: "closed_by_empty.json"}},
	}
	s := newReplayServer(t, responses)
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
	wantKeys := []string{"gitlab-org/cli#101", "gitlab-org/platform/core#7"}
	wantTitles := []string{"Ship the epic monitor", "Publish the release archives"}
	if len(snap.Tickets) != len(wantKeys) {
		t.Fatalf("Tickets = %d, want %d", len(snap.Tickets), len(wantKeys))
	}
	for i := range wantKeys {
		if snap.Tickets[i].Key != wantKeys[i] || snap.Tickets[i].Title != wantTitles[i] {
			t.Errorf("Tickets[%d] = {%q %q}, want {%q %q} from direct state",
				i, snap.Tickets[i].Key, snap.Tickets[i].Title, wantKeys[i], wantTitles[i])
		}
	}

	searches := s.requestsTo(globalIssues)
	if len(searches) != 1 {
		t.Fatalf("membership searches = %d, want one maximal first page", len(searches))
	}
	membership := searches[0]
	if membership.method != http.MethodGet {
		t.Errorf("membership method = %s, want GET", membership.method)
	}
	if got, want := membership.rawQuery, query+"&per_page=100&page=1"; got != want {
		t.Errorf("raw membership query = %q, want opaque bytes plus bound %q", got, want)
	}
	if got := membership.query["per_page"]; !reflect.DeepEqual(got, []string{"7", "100"}) {
		t.Errorf("per_page values = %v, want original value plus appended first-page bound", got)
	}
	for _, path := range []string{cli101Path, platform7Path, cli101MRs, platform7MRs} {
		if got := len(s.requestsTo(path)); got != 1 {
			t.Errorf("requests to %s = %d, want 1", path, got)
		}
	}
	if got := len(s.recorded()); got != 5 {
		t.Errorf("all requests = %d, want membership, two roots and two correlation reads", got)
	}
	for _, request := range s.recorded() {
		if strings.Contains(request.path, "/notes") || strings.Contains(request.path, "/links") {
			t.Errorf("Query Resolve requested Detail path %s", request.path)
		}
	}
}

func TestResolveQueryPaginatesBeforeExactReads(t *testing.T) {
	const query = "state=opened&per_page=7&page=44&search=ready%20%26%20waiting"
	responses := queryResponses(
		queryPage(queryIssueCLI101+","+queryIssueCLI101, "2"),
		queryPage(queryIssueCore7, ""),
	)
	s := newReplayServer(t, responses)
	p := newProvider(s, gitlab.WithMaxTickets(4))

	snap, err := p.Resolve(context.Background(), provider.QuerySelector{Query: query})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.LimitReached {
		t.Error("LimitReached = true below an exhausted budget")
	}
	wantKeys := []string{"gitlab-org/cli#101", "gitlab-org/platform/core#7"}
	if got := ticketKeys(snap.Tickets); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("Ticket keys = %v, want %v", got, wantKeys)
	}

	searches := s.requestsTo(queryGlobalIssues)
	if len(searches) != 2 {
		t.Fatalf("membership searches = %d, want two", len(searches))
	}
	all := s.recorded()
	if len(all) < 2 || all[0].path != queryGlobalIssues || all[1].path != queryGlobalIssues {
		t.Fatalf("first requests = %+v, want both membership pages before exact reads", all[:min(len(all), 2)])
	}
	for i, wantSuffix := range []string{"&per_page=4&page=1", "&per_page=4&page=2"} {
		if got, want := searches[i].rawQuery, query+wantSuffix; got != want {
			t.Errorf("page %d raw query = %q, want exact opaque prefix %q", i+1, got, want)
		}
	}
	if got := searches[0].query["per_page"]; !reflect.DeepEqual(got, []string{"7", "4"}) {
		t.Errorf("page one per_page values = %v, want user value followed by Provider value", got)
	}
	if got := searches[1].query["page"]; !reflect.DeepEqual(got, []string{"44", "2"}) {
		t.Errorf("page two page values = %v, want user value followed by Provider value", got)
	}
	for _, path := range []string{queryCLI101Path, queryPlatform7Path} {
		if got := len(s.requestsTo(path)); got != 1 {
			t.Errorf("authoritative reads to %s = %d, want one after membership", path, got)
		}
	}
}

func TestResolveQueryKeepsOffsetPageSizeStable(t *testing.T) {
	const total = 102
	var mu sync.Mutex
	var membershipQueries []url.Values

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		switch {
		case path == queryGlobalIssues:
			values := r.URL.Query()
			perPageValues := values["per_page"]
			pageValues := values["page"]
			if len(perPageValues) == 0 || len(pageValues) == 0 {
				t.Errorf("membership query omits Provider paging: %q", r.URL.RawQuery)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			perPage, err := strconv.Atoi(perPageValues[len(perPageValues)-1])
			if err != nil {
				t.Errorf("per_page = %q: %v", perPageValues[len(perPageValues)-1], err)
			}
			page, err := strconv.Atoi(pageValues[len(pageValues)-1])
			if err != nil {
				t.Errorf("page = %q: %v", pageValues[len(pageValues)-1], err)
			}
			start := (page - 1) * perPage
			end := min(start+perPage, total)
			issues := make([]map[string]any, 0, max(0, end-start))
			for iid := start + 1; iid <= end; iid++ {
				issues = append(issues, syntheticGitLabIssue(iid))
			}
			if end < total {
				w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
			}
			mu.Lock()
			membershipQueries = append(membershipQueries, values)
			mu.Unlock()
			writeTestJSON(t, w, issues)

		case strings.HasSuffix(path, "/closed_by"):
			writeTestJSON(t, w, []any{})

		default:
			iid, ok := exactIssueIID(path)
			if !ok {
				t.Errorf("unexpected request path %s", path)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			writeTestJSON(t, w, syntheticGitLabIssue(iid))
		}
	}))
	t.Cleanup(s.Close)

	p := providerAtGitLabServer(s.URL, gitlab.WithMaxTickets(101))
	snap, err := p.Resolve(context.Background(), provider.QuerySelector{
		Query: "state=opened&per_page=7&page=44",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !snap.LimitReached {
		t.Error("LimitReached = false, want evidence for the clipped 102nd result")
	}
	if len(snap.Tickets) != 101 {
		t.Fatalf("Tickets = %d, want 101", len(snap.Tickets))
	}
	for i, ticket := range snap.Tickets {
		want := "acme/widgets#" + strconv.Itoa(i+1)
		if ticket.Key != want {
			t.Errorf("Tickets[%d].Key = %q, want %q", i, ticket.Key, want)
			break
		}
	}

	mu.Lock()
	queries := append([]url.Values(nil), membershipQueries...)
	mu.Unlock()
	if len(queries) != 2 {
		t.Fatalf("membership requests = %d, want 2", len(queries))
	}
	for i, values := range queries {
		perPageValues := values["per_page"]
		pageValues := values["page"]
		if got := perPageValues[len(perPageValues)-1]; got != "100" {
			t.Errorf("page %d Provider per_page = %q, want stable 100", i+1, got)
		}
		if got := pageValues[len(pageValues)-1]; got != strconv.Itoa(i+1) {
			t.Errorf("request %d Provider page = %q, want %d", i+1, got, i+1)
		}
	}
}

func TestResolveQueryCutoffAccounting(t *testing.T) {
	tests := []struct {
		name       string
		issues     string
		next       string
		maxTickets int
		wantKeys   []string
		wantLimit  bool
	}{
		{
			name:       "exact boundary exhausted",
			issues:     queryIssueCLI101 + "," + queryIssueCore7,
			maxTickets: 2,
			wantKeys:   []string{"gitlab-org/cli#101", "gitlab-org/platform/core#7"},
		},
		{
			name:       "exact boundary with unused malformed continuation",
			issues:     queryIssueCLI101 + "," + queryIssueCore7,
			next:       "not-a-page",
			maxTickets: 2,
			wantKeys:   []string{"gitlab-org/cli#101", "gitlab-org/platform/core#7"},
			wantLimit:  true,
		},
		{
			name:       "oversized response is clipped",
			issues:     queryIssueCLI101 + "," + queryIssueCore7 + "," + queryIssueCLI101,
			maxTickets: 2,
			wantKeys:   []string{"gitlab-org/cli#101", "gitlab-org/platform/core#7"},
			wantLimit:  true,
		},
		{
			name:       "duplicate consumes native budget before de-duplication",
			issues:     queryIssueCLI101 + "," + queryIssueCLI101,
			next:       "2",
			maxTickets: 2,
			wantKeys:   []string{"gitlab-org/cli#101"},
			wantLimit:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, queryResponses(queryPage(tt.issues, tt.next)))
			snap, err := newProvider(s, gitlab.WithMaxTickets(tt.maxTickets)).Resolve(
				context.Background(), provider.QuerySelector{Query: "state=opened"})
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

func TestResolveQueryFollowsKeysetContinuation(t *testing.T) {
	const query = "state=opened&search=ready&order_by=iid&sort=asc"
	first := queryPage(queryIssueCLI101, "")
	first.headers = map[string]string{
		"X-Next-Cursor": "part,second",
		"Link": "</api/v4/issues?per_page=4&cursor=before>; rel=\"prev\", " +
			"</api/v4/issues?sort=asc&search=rea%64y&pagination=keyset&order_by=%69id&state=opened&per_page=4&cursor=part%2Csecond>; rel=\"next\"",
	}
	s := newReplayServer(t, queryResponses(first, queryPage(queryIssueCore7, "")))

	snap, err := newProvider(s, gitlab.WithMaxTickets(4)).Resolve(
		context.Background(), provider.QuerySelector{Query: query})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.LimitReached {
		t.Error("LimitReached = true after the keyset continuation exhausted")
	}
	if got, want := ticketKeys(snap.Tickets), []string{
		"gitlab-org/cli#101", "gitlab-org/platform/core#7",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("Ticket keys = %v, want %v", got, want)
	}

	searches := s.requestsTo(queryGlobalIssues)
	if len(searches) != 2 {
		t.Fatalf("membership requests = %d, want two", len(searches))
	}
	if got, want := searches[0].rawQuery, query+"&per_page=4&page=1"; got != want {
		t.Errorf("first membership query = %q, want %q", got, want)
	}
	if got, want := searches[1].rawQuery,
		"sort=asc&search=rea%64y&pagination=keyset&order_by=%69id&state=opened&per_page=4&cursor=part%2Csecond"; got != want {
		t.Errorf("keyset continuation query = %q, want Tracker-supplied %q", got, want)
	}
	all := s.recorded()
	if len(all) < 2 || all[0].path != queryGlobalIssues || all[1].path != queryGlobalIssues {
		t.Errorf("request order = %+v, want all membership pages before exact roots", all)
	}
}

func TestResolveQueryRejectsContinuationMembershipChanges(t *testing.T) {
	const defaultQuery = "state=opened&search=SECRET_QUERY_47"
	tests := []struct {
		name      string
		query     string
		nextQuery string
	}{
		{name: "dropped filter", query: defaultQuery, nextQuery: "state=opened&per_page=4&cursor=next"},
		{name: "changed value", query: defaultQuery, nextQuery: "state=closed&search=SECRET_QUERY_47&per_page=4&cursor=next"},
		{name: "duplicate changed value", query: defaultQuery, nextQuery: "state=opened&state=closed&search=SECRET_QUERY_47&per_page=4&cursor=next"},
		{name: "added filter", query: defaultQuery, nextQuery: "state=opened&search=SECRET_QUERY_47&milestone=7&per_page=4&cursor=next"},
		{name: "added order by", query: defaultQuery, nextQuery: "state=opened&search=SECRET_QUERY_47&order_by=iid&per_page=4&cursor=next"},
		{name: "added sort", query: defaultQuery, nextQuery: "state=opened&search=SECRET_QUERY_47&sort=asc&per_page=4&cursor=next"},
		{
			name: "changed original order by", query: defaultQuery + "&order_by=iid",
			nextQuery: "state=opened&search=SECRET_QUERY_47&order_by=created_at&per_page=4&cursor=next",
		},
		{
			name: "changed original sort", query: defaultQuery + "&sort=asc",
			nextQuery: "state=opened&search=SECRET_QUERY_47&sort=desc&per_page=4&cursor=next",
		},
		{
			name: "conflicting duplicate ordering control", query: defaultQuery + "&order_by=iid&sort=asc",
			nextQuery: "state=opened&search=SECRET_QUERY_47&order_by=iid&sort=asc&sort=desc&per_page=4&cursor=next",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := queryPage(queryIssueCLI101, "")
			first.headers = map[string]string{
				"X-Next-Cursor": "next",
				"Link":          "</api/v4/issues?" + tt.nextQuery + ">; rel=\"next\"",
			}
			s := newReplayServer(t, map[string][]response{queryGlobalIssues: {first}})

			snap, err := newProvider(s, gitlab.WithMaxTickets(4)).Resolve(
				context.Background(), provider.QuerySelector{Query: tt.query})
			providertest.CheckError(t, "gitlab", err, providertest.Want{
				Kind:     provider.KindUnavailable,
				Contains: []string{"pagination", "Selector membership"},
				Secret:   tt.query,
			})
			if strings.Contains(err.Error(), fixtureToken) || strings.Contains(err.Error(), "SECRET_QUERY_47") {
				t.Errorf("error = %q, leaked credential or Query", err)
			}
			if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
				t.Errorf("snapshot = %+v, want no partial membership", snap)
			}
			if got := len(s.requestsTo(queryGlobalIssues)); got != 1 {
				t.Errorf("membership requests = %d, want 1", got)
			}
			if got := len(s.recorded()) - len(s.requestsTo(queryGlobalIssues)); got != 0 {
				t.Errorf("exact reads = %d, want none", got)
			}
		})
	}
}

func TestResolveQueryReportsUnusedKeysetContinuationAtBudget(t *testing.T) {
	first := queryPage(queryIssueCLI101, "")
	first.headers = map[string]string{
		"X-Next-Cursor": "unused",
		"Link":          "<?search=SECRET_QUERY_47&per_page=1&cursor=unused>; rel=\"next\"",
	}
	s := newReplayServer(t, queryResponses(first))

	snap, err := newProvider(s, gitlab.WithMaxTickets(1)).Resolve(
		context.Background(), provider.QuerySelector{Query: "state=opened"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !snap.LimitReached {
		t.Error("LimitReached = false, want unused keyset continuation evidence")
	}
	if got := len(s.requestsTo(queryGlobalIssues)); got != 1 {
		t.Errorf("membership requests = %d, want no fetch of the unused continuation", got)
	}
}

func TestResolveQueryRejectsMalformedOrCyclicKeysetContinuation(t *testing.T) {
	const secretQuery = "search=SECRET_QUERY_47"
	keysetPage := func(issue, cursor, link string) response {
		return response{
			body: "[" + issue + "]",
			headers: map[string]string{
				"X-Next-Cursor": cursor,
				"Link":          link,
			},
		}
	}
	tests := []struct {
		name  string
		pages []response
	}{
		{
			name:  "cursor without link",
			pages: []response{keysetPage(queryIssueCLI101, "cursor", "")},
		},
		{
			name:  "malformed link",
			pages: []response{keysetPage(queryIssueCLI101, "cursor", "<not-closed; rel=\"next\"")},
		},
		{
			name: "changed membership path",
			pages: []response{keysetPage(queryIssueCLI101, "cursor",
				"</api/v4/projects/other/issues?per_page=3&cursor=x>; rel=\"next\"")},
		},
		{
			name: "changed Provider page size",
			pages: []response{keysetPage(queryIssueCLI101, "cursor",
				"<?search=SECRET_QUERY_47&per_page=1&cursor=x>; rel=\"next\"")},
		},
		{
			name: "fragment",
			pages: []response{keysetPage(queryIssueCLI101, "cursor",
				"<?search=SECRET_QUERY_47&per_page=3&cursor=x#fragment>; rel=\"next\"")},
		},
		{
			name: "repeated continuation",
			pages: []response{
				keysetPage(queryIssueCLI101, "a", "<?search=SECRET_QUERY_47&per_page=3&cursor=a>; rel=\"next\""),
				keysetPage(queryIssueCore7, "a", "<?search=SECRET_QUERY_47&per_page=3&cursor=a>; rel=\"next\""),
			},
		},
		{
			name: "non-adjacent cycle",
			pages: []response{
				keysetPage(queryIssueCLI101, "a", "<?search=SECRET_QUERY_47&per_page=4&cursor=a>; rel=\"next\""),
				keysetPage(queryIssueCore7, "b", "<?search=SECRET_QUERY_47&per_page=4&cursor=b>; rel=\"next\""),
				keysetPage(queryIssueCLI101, "a", "<?search=SECRET_QUERY_47&per_page=4&cursor=a>; rel=\"next\""),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			maxTickets := len(tt.pages) + 1
			s := newReplayServer(t, map[string][]response{queryGlobalIssues: tt.pages})
			snap, err := newProvider(s, gitlab.WithMaxTickets(maxTickets)).Resolve(
				context.Background(), provider.QuerySelector{Query: secretQuery})
			providertest.CheckError(t, "gitlab", err, providertest.Want{
				Kind:     provider.KindUnavailable,
				Contains: []string{"query"},
				Secret:   secretQuery,
			})
			if strings.Contains(err.Error(), fixtureToken) {
				t.Errorf("error = %q, leaked credential", err)
			}
			if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
				t.Errorf("snapshot = %+v, want no partial output", snap)
			}
			if got := len(s.recorded()) - len(s.requestsTo(queryGlobalIssues)); got != 0 {
				t.Errorf("non-membership requests = %d, want none", got)
			}
		})
	}
}

func TestResolveQueryRejectsOffOriginKeysetContinuationWithoutCredentials(t *testing.T) {
	var receiverMu sync.Mutex
	receiverRequests := 0
	receiverAuthorization := ""
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receiverMu.Lock()
		receiverRequests++
		receiverAuthorization = r.Header.Get("Authorization")
		receiverMu.Unlock()
		writeTestJSON(t, w, []any{})
	}))
	t.Cleanup(receiver.Close)

	first := queryPage(queryIssueCLI101, "")
	first.headers = map[string]string{
		"X-Next-Cursor": "outside",
		"Link": receiver.URL +
			"/api/v4/issues?per_page=3&cursor=outside>; rel=\"next\"",
	}
	first.headers["Link"] = "<" + first.headers["Link"]
	s := newReplayServer(t, map[string][]response{queryGlobalIssues: {first}})

	snap, err := newProvider(s, gitlab.WithMaxTickets(3)).Resolve(
		context.Background(), provider.QuerySelector{Query: "search=SECRET_QUERY_47"})
	providertest.CheckError(t, "gitlab", err, providertest.Want{
		Kind:     provider.KindUnavailable,
		Contains: []string{"unsafe next link"},
		Secret:   "SECRET_QUERY_47",
	})
	if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
		t.Errorf("snapshot = %+v, want no partial output", snap)
	}
	receiverMu.Lock()
	gotRequests := receiverRequests
	gotAuthorization := receiverAuthorization
	receiverMu.Unlock()
	if gotRequests != 0 || gotAuthorization != "" {
		t.Errorf("off-origin receiver saw %d requests and Authorization %q, want neither",
			gotRequests, gotAuthorization)
	}
}

func TestResolveQueryRejectsNonProgressingPagination(t *testing.T) {
	tests := []struct {
		name  string
		pages []response
	}{
		{
			name:  "malformed next page",
			pages: []response{queryPage(queryIssueCLI101, "oops")},
		},
		{
			name:  "non-forward next page",
			pages: []response{queryPage(queryIssueCLI101, "1")},
		},
		{
			name: "repeated next page",
			pages: []response{
				queryPage(queryIssueCLI101, "2"),
				queryPage(queryIssueCore7, "2"),
			},
		},
		{
			name: "empty continuation page",
			pages: []response{
				queryPage(queryIssueCLI101, "2"),
				queryPage("", ""),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{queryGlobalIssues: tt.pages})
			snap, err := newProvider(s, gitlab.WithMaxTickets(3)).Resolve(
				context.Background(), provider.QuerySelector{Query: "state=opened"})
			providertest.CheckError(t, "gitlab", err, providertest.Want{
				Kind:     provider.KindUnavailable,
				Contains: []string{"query"},
				Secret:   fixtureToken,
			})
			if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
				t.Errorf("snapshot = %+v, want no partial membership", snap)
			}
			if got := len(s.requestsTo(queryGlobalIssues)); got != len(tt.pages) {
				t.Errorf("membership requests = %d, want %d", got, len(tt.pages))
			}
			if got := len(s.recorded()) - len(s.requestsTo(queryGlobalIssues)); got != 0 {
				t.Errorf("non-membership reads = %d, want none", got)
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
			resp: response{status: http.StatusUnauthorized, body: `{"message":"401 Unauthorized"}`},
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
			want: providertest.Want{Kind: provider.KindUnavailable, Contains: []string{"API error", "down"}, Secret: fixtureToken},
		},
		{
			name: "decode",
			resp: response{body: `[{`},
			want: providertest.Want{Kind: provider.KindUnavailable, Contains: []string{"decoding the response"}, Secret: fixtureToken},
		},
		{
			name: "malformed native query",
			resp: response{status: http.StatusUnprocessableEntity, body: `{"message":{"state":["invalid"]}}`},
			want: providertest.Want{Kind: provider.KindBadRef, Contains: []string{"query rejected"}, Secret: fixtureToken},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{
				queryGlobalIssues: {
					queryPage(queryIssueCLI101, "2"),
					tt.resp,
				},
			})
			snap, err := newProvider(s, gitlab.WithMaxTickets(3)).Resolve(
				context.Background(), provider.QuerySelector{Query: "state=opened"})
			providertest.CheckError(t, "gitlab", err, tt.want)
			if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
				t.Errorf("snapshot = %+v, want no partial membership", snap)
			}
			if got := len(s.requestsTo(queryGlobalIssues)); got != 2 {
				t.Errorf("membership requests = %d, want 2", got)
			}
			if got := len(s.recorded()) - 2; got != 0 {
				t.Errorf("non-membership reads = %d, want none", got)
			}
		})
	}
}

func TestResolveQueryUsesProjectScopedMembershipWhenPathIsConfigured(t *testing.T) {
	const scoped = "/api/v4/projects/gitlab-org%2Fcli/issues"
	s := newReplayServer(t, map[string][]response{
		scoped: {{body: `[]`}},
	})
	p := newProvider(s, gitlab.WithPath("gitlab-org/cli"))
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
	requests := s.recorded()
	if len(requests) != 1 || requests[0].path != scoped || requests[0].rawQuery != "per_page=100&page=1" {
		t.Errorf("requests = %+v, want one project-scoped maximal first page", requests)
	}
}

func TestResolveQueryPreservesLiteralHashInRawQuery(t *testing.T) {
	const (
		query        = "search=#47&labels=agent%23ready"
		globalIssues = "/api/v4/issues"
	)
	s := newReplayServer(t, map[string][]response{globalIssues: {{body: `[]`}}})

	_, err := newProvider(s).Resolve(context.Background(), provider.QuerySelector{Query: query})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	requests := s.requestsTo(globalIssues)
	if len(requests) != 1 {
		t.Fatalf("membership requests = %d, want 1", len(requests))
	}
	if got, want := requests[0].rawQuery, query+"&per_page=100&page=1"; got != want {
		t.Errorf("raw query = %q, want literal bytes %q", got, want)
	}
}

func TestResolveQueryHugeLimitDoesNotPreallocateTheBudget(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		queryGlobalIssues: {{body: `[]`}},
	})
	p := newProvider(s, gitlab.WithMaxTickets(int(^uint(0)>>1)))
	if _, err := p.Resolve(context.Background(), provider.QuerySelector{Query: "q=1"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}

func TestResolveQueryRejectsMalformedNativeQuery(t *testing.T) {
	const (
		globalIssues = "/api/v4/issues"
		query        = "state=wat"
	)
	s := newReplayServer(t, map[string][]response{
		globalIssues: {{
			status: http.StatusUnprocessableEntity,
			body:   `{"message":{"labels":["is invalid"],"query":["state=wat is invalid"],"state":["does not have a valid value"]}}`,
		}},
	})
	snap, err := newProvider(s).Resolve(context.Background(), provider.QuerySelector{Query: query})
	providertest.CheckError(t, "gitlab", err, providertest.Want{
		Kind: provider.KindBadRef,
		Contains: []string{
			"query rejected",
			`labels: ["is invalid"]`,
			`query: ["[query] is invalid"]`,
			`state: ["does not have a valid value"]`,
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
		t.Error("malformed native query must not be retryable")
	}
}

func TestResolveQueryRedactsDecodedAndNormalizedNativeExplanation(t *testing.T) {
	const query = "search=needs%20triage&labels=bug%2fui"
	s := newReplayServer(t, map[string][]response{
		queryGlobalIssues: {{
			status: http.StatusUnprocessableEntity,
			body: `{"message":{"query":["search=needs triage&labels=bug/ui is invalid",` +
				`"labels=bug%2Fui&search=needs+triage was normalized"]}}`,
		}},
	})

	snap, err := newProvider(s).Resolve(context.Background(), provider.QuerySelector{Query: query})
	providertest.CheckError(t, "gitlab", err, providertest.Want{
		Kind:     provider.KindBadRef,
		Contains: []string{"query rejected", "is invalid", "was normalized", "[query]"},
		Secret:   query,
	})
	for _, sensitive := range []string{
		"search=needs triage&labels=bug/ui", "labels=bug%2Fui&search=needs+triage", fixtureToken,
	} {
		if strings.Contains(err.Error(), sensitive) {
			t.Errorf("error = %q, leaked sensitive form %q", err, sensitive)
		}
	}
	if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
		t.Errorf("snapshot = %+v, want no partial output", snap)
	}
	if got := len(s.requestsTo(queryGlobalIssues)); got != 1 {
		t.Errorf("membership requests = %d, want one", got)
	}
	if got := len(s.recorded()) - len(s.requestsTo(queryGlobalIssues)); got != 0 {
		t.Errorf("exact reads = %d, want none", got)
	}
}

func TestResolveQueryPreservesMembershipFailureClasses(t *testing.T) {
	const globalIssues = "/api/v4/issues"
	tests := []struct {
		name string
		resp response
		want providertest.Want
	}{
		{
			name: "auth",
			resp: response{status: http.StatusUnauthorized, body: `{"message":"401 Unauthorized"}`},
			want: providertest.Want{Kind: provider.KindAuth, Contains: []string{"authentication failed"}, Secret: fixtureToken},
		},
		{
			name: "rate limit",
			resp: response{status: http.StatusTooManyRequests, body: `{"message":"Too many requests"}`, headers: map[string]string{"Retry-After": "30"}},
			want: providertest.Want{Kind: provider.KindRateLimit, Contains: []string{"rate limit", "30"}, Secret: fixtureToken},
		},
		{
			name: "decode",
			resp: response{body: `[{`},
			want: providertest.Want{Kind: provider.KindUnavailable, Contains: []string{"decoding the response"}, Secret: fixtureToken},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{globalIssues: {tt.resp}})
			snap, err := newProvider(s).Resolve(context.Background(), provider.QuerySelector{Query: "q=1"})
			providertest.CheckError(t, "gitlab", err, tt.want)
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
	const query = "search=SECRET_QUERY_47"
	transportErr := errors.New("dial failed")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})}
	p := gitlab.New(fixtureHost,
		gitlab.WithTokenSource(func(context.Context, string) (string, error) {
			return fixtureToken, nil
		}),
		gitlab.WithHTTPClient(client),
	)

	snap, err := p.Resolve(context.Background(), provider.QuerySelector{Query: query})
	providertest.CheckError(t, "gitlab", err, providertest.Want{
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

func TestResolveQueryRootFailureReturnsNoPartialSnapshot(t *testing.T) {
	const (
		globalIssues  = "/api/v4/issues"
		cli101Path    = "/api/v4/projects/gitlab-org%2Fcli/issues/101"
		platform7Path = "/api/v4/projects/gitlab-org%2Fplatform%2Fcore/issues/7"
	)
	s := newReplayServer(t, map[string][]response{
		globalIssues:  {{file: "query_membership.json"}},
		cli101Path:    {{file: "query_issue_cli_101.json"}},
		platform7Path: {{status: http.StatusNotFound, body: `{"message":"404 Issue Not Found"}`}},
	})
	snap, err := newProvider(s).Resolve(context.Background(), provider.QuerySelector{Query: "q=1"})
	providertest.CheckError(t, "gitlab", err, providertest.Want{
		Kind:     provider.KindBadRef,
		Contains: []string{"gitlab-org/platform/core#7", "not found"},
		Secret:   fixtureToken,
	})
	if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
		t.Errorf("snapshot = %+v, want no partial membership", snap)
	}
	if len(s.recorded()) != 3 {
		t.Errorf("requests = %d, want membership and roots only", len(s.recorded()))
	}
	for _, request := range s.recorded() {
		if strings.Contains(request.path, "/closed_by") {
			t.Errorf("correlation started after root failure: %s", request.path)
		}
	}
}

func TestResolveQueryRejectsInvalidSearchIdentity(t *testing.T) {
	const globalIssues = "/api/v4/issues"
	s := newReplayServer(t, map[string][]response{
		globalIssues: {{body: `[{"iid":0,"project_id":34675721,"references":{"full":"gitlab-org/cli#0"}}]`}},
	})
	snap, err := newProvider(s).Resolve(context.Background(), provider.QuerySelector{Query: "q=1"})
	providertest.CheckError(t, "gitlab", err, providertest.Want{
		Kind:     provider.KindUnavailable,
		Contains: []string{"invalid identity"},
		Secret:   fixtureToken,
	})
	if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
		t.Errorf("snapshot = %+v, want none", snap)
	}
}

func TestResolveExactRootsAreBoundedConcurrentAndOrdered(t *testing.T) {
	const rootCount = 10
	tests := []struct {
		name         string
		selector     func() provider.Selector
		wantSearches int
	}{
		{
			name: "Query",
			selector: func() provider.Selector {
				return provider.QuerySelector{Query: "state=opened"}
			},
			wantSearches: 1,
		},
		{
			name: "Ref-list",
			selector: func() provider.Selector {
				refs := make([]ref.Ref, rootCount)
				for i := range refs {
					refs[i] = syntheticGitLabRef(i + 1)
				}
				return provider.RefListSelector{Refs: refs}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			started := make(chan int, rootCount)
			release := make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

			var mu sync.Mutex
			inFlight := 0
			maxInFlight := 0
			rootRequests := 0
			correlationRequests := 0
			membershipRequests := 0

			issues := make([]map[string]any, rootCount)
			for i := range issues {
				issues[i] = syntheticGitLabIssue(i + 1)
			}
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := r.URL.EscapedPath()
				switch {
				case path == queryGlobalIssues:
					mu.Lock()
					membershipRequests++
					mu.Unlock()
					writeTestJSON(t, w, issues)
				case strings.HasSuffix(path, "/closed_by"):
					mu.Lock()
					correlationRequests++
					mu.Unlock()
					writeTestJSON(t, w, []any{})
				default:
					iid, ok := exactIssueIID(path)
					if !ok {
						t.Errorf("unexpected request path %s", path)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					mu.Lock()
					rootRequests++
					inFlight++
					maxInFlight = max(maxInFlight, inFlight)
					mu.Unlock()
					started <- iid
					<-release
					mu.Lock()
					inFlight--
					mu.Unlock()
					writeTestJSON(t, w, syntheticGitLabIssue(iid))
				}
			}))
			t.Cleanup(s.Close)

			type resolveResult struct {
				snapshot model.WatchlistSnapshot
				err      error
			}
			result := make(chan resolveResult, 1)
			go func() {
				snapshot, err := providerAtGitLabServer(s.URL, gitlab.WithMaxTickets(20)).Resolve(
					context.Background(), tt.selector())
				result <- resolveResult{snapshot: snapshot, err: err}
			}()

			seenStarts := make(map[int]struct{})
			for range 8 {
				seenStarts[receiveWithin(t, started, "one of the first eight exact roots")] = struct{}{}
			}
			if len(seenStarts) != 8 {
				t.Errorf("distinct roots started = %d, want 8", len(seenStarts))
			}
			mu.Lock()
			gotInFlight := inFlight
			mu.Unlock()
			if gotInFlight != 8 {
				t.Errorf("roots in flight before release = %d, want worker bound 8", gotInFlight)
			}
			releaseOnce.Do(func() { close(release) })

			got := receiveWithin(t, result, "bounded exact-root Resolve")
			if got.err != nil {
				t.Fatalf("Resolve: %v", got.err)
			}
			if len(got.snapshot.Tickets) != rootCount {
				t.Fatalf("Tickets = %d, want %d", len(got.snapshot.Tickets), rootCount)
			}
			for i, ticket := range got.snapshot.Tickets {
				want := "acme/widgets#" + strconv.Itoa(i+1)
				if ticket.Key != want {
					t.Errorf("Tickets[%d].Key = %q, want Selector order %q", i, ticket.Key, want)
				}
			}

			mu.Lock()
			gotMax := maxInFlight
			gotRoots := rootRequests
			gotCorrelations := correlationRequests
			gotSearches := membershipRequests
			mu.Unlock()
			if gotMax != 8 {
				t.Errorf("maximum exact roots in flight = %d, want 8", gotMax)
			}
			if gotRoots != rootCount || gotCorrelations != rootCount {
				t.Errorf("root/correlation requests = %d/%d, want %d/%d",
					gotRoots, gotCorrelations, rootCount, rootCount)
			}
			if gotSearches != tt.wantSearches {
				t.Errorf("membership requests = %d, want %d", gotSearches, tt.wantSearches)
			}
		})
	}
}

func TestResolveQueryExactRootsPreserveOrderAfterOutOfOrderCompletion(t *testing.T) {
	const rootCount = 3
	started := make(chan int, rootCount)
	finished := make(chan int, rootCount)
	releases := make([]chan struct{}, rootCount+1)
	for iid := 1; iid <= rootCount; iid++ {
		releases[iid] = make(chan struct{})
	}
	var releaseOnce [rootCount + 1]sync.Once
	t.Cleanup(func() {
		for iid := 1; iid <= rootCount; iid++ {
			releaseOnce[iid].Do(func() { close(releases[iid]) })
		}
	})

	issues := make([]map[string]any, rootCount)
	for i := range issues {
		issues[i] = syntheticGitLabIssue(i + 1)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		switch {
		case path == queryGlobalIssues:
			writeTestJSON(t, w, issues)
		case strings.HasSuffix(path, "/closed_by"):
			writeTestJSON(t, w, []any{})
		default:
			iid, ok := exactIssueIID(path)
			if !ok {
				t.Errorf("unexpected request path %s", path)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			started <- iid
			<-releases[iid]
			writeTestJSON(t, w, syntheticGitLabIssue(iid))
			finished <- iid
		}
	}))
	t.Cleanup(s.Close)

	type resolveResult struct {
		snapshot model.WatchlistSnapshot
		err      error
	}
	result := make(chan resolveResult, 1)
	go func() {
		snapshot, err := providerAtGitLabServer(s.URL, gitlab.WithMaxTickets(rootCount)).Resolve(
			context.Background(), provider.QuerySelector{Query: "state=opened"})
		result <- resolveResult{snapshot: snapshot, err: err}
	}()
	for range rootCount {
		receiveWithin(t, started, "all out-of-order exact roots")
	}
	for _, iid := range []int{3, 1, 2} {
		releaseOnce[iid].Do(func() { close(releases[iid]) })
		if got := receiveWithin(t, finished, "out-of-order exact-root completion"); got != iid {
			t.Errorf("completed root = %d, want %d", got, iid)
		}
	}

	got := receiveWithin(t, result, "out-of-order Resolve")
	if got.err != nil {
		t.Fatalf("Resolve: %v", got.err)
	}
	if keys := ticketKeys(got.snapshot.Tickets); !reflect.DeepEqual(keys, []string{
		"acme/widgets#1", "acme/widgets#2", "acme/widgets#3",
	}) {
		t.Errorf("Ticket keys = %v, want membership order", keys)
	}
}

func TestResolveQueryExactRootFailureIsDeterministic(t *testing.T) {
	started := make(chan int, 2)
	higherFinished := make(chan struct{}, 1)
	releaseLower := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseLower) }) })

	var mu sync.Mutex
	correlationRequests := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		switch {
		case path == queryGlobalIssues:
			writeTestJSON(t, w, []map[string]any{
				syntheticGitLabIssue(1), syntheticGitLabIssue(2),
			})
		case strings.HasSuffix(path, "/closed_by"):
			mu.Lock()
			correlationRequests++
			mu.Unlock()
			writeTestJSON(t, w, []any{})
		default:
			iid, ok := exactIssueIID(path)
			if !ok {
				t.Errorf("unexpected request path %s", path)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			started <- iid
			w.Header().Set("Content-Type", "application/json")
			if iid == 1 {
				<-releaseLower
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"404 Issue Not Found"}`))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"401 Unauthorized"}`))
			higherFinished <- struct{}{}
		}
	}))
	t.Cleanup(s.Close)

	type resolveResult struct {
		snapshot model.WatchlistSnapshot
		err      error
	}
	result := make(chan resolveResult, 1)
	go func() {
		snapshot, err := providerAtGitLabServer(s.URL, gitlab.WithMaxTickets(2)).Resolve(
			context.Background(), provider.QuerySelector{Query: "state=opened"})
		result <- resolveResult{snapshot: snapshot, err: err}
	}()
	for range 2 {
		receiveWithin(t, started, "both failing exact roots")
	}
	receiveWithin(t, higherFinished, "higher-index root failure")
	releaseOnce.Do(func() { close(releaseLower) })

	got := receiveWithin(t, result, "deterministic failure Resolve")
	providertest.CheckError(t, "gitlab", got.err, providertest.Want{
		Kind:     provider.KindBadRef,
		Contains: []string{"acme/widgets#1", "not found"},
		Secret:   fixtureToken,
	})
	if !reflect.DeepEqual(got.snapshot, model.WatchlistSnapshot{}) {
		t.Errorf("snapshot = %+v, want no partial output", got.snapshot)
	}
	mu.Lock()
	gotCorrelations := correlationRequests
	mu.Unlock()
	if gotCorrelations != 0 {
		t.Errorf("correlation requests = %d, want none after an exact-root failure", gotCorrelations)
	}
}

func TestResolveQueryCancellationStopsQueuedExactRoots(t *testing.T) {
	const rootCount = 12
	started := make(chan int, rootCount)
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	var mu sync.Mutex
	rootRequests := 0
	correlationRequests := 0
	issues := make([]map[string]any, rootCount)
	for i := range issues {
		issues[i] = syntheticGitLabIssue(i + 1)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		switch {
		case path == queryGlobalIssues:
			writeTestJSON(t, w, issues)
		case strings.HasSuffix(path, "/closed_by"):
			mu.Lock()
			correlationRequests++
			mu.Unlock()
			writeTestJSON(t, w, []any{})
		default:
			iid, ok := exactIssueIID(path)
			if !ok {
				t.Errorf("unexpected request path %s", path)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			mu.Lock()
			rootRequests++
			mu.Unlock()
			started <- iid
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			writeTestJSON(t, w, syntheticGitLabIssue(iid))
		}
	}))
	t.Cleanup(s.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	type resolveResult struct {
		snapshot model.WatchlistSnapshot
		err      error
	}
	result := make(chan resolveResult, 1)
	go func() {
		snapshot, err := providerAtGitLabServer(s.URL, gitlab.WithMaxTickets(rootCount)).Resolve(
			ctx, provider.QuerySelector{Query: "state=opened"})
		result <- resolveResult{snapshot: snapshot, err: err}
	}()
	for range 8 {
		receiveWithin(t, started, "the bounded exact-root fan-out before cancellation")
	}
	cancel()
	got := receiveWithin(t, result, "cancelled exact-root Resolve")
	releaseOnce.Do(func() { close(release) })
	if got.err == nil {
		t.Fatal("Resolve succeeded after cancellation")
	}
	if !reflect.DeepEqual(got.snapshot, model.WatchlistSnapshot{}) {
		t.Errorf("snapshot = %+v, want no partial output", got.snapshot)
	}
	mu.Lock()
	gotRoots := rootRequests
	gotCorrelations := correlationRequests
	mu.Unlock()
	if gotRoots != 8 {
		t.Errorf("root requests = %d, want only the eight already in flight", gotRoots)
	}
	if gotCorrelations != 0 {
		t.Errorf("correlation requests = %d, want none after cancellation", gotCorrelations)
	}
}

func TestResolveRefListReadsExactHeterogeneousRootsInOrder(t *testing.T) {
	responses := map[string][]response{
		groupMilestonesPath:       {{file: "milestone_group.json"}},
		issuePath:                 {{file: "issue_with_epic.json"}, {file: "issue_detail.json"}},
		epicPath:                  {{file: "epic.json"}},
		projectMilestonesPath:     {{file: "milestone_project.json"}},
		issueMRsPath:              {{file: "closed_by.json"}},
		mergeRequestApprovalsPath: {{file: "approvals_approved.json"}},
		issueNotesPath:            {{file: "notes_empty.json"}},
		issueLinksPath:            {{file: "links_empty.json"}},
		epicNotesPath:             {{file: "notes_empty.json"}},
	}
	s := newReplayServer(t, responses)
	p := newProvider(s)
	selector := provider.RefListSelector{Refs: []ref.Ref{
		groupMilestoneRef,
		issueRef,
		epicRef,
		projectMilestoneRef,
	}}

	snap, err := p.Resolve(context.Background(), selector)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if snap.Tickets == nil {
		t.Fatal("Tickets is nil; every successful Ref-list must return a non-nil slice")
	}
	if snap.Header != (model.WatchlistHeader{Title: "4 tickets"}) {
		t.Errorf("Header = %+v, want the four-Ticket Ref-list identity", snap.Header)
	}
	if !reflect.DeepEqual(snap.Epic, model.Epic{}) {
		t.Errorf("Epic = %+v, want the zero Epic for a Ref-list", snap.Epic)
	}
	if !snap.Parent.IsZero() {
		t.Errorf("Parent = %+v, want the zero Parent for a Ref-list", snap.Parent)
	}
	if snap.Capabilities != p.Capabilities() {
		t.Errorf("Capabilities = %+v, want the Provider's %+v", snap.Capabilities, p.Capabilities())
	}
	if !snap.FetchedAt.IsZero() {
		t.Errorf("FetchedAt = %s, want the zero time", snap.FetchedAt)
	}

	wants := []struct {
		id         model.TicketID
		key        string
		status     model.StatusCategory
		native     string
		repository string
	}{
		{"group-milestone:gitlab-org%3", "groups/gitlab-org%3", model.StatusDone, "closed", "gitlab-org"},
		{issueTicketID, "gitlab-org/cli#8509", model.StatusInProgress, "open", "gitlab-org/cli"},
		{epicTicketID, "gitlab-org&23356", model.StatusTodo, "open", "gitlab-org"},
		{"project-milestone:gitlab-org/cli%3", "gitlab-org/cli%3", model.StatusTodo, "active", "gitlab-org/cli"},
	}
	if len(snap.Tickets) != len(wants) {
		t.Fatalf("got %d Tickets, want %d", len(snap.Tickets), len(wants))
	}
	for i, want := range wants {
		got := snap.Tickets[i]
		if got.ID != want.id || got.Key != want.key || got.Status != want.status ||
			got.NativeStatus != want.native || got.Repository != want.repository {
			t.Errorf("Tickets[%d] = {%q %q %v %q %q}, want {%q %q %v %q %q}",
				i, got.ID, got.Key, got.Status, got.NativeStatus, got.Repository,
				want.id, want.key, want.status, want.native, want.repository)
		}
	}
	if got := snap.Tickets[1].Assignees; len(got) != 1 || got[0].Login != "jay_mccure" {
		t.Errorf("issue Assignees = %+v, want the recorded assignee", got)
	}
	if got := snap.Tickets[1].PullRequests; len(got) != 1 || got[0].Number != 3761 {
		t.Errorf("issue PullRequests = %+v, want correlation on the ordered root slice", got)
	}
	for _, i := range []int{0, 2, 3} {
		if snap.Tickets[i].PullRequests != nil {
			t.Errorf("Tickets[%d].PullRequests = %+v, want no correlation request for a non-issue ID",
				i, snap.Tickets[i].PullRequests)
		}
	}

	for _, path := range []string{groupMilestonesPath, issuePath, epicPath, projectMilestonesPath} {
		if n := len(s.requestsTo(path)); n != 1 {
			t.Errorf("%d root requests to %s, want 1", n, path)
		}
	}
	for _, path := range []string{
		issueMRsPath,
		relatedMergeRequestsPath("gitlab-org/cli", 8509),
		mergeRequestApprovalsPath,
	} {
		if n := len(s.requestsTo(path)); n != 1 {
			t.Errorf("%d correlation requests to %s, want 1", n, path)
		}
	}
	if n := len(s.recorded()); n != 7 {
		t.Errorf("Resolve made %d HTTP requests, want four root reads and three issue-correlation reads", n)
	}
	for _, path := range []string{epicIssuesPath, projectMilestoneIssues, groupMilestoneIssuesPath} {
		if n := len(s.requestsTo(path)); n != 0 {
			t.Errorf("Resolve made %d requests to child endpoint %s, want none", n, path)
		}
	}
	for _, r := range s.recorded() {
		if strings.Contains(r.path, "/notes") || strings.Contains(r.path, "/links") {
			t.Errorf("Resolve requested %s; Detail must stay lazy", r.path)
		}
	}

	for _, ticket := range snap.Tickets {
		detail, err := p.FetchDetail(context.Background(), ticket.ID)
		if err != nil {
			t.Fatalf("FetchDetail(%q): %v", ticket.ID, err)
		}
		if detail.TicketID != ticket.ID {
			t.Errorf("FetchDetail(%q).TicketID = %q", ticket.ID, detail.TicketID)
		}
	}
	for path, want := range map[string]int{
		groupMilestonesPath:   2,
		issuePath:             2,
		issueNotesPath:        1,
		issueLinksPath:        1,
		epicPath:              2,
		epicNotesPath:         1,
		projectMilestonesPath: 2,
	} {
		if got := len(s.requestsTo(path)); got != want {
			t.Errorf("after lazy drill-in, %s received %d requests, want %d", path, got, want)
		}
	}
	for _, path := range []string{epicIssuesPath, projectMilestoneIssues, groupMilestoneIssuesPath} {
		if n := len(s.requestsTo(path)); n != 0 {
			t.Errorf("drill-in made %d requests to child endpoint %s, want none", n, path)
		}
	}
}

func TestResolveRefListRootFailureReturnsNoPartialSnapshot(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		issuePath: {{file: "issue.json"}},
		epicPath:  {{status: http.StatusNotFound, body: `{"message":"404 Epic Not Found"}`}},
	})

	snap, err := newProvider(s).Resolve(context.Background(), provider.RefListSelector{Refs: []ref.Ref{
		issueRef,
		epicRef,
	}})
	providertest.CheckError(t, "gitlab", err, providertest.Want{
		Kind:     provider.KindBadRef,
		Contains: []string{"gitlab-org&23356", "not found (or you lack access)"},
		Secret:   fixtureToken,
	})
	if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
		t.Errorf("snapshot = %+v, want no partial output", snap)
	}
	if n := len(s.requestsTo(issueMRsPath)); n != 0 {
		t.Errorf("%d correlation requests were made after a root failed, want none", n)
	}
}

func TestResolveRefListRejectsEmptyAndUnknownSelectorsWithoutIO(t *testing.T) {
	tests := []struct {
		name     string
		selector provider.Selector
		contains string
	}{
		{"empty Ref-list", provider.RefListSelector{}, "at least one Ref"},
		{"unknown nil Selector", nil, "unsupported Watchlist selector"},
		{"pointer Selector", &provider.EpicSelector{Ref: epicRef}, "unsupported Watchlist selector"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{})
			snap, err := newProvider(s).Resolve(context.Background(), tt.selector)
			providertest.CheckError(t, "gitlab", err, providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{tt.contains},
			})
			if !reflect.DeepEqual(snap, model.WatchlistSnapshot{}) {
				t.Errorf("snapshot = %+v, want zero", snap)
			}
			if n := len(s.recorded()); n != 0 {
				t.Errorf("%d requests were sent; selector validation must be immediate", n)
			}
		})
	}
}

func TestResolveRefListValidatesEveryTargetBeforeIO(t *testing.T) {
	s := newReplayServer(t, map[string][]response{})
	bad := ref.Ref{Tracker: ref.TrackerGitHub, Owner: "acme", Repo: "widgets", Number: 7, Raw: "acme/widgets#7"}

	_, err := newProvider(s).Resolve(context.Background(), provider.RefListSelector{Refs: []ref.Ref{issueRef, bad}})
	providertest.CheckError(t, "gitlab", err, providertest.Want{
		Kind:     provider.KindBadRef,
		Contains: []string{"not a GitLab Ref"},
	})
	if n := len(s.recorded()); n != 0 {
		t.Errorf("%d requests were sent before all exact roots were validated", n)
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
			want: "is not a GitLab Ref",
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
			_, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: tt.r})
			providertest.CheckError(t, "gitlab", err, providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{tt.want},
			})
			if n := len(s.recorded()); n != 0 {
				t.Errorf("%d requests were sent; a bad Ref must fail before any of them", n)
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
			name: "401",
			resp: response{status: http.StatusUnauthorized, body: `{"message":"401 Unauthorized"}`},
			want: providertest.Want{
				Kind:     provider.KindAuth,
				Contains: []string{"authentication failed (401)", "glab auth status", "GITLAB_TOKEN"},
				Secret:   fixtureToken,
			},
		},
		{
			// Epics are Premium/Ultimate, so a 403 on an epic path is a tier
			// problem and says so.
			name: "403 on an epic path",
			resp: response{status: http.StatusForbidden, file: "error_forbidden.json"},
			want: providertest.Want{
				Kind:     provider.KindAuth,
				Contains: []string{"GitLab Premium or Ultimate (403)", "gitlab-org&23356"},
				Secret:   fixtureToken,
			},
		},
		{
			name: "404",
			resp: response{status: http.StatusNotFound, body: `{"message":"404 Group Not Found"}`},
			want: providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{"gitlab-org&23356", "not found (or you lack access)"},
				Secret:   fixtureToken,
			},
		},
		{
			name: "429 with Retry-After",
			resp: response{status: http.StatusTooManyRequests, body: `{}`,
				headers: map[string]string{"Retry-After": "60"}},
			want: providertest.Want{
				Kind:     provider.KindRateLimit,
				Contains: []string{"rate limit exceeded", "1m0s"},
				Secret:   fixtureToken,
			},
		},
		{
			name: "429 with only a ratelimit-reset",
			resp: response{status: http.StatusTooManyRequests, body: `{}`,
				headers: map[string]string{"ratelimit-reset": "1755000000"}},
			want: providertest.Want{
				Kind:     provider.KindRateLimit,
				Contains: []string{"rate limit exceeded", "retry after"},
				Secret:   fixtureToken,
			},
		},
		{
			// GitLab sends this HTTP-date twin of ratelimit-reset alongside it,
			// and sometimes instead of it.
			name: "429 with only a ratelimit-resettime",
			resp: response{status: http.StatusTooManyRequests, body: `{}`,
				headers: map[string]string{"ratelimit-resettime": "Wed, 12 Aug 2026 12:00:00 GMT"}},
			want: providertest.Want{
				Kind:     provider.KindRateLimit,
				Contains: []string{"rate limit exceeded", "retry after"},
				Secret:   fixtureToken,
			},
		},
		{
			name: "429 with nothing to say",
			resp: response{status: http.StatusTooManyRequests, body: `{}`},
			want: providertest.Want{
				Kind:     provider.KindRateLimit,
				Contains: []string{"rate limit exceeded", "an unknown time"},
				Secret:   fixtureToken,
			},
		},
		{
			name: "an error payload",
			resp: response{status: http.StatusInternalServerError, file: "error_message.json"},
			want: providertest.Want{
				Kind:     provider.KindUnavailable,
				Contains: []string{"API error:", "500 Internal Server Error"},
				Secret:   fixtureToken,
			},
		},
		{
			name: "a status with nothing to say",
			resp: response{status: http.StatusBadGateway, body: `<html>bad gateway</html>`},
			want: providertest.Want{
				Kind:     provider.KindUnavailable,
				Contains: []string{"unexpected response 502"},
				Secret:   fixtureToken,
			},
		},
		{
			name: "malformed JSON",
			resp: response{body: `{"iid": `},
			want: providertest.Want{
				Kind:     provider.KindUnavailable,
				Contains: []string{"decoding the response from"},
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
			providertest.CheckError(t, "gitlab", err, tt.want)
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
	providertest.CheckCoversTheNamedClasses(t, "gitlab", kinds)
}

// A 403 away from an epic endpoint is a permission problem, not a tier one —
// and the commonest cause is a token created without a scope that may read the
// API, so the message names it.
func TestForbiddenOnANonEpicPath(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		issuePath: {{status: http.StatusForbidden, body: `{"message":"403 Forbidden"}`}},
	})

	_, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: issueRef})
	providertest.CheckError(t, "gitlab", err, providertest.Want{
		Kind:     provider.KindAuth,
		Contains: []string{"access denied (403)", "read_api"},
		Secret:   fixtureToken,
	})
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

	_, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err == nil {
		t.Fatal("Resolve succeeded, want an error")
	}
	// A paging API contradicting itself is a server fault, which is what
	// KindUnavailable means — and it stays retryable, on the same terms as a
	// 500.
	providertest.CheckError(t, "gitlab", err, providertest.Want{
		Kind:     provider.KindUnavailable,
		Contains: []string{"which is not after page 1"},
		Secret:   fixtureToken,
	})
	if !provider.KindOf(err).Retryable() {
		t.Error("a server fault must stay retryable; the monitor recovers when the server does")
	}
}

// x-next-page is tracker-supplied text quoted into an error on its way to a
// terminal, so it goes through provider.Errorf's sanitization funnel like every
// other driver sentence.
func TestPaginationSanitizesTheNextPageHeader(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		epicPath: {{file: "epic.json"}},
		epicIssuesPath: {{
			file:    "epic_children_empty.json",
			headers: map[string]string{"x-next-page": "0\x1b[2Jgotcha"},
		}},
	})

	_, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err == nil {
		t.Fatal("Resolve succeeded, want an error")
	}
	if strings.ContainsRune(err.Error(), '\x1b') {
		t.Errorf("error %q carries a terminal escape", err)
	}
	providertest.CheckError(t, "gitlab", err, providertest.Want{
		Kind:     provider.KindUnavailable,
		Contains: []string{"gotcha"},
		Secret:   fixtureToken,
	})
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

	_, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err == nil {
		t.Fatal("Resolve succeeded, want an error")
	}
	providertest.CheckError(t, "gitlab", err, providertest.Want{
		Kind:     provider.KindBadRef,
		Contains: []string{"refusing to keep paging"},
		Secret:   fixtureToken,
	})
	if provider.KindOf(err).Retryable() {
		t.Error("an over-cap collection is as large on the next tick; retrying buys nothing")
	}
}

// The polled path: two consecutive fetches are equal, re-issue the same
// requests, and resolve the token exactly once.
// Token discovery shells out to glab, which a locked keyring or a slow network
// can fail transiently. Caching that failure for the process lifetime means an
// open monitor never recovers, however healthy the machine gets.
func TestATransientTokenFailureIsNotCached(t *testing.T) {
	responses := map[string][]response{
		epicPath:       {{file: "epic.json"}},
		epicIssuesPath: {{file: "epic_children_empty.json"}},
	}
	s := newReplayServer(t, responses)

	var calls int
	p := newProvider(s, gitlab.WithTokenSource(func(context.Context, string) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("the keyring is locked")
		}
		return fixtureToken, nil
	}))

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

func TestPollingIsStableAndResolvesTheTokenOnce(t *testing.T) {
	// One page, so that the replay server's "the last response repeats" rule
	// serves the same epic to both fetches rather than advancing through pages.
	responses := map[string][]response{
		epicPath:       {{file: "epic.json"}},
		epicIssuesPath: {{file: "epic_children_page1.json"}},
	}
	noMergeRequests(responses, "gitlab-org/cli", 102, 103, 104)
	noMergeRequests(responses, "gitlab-org/platform/core", 7)
	// One Ticket really does correlate, so the determinism this asserts covers
	// the fan-out's ordering and not just an empty one.
	responses[mergeRequestsPath("gitlab-org/cli", 101)] = []response{{file: "closed_by_lead.json"}}
	responses[mergeRequestApprovalsPath] = []response{{file: "approvals_pending.json"}}
	s := newReplayServer(t, responses)

	var tokens int
	p := newProvider(s, gitlab.WithTokenSource(func(context.Context, string) (string, error) {
		tokens++
		return fixtureToken, nil
	}))

	first, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	second, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
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

	_, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	providertest.CheckError(t, "gitlab", err, providertest.Want{
		Kind:     provider.KindAuth,
		Contains: []string{"glab auth login", "GITLAB_TOKEN"},
	})
	if n := len(s.recorded()); n != 0 {
		t.Errorf("%d requests were sent without a token", n)
	}
}

// ---------------------------------------------------------------------------
// Milestone as Epic
// ---------------------------------------------------------------------------

// The Refs the milestone fixtures were written for.
var (
	projectMilestoneRef = ref.Ref{
		Tracker: ref.TrackerGitLab,
		Host:    fixtureHost,
		Owner:   "gitlab-org",
		Repo:    "cli",
		Number:  3,
		Key:     "gitlab-org/cli%3",
		Raw:     "https://gitlab.com/gitlab-org/cli/-/milestones/3",
	}
	groupMilestoneRef = ref.Ref{
		Tracker: ref.TrackerGitLab,
		Host:    fixtureHost,
		Owner:   "gitlab-org",
		Number:  3,
		Key:     "groups/gitlab-org%3",
		Raw:     "https://gitlab.com/groups/gitlab-org/-/milestones/3",
	}
)

// fullProjectMilestone serves the two-page fixture milestone, with every child
// correlating to no merge requests.
func fullProjectMilestone(t *testing.T) *replayServer {
	t.Helper()
	responses := map[string][]response{
		projectMilestonesPath: {{file: "milestone_project.json"}},
		projectMilestoneIssues: {
			{file: "milestone_issues_page1.json", headers: map[string]string{"x-next-page": "2"}},
			{file: "milestone_issues_page2.json", headers: map[string]string{"x-next-page": ""}},
		},
	}
	noMergeRequests(responses, "gitlab-org/cli", 201, 202, 203, 204, 205, 206)
	noMergeRequests(responses, "gitlab-org/platform/core", 7)
	// Whichever merge request a test's fixture makes the lead, its approvals
	// come from here; approvalsFallback routes every iid to this one path.
	responses[mergeRequestApprovalsPath] = []response{{file: "approvals_pending.json"}}
	return newReplayServer(t, responses)
}

// The headline of this ticket, executable: a milestone Ref renders a full Epic
// view, exactly as a Premium epic does.
func TestResolveOnAProjectMilestone(t *testing.T) {
	s := fullProjectMilestone(t)
	p := newProvider(s)

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: projectMilestoneRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	want := model.Epic{
		ID:           "project-milestone:gitlab-org/cli%3",
		Key:          "gitlab-org/cli%3",
		Title:        "v1.4 — CLI polish",
		URL:          "https://gitlab.com/gitlab-org/cli/-/milestones/3",
		Status:       model.StatusTodo,
		NativeStatus: "active",
		Repository:   "gitlab-org/cli",
	}
	if !reflect.DeepEqual(snap.Epic, want) {
		t.Errorf("Epic = %+v, want %+v", snap.Epic, want)
	}
	if snap.Header != provider.EpicHeader(want) {
		t.Errorf("Header = %+v, want the milestone display identity", snap.Header)
	}
	// A milestone belongs to nothing sitrep models: a project milestone's group
	// is not its parent.
	if !snap.Parent.IsZero() {
		t.Errorf("Parent = %+v, want the zero Parent", snap.Parent)
	}
	if snap.Capabilities != p.Capabilities() {
		t.Errorf("Capabilities = %+v, want the Provider's %+v", snap.Capabilities, p.Capabilities())
	}
	if !snap.FetchedAt.IsZero() {
		t.Errorf("FetchedAt = %s, want the zero time: the caller stamps it", snap.FetchedAt)
	}

	type child struct {
		key        string
		status     model.StatusCategory
		native     string
		repository string
	}
	wants := []child{
		{"gitlab-org/cli#201", model.StatusTodo, "open", "gitlab-org/cli"},
		{"gitlab-org/cli#202", model.StatusTodo, "open", "gitlab-org/cli"},
		{"gitlab-org/cli#203", model.StatusTodo, "open", "gitlab-org/cli"},
		// A child in another project identifies itself through its own path.
		{"gitlab-org/platform/core#7", model.StatusTodo, "open", "gitlab-org/platform/core"},
		{"gitlab-org/cli#204", model.StatusTodo, "open", "gitlab-org/cli"},
		{"gitlab-org/cli#205", model.StatusDone, "closed", "gitlab-org/cli"},
		// #14's won't-do rule, still working under a milestone.
		{"gitlab-org/cli#206", model.StatusCancelled, "workflow::wontfix", "gitlab-org/cli"},
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
		if got.ParentID != "" {
			t.Errorf("Tickets[%d].ParentID = %q, want it empty for a direct child", i, got.ParentID)
		}
	}
	if got := snap.Tickets[1].Title; got != "Filtering & fuzzy find, « éclair » included" {
		t.Errorf("Tickets[1].Title = %q, want it unmangled", got)
	}
	if got := snap.Tickets[0].Assignees; got != nil {
		t.Errorf("Tickets[0].Assignees = %+v, want nil for an unassigned issue", got)
	}
}

// The iid/id bridge, asserted as what was sent: the lookup filters the *list*
// with iids[], and the issues path carries the resolved database id.
func TestMilestoneIsResolvedByIIDAndReadByID(t *testing.T) {
	s := fullProjectMilestone(t)

	if _, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: projectMilestoneRef}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	lookups := s.requestsTo(projectMilestonesPath)
	if len(lookups) != 1 {
		t.Fatalf("%d milestone lookups, want 1", len(lookups))
	}
	if got := lookups[0].query["iids[]"]; len(got) != 1 || got[0] != "3" {
		t.Errorf("the lookup carried iids[]=%v, want the Ref's iid", got)
	}
	if got := lookups[0].query["per_page"]; len(got) != 1 || got[0] != "2" {
		t.Errorf("the lookup carried per_page=%v, want 2 so a duplicate is visible", got)
	}
	// sitrep never reads /milestones/:milestone_id.
	for _, r := range s.recorded() {
		if r.path == projectMilestonesPath+"/6239395" || r.path == projectMilestonesPath+"/3" {
			t.Errorf("the driver requested %s; the list form is what bridges iid to id", r.path)
		}
	}

	pages := s.requestsTo(projectMilestoneIssues)
	if len(pages) != 2 {
		t.Fatalf("%d children requests, want 2", len(pages))
	}
	for i, page := range pages {
		if got := page.query["per_page"]; len(got) != 1 || got[0] != "100" {
			t.Errorf("children request %d carried per_page=%v, want GitLab's maximum", i, got)
		}
	}
	if got := pages[1].query["page"]; len(got) != 1 || got[0] != "2" {
		t.Errorf("the second children request carried page=%v, want the header's next page", got)
	}
}

// A group milestone: sitrep's own "groups/" spelling, GitLab's group scope, and
// children from more than one project.
func TestResolveOnAGroupMilestone(t *testing.T) {
	responses := map[string][]response{
		groupMilestonesPath:      {{file: "milestone_group.json"}},
		groupMilestoneIssuesPath: {{file: "milestone_issues_page1.json"}},
	}
	noMergeRequests(responses, "gitlab-org/cli", 201, 202, 203, 204)
	noMergeRequests(responses, "gitlab-org/platform/core", 7)
	s := newReplayServer(t, responses)

	snap, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: groupMilestoneRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	want := model.Epic{
		ID:           "group-milestone:gitlab-org%3",
		Key:          "groups/gitlab-org%3",
		Title:        "19.4",
		URL:          "https://gitlab.com/groups/gitlab-org/-/milestones/3",
		Status:       model.StatusDone,
		NativeStatus: "closed",
		Repository:   "gitlab-org",
	}
	if !reflect.DeepEqual(snap.Epic, want) {
		t.Errorf("Epic = %+v, want %+v", snap.Epic, want)
	}
	if snap.Header != provider.EpicHeader(want) {
		t.Errorf("Header = %+v, want the group milestone display identity", snap.Header)
	}
	// Children of a group milestone span projects, and each says which.
	repositories := map[string]bool{}
	for _, ticket := range snap.Tickets {
		repositories[ticket.Repository] = true
	}
	if !repositories["gitlab-org/cli"] || !repositories["gitlab-org/platform/core"] {
		t.Errorf("the group milestone's children came from %v, want two projects", repositories)
	}
}

// GitLab's own web_url is preferred; sitrep builds one when the payload carries
// none, which the project-milestone shape sometimes does.
func TestMilestoneWithNoWebURLGetsABuiltOne(t *testing.T) {
	ref4 := projectMilestoneRef
	ref4.Number, ref4.Key = 4, "gitlab-org/cli%4"
	s := newReplayServer(t, map[string][]response{
		projectMilestonesPath:                     {{file: "milestone_project_no_web_url.json"}},
		projectMilestonesPath + "/6239397/issues": {{file: "milestone_issues_empty.json"}},
	})

	snap, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: ref4})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := snap.Epic.URL; got != "https://gitlab.com/gitlab-org/cli/-/milestones/4" {
		t.Errorf("Epic.URL = %q, want the built one", got)
	}
	// A milestone with no issues decodes to a Ticket, not an error.
	if snap.Tickets == nil {
		t.Fatal("Tickets is nil; a milestone with no issues renders as none, not as null")
	}
	if len(snap.Tickets) != 0 {
		t.Errorf("got %d Tickets, want none", len(snap.Tickets))
	}
}

func TestMilestoneLookupFailures(t *testing.T) {
	tests := []struct {
		name string
		resp response
		kind provider.Kind
		want []string
	}{
		{
			name: "an iid that resolves to nothing",
			resp: response{file: "milestone_empty.json"},
			kind: provider.KindBadRef,
			want: []string{"gitlab:", "gitlab-org/cli%3", "not found (or you lack access)"},
		},
		{
			// An iid is documented to be unique within its scope, so two answers
			// mean sitrep's assumption is wrong — and an iid that names two
			// milestones names two on the next tick as well, so it is the ref
			// that is wrong rather than the moment.
			name: "two answers to a unique iid",
			resp: response{file: "milestone_duplicate.json"},
			kind: provider.KindBadRef,
			want: []string{"gitlab:", "matched 2 milestones", "will not guess"},
		},
		{
			// Milestones are Free tier, so a 403 is an access problem and saying
			// "Premium" would send the user shopping.
			name: "a 403 on a milestone path",
			resp: response{status: http.StatusForbidden, file: "error_forbidden.json"},
			kind: provider.KindAuth,
			want: []string{"gitlab:", "access denied (403) to the milestone", "Reporter access"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{projectMilestonesPath: {tt.resp}})

			_, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: projectMilestoneRef})
			if err == nil {
				t.Fatal("Resolve succeeded, want an error")
			}
			providertest.CheckError(t, "gitlab", err, providertest.Want{
				Kind:     tt.kind,
				Contains: tt.want[1:],
				Secret:   fixtureToken,
			})
			if strings.Contains(err.Error(), "Premium") {
				t.Errorf("error %q, want no tier claim about a Free-tier endpoint", err)
			}
		})
	}
}

// The Premium-403 on the epic path says what to do instead, because the
// milestone route finally gives the user something to do instead.
func TestPremiumForbiddenPointsAtMilestones(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		epicPath: {{status: http.StatusForbidden, file: "error_forbidden.json"}},
	})

	_, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err == nil {
		t.Fatal("Resolve succeeded, want an error")
	}
	for _, want := range []string{
		"GitLab Premium or Ultimate (403)",
		"point sitrep at a milestone instead",
		"https://gitlab.com/groups/<group>/-/milestones/<n>",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q, want it to mention %q", err, want)
		}
	}
}

// A milestone Ref that names nothing fetchable fails before any request.
func TestBadMilestoneRefsFailBeforeAnyRequest(t *testing.T) {
	tests := []struct {
		name string
		r    ref.Ref
		want string
	}{
		{
			name: "no path and no Profile project",
			r:    ref.Ref{Tracker: ref.TrackerGitLab, Number: 3, Key: "%3", Raw: "%3"},
			want: "acme/widgets%3",
		},
		{
			name: "an iid of zero",
			r: ref.Ref{Tracker: ref.TrackerGitLab, Owner: "acme", Repo: "widgets",
				Key: "acme/widgets%0", Raw: "acme/widgets%0"},
			want: "does not name a GitLab milestone",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{})
			_, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: tt.r})
			if err == nil {
				t.Fatal("Resolve succeeded, want an error")
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

// The Detail a milestone with no issues decodes to: one request, a verbatim
// description, and no notes or links endpoint that does not exist.
func TestFetchDetailOnAMilestone(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		projectMilestonesPath: {{file: "milestone_project.json"}},
	})

	detail, err := newProvider(s).FetchDetail(context.Background(), "project-milestone:gitlab-org/cli%3")
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	const wantDescription = "The milestone a Free instance delegates into.\n\n" +
		"Read only by FetchDetail: `sitrep --plain` & « éclair » must survive verbatim."
	if detail.Description != wantDescription {
		t.Errorf("Description = %q, want it verbatim", detail.Description)
	}
	if detail.TicketID != "project-milestone:gitlab-org/cli%3" {
		t.Errorf("TicketID = %q, want the one asked for", detail.TicketID)
	}
	// GitLab has no milestone notes endpoint and no milestone links endpoint;
	// nil is the ordinary state, not a Capability lie.
	if detail.Comments != nil {
		t.Errorf("Comments = %+v, want nil", detail.Comments)
	}
	if detail.Links != nil {
		t.Errorf("Links = %+v, want nil", detail.Links)
	}
	for _, r := range s.recorded() {
		if strings.Contains(r.path, "/notes") || strings.Contains(r.path, "/links") {
			t.Errorf("the driver requested %s; a milestone has neither endpoint", r.path)
		}
	}
}

// An empty description is the ordinary state of a milestone nobody wrote about.
func TestFetchDetailOnAMilestoneWithNoDescription(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		projectMilestonesPath: {{file: "milestone_project_no_web_url.json"}},
	})

	detail, err := newProvider(s).FetchDetail(context.Background(), "project-milestone:gitlab-org/cli%4")
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}
	if detail.Description != "" {
		t.Errorf("Description = %q, want empty", detail.Description)
	}
}

// The breadcrumb rule: an epic wins, a milestone is the fallback, and neither is
// the zero Parent.
func TestChildIssueBreadcrumb(t *testing.T) {
	tests := []struct {
		name string
		file string
		want model.Parent
	}{
		{
			name: "a milestone, on an instance with no epics",
			file: "issue_with_milestone.json",
			want: model.Parent{
				ID:    "project-milestone:gitlab-org/cli%3",
				Key:   "gitlab-org/cli%3",
				Title: "v1.4 — CLI polish",
				URL:   "https://gitlab.com/gitlab-org/cli/-/milestones/3",
			},
		},
		{
			// A Premium instance gives an issue both, and the epic is the
			// collection a human means.
			name: "both, where the epic wins",
			file: "issue_with_epic_and_milestone.json",
			want: model.Parent{
				ID:    epicTicketID,
				Key:   "gitlab-org&23356",
				Title: "AI Clients surfaces feature parity ledger (Web Chat, IDEs, Duo CLI)",
				URL:   "https://gitlab.com/groups/gitlab-org/-/epics/23356",
			},
		},
		{
			name: "neither",
			file: "issue.json",
			want: model.Parent{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, map[string][]response{
				issuePath:    {{file: tt.file}},
				issueMRsPath: {{file: "closed_by_empty.json"}},
			})

			snap, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: issueRef})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if snap.Parent != tt.want {
				t.Errorf("Parent = %+v, want %+v", snap.Parent, tt.want)
			}
			// The breadcrumb costs no extra request: it is embedded.
			for _, r := range s.recorded() {
				if strings.Contains(r.path, "/milestones") || strings.Contains(r.path, "/epics/") {
					t.Errorf("the driver requested %s; the breadcrumb is embedded", r.path)
				}
			}
		})
	}
}
