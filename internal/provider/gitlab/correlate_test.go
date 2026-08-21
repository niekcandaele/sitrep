package gitlab_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

// oneChildEpic serves an epic with a single child issue, so a test about merge
// request correlation can talk about exactly one Ticket. The child is
// gitlab-org/cli#101 and its correlation answer is mrs.
func oneChildEpic(t *testing.T, mrs ...response) *replayServer {
	t.Helper()
	responses := map[string][]response{
		epicPath:       {{file: "epic.json"}},
		epicIssuesPath: {{file: "epic_children_one.json"}},
	}
	noMergeRequests(responses, "gitlab-org/cli", 101)
	if len(mrs) > 0 {
		responses[mergeRequestsPath("gitlab-org/cli", 101)] = mrs
	}
	// The approvals answers the inline merge requests in this file may ask for.
	responses[mergeRequestApprovalsPath] = []response{{file: "approvals_pending.json"}}
	responses["/api/v4/projects/gitlab-org%2Fcli/merge_requests/1/approvals"] =
		[]response{{file: "approvals_pending.json"}}
	return newReplayServer(t, responses)
}

// firstTicket fetches the one-child epic and returns its only Ticket.
func firstTicket(t *testing.T, s *replayServer) model.Ticket {
	t.Helper()
	snap, err := newProvider(s).FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	if len(snap.Tickets) != 1 {
		t.Fatalf("got %d Tickets, want the one child", len(snap.Tickets))
	}
	return snap.Tickets[0]
}

// byNumber indexes a Ticket's merge requests, because leadFirst reorders them
// and a table asserting one row per situation should not care where it landed.
func byNumber(prs []model.PullRequest) map[int]model.PullRequest {
	out := make(map[int]model.PullRequest, len(prs))
	for _, pr := range prs {
		out[pr.Number] = pr
	}
	return out
}

// One assertion per mapping row, on State, Review and Checks together: the
// review direction and the CI colour are what a human acts on.
func TestMergeRequestMapping(t *testing.T) {
	ticket := firstTicket(t, oneChildEpic(t, response{file: "related_merge_requests_states.json"}))
	prs := byNumber(ticket.PullRequests)

	if len(ticket.PullRequests) != 11 {
		t.Fatalf("got %d merge requests, want all 11: nothing is dropped", len(ticket.PullRequests))
	}

	tests := []struct {
		number int
		name   string
		state  model.PRState
		review model.ReviewState
		checks model.CheckState
	}{
		{100, "merged, with no pipeline", model.PRMerged, model.ReviewNone, model.ChecksNone},
		{101, "closed", model.PRClosed, model.ReviewNone, model.ChecksNone},
		{102, "an open draft, pipeline running", model.PRDraft, model.ReviewNone, model.ChecksPending},
		{103, "locked reads as open; a failed pipeline is red", model.PROpen, model.ReviewNone, model.ChecksFailing},
		{104, "an unmapped state says so", model.PRUnknown, model.ReviewNone, model.ChecksNone},
		{105, "a cancelled pipeline is not a green one", model.PROpen, model.ReviewNone, model.ChecksFailing},
		{106, "a skipped pipeline ran nothing", model.PROpen, model.ReviewNone, model.ChecksNone},
		{107, "an unmapped pipeline status is never green", model.PROpen, model.ReviewNone, model.ChecksPending},
		{108, "requested changes is a hard block", model.PROpen, model.ReviewChangesRequested, model.ChecksPassing},
		{109, "another project entirely", model.PROpen, model.ReviewPending, model.ChecksPassing},
		{110, "no references object", model.PROpen, model.ReviewNone, model.ChecksNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr, ok := prs[tt.number]
			if !ok {
				t.Fatalf("no merge request numbered %d", tt.number)
			}
			if pr.State != tt.state || pr.Review != tt.review || pr.Checks != tt.checks {
				t.Errorf("!%d = {%v %v %v}, want {%v %v %v}",
					tt.number, pr.State, pr.Review, pr.Checks, tt.state, tt.review, tt.checks)
			}
		})
	}

	// A merge request can live in a different project from the Ticket it moves,
	// and it always says which.
	if got := prs[109].Repository; got != "gitlab-org/platform/core" {
		t.Errorf("!109.Repository = %q, want the merge request's own project", got)
	}
	// With no references object the project comes from web_url.
	if got := prs[110].Repository; got != "gitlab-org/cli" {
		t.Errorf("!110.Repository = %q, want it derived from web_url", got)
	}
	// A merged merge request leads, and it is index 0.
	if ticket.PullRequests[0].Number != 100 {
		t.Errorf("PullRequests[0] = !%d, want the merged one to lead", ticket.PullRequests[0].Number)
	}
}

