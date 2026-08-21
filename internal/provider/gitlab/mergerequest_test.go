package gitlab

import (
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

func TestNormalizePRState(t *testing.T) {
	tests := []struct {
		state string
		draft bool
		want  model.PRState
	}{
		{"merged", false, model.PRMerged},
		// How it ended outranks the draft flag: a draft can be merged or closed.
		{"merged", true, model.PRMerged},
		{"closed", false, model.PRClosed},
		{"closed", true, model.PRClosed},
		// GitLab locks a merge request while a merge is in flight.
		{"locked", false, model.PROpen},
		{"opened", true, model.PRDraft},
		{"opened", false, model.PROpen},
		{"", false, model.PRUnknown},
		{"quantum_superposition", false, model.PRUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			if got := normalizePRState(tt.state, tt.draft); got != tt.want {
				t.Errorf("normalizePRState(%q, %v) = %v, want %v", tt.state, tt.draft, got, tt.want)
			}
		})
	}
}

func TestNormalizeReview(t *testing.T) {
	approved := &approvalsWire{ApprovedBy: []approvedByWire{{}}, ApprovalsRequired: 0}
	pending := &approvalsWire{ApprovalsLeft: 1, ApprovalsRequired: 1}
	// The trap: `approved` is true here because the requirement is zero, and
	// nobody has approved anything.
	zeroRequired := &approvalsWire{ApprovalsRequired: 0, ApprovalsLeft: 0}

	tests := []struct {
		name      string
		status    string
		reviewers int
		approvals *approvalsWire
		want      model.ReviewState
	}{
		{"requested changes wins outright", "requested_changes", 0, approved, model.ReviewChangesRequested},
		{"somebody clicked Approve", "not_approved", 1, approved, model.ReviewApproved},
		{"an approval is still owed", "not_approved", 0, nil, model.ReviewPending},
		{"approvals_left says so", "mergeable", 0, pending, model.ReviewPending},
		{"somebody was asked to review", "mergeable", 1, nil, model.ReviewPending},
		{"zero approvals required is not approved", "mergeable", 0, zeroRequired, model.ReviewNone},
		{"nobody has been asked", "mergeable", 0, nil, model.ReviewNone},
		// A merged or closed merge request reads not_open.
		{"a merge request that is no longer open", "not_open", 0, nil, model.ReviewNone},
		{"no merge status at all", "", 0, nil, model.ReviewNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeReview(tt.status, tt.reviewers, tt.approvals)
			if got != tt.want {
				t.Errorf("normalizeReview(%q, %d, %+v) = %v, want %v",
					tt.status, tt.reviewers, tt.approvals, got, tt.want)
			}
		})
	}
}

