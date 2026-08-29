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
	if got := len(newPullRequests(&conn, nil)); got != 20 {
		t.Errorf("newPullRequests returned %d pull requests, want the 20 the cap allowed", got)
	}
	if conn.Nodes[0].CreatedAt.IsZero() {
		t.Error("createdAt did not decode; lead selection would silently fall back to the number")
	}
}

func pullRequestWire(number int, repository, state string, draft bool, created time.Time) pullRequestNode {
	node := pullRequestNode{
		TypeName:  "PullRequest",
		Number:    number,
		Title:     fmt.Sprintf("PR %d", number),
		URL:       fmt.Sprintf("https://github.com/%s/pull/%d", repository, number),
		State:     state,
		IsDraft:   draft,
		CreatedAt: created,
	}
	if repository != "" {
		node.Repository = &repositoryRef{NameWithOwner: repository}
	}
	return node
}

func crossReferenceEvents(nodes ...*pullRequestNode) *crossReferenceConnection {
	events := make([]crossReferencedEventNode, len(nodes))
	for i, node := range nodes {
		events[i].Source = node
	}
	return &crossReferenceConnection{Nodes: events}
}

func TestPullRequestsDeduplicateStableIdentityFirstOccurrenceWins(t *testing.T) {
	closingNode := pullRequestWire(51, "Acme/Widgets", "CLOSED", false, time.Time{})
	closingNode.Title = "closing relationship payload"
	timelineNode := pullRequestWire(51, "acme/widgets", "OPEN", false, time.Now())
	timelineNode.Title = "timeline payload"

	got := newPullRequests(
		&pullRequestConnection{Nodes: []pullRequestNode{closingNode}},
		crossReferenceEvents(&timelineNode, &timelineNode),
	)

	if len(got) != 1 {
		t.Fatalf("PullRequests = %+v, want one case-insensitively deduplicated PR", got)
	}
	if got[0].Title != closingNode.Title || got[0].State != model.PRClosed {
		t.Errorf("PullRequests[0] = %+v, want the closing relationship's first payload", got[0])
	}
}

func TestPullRequestsDeduplicateRepeatedTimelineEvents(t *testing.T) {
	first := pullRequestWire(51, "acme/widgets", "OPEN", false, time.Time{})
	first.Title = "first event payload"
	repeated := first
	repeated.Title = "later event payload"

	got := newPullRequests(nil, crossReferenceEvents(&first, &repeated))

	if len(got) != 1 || got[0].Title != "first event payload" {
		t.Errorf("PullRequests = %+v, want one PR retaining the first timeline event payload", got)
	}
}

func TestPullRequestsKeepEqualNumbersFromDifferentRepositories(t *testing.T) {
	first := pullRequestWire(7, "acme/widgets", "MERGED", false, time.Time{})
	second := pullRequestWire(7, "niekcandaele/sitrep", "MERGED", false, time.Time{})

	got := newPullRequests(
		&pullRequestConnection{Nodes: []pullRequestNode{first}},
		crossReferenceEvents(&second),
	)

	if len(got) != 2 {
		t.Fatalf("PullRequests = %+v, want both repository-qualified identities", got)
	}
	if got[0].Repository != "acme/widgets" || got[1].Repository != "niekcandaele/sitrep" {
		t.Errorf("PullRequest repositories = %q, %q, want closing first then timeline",
			got[0].Repository, got[1].Repository)
	}
}

func TestPullRequestsPreserveRelationshipOrderBeforeLeadSelection(t *testing.T) {
	closingOne := pullRequestWire(1, "acme/widgets", "MERGED", false, time.Time{})
	closingTwo := pullRequestWire(2, "acme/widgets", "MERGED", false, time.Time{})
	timelineOne := pullRequestWire(3, "acme/widgets", "MERGED", false, time.Time{})
	timelineTwo := pullRequestWire(4, "acme/widgets", "MERGED", false, time.Time{})

	got := newPullRequests(
		&pullRequestConnection{Nodes: []pullRequestNode{closingOne, closingTwo}},
		crossReferenceEvents(&timelineOne, &timelineTwo),
	)

	want := []int{1, 2, 3, 4}
	for i, number := range want {
		if len(got) <= i || got[i].Number != number {
			t.Fatalf("PullRequest order = %+v, want closing candidates then timeline candidates %v", got, want)
		}
	}
}

func TestPullRequestsChooseLeadAfterUnion(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	closingMerged := pullRequestWire(500, "acme/widgets", "MERGED", false, older)
	closingClosed := pullRequestWire(20, "acme/widgets", "CLOSED", false, newer)
	timelineOpen := pullRequestWire(900, "other/large-numbers", "OPEN", false, older)
	timelineDraft := pullRequestWire(4, "niekcandaele/sitrep", "OPEN", true, newer)
	timelineClosed := pullRequestWire(5, "niekcandaele/sitrep", "CLOSED", false, newer.Add(time.Hour))

	got := newPullRequests(
		&pullRequestConnection{Nodes: []pullRequestNode{closingMerged, closingClosed}},
		crossReferenceEvents(&timelineOpen, &timelineDraft, &timelineClosed),
	)

	want := []struct {
		number     int
		repository string
	}{
		{4, "niekcandaele/sitrep"},
		{500, "acme/widgets"},
		{20, "acme/widgets"},
		{900, "other/large-numbers"},
		{5, "niekcandaele/sitrep"},
	}
	if len(got) != len(want) {
		t.Fatalf("PullRequests = %+v, want %d candidates after union", got, len(want))
	}
	for i, expected := range want {
		if got[i].Number != expected.number || got[i].Repository != expected.repository {
			t.Errorf("PullRequests[%d] = %+v, want #%d in %s", i, got[i], expected.number, expected.repository)
		}
	}
	if got[0].State != model.PRDraft {
		t.Errorf("lead State = %s, want the newer in-flight draft", got[0].State)
	}
}