// Lead selection, at the seam: three merge requests on one issue still read as
// one situation, and nothing is dropped.
func TestLeadMergeRequestIsFirst(t *testing.T) {
	ticket := firstTicket(t, oneChildEpic(t, response{file: "related_merge_requests_lead.json"}))

	if len(ticket.PullRequests) != 3 {
		t.Fatalf("got %d merge requests, want all 3", len(ticket.PullRequests))
	}
	if got := ticket.PullRequests[0].Number; got != 201 {
		t.Errorf("PullRequests[0] = !%d, want the merged !201 to lead", got)
	}
	// The Status Category reads every merge request, not only the lead: the
	// merged one leads because it is the headline, but the newer open follow-up
	// is somebody coding right now.
	if ticket.Status != model.StatusInProgress {
		t.Errorf("Status = %v, want InProgress: !202 is still open", ticket.Status)
	}
}

// A Ticket with no related merge requests carries nil, the model's documented
// "none", and stays where it was.
func TestTicketWithNoMergeRequests(t *testing.T) {
	ticket := firstTicket(t, oneChildEpic(t))

	if ticket.PullRequests != nil {
		t.Errorf("PullRequests = %+v, want nil rather than an empty slice", ticket.PullRequests)
	}
	if ticket.Status != model.StatusTodo {
		t.Errorf("Status = %v, want Todo", ticket.Status)
	}
}

// The approvals request is what buys ReviewApproved, and approved_by is what it
// is read for.
func TestApprovals(t *testing.T) {
	tests := []struct {
		name string
		file string
		want model.ReviewState
	}{
		{"somebody clicked Approve", "approvals_approved.json", model.ReviewApproved},
		{"an approval is still owed", "approvals_pending.json", model.ReviewPending},
		// The trap: `approved` is true because the requirement is zero, and
		// nobody has approved anything.
		{"zero approvals required is not approved", "approvals_zero_required.json", model.ReviewPending},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := oneChildEpic(t, response{file: "related_merge_requests.json"})
			s.responses[mergeRequestApprovalsPath] = []response{{file: tt.file}}

			ticket := firstTicket(t, s)
			if got := ticket.PullRequests[0].Review; got != tt.want {
				t.Errorf("Review = %v, want %v", got, tt.want)
			}
			if n := len(s.requestsTo(mergeRequestApprovalsPath)); n != 1 {
				t.Errorf("%d approvals requests, want exactly 1 for the live lead", n)
			}
		})
	}
}

// requested_changes short-circuits the approvals request: GitLab has already
// answered the question, and the answer outranks an approval anyway.
func TestRequestedChangesSkipsTheApprovalsRequest(t *testing.T) {
	s := oneChildEpic(t, response{body: `[{
		"iid": 3761, "state": "opened", "draft": false,
		"web_url": "https://gitlab.com/gitlab-org/cli/-/merge_requests/3761",
		"references": {"full": "gitlab-org/cli!3761"},
		"detailed_merge_status": "requested_changes",
		"reviewers": [], "head_pipeline": null
	}]`})

	ticket := firstTicket(t, s)
	if got := ticket.PullRequests[0].Review; got != model.ReviewChangesRequested {
		t.Errorf("Review = %v, want ChangesRequested", got)
	}
	if n := len(s.requestsTo(mergeRequestApprovalsPath)); n != 0 {
		t.Errorf("%d approvals requests, want none: the question is already answered", n)
	}
}

// A merged or closed lead is history: its review posture is not worth a request.
func TestNoApprovalsRequestForADeadLead(t *testing.T) {
	s := oneChildEpic(t, response{file: "related_merge_requests_lead.json"})

	firstTicket(t, s)
	for _, r := range s.recorded() {
		if strings.Contains(r.path, "/approvals") {
			t.Errorf("the driver requested %s for a merged lead", r.path)
		}
	}
}

// An approvals failure of any status is never fatal: the report is one nuance
// poorer rather than absent.
func TestApprovalsFailureIsNeverFatal(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := oneChildEpic(t, response{file: "related_merge_requests.json"})
			s.responses[mergeRequestApprovalsPath] = []response{{status: status, body: `{}`}}

			ticket := firstTicket(t, s)
			// detailed_merge_status is not_approved, so the fallback is Pending.
			if got := ticket.PullRequests[0].Review; got != model.ReviewPending {
				t.Errorf("Review = %v, want Pending from detailed_merge_status alone", got)
			}
		})
	}
}

