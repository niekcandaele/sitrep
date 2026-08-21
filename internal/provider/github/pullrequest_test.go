package github

// The mapping rules this ticket exists for are tested directly, in-package:
// they are documented business rules, the same narrow exception normalizeStatus
// gets. Everything else about the driver is asserted on the normalized model
// through the replay server in github_test.go.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
)

func TestNormalizePRState(t *testing.T) {
	tests := []struct {
		state   string
		isDraft bool
		want    model.PRState
	}{
		{"MERGED", false, model.PRMerged},
		// Merged and closed win over the draft flag: a draft pull request can
		// be closed, and how it ended is the more important half.
		{"MERGED", true, model.PRMerged},
		{"CLOSED", false, model.PRClosed},
		{"CLOSED", true, model.PRClosed},
		{"OPEN", false, model.PROpen},
		{"OPEN", true, model.PRDraft},
		{"open", true, model.PRDraft},
		{" OPEN ", false, model.PROpen},
		{"", false, model.PRUnknown},
		{"STATE_FROM_THE_FUTURE", false, model.PRUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.state+map[bool]string{true: " draft", false: ""}[tt.isDraft], func(t *testing.T) {
			if got := normalizePRState(tt.state, tt.isDraft); got != tt.want {
				t.Errorf("normalizePRState(%q, %v) = %v, want %v", tt.state, tt.isDraft, got, tt.want)
			}
		})
	}
}

func TestNormalizeReview(t *testing.T) {
	tests := map[string]model.ReviewState{
		"APPROVED":                 model.ReviewApproved,
		"CHANGES_REQUESTED":        model.ReviewChangesRequested,
		"REVIEW_REQUIRED":          model.ReviewPending,
		"approved":                 model.ReviewApproved,
		"":                         model.ReviewNone,
		"  ":                       model.ReviewNone,
		"DECISION_FROM_THE_FUTURE": model.ReviewNone,
	}

	for decision, want := range tests {
		t.Run(decision, func(t *testing.T) {
			if got := normalizeReview(decision); got != want {
				t.Errorf("normalizeReview(%q) = %v, want %v", decision, got, want)
			}
		})
	}
}

func TestNormalizeChecks(t *testing.T) {
	tests := map[string]model.CheckState{
		"SUCCESS":  model.ChecksPassing,
		"FAILURE":  model.ChecksFailing,
		"ERROR":    model.ChecksFailing,
		"PENDING":  model.ChecksPending,
		"EXPECTED": model.ChecksPending,
		"success":  model.ChecksPassing,
		// No rollup means no CI configured, which is a real answer.
		"": model.ChecksNone,
		// A state sitrep does not know is not green. It never reports a green
		// it did not see.
		"ROLLUP_FROM_THE_FUTURE": model.ChecksPending,
	}

	for rollup, want := range tests {
		t.Run(rollup, func(t *testing.T) {
			if got := normalizeChecks(rollup); got != want {
				t.Errorf("normalizeChecks(%q) = %v, want %v", rollup, got, want)
			}
		})
	}
}

// pr is a PullRequest with only the fields lead selection reads.
func pr(number int, state model.PRState) model.PullRequest {
	return model.PullRequest{Number: number, State: state}
}

func TestLeadPullRequest(t *testing.T) {
	tests := []struct {
		name string
		prs  []model.PullRequest
		want int // the lead's number, or 0 for "no lead"
	}{
		{"none", nil, 0},
		{"empty", []model.PullRequest{}, 0},
		{"a single open one", []model.PullRequest{pr(3, model.PROpen)}, 3},
		{
			// Work in flight is the Ticket's current situation, and it is the
			// same evidence statusWithPullRequests reads.
			"an open one wins over a merged one",
			[]model.PullRequest{pr(1, model.PRClosed), pr(2, model.PRMerged), pr(9, model.PROpen)},
			9,
		},
		{
			"merged wins when nothing is in flight",
			[]model.PullRequest{pr(1, model.PRClosed), pr(2, model.PRMerged)},
			2,
		},
		{
			"the first merged one in API order wins",
			[]model.PullRequest{pr(5, model.PRMerged), pr(6, model.PRMerged)},
			5,
		},
		{
			"the newest open one wins over an older one",
			[]model.PullRequest{pr(7, model.PROpen), pr(9, model.PROpen)},
			9,
		},
		{
			"a draft counts as open for lead selection",
			[]model.PullRequest{pr(4, model.PRClosed), pr(8, model.PRDraft)},
			8,
		},
		{
			"the newest of whatever is left",
			[]model.PullRequest{pr(2, model.PRClosed), pr(6, model.PRClosed), pr(3, model.PRUnknown)},
			6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := leadPullRequest(tt.prs, nil)
			if tt.want == 0 {
				if ok {
					t.Fatalf("leadPullRequest = %+v, want no lead", got)
				}
				return
			}
			if !ok {
				t.Fatal("leadPullRequest found no lead, want one")
			}
			if got.Number != tt.want {
				t.Errorf("lead = #%d, want #%d", got.Number, tt.want)
			}
		})
	}
}

// Across repositories two pull requests can share a number. The earlier one in
// GitHub's order wins, so the answer never depends on map iteration or sort
// stability.
func TestLeadPullRequestBreaksACrossRepoTieDeterministically(t *testing.T) {
	prs := []model.PullRequest{
		{Number: 4, Repository: "acme/widgets", State: model.PROpen},
		{Number: 4, Repository: "niekcandaele/sitrep", State: model.PROpen},
	}

	for range 10 {
		got, ok := leadPullRequest(prs, nil)
		if !ok || got.Repository != "acme/widgets" {
			t.Fatalf("lead = %+v (ok=%v), want the first element in API order", got, ok)
		}
	}
}