func TestPullRequestsPreserveLegacyIdentitylessClosingNode(t *testing.T) {
	legacyClosing := pullRequestWire(51, "", "MERGED", false, time.Time{})
	timeline := pullRequestWire(51, "acme/widgets", "MERGED", false, time.Time{})

	got := newPullRequests(
		&pullRequestConnection{Nodes: []pullRequestNode{legacyClosing}},
		crossReferenceEvents(&timeline),
	)

	if len(got) != 2 {
		t.Fatalf("PullRequests = %+v, want the legacy closing payload and separately identified timeline PR", got)
	}
	if got[0].Number != 51 || got[0].Repository != "" || got[1].Repository != "acme/widgets" {
		t.Errorf("PullRequests = %+v, want identity-less closing data preserved without unsafe deduplication", got)
	}
}

func TestCrossReferenceSourcesRequirePullRequestIdentity(t *testing.T) {
	issueSource := pullRequestWire(8, "acme/widgets", "OPEN", false, time.Time{})
	issueSource.TypeName = "Issue"
	missingRepository := pullRequestWire(9, "", "OPEN", false, time.Time{})
	missingNumber := pullRequestWire(0, "acme/widgets", "OPEN", false, time.Time{})
	wrongCaseType := pullRequestWire(10, "acme/widgets", "OPEN", false, time.Time{})
	wrongCaseType.TypeName = "pullrequest"

	tests := []struct {
		name       string
		connection *crossReferenceConnection
	}{
		{name: "absent connection"},
		{name: "null source", connection: crossReferenceEvents(nil)},
		{name: "Issue source", connection: crossReferenceEvents(&issueSource)},
		{name: "missing repository", connection: crossReferenceEvents(&missingRepository)},
		{name: "non-positive number", connection: crossReferenceEvents(&missingNumber)},
		{name: "typename is exact", connection: crossReferenceEvents(&wrongCaseType)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := newPullRequests(nil, tt.connection); got != nil {
				t.Errorf("PullRequests = %+v, want nil without a usable PullRequest source", got)
			}
		})
	}
}

// GitHub exposes no implementation-specific discriminator for a PR-sourced
// mention whose willCloseTarget is false. Including every valid PullRequest
// source deliberately admits incidental mentions rather than guessing from a
// branch name, PR text, or any unrequested field.
func TestCrossReferenceIncludesAmbiguousPullRequestMention(t *testing.T) {
	mention := pullRequestWire(51, "acme/integration", "OPEN", false, time.Time{})

	got := newPullRequests(nil, crossReferenceEvents(&mention))

	if len(got) != 1 || got[0].Number != 51 || got[0].Repository != "acme/integration" {
		t.Errorf("PullRequests = %+v, want the valid PR-sourced mention included", got)
	}
}

func TestCrossReferenceConnectionDecodesBoundedWindowMetadata(t *testing.T) {
	payload := `{
		"totalCount": 27,
		"pageInfo": {"hasPreviousPage": true, "startCursor": "oldest-retained"},
		"nodes": [
			{"source": null},
			{"source": {"__typename": "Issue"}},
			{"source": {"__typename": "PullRequest", "number": 51,
				"repository": {"nameWithOwner": "acme/widgets"}}}
		]
	}`

	var connection crossReferenceConnection
	if err := json.Unmarshal([]byte(payload), &connection); err != nil {
		t.Fatalf("decoding cross-reference connection: %v", err)
	}
	if connection.TotalCount != 27 || !connection.PageInfo.HasPreviousPage ||
		connection.PageInfo.StartCursor != "oldest-retained" {
		t.Errorf("connection metadata = %+v, want explicit retained-window evidence", connection)
	}
	got := newPullRequests(nil, &connection)
	if len(got) != 1 || got[0].Number != 51 {
		t.Errorf("PullRequests = %+v, want only the usable PullRequest source", got)
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

func TestPullRequestTotal(t *testing.T) {
	held := func(n int) []model.PullRequest {
		prs := make([]model.PullRequest, n)
		for i := range prs {
			prs[i] = model.PullRequest{Number: 100 + i}
		}
		return prs
	}

	tests := []struct {
		name    string
		closing *pullRequestConnection
		prs     []model.PullRequest
		want    int
	}{
		{
			name: "no connection and no pull requests is no total",
			want: 0,
		},
		{
			name:    "an absent totalCount falls back to what is held",
			closing: &pullRequestConnection{},
			prs:     held(3),
			want:    3,
		},
		{
			name:    "a totalCount matching the node count is that count",
			closing: &pullRequestConnection{TotalCount: 3},
			prs:     held(3),
			want:    3,
		},
		{
			name:    "a truncated connection reports GitHub's own total",
			closing: &pullRequestConnection{TotalCount: 34},
			prs:     held(20),
			want:    34,
		},
		{
			name:    "cross-reference extras above the totalCount floor the answer at what is held",
			closing: &pullRequestConnection{TotalCount: 20},
			prs:     held(23),
			want:    23,
		},
		{
			name:    "a nil connection beside cross-referenced pull requests counts those",
			closing: nil,
			prs:     held(2),
			want:    2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pullRequestTotal(tc.closing, tc.prs); got != tc.want {
				t.Errorf("pullRequestTotal = %d, want %d", got, tc.want)
			}
		})
	}
}