func TestNormalizeChecks(t *testing.T) {
	tests := []struct {
		status string
		want   model.CheckState
	}{
		{"", model.ChecksNone},
		{"success", model.ChecksPassing},
		{"failed", model.ChecksFailing},
		// A cancelled pipeline is not a green pipeline.
		{"canceled", model.ChecksFailing},
		{"canceling", model.ChecksFailing},
		{"skipped", model.ChecksNone},
		{"created", model.ChecksPending},
		{"waiting_for_resource", model.ChecksPending},
		{"waiting_for_callback", model.ChecksPending},
		{"preparing", model.ChecksPending},
		{"pending", model.ChecksPending},
		{"running", model.ChecksPending},
		{"scheduled", model.ChecksPending},
		{"manual", model.ChecksPending},
		// sitrep does not report green it did not see.
		{"levitating", model.ChecksPending},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			if got := normalizeChecks(tt.status); got != tt.want {
				t.Errorf("normalizeChecks(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestLeadFirst(t *testing.T) {
	tests := []struct {
		name string
		prs  []model.PullRequest
		want []int
	}{
		{
			name: "a merged one leads however old it is",
			prs: []model.PullRequest{
				{Number: 3, State: model.PROpen},
				{Number: 1, State: model.PRMerged},
				{Number: 2, State: model.PRClosed},
			},
			want: []int{1, 3, 2},
		},
		{
			name: "otherwise the newest live one leads",
			prs: []model.PullRequest{
				{Number: 1, State: model.PROpen},
				{Number: 9, State: model.PRClosed},
				{Number: 4, State: model.PRDraft},
			},
			want: []int{4, 1, 9},
		},
		{
			name: "otherwise the newest of whatever is left",
			prs: []model.PullRequest{
				{Number: 1, State: model.PRClosed},
				{Number: 7, State: model.PRClosed},
			},
			want: []int{7, 1},
		},
		{
			name: "a tie keeps the earlier element",
			prs: []model.PullRequest{
				{Number: 5, State: model.PROpen, Repository: "acme/one"},
				{Number: 5, State: model.PROpen, Repository: "acme/two"},
			},
			want: []int{5, 5},
		},
		{
			name: "the lead is already first",
			prs:  []model.PullRequest{{Number: 2, State: model.PRMerged}, {Number: 1, State: model.PROpen}},
			want: []int{2, 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := leadFirst(tt.prs)
			if len(got) != len(tt.want) {
				t.Fatalf("leadFirst dropped something: got %d, want %d", len(got), len(tt.want))
			}
			for i, want := range tt.want {
				if got[i].Number != want {
					t.Errorf("leadFirst[%d].Number = %d, want %d", i, got[i].Number, want)
				}
			}
		})
	}
}

func TestStatusWithMergeRequests(t *testing.T) {
	open := []model.PullRequest{{Number: 1, State: model.PROpen}}
	draft := []model.PullRequest{{Number: 1, State: model.PRDraft}}
	merged := []model.PullRequest{{Number: 1, State: model.PRMerged}}

	tests := []struct {
		name string
		base model.StatusCategory
		prs  []model.PullRequest
		want model.StatusCategory
	}{
		{"an open merge request is work happening", model.StatusTodo, open, model.StatusInProgress},
		{"so is a draft one", model.StatusTodo, draft, model.StatusInProgress},
		{"a merged one means nobody is coding", model.StatusTodo, merged, model.StatusTodo},
		{"no merge requests at all", model.StatusTodo, nil, model.StatusTodo},
		{"a Done Ticket is never reopened", model.StatusDone, open, model.StatusDone},
		{"nor is a Cancelled one", model.StatusCancelled, open, model.StatusCancelled},
		{"an Unknown Status stays Unknown", model.StatusUnknown, open, model.StatusUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusWithMergeRequests(tt.base, tt.prs); got != tt.want {
				t.Errorf("statusWithMergeRequests(%v, %+v) = %v, want %v", tt.base, tt.prs, got, tt.want)
			}
		})
	}
}

// A merge request's project comes from its own payload, because it can live
// somewhere else than the Ticket it moves.
func TestMergeRequestProjectPath(t *testing.T) {
	tests := []struct {
		name string
		mr   mergeRequestWire
		want string
	}{
		{
			name: "from references.full",
			mr:   mergeRequestWire{Reference: referencesWire{Full: "gitlab-org/cli!3761"}},
			want: "gitlab-org/cli",
		},
		{
			name: "a nested namespace",
			mr:   mergeRequestWire{Reference: referencesWire{Full: "gitlab-org/platform/core!109"}},
			want: "gitlab-org/platform/core",
		},
		{
			name: "from web_url when references is absent",
			mr:   mergeRequestWire{WebURL: "https://gitlab.com/gitlab-org/cli/-/merge_requests/110"},
			want: "gitlab-org/cli",
		},
		{
			name: "neither",
			mr:   mergeRequestWire{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.mr.projectPath(); got != tt.want {
				t.Errorf("projectPath = %q, want %q", got, tt.want)
			}
		})
	}
}

// An empty related_merge_requests list is nil, the model's documented "none".
func TestNewPullRequestsOnNothing(t *testing.T) {
	if got := newPullRequests(nil); got != nil {
		t.Errorf("newPullRequests(nil) = %+v, want nil", got)
	}
	if got := newPullRequests([]mergeRequestWire{}); got != nil {
		t.Errorf("newPullRequests([]) = %+v, want nil", got)
	}
}