// A pull request number is unique only within its repository, so the pull
// request with the larger number can be the older one. Creation time is what
// decides, not the number.
func TestLeadPullRequestPrefersTheNewerAcrossRepositories(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	prs := []model.PullRequest{
		{Number: 900, Repository: "acme/widgets", State: model.PROpen},
		{Number: 4, Repository: "niekcandaele/sitrep", State: model.PROpen},
	}
	created := []time.Time{older, newer}

	got, ok := leadPullRequest(prs, created)
	if !ok {
		t.Fatal("leadPullRequest found no lead, want one")
	}
	if got.Repository != "niekcandaele/sitrep" {
		t.Errorf("lead = %+v, want the newer pull request despite its lower number", got)
	}
}

// An old fixture, or a payload that omitted createdAt, still gets a
// deterministic answer: the number is the fallback.
func TestLeadPullRequestFallsBackToTheNumberWithoutTimestamps(t *testing.T) {
	prs := []model.PullRequest{pr(9, model.PROpen), pr(3, model.PROpen)}

	for _, created := range [][]time.Time{nil, {{}, {}}, {time.Now(), {}}} {
		got, ok := leadPullRequest(prs, created)
		if !ok || got.Number != 9 {
			t.Errorf("lead = %+v (ok=%v) for created=%v, want #9 by number", got, ok, created)
		}
	}
}

// The connection is capped at twenty and does not paginate, so TotalCount is
// the only thing that knows a Ticket lost pull requests. Nothing renders it
// yet; this pins the shape a renderer would read, and the ordering that comes
// off the wire with it.
func TestPullRequestConnectionDecodesTotalCountAndCreatedAt(t *testing.T) {
	var nodes []string
	for i := range 20 {
		nodes = append(nodes, fmt.Sprintf(
			`{"number":%d,"title":"pr","url":"u","state":"OPEN","isDraft":false,`+
				`"repository":{"nameWithOwner":"acme/widgets"},"createdAt":"2026-0%d-01T00:00:00Z"}`,
			100+i, 1+i%9))
	}
	payload := `{"totalCount":25,"nodes":[` + strings.Join(nodes, ",") + `]}`

	var conn pullRequestConnection
	if err := json.Unmarshal([]byte(payload), &conn); err != nil {
		t.Fatalf("decoding the connection: %v", err)
	}
	if conn.TotalCount != 25 {
		t.Errorf("TotalCount = %d, want 25: the Ticket has more than the cap", conn.TotalCount)
	}
	if got := len(newPullRequests(&conn)); got != 20 {
		t.Errorf("newPullRequests returned %d pull requests, want the 20 the cap allowed", got)
	}
	if conn.Nodes[0].CreatedAt.IsZero() {
		t.Error("createdAt did not decode; lead selection would silently fall back to the number")
	}
}

// The Provider's ordering contract: index 0 is the lead, everything else keeps
// GitHub's order, and nothing is dropped.
func TestLeadFirstMovesTheLeadAndKeepsTheRest(t *testing.T) {
	prs := []model.PullRequest{pr(1, model.PRClosed), pr(2, model.PRMerged), pr(9, model.PROpen)}

	got := leadFirst(prs, nil)

	want := []int{9, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("leadFirst returned %d pull requests, want %d: none may be dropped", len(got), len(want))
	}
	for i, n := range want {
		if got[i].Number != n {
			t.Errorf("pull request %d = #%d, want #%d", i, got[i].Number, n)
		}
	}
}

func TestStatusWithPullRequests(t *testing.T) {
	tests := []struct {
		name string
		base model.StatusCategory
		prs  []model.PullRequest
		want model.StatusCategory
	}{
		{"an open Ticket with an open pull request is being worked on",
			model.StatusTodo, []model.PullRequest{pr(1, model.PROpen)}, model.StatusInProgress},
		{"a draft pull request means the agent is still coding",
			model.StatusTodo, []model.PullRequest{pr(1, model.PRDraft)}, model.StatusInProgress},
		{"a rejected pull request leaves the Ticket where it was",
			model.StatusTodo, []model.PullRequest{pr(1, model.PRClosed)}, model.StatusTodo},
		{"a merged pull request does not promote a still-open Ticket",
			model.StatusTodo, []model.PullRequest{pr(1, model.PRMerged)}, model.StatusTodo},
		{"an unknown pull request state promotes nothing",
			model.StatusTodo, []model.PullRequest{pr(1, model.PRUnknown)}, model.StatusTodo},
		{"no pull requests at all",
			model.StatusTodo, nil, model.StatusTodo},
		{"a finished Ticket is never reopened by a pull request",
			model.StatusDone, []model.PullRequest{pr(1, model.PROpen)}, model.StatusDone},
		{"a cancelled Ticket stays cancelled and stays out of the denominator",
			model.StatusCancelled, []model.PullRequest{pr(1, model.PROpen)}, model.StatusCancelled},
		{"an unknown status is left alone rather than guessed at",
			model.StatusUnknown, []model.PullRequest{pr(1, model.PROpen)}, model.StatusUnknown},
		{"an already in-progress status is unchanged",
			model.StatusInProgress, nil, model.StatusInProgress},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusWithPullRequests(tt.base, tt.prs); got != tt.want {
				t.Errorf("statusWithPullRequests(%v, %+v) = %v, want %v", tt.base, tt.prs, got, tt.want)
			}
		})
	}
}
