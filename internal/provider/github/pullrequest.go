package github

import (
	"strconv"
	"strings"
	"time"

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
	// more than Nodes holds: the query caps the connection at twenty and does
	// not paginate it (see query.go), so a Ticket with more than twenty shows
	// twenty of them and loses the rest silently. TotalCount is decoded so that
	// a renderer could say "showing 20 of 34" — and **nothing renders it
	// today**. Reporting the truncation to the user is separate work.
	TotalCount int               `json:"totalCount"`
	Nodes      []pullRequestNode `json:"nodes"`
}

// crossReferenceConnection is the retained last-twenty window of GitHub
// CrossReferencedEvent timeline items. The bound counts every event, including
// Issue sources newPullRequests later ignores. PageInfo records that an older
// window exists; truncation is intentionally Provider-local and is not rendered.
type crossReferenceConnection struct {
	TotalCount int `json:"totalCount"`
	PageInfo   struct {
		HasPreviousPage bool   `json:"hasPreviousPage"`
		StartCursor     string `json:"startCursor"`
	} `json:"pageInfo"`
	Nodes []crossReferencedEventNode `json:"nodes"`
}

type crossReferencedEventNode struct {
	// Source is modeled as nullable even though GitHub's public schema declares
	// it non-null: deleted or inaccessible sources must contribute no invented PR.
	// A non-PR source decodes only TypeName because the query selects the remaining
	// fields inside a PullRequest fragment.
	Source *pullRequestNode `json:"source"`
}

type pullRequestNode struct {
	TypeName       string         `json:"__typename"`
	Number         int            `json:"number"`
	Title          string         `json:"title"`
	URL            string         `json:"url"`
	State          string         `json:"state"`
	IsDraft        bool           `json:"isDraft"`
	ReviewDecision string         `json:"reviewDecision"`
	Repository     *repositoryRef `json:"repository"`
	// CreatedAt orders the lead pull request. It is not mapped onto
	// model.PullRequest: nothing renders a timestamp, and it is only ever read
	// as "which of these is newer".
	CreatedAt time.Time `json:"createdAt"`
	Commits   struct {
		Nodes []struct {
			Commit *struct {
				StatusCheckRollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// newPullRequests unions GitHub's two native PR relationships before mapping
// them onto sitrep's model. Closing references retain their existing order and
// precedence; usable PR-sourced timeline mentions append in GitHub's retained
// timeline order. GitHub cannot distinguish an implementation reference from
// an incidental PR mention when willCloseTarget is false, so every PullRequest
// source with stable repository-plus-number identity is deliberately included.
// Issue, null, inaccessible, and identity-less timeline sources are ignored.
//
// Stable identities deduplicate case-insensitively across and within both paths,
// first occurrence wins. Legacy closing nodes without repository identity still
// map exactly as before, but cannot prove equivalence to another candidate.
// leadFirst runs once after the union. A Ticket with no candidates gets nil, the
// model's documented "none", rather than an empty slice.
func newPullRequests(
	closing *pullRequestConnection,
	crossReferences *crossReferenceConnection,
) []model.PullRequest {
	closingCount := 0
	if closing != nil {
		closingCount = len(closing.Nodes)
	}
	crossReferenceCount := 0
	if crossReferences != nil {
		crossReferenceCount = len(crossReferences.Nodes)
	}
	if closingCount+crossReferenceCount == 0 {
		return nil
	}

	candidates := make([]pullRequestNode, 0, closingCount+crossReferenceCount)
	seen := make(map[string]struct{}, closingCount+crossReferenceCount)
	if closing != nil {
		for _, candidate := range closing.Nodes {
			if identity, ok := stablePullRequestIdentity(candidate); ok {
				if _, duplicate := seen[identity]; duplicate {
					continue
				}
				seen[identity] = struct{}{}
			}
			candidates = append(candidates, candidate)
		}
	}
	if crossReferences != nil {
		for _, event := range crossReferences.Nodes {
			if event.Source == nil || event.Source.TypeName != "PullRequest" {
				continue
			}
			candidate := *event.Source
			identity, ok := stablePullRequestIdentity(candidate)
			if !ok {
				continue
			}
			if _, duplicate := seen[identity]; duplicate {
				continue
			}
			seen[identity] = struct{}{}
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	prs := make([]model.PullRequest, 0, len(candidates))
	created := make([]time.Time, 0, len(candidates))
	for _, candidate := range candidates {
		prs = append(prs, newPullRequest(candidate))
		created = append(created, candidate.CreatedAt)
	}
	return leadFirst(prs, created)
}

func stablePullRequestIdentity(n pullRequestNode) (string, bool) {
	if n.Number <= 0 || n.Repository == nil {
		return "", false
	}
	repository := strings.TrimSpace(n.Repository.NameWithOwner)
	if repository == "" {
		return "", false
	}
	return strings.ToLower(repository) + "#" + strconv.Itoa(n.Number), true
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
// every other element in GitHub's order. Nothing is dropped. created is each
// pull request's creation time, positionally aligned with prs; see leadIndex.
func leadFirst(prs []model.PullRequest, created []time.Time) []model.PullRequest {
	lead, ok := leadIndex(prs, created)
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
// "Newest" is the most recently created pull request. A pull request number is
// unique only within its repository, so ordering by number puts a cross-repo
// pull request with a larger number first even when it is the older one;
// created (positionally aligned with prs) is what settles it. Where a timestamp
// is missing — an older fixture, a payload that omitted it — the number is the
// fallback, and an exact tie keeps the earlier element in GitHub's order, so
// the answer is always deterministic.
func leadPullRequest(prs []model.PullRequest, created []time.Time) (model.PullRequest, bool) {
	i, ok := leadIndex(prs, created)
	if !ok {
		return model.PullRequest{}, false
	}
	return prs[i], true
}

// leadIndex is leadPullRequest by position, which is what moving the lead to
// the front needs: two pull requests in different repositories can share a
// number, so a value is not an identity here.
func leadIndex(prs []model.PullRequest, created []time.Time) (int, bool) {
	if i, ok := newestWhere(prs, created, func(pr model.PullRequest) bool {
		return pr.State == model.PROpen || pr.State == model.PRDraft
	}); ok {
		return i, true
	}

	for i, pr := range prs {
		if pr.State == model.PRMerged {
			return i, true
		}
	}

	return newestWhere(prs, created, func(model.PullRequest) bool { return true })
}

// newestWhere returns the index of the newest pull request satisfying keep, or
// false when none does. A tie keeps the earlier element, so the answer never
// depends on iteration luck.
func newestWhere(prs []model.PullRequest, created []time.Time, keep func(model.PullRequest) bool) (int, bool) {
	best, found := 0, false
	for i, pr := range prs {
		if !keep(pr) {
			continue
		}
		if !found || newerThan(i, best, prs, created) {
			best, found = i, true
		}
	}
	return best, found
}

// newerThan reports whether prs[i] is newer than prs[best], by creation time
// when both are known and by number otherwise.
func newerThan(i, best int, prs []model.PullRequest, created []time.Time) bool {
	if i < len(created) && best < len(created) && !created[i].IsZero() && !created[best].IsZero() {
		return created[i].After(created[best])
	}
	return prs[i].Number > prs[best].Number
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
