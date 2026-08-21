package github

import (
	"strings"

	"github.com/niekcandaele/sitrep/internal/model"
)

// The wire structs mirror GitHub's vocabulary, as the rest of this package
// does: GraphQL says closedByPullRequestsReferences, so this says so too.
// Everything downstream of newPullRequests says PullRequest.
//
// Every nested object is a pointer because every one of them can come back
// null: a pull request in a repository the token cannot see, a head commit that
// did not resolve, a repository with no CI configured.

type pullRequestConnection struct {
	// TotalCount is how many closing pull requests the Ticket has, which can be
	// more than Nodes holds: the query caps the connection rather than
	// paginating it (see query.go). Nothing renders it yet; it is selected so
	// the driver knows when it truncated instead of quietly reporting a subset.
	TotalCount int               `json:"totalCount"`
	Nodes      []pullRequestNode `json:"nodes"`
}

type pullRequestNode struct {
	Number         int            `json:"number"`
	Title          string         `json:"title"`
	URL            string         `json:"url"`
	State          string         `json:"state"`
	IsDraft        bool           `json:"isDraft"`
	ReviewDecision string         `json:"reviewDecision"`
	Repository     *repositoryRef `json:"repository"`
	Commits        struct {
		Nodes []struct {
			Commit *struct {
				StatusCheckRollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// newPullRequests maps a Ticket's linked pull requests onto sitrep's model, in
// GitHub's own order but with the lead pull request moved to the front. Index 0
// is the Provider's contract with renderers that show one pull request per row;
// see the doc comment on model.Ticket.PullRequests.
//
// A Ticket with no linked pull requests gets nil, the model's documented
// "none", rather than an empty slice.
func newPullRequests(conn *pullRequestConnection) []model.PullRequest {
	if conn == nil || len(conn.Nodes) == 0 {
		return nil
	}

	prs := make([]model.PullRequest, 0, len(conn.Nodes))
	for _, n := range conn.Nodes {
		prs = append(prs, newPullRequest(n))
	}
	return leadFirst(prs)
}

// newPullRequest maps one pull request node. Every pull request is reported as
// GitHub describes it, whatever its state: suppressing the review posture of a
// merged pull request is a rendering decision, and renderers can make it from
// this data.
func newPullRequest(n pullRequestNode) model.PullRequest {
	pr := model.PullRequest{
		Number: n.Number,
		Title:  n.Title,
		URL:    n.URL,
		State:  normalizePRState(n.State, n.IsDraft),
		Review: normalizeReview(n.ReviewDecision),
		Checks: normalizeChecks(rollupState(n)),
	}
	// A pull request can live in a different repository than the Ticket it
	// closes, so Repository always comes from the pull request's own node.
	if n.Repository != nil {
		pr.Repository = n.Repository.NameWithOwner
	}
	return pr
}

// rollupState digs the head commit's aggregate check state out of the node, or
// returns the empty string when any link in the chain is absent. commits.nodes
// is empty on a pull request whose head commit did not resolve, and
// statusCheckRollup is null in a repository with no CI at all.
func rollupState(n pullRequestNode) string {
	if len(n.Commits.Nodes) == 0 {
		return ""
	}
	commit := n.Commits.Nodes[0].Commit
	if commit == nil || commit.StatusCheckRollup == nil {
		return ""
	}
	return commit.StatusCheckRollup.State
}

// normalizePRState maps a GitHub pull request state and its draft flag onto
// sitrep's PRState. Draft is a flag on GitHub and a state in sitrep, because a
// draft pull request means "the agent is still coding" and that is a different
// situation report from "waiting on review".
//
// Merged and closed are checked before the draft flag: a draft pull request can
// be closed, and how it ended is the more important half of the answer.
func normalizePRState(state string, isDraft bool) model.PRState {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "MERGED":
		return model.PRMerged
	case "CLOSED":
		return model.PRClosed
	case "OPEN":
		if isDraft {
			return model.PRDraft
		}
		return model.PROpen
	default:
		return model.PRUnknown
	}
}

// normalizeReview maps GitHub's reviewDecision onto sitrep's ReviewState.
//
// reviewDecision is null on a repository with no review requirements and no
// submitted review. That is ReviewNone — the documented "nobody has been asked"
// answer — not a mapping failure, which is why ReviewState has no unknown.
func normalizeReview(decision string) model.ReviewState {
	switch strings.ToUpper(strings.TrimSpace(decision)) {
	case "APPROVED":
		return model.ReviewApproved
	case "CHANGES_REQUESTED":
		return model.ReviewChangesRequested
	case "REVIEW_REQUIRED":
		return model.ReviewPending
	default:
		return model.ReviewNone
	}
}

// normalizeChecks maps the head commit's statusCheckRollup state onto sitrep's
// CheckState.
//
// An unrecognized rollup state is ChecksPending, never ChecksPassing and never
// ChecksNone: sitrep does not report green it did not see, and "no CI
// configured" is a different, meaningful answer from "a state we do not know
// yet". An absent rollup is ChecksNone.
func normalizeChecks(rollup string) model.CheckState {
	switch strings.ToUpper(strings.TrimSpace(rollup)) {
	case "":
		return model.ChecksNone
	case "SUCCESS":
		return model.ChecksPassing
	case "FAILURE", "ERROR":
		return model.ChecksFailing
	default:
		return model.ChecksPending
	}
}

// leadFirst returns prs with the lead pull request moved to the front, leaving
// every other element in GitHub's order. Nothing is dropped.
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

// leadPullRequest returns the pull request that best represents a Ticket's
// current state: the newest open or draft one if anything is still in flight,
// otherwise a merged one, otherwise the newest of whatever is left. It is the
// rule that makes a Ticket with three pull requests still read as one
// situation.
//
// Work in flight outranks work that landed because that is the same evidence
// statusWithPullRequests reads: a Ticket with one merged and one open pull
// request is grouped as In Progress, so the pull request its row shows has to
// be the open one. Two answers about one Ticket is worse than either answer.
//
// "Newest" is the highest pull request number. Numbers are unique per
// repository, so a tie is only possible across repositories; it is broken by
// keeping the earlier element in GitHub's order, so the answer is
// deterministic.
func leadPullRequest(prs []model.PullRequest) (model.PullRequest, bool) {
	i, ok := leadIndex(prs)
	if !ok {
		return model.PullRequest{}, false
	}
	return prs[i], true
}

// leadIndex is leadPullRequest by position, which is what moving the lead to
// the front needs: two pull requests in different repositories can share a
// number, so a value is not an identity here.
func leadIndex(prs []model.PullRequest) (int, bool) {
	if i, ok := newestWhere(prs, func(pr model.PullRequest) bool {
		return pr.State == model.PROpen || pr.State == model.PRDraft
	}); ok {
		return i, true
	}

	for i, pr := range prs {
		if pr.State == model.PRMerged {
			return i, true
		}
	}

	return newestWhere(prs, func(model.PullRequest) bool { return true })
}

// newestWhere returns the index of the highest-numbered pull request satisfying
// keep, or false when none does. A tie keeps the earlier element, so the answer
// never depends on iteration luck.
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

// statusWithPullRequests refines a Ticket's Status Category using the pull
// requests moving it. GitHub has no in-progress state, so sitrep infers one: an
// open Ticket with an open or draft pull request is being worked on right now.
// It is the only place in this driver that produces StatusInProgress.
//
// A finished Ticket is never reopened by a pull request — how a Ticket closed
// is authoritative, so a Ticket closed as not planned stays Cancelled and stays
// out of the progress denominator. A merged pull request on a still-open Ticket
// does not promote it either: the pull request landed, so nobody is coding, and
// the Ticket is simply waiting to be closed.
//
// Native Status is deliberately untouched. It means the Tracker's own label,
// and GitHub's label is "open"; the coding / waiting-on-review / checks-are-red
// distinction is carried by the pull request's State, Review and Checks.
func statusWithPullRequests(base model.StatusCategory, prs []model.PullRequest) model.StatusCategory {
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
