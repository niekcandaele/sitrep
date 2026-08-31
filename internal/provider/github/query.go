package github

import (
	"strconv"
	"strings"
)

const queryPageSize = 100

// querySearchResultLimit is GitHub Search's documented 1,000-result ceiling.
const querySearchResultLimit = 1000

const queryMembershipDocument = `query($query:String!, $first:Int!, $after:String) {
  search(query:$query, type:ISSUE, first:$first, after:$after) {
    issueCount
    pageInfo { hasNextPage endCursor }
    nodes {
      __typename
      ... on Issue {
        number
        repository { nameWithOwner }
      }
    }
  }
}`

// epicQuery is the one GraphQL document the epic hot path sends. It is a
// `query` and always will be: sitrep is read-only by design (ADR-0002) and no
// mutation exists anywhere in this package.
//
// It lives alone in this file so the tickets that widen it — ticket detail —
// extend one visible place. Per ADR-0003 it must never grow description,
// comments or links: those belong to FetchDetail.
//
// Pull request correlation rides on this same document as two bounded nested
// relationships, not a second request per Ticket: the epic query is polled,
// and turning one request into N is exactly what ADR-0003's split exists to
// prevent. closedByPullRequestsReferences is GitHub's own "this pull request
// will close this issue" linkage, which is what a `Closes #N` body produces;
// includeClosedPrs keeps rejected work visible. The newest twenty
// CrossReferencedEvent timeline items add PR-sourced mentions that GitHub does
// not classify as closing — notably references from non-default integration
// branches. GitHub exposes no reliable native discriminator between an
// implementation reference and an incidental PR mention when willCloseTarget
// is false, so every usable PullRequest source is deliberately included. Issue
// sources are ignored, and branch-name, title, body, and willCloseTarget
// heuristics are deliberately rejected.
//
// Each relationship is capped at twenty per Ticket and neither paginates. The
// closing connection keeps GitHub's first-twenty order; timelineItems(last:20)
// favors current or replacement work and counts all cross-reference events,
// including Issue sources the mapper ignores. Closing candidates are considered
// first, then timeline candidates in GitHub's retained order, and stable
// repository-plus-number identity is deduplicated first-occurrence-wins. The
// union therefore contains at most forty pull requests. Older events and nodes
// past either bound are silently absent: this Provider cap is not a Query
// membership LimitReached condition. The closing connection's totalCount is
// decoded and reported: it becomes model.Ticket's PullRequestTotal, so a
// truncated row counts all of a Ticket's pull requests rather than the twenty
// that were fetched. The timeline totalCount and pageInfo are decoded only so
// the bounds stay explicit in this package; no renderer reports those.
//
// The shared PullRequest fragment contains only thin list fields. Its aggregate
// head-commit statusCheckRollup — rather than per-check detail — keeps the
// polled path cheap. createdAt orders the lead pull request: a number is unique
// only within its repository, so a cross-repo pull request with a larger number
// can be the older one; see leadIndex.
//
// The root issue carries the same assignees and correlation relationships as
// its children because a Ref may name a plain Ticket rather than a collection,
// and the answer to "which is it, and what does it hang off" has to come from
// this same batched call (ADR-0003, no third Provider method). They are O(1) per
// fetch, not per Ticket: one issue's parent, one issue's assignees, and two
// bounded relationship windows, whatever the epic's size. `parent` is the
// sub-issues feature's own field and rides on the GraphQL-Features header the
// driver already sends.
const epicQuery = `query($owner:String!, $repo:String!, $number:Int!, $cursor:String) {
  repository(owner:$owner, name:$repo) {
    kind: issueOrPullRequest(number:$number) { __typename }
    issue(number:$number) {
      id number title url state stateReason
      repository { nameWithOwner }
      parent { id number title url repository { nameWithOwner } }
      assignees(first:10) { nodes { login name avatarUrl } }
      ...IssuePullRequestRelationships
      subIssues(first:100, after:$cursor) {
        totalCount
        pageInfo { hasNextPage endCursor }
        nodes {
          id number title url state stateReason
          repository { nameWithOwner }
          assignees(first:10) { nodes { login name avatarUrl } }
          ...IssuePullRequestRelationships
        }
      }
    }
  }
}
` + issuePullRequestRelationshipsFragment + "\n" + pullRequestListFragment

const issuePullRequestRelationshipsFragment = `fragment IssuePullRequestRelationships on Issue {
  closedByPullRequestsReferences(first:20, includeClosedPrs:true) {
    totalCount
    nodes { ...PullRequestListFields }
  }
  crossReferences: timelineItems(last:20, itemTypes:[CROSS_REFERENCED_EVENT]) {
    totalCount
    pageInfo { hasPreviousPage startCursor }
    nodes {
      ... on CrossReferencedEvent {
        source {
          __typename
          ... on PullRequest { ...PullRequestListFields }
        }
      }
    }
  }
}`