// StatusInProgress finally exists on GitLab, and only an open or draft merge
// request produces it.
func TestStatusInProgressAppears(t *testing.T) {
	tests := []struct {
		name string
		body string
		want model.StatusCategory
	}{
		{
			name: "an open merge request",
			body: `[{"iid": 1, "state": "opened", "draft": false,
				"web_url": "https://gitlab.com/gitlab-org/cli/-/merge_requests/1",
				"detailed_merge_status": "mergeable"}]`,
			want: model.StatusInProgress,
		},
		{
			name: "a draft one",
			body: `[{"iid": 1, "state": "opened", "draft": true,
				"web_url": "https://gitlab.com/gitlab-org/cli/-/merge_requests/1",
				"detailed_merge_status": "draft_status"}]`,
			want: model.StatusInProgress,
		},
		{
			name: "only a merged one",
			body: `[{"iid": 1, "state": "merged", "draft": false,
				"web_url": "https://gitlab.com/gitlab-org/cli/-/merge_requests/1",
				"detailed_merge_status": "not_open"}]`,
			want: model.StatusTodo,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ticket := firstTicket(t, oneChildEpic(t, response{body: tt.body}))
			if ticket.Status != tt.want {
				t.Errorf("Status = %v, want %v", ticket.Status, tt.want)
			}
			// Native Status means GitLab's own word, and GitLab's word is "open".
			if ticket.NativeStatus != "open" {
				t.Errorf("NativeStatus = %q, want GitLab's own word untouched", ticket.NativeStatus)
			}
		})
	}
}

// A finished Ticket is never reopened by a merge request, and a Cancelled one
// stays out of the progress denominator.
func TestFinishedTicketsAreNeverReopened(t *testing.T) {
	const openMR = `[{"iid": 1, "state": "opened", "draft": false,
		"web_url": "https://gitlab.com/gitlab-org/cli/-/merge_requests/1",
		"detailed_merge_status": "mergeable"}]`

	responses := map[string][]response{
		epicPath:       {{file: "epic.json"}},
		epicIssuesPath: {{file: "epic_children_page2.json"}},
	}
	for _, iid := range []int{105, 106, 107, 108, 109} {
		responses[mergeRequestsPath("gitlab-org/cli", iid)] = []response{{body: openMR}}
	}
	responses["/api/v4/projects/gitlab-org%2Fcli/merge_requests/1/approvals"] =
		[]response{{file: "approvals_pending.json"}}
	s := newReplayServer(t, responses)

	snap, err := newProvider(s).FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	want := []struct {
		key    string
		status model.StatusCategory
	}{
		{"gitlab-org/cli#105", model.StatusCancelled},
		{"gitlab-org/cli#106", model.StatusCancelled},
		{"gitlab-org/cli#107", model.StatusDone},
		{"gitlab-org/cli#108", model.StatusUnknown},
		// The only open one, so the only one an open merge request promotes.
		{"#109", model.StatusInProgress},
	}
	if len(snap.Tickets) != len(want) {
		t.Fatalf("got %d Tickets, want %d", len(snap.Tickets), len(want))
	}
	for i, w := range want {
		if snap.Tickets[i].Key != w.key || snap.Tickets[i].Status != w.status {
			t.Errorf("Tickets[%d] = %s %v, want %s %v",
				i, snap.Tickets[i].Key, snap.Tickets[i].Status, w.key, w.status)
		}
	}

	// A Cancelled Ticket stays out of the progress denominator whatever code is
	// open against it.
	progress := model.ComputeProgress(snap.Tickets)
	if progress.Cancelled != 2 || progress.Denominator != 3 {
		t.Errorf("Progress = %+v, want 2 cancelled out of a denominator of 3", progress)
	}
}

// One related_merge_requests request per Ticket, at most one approvals request
// per Ticket, and every one of them a GET carrying sitrep's headers.
func TestCorrelationSendsExactlyWhatItNeeds(t *testing.T) {
	s := fullEpic(t)
	s.responses[mergeRequestsPath("gitlab-org/cli", 101)] = []response{{file: "related_merge_requests.json"}}
	s.responses[mergeRequestApprovalsPath] = []response{{file: "approvals_pending.json"}}

	if _, err := newProvider(s).FetchEpic(context.Background(), epicRef); err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	var correlations, approvals int
	for _, r := range s.recorded() {
		switch {
		case strings.HasSuffix(r.path, "/related_merge_requests"):
			correlations++
			if got := r.query["per_page"]; len(got) != 1 || got[0] != "100" {
				t.Errorf("a correlation request carried per_page=%v, want GitLab's maximum", got)
			}
		case strings.HasSuffix(r.path, "/approvals"):
			approvals++
		}
		if r.method != http.MethodGet {
			t.Errorf("the driver sent %s %s; this Tracker driver is read-only", r.method, r.path)
		}
		if got := r.headers.Get("Authorization"); got != "Bearer "+fixtureToken {
			t.Errorf("Authorization = %q, want the Bearer form", got)
		}
		if got := r.headers.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want application/json", got)
		}
		if got := r.headers.Get("User-Agent"); got != "sitrep/test" {
			t.Errorf("User-Agent = %q, want sitrep's", got)
		}
	}
	if correlations != 10 {
		t.Errorf("%d correlation requests, want one per Ticket", correlations)
	}
	if approvals != 1 {
		t.Errorf("%d approvals requests, want one: only one Ticket has a live lead", approvals)
	}
}

