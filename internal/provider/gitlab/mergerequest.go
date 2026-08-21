package gitlab

import (
	"strings"

	"github.com/niekcandaele/sitrep/internal/model"
)

// The wire structs below mirror GitLab's vocabulary, as the rest of this package
// does: GitLab says merge request, so these say merge request. Everything
// downstream of newPullRequests says pull request, because model.PullRequest is
// sitrep's word for the code moving a Ticket whichever Tracker serves it. There
// is no model.MergeRequest and there will not be one.
//
// Every nested object is a pointer or a slice that can be absent:
// head_pipeline is null on a merge request whose project runs no CI, and
// references is missing from some payloads entirely.

// mergeRequestWire is one merge request, in the *single*-merge-request shape the
// related_merge_requests endpoint returns — which is the shape that carries
// head_pipeline. See the package doc.
type mergeRequestWire struct {
	ID        int            `json:"id"`
	IID       int            `json:"iid"`
	ProjectID int            `json:"project_id"`
	Title     string         `json:"title"`
	State     string         `json:"state"`
	Draft     bool           `json:"draft"`
	WebURL    string         `json:"web_url"`
	Reference referencesWire `json:"references"`

	// DetailedMergeStatus is GitLab's own summary of what stands between this
	// merge request and merging. It replaced the deprecated merge_status in 15.6,
	// and it is the only free source of a review verdict; see normalizeReview.
	DetailedMergeStatus string `json:"detailed_merge_status"`

	// Reviewers is read only as "was anybody asked to review". Its entries carry
	// a `state` field, but that state is the reviewer's *user account* state
	// (active / blocked / deactivated), not a review verdict — reading it as one
	// would be a silent lie, so userWire has no field for it.
	Reviewers []userWire `json:"reviewers"`

	HeadPipeline *pipelineWire `json:"head_pipeline"`
}

// pipelineWire is the merge request's head pipeline. Only status is read:
// detailed_status is the web UI's presentation object — icons, favicon paths,
// tooltips — and not an API contract.
type pipelineWire struct {
	Status string `json:"status"`
}

// approvalsWire is the Free-tier merge request approvals payload.
type approvalsWire struct {
	// ApprovedBy is the honest signal, and `approved` is the trap; see
	// normalizeReview.
	ApprovedBy        []approvedByWire `json:"approved_by"`
	ApprovalsLeft     int              `json:"approvals_left"`
	ApprovalsRequired int              `json:"approvals_required"`
}

// approvedByWire is one approval: GitLab nests the account under "user".
type approvedByWire struct {
	User *userWire `json:"user"`
}

// GitLab's merge request states. There are four; "locked" is the one that
// surprises people.
const (
	mrStateOpened = "opened"
	mrStateClosed = "closed"
	mrStateMerged = "merged"
	mrStateLocked = "locked"
)

// The detailed_merge_status values this driver reads. GitLab documents about two
// dozen; these two are the only ones that carry a review verdict.
const (
	mergeStatusRequestedChanges = "requested_changes"
	mergeStatusNotApproved      = "not_approved"
)

// projectPath is the project a merge request lives in, taken from
// references.full ("gitlab-org/cli!3761") and, when that is absent, from the
// path in web_url. A merge request can live in a different project from the
// Ticket it moves, so this always comes from the merge request's own payload.
func (m mergeRequestWire) projectPath() string {
	if at := strings.Index(m.Reference.Full, "!"); at > 0 {
		return m.Reference.Full[:at]
	}
	return projectPathFromWebURL(m.WebURL)
}

// pipelineStatus is the head pipeline's status, or "" when the merge request has
// no pipeline at all.
func (m mergeRequestWire) pipelineStatus() string {
	if m.HeadPipeline == nil {
		return ""
	}
	return m.HeadPipeline.Status
}