const pullRequestListFragment = `fragment PullRequestListFields on PullRequest {
  number title url state isDraft reviewDecision createdAt
  repository { nameWithOwner }
  commits(last:1) { nodes { commit { statusCheckRollup { state } } } }
}`

// buildRefListQuery constructs one direct issue lookup per Ref. The only
// generated text is the numeric suffix on aliases and variable names; Tracker
// values remain in the variables map sent beside the document.
func buildRefListQuery(count int) string {
	var document strings.Builder
	document.WriteString("query(")
	for i := range count {
		if i > 0 {
			document.WriteString(", ")
		}
		suffix := strconv.Itoa(i)
		document.WriteString("$owner" + suffix + ":String!, $repo" + suffix + ":String!, $number" + suffix + ":Int!")
	}
	document.WriteString(") {\n")
	for i := range count {
		suffix := strconv.Itoa(i)
		document.WriteString("  ref" + suffix + ": repository(owner:$owner" + suffix + ", name:$repo" + suffix + ") {\n")
		document.WriteString("    kind: issueOrPullRequest(number:$number" + suffix + ") { __typename }\n")
		document.WriteString("    issue(number:$number" + suffix + ") { ...RefListTicketFields }\n")
		document.WriteString("  }\n")
	}
	document.WriteString("}\n")
	document.WriteString(refListTicketFragment)
	document.WriteString("\n")
	document.WriteString(issuePullRequestRelationshipsFragment)
	document.WriteString("\n")
	document.WriteString(pullRequestListFragment)
	return document.String()
}

// refListTicketFragment is deliberately the same thin issue shape used for an
// Epic's child Tickets. It excludes hierarchy and Detail fields because an
// explicit Ref list names membership directly and remains on the polled path.
const refListTicketFragment = `fragment RefListTicketFields on Issue {
  id number title url state stateReason
  repository { nameWithOwner }
  assignees(first:10) { nodes { login name avatarUrl } }
  ...IssuePullRequestRelationships
}`

// detailQuery is the second GraphQL document this driver sends, and it is
// deliberately separate from epicQuery rather than an addition to it: the epic
// document is polled every interval, this one is sent once, when a human opens a
// Ticket (ADR-0003). Merging them would put a body, a hundred comments and two
// dependency connections per Ticket on the hot path.
//
// It is a node(id:) lookup because model.TicketID already *is* GitHub's GraphQL
// node ID for this driver (see newTicket), so no owner, repo or number has to be
// carried around to reach one Ticket again.
//
// The caps are one request's worth of data and nothing paginates:
//
//   - comments(last:100) rather than first:100 — GraphQL returns a `last` page
//     in chronological order, so model.Detail.Comments stays oldest-first while
//     a thousand-comment Ticket shows the recent hundred rather than the ancient
//     hundred.
//   - blockedBy(first:50) / blocking(first:50) are GitHub's issue-dependency
//     connections. Fifty of either is already more than a screen.
//
// author is an Actor and is nullable: a deleted account arrives as null and a
// bot as an Actor with no name or avatarUrl, which is why name and avatarUrl sit
// behind the `... on User` fragment.
const detailQuery = `query($id:ID!) {
  node(id:$id) {
    ...DetailFields
  }
}
` + detailFieldsFragment

// buildDetailBatchQuery constructs one node lookup per requested Ticket. Only
// numeric suffixes are generated; Ticket IDs remain in the variables map.
func buildDetailBatchQuery(count int) string {
	var document strings.Builder
	document.WriteString("query(")
	for i := range count {
		if i > 0 {
			document.WriteString(", ")
		}
		suffix := strconv.Itoa(i)
		document.WriteString("$id" + suffix + ":ID!")
	}
	document.WriteString(") {\n")
	for i := range count {
		suffix := strconv.Itoa(i)
		document.WriteString("  detail" + suffix + ": node(id:$id" + suffix + ") { ...DetailFields }\n")
	}
	document.WriteString("}\n")
	document.WriteString(detailFieldsFragment)
	return document.String()
}

// detailFieldsFragment is shared verbatim by singular and plural Detail reads,
// keeping their normalized wire fields identical.
const detailFieldsFragment = `fragment DetailFields on Issue {
  id number url body
  repository { nameWithOwner }
  comments(last:100) {
    totalCount
    nodes {
      id url body createdAt
      author { login ... on User { name avatarUrl } }
    }
  }
  blockedBy(first:50) {
    nodes {
      id number title url state stateReason
      repository { nameWithOwner }
    }
  }
  blocking(first:50) {
    nodes {
      id number title url state stateReason
      repository { nameWithOwner }
    }
  }
}`