// Degrade per Ticket, fail as a whole only for systemic problems.
func TestCorrelationDegradesPerTicket(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := fullEpic(t)
			s.responses[mergeRequestsPath("gitlab-org/cli", 101)] = []response{{status: status, body: `{}`}}
			s.responses[mergeRequestsPath("gitlab-org/cli", 102)] = []response{{file: "related_merge_requests_lead.json"}}

			snap, err := newProvider(s).FetchEpic(context.Background(), epicRef)
			if err != nil {
				t.Fatalf("FetchEpic: %v; one invisible project must not sink the report", err)
			}
			if len(snap.Tickets) != 10 {
				t.Errorf("got %d Tickets, want the whole epic intact", len(snap.Tickets))
			}
			if snap.Tickets[0].PullRequests != nil {
				t.Errorf("Tickets[0].PullRequests = %+v, want none", snap.Tickets[0].PullRequests)
			}
			if len(snap.Tickets[1].PullRequests) != 3 {
				t.Errorf("Tickets[1].PullRequests = %+v, want the rest of the snapshot intact",
					snap.Tickets[1].PullRequests)
			}
		})
	}
}

func TestCorrelationFailsTheFetchOnSystemicProblems(t *testing.T) {
	tests := []struct {
		name string
		resp response
		want string
	}{
		{"401", response{status: http.StatusUnauthorized, body: `{}`}, "authentication failed (401)"},
		{"429", response{status: http.StatusTooManyRequests, body: `{}`}, "rate limit exceeded"},
		{"500", response{status: http.StatusInternalServerError, file: "error_message.json"}, "API error:"},
		{"malformed JSON", response{body: `[{"iid": `}, "decoding the response from"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := fullEpic(t)
			s.responses[mergeRequestsPath("gitlab-org/cli", 101)] = []response{tt.resp}

			_, err := newProvider(s).FetchEpic(context.Background(), epicRef)
			if err == nil {
				t.Fatal("FetchEpic succeeded; a systemic failure hits every Ticket")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q, want it to mention %q", err, tt.want)
			}
			if strings.Contains(err.Error(), fixtureToken) {
				t.Error("the token reached an error message")
			}
		})
	}
}

// A cancelled context stops the fan-out promptly and does not report success.
func TestCorrelationIsCancellable(t *testing.T) {
	s := fullEpic(t)
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel as soon as the first correlation request arrives, so the rest of the
	// fan-out sees a dead context. No sleep and no timing assumption.
	s.onRequest = func(path string) {
		if strings.HasSuffix(path, "/related_merge_requests") {
			cancel()
		}
	}

	if _, err := newProvider(s).FetchEpic(ctx, epicRef); err == nil {
		t.Fatal("FetchEpic succeeded on a cancelled context, want an error")
	}
	cancel()
}

// The decoded-issue path carries the issue's own merge requests on the Epic,
// which is what cli.decodedTicket copies onto the Ticket it renders.
func TestDecodedIssueCarriesItsMergeRequests(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		issuePath:                 {{file: "issue.json"}},
		issueMRsPath:              {{file: "related_merge_requests.json"}},
		mergeRequestApprovalsPath: {{file: "approvals_approved.json"}},
	})

	snap, err := newProvider(s).FetchEpic(context.Background(), issueRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	if len(snap.Epic.PullRequests) != 1 {
		t.Fatalf("Epic.PullRequests = %+v, want the one related merge request", snap.Epic.PullRequests)
	}
	if got := snap.Epic.PullRequests[0].Review; got != model.ReviewApproved {
		t.Errorf("Review = %v, want Approved", got)
	}
	// The decoded Ticket reads the same as it would in a list.
	if snap.Epic.Status != model.StatusInProgress {
		t.Errorf("Epic.Status = %v, want InProgress", snap.Epic.Status)
	}
}

// A collection is not an issue: there is no endpoint and no meaning.
func TestCollectionsCarryNoMergeRequestsOfTheirOwn(t *testing.T) {
	epic, err := newProvider(fullEpic(t)).FetchEpic(context.Background(), epicRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	if epic.Epic.PullRequests != nil {
		t.Errorf("an epic's Epic.PullRequests = %+v, want nil", epic.Epic.PullRequests)
	}

	milestone, err := newProvider(fullProjectMilestone(t)).FetchEpic(context.Background(), projectMilestoneRef)
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	if milestone.Epic.PullRequests != nil {
		t.Errorf("a milestone's Epic.PullRequests = %+v, want nil", milestone.Epic.PullRequests)
	}
}