// newPullRequests maps a Ticket's related merge requests onto sitrep's model, in
// GitLab's own order. correlate refines the lead's review posture and then moves
// the lead to the front with leadFirst; index 0 is the Provider's contract with
// renderers that show one merge request per row (see model.Ticket.PullRequests).
//
// A Ticket with no related merge requests gets nil, the model's documented
// "none", rather than an empty slice.
func newPullRequests(mrs []mergeRequestWire) []model.PullRequest {
	if len(mrs) == 0 {
		return nil
	}
	prs := make([]model.PullRequest, 0, len(mrs))
	for _, m := range mrs {
		prs = append(prs, newPullRequest(m, nil))
	}
	return prs
}

// newPullRequest maps one merge request. approvals is the answer to the one
// question detailed_merge_status cannot carry, and it is nil for every merge
// request correlate did not ask about.
func newPullRequest(m mergeRequestWire, approvals *approvalsWire) model.PullRequest {
	return model.PullRequest{
		Number:     m.IID,
		Title:      m.Title,
		URL:        m.WebURL,
		Repository: m.projectPath(),
		State:      normalizePRState(m.State, m.Draft),
		Review:     normalizeReview(m.DetailedMergeStatus, len(m.Reviewers), approvals),
		Checks:     normalizeChecks(m.pipelineStatus()),
	}
}

// normalizePRState maps a GitLab merge request state and its draft flag onto
// sitrep's PRState. Draft is a flag on GitLab and a state in sitrep, because a
// draft merge request means "the agent is still coding" and that is a different
// situation report from "waiting on review".
//
// Merged and closed are checked before the draft flag: a draft merge request can
// be closed, and how it ended is the more important half of the answer.
//
// The flag read is `draft`. Its twin `work_in_progress` is deprecated and says
// the same thing; reading both could only ever disagree.
func normalizePRState(state string, draft bool) model.PRState {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case mrStateMerged:
		return model.PRMerged
	case mrStateClosed:
		return model.PRClosed
	case mrStateLocked:
		// GitLab locks a merge request while a merge is in flight, so it is open
		// and about to land. Reporting Unknown would read as a driver bug on a
		// perfectly healthy merge request, and Merged would claim work landed that
		// has not.
		return model.PROpen
	case mrStateOpened:
		if draft {
			return model.PRDraft
		}
		return model.PROpen
	default:
		return model.PRUnknown
	}
}

// normalizeReview maps GitLab's detailed_merge_status, its reviewer list and —
// where correlate paid for it — its approvals onto sitrep's ReviewState.
//
// The order is the rule, and each step is a fact rather than a guess:
//
//  1. requested_changes is GitLab stating that reviewers have requested changes,
//     and it is a hard block, so it wins.
//  2. approvals.approved_by non-empty means somebody clicked Approve. Read
//     approved_by, *not* `approved`: `approved` means "the approval requirement
//     is satisfied", which is true with nobody having approved anything on a
//     project whose approvals_required is 0.
//  3. not_approved, an outstanding approvals_left, or anybody asked to review at
//     all is Pending.
//
// Anything else is ReviewNone — the model's documented "nobody has been asked to
// review", not a mapping failure, which is why ReviewState has no unknown. Note
// that detailed_merge_status is only meaningful while a merge request is open; a
// merged or closed one reads not_open, which falls through to step 4.
func normalizeReview(detailedMergeStatus string, reviewerCount int, approvals *approvalsWire) model.ReviewState {
	status := strings.ToLower(strings.TrimSpace(detailedMergeStatus))

	if status == mergeStatusRequestedChanges {
		return model.ReviewChangesRequested
	}
	if approvals != nil && len(approvals.ApprovedBy) > 0 {
		return model.ReviewApproved
	}
	if status == mergeStatusNotApproved || (approvals != nil && approvals.ApprovalsLeft > 0) || reviewerCount > 0 {
		return model.ReviewPending
	}
	return model.ReviewNone
}

