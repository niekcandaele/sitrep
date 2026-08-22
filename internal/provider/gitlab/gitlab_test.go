package gitlab_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

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
			method:  r.Method,
			path:    r.URL.EscapedPath(),
			query:   r.URL.Query(),
			headers: r.Header.Clone(),
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

func TestName(t *testing.T) {
	if p := gitlab.New(fixtureHost); p.Name() != "gitlab" {
		t.Errorf("Name() = %q, want %q", p.Name(), "gitlab")
	}
}

func TestCapabilities(t *testing.T) {
	want := model.Capabilities{Hierarchy: true, BlockingLinks: true, Comments: true, PullRequests: true}
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

// The capability-on acceptance criterion, executable — the mirror image of the
// assertion #14 made while correlation was unimplemented: the Capability is
// declared, the data behind it is served, and it reaches the renderer.
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
		{"unknown nil Selector", nil, "unsupported Selector"},
		{"pointer Selector", &provider.EpicSelector{Ref: epicRef}, "unsupported Selector"},
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
		Contains: []string{"not a GitLab Epic Ref"},
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