// normalizeChecks maps the head pipeline's status onto sitrep's CheckState. An
// absent head_pipeline arrives as "".
//
// A cancelled pipeline is reported as failing. model.CheckState has no
// "cancelled": Pending would leave a dead pipeline looking alive and None would
// look like a project with no CI, where Failing is the value that makes a human
// go and look — which is what a situation report is for. `skipped` is None for
// the same reasoning read the other way: nothing ran.
//
// An unrecognized status is ChecksPending, never ChecksPassing and never
// ChecksNone: sitrep does not report green it did not see, and "no CI" is a
// different, meaningful answer from "a state we do not know yet".
func normalizeChecks(pipelineStatus string) model.CheckState {
	switch strings.ToLower(strings.TrimSpace(pipelineStatus)) {
	case "":
		return model.ChecksNone
	case "success":
		return model.ChecksPassing
	case "failed", "canceled", "canceling":
		return model.ChecksFailing
	case "skipped":
		return model.ChecksNone
	default:
		return model.ChecksPending
	}
}

// leadFirst returns prs with the lead merge request moved to the front, leaving
// every other element in GitLab's order. Nothing is dropped.
//
// This rule and the three functions below it are a deliberate copy of the GitHub
// driver's, not a shared helper: a Provider owns its own translation layer, and
// hoisting the lead rule into internal/model would couple two drivers' mappings
// through a package neither of them owns.
func leadFirst(prs []model.PullRequest) []model.PullRequest {
	lead, ok := leadIndex(prs)
	if !ok || lead == 0 {
		return prs
	}
	out := make([]model.PullRequest, 0, len(prs))
	out = append(out, prs[lead])
	out = append(out, prs[:lead]...)
	return append(out, prs[lead+1:]...)
}

// leadIndex returns the position of the merge request that best represents a
// Ticket's current state: a merged one if the work landed, otherwise the newest
// open or draft one, otherwise the newest of whatever is left. It is the rule
// that makes a Ticket with three merge requests still read as one situation.
//
// "Newest" is the highest merge request number. Numbers are unique per project,
// so a tie is only possible across projects; it is broken by keeping the earlier
// element in GitLab's order, so the answer never depends on iteration luck.
//
// The index rather than the value is what the callers need: two merge requests
// in different projects can share a number, so a value is not an identity here.
func leadIndex(prs []model.PullRequest) (int, bool) {
	for i, pr := range prs {
		if pr.State == model.PRMerged {
			return i, true
		}
	}

	if i, ok := newestWhere(prs, func(pr model.PullRequest) bool {
		return pr.State == model.PROpen || pr.State == model.PRDraft
	}); ok {
		return i, true
	}

	return newestWhere(prs, func(model.PullRequest) bool { return true })
}

// newestWhere returns the index of the highest-numbered merge request satisfying
// keep, or false when none does. A tie keeps the earlier element.
func newestWhere(prs []model.PullRequest, keep func(model.PullRequest) bool) (int, bool) {
	best, found := 0, false
	for i, pr := range prs {
		if !keep(pr) {
			continue
		}
		if !found || pr.Number > prs[best].Number {
			best, found = i, true
		}
	}
	return best, found
}

// statusWithMergeRequests refines a Ticket's Status Category using the merge
// requests moving it. GitLab issues are opened or closed in REST and nothing
// else, so sitrep infers the middle: an open Ticket with an open or draft merge
// request is being worked on right now. This is the only place the GitLab driver
// produces StatusInProgress.
//
// A finished Ticket is never reopened by a merge request — how a Ticket closed is
// authoritative, so a Ticket closed as won't-do stays Cancelled and stays out of
// the progress denominator. A merged merge request on a still-open Ticket does
// not promote it either: the code landed, so nobody is coding, and the Ticket is
// simply waiting to be closed.
//
// Native Status is deliberately untouched. It means GitLab's own word, and
// GitLab's word is "open"; the coding / waiting-on-review / pipeline-is-red
// distinction is carried by the merge request's State, Review and Checks.
func statusWithMergeRequests(base model.StatusCategory, prs []model.PullRequest) model.StatusCategory {
	if base != model.StatusTodo {
		return base
	}
	for _, pr := range prs {
		if pr.State == model.PROpen || pr.State == model.PRDraft {
			return model.StatusInProgress
		}
	}
	return base
}
