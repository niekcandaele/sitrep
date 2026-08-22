package github

import (
	"strconv"
	"strings"
)

const queryMembershipDocument = `query($query:String!) {
  search(query:$query, type:ISSUE, first:100) {
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
// Pull request correlation rides on this same document as a nested selection,
// not a second request per Ticket: the epic query is polled, and turning one
// request into N is exactly what ADR-0003's split exists to prevent.
// closedByPullRequestsReferences is GitHub's own "this pull request will close
// this issue" linkage, which is what a `Closes #N` body produces;
// includeClosedPrs keeps a rejected pull request visible, because "the agent's
// work was turned down" must not look like "no work started". The head-commit
// statusCheckRollup — rather than per-check detail — keeps the polled path
// cheap.
//
// closedByPullRequestsReferences is capped at twenty per Ticket and does not
// paginate. It is a cap rather than a page because ADR-0003 makes the epic
// document one request for the whole polled path, and paginating a nested
// connection per Ticket is exactly the N-request fan-out that split exists to
// prevent. Twenty is far past what a readable Ticket has, but the truncation is
// silent: a Ticket with more than twenty closing pull requests shows twenty of
// them and says nothing about the rest. totalCount is selected so that a
// renderer could one day say "showing 20 of 34"; nothing renders it today.
//
// createdAt is selected because it is what orders the lead pull request. A
// pull request number is unique only within its repository, so a cross-repo
// pull request with a larger number can be the older one; see leadIndex.
//
// The root issue carries three selections its children already had — parent,
// assignees and closedByPullRequestsReferences — because a Ref may name a
// plain Ticket rather than a collection, and the answer to "which is it, and
// what does it hang off" has to come from this same batched call (ADR-0003, no
// third Provider method). They are O(1) per fetch, not per Ticket: one issue's
// parent, one issue's assignees, one issue's pull requests, whatever the epic's
// size. `parent` is the sub-issues feature's own field and rides on the
// GraphQL-Features header the driver already sends.
const epicQuery = `query($owner:String!, $repo:String!, $number:Int!, $cursor:String) {
  repository(owner:$owner, name:$repo) {
    kind: issueOrPullRequest(number:$number) { __typename }
    issue(number:$number) {
      id number title url state stateReason
      repository { nameWithOwner }
      parent { id number title url repository { nameWithOwner } }
      assignees(first:10) { nodes { login name avatarUrl } }
      closedByPullRequestsReferences(first:20, includeClosedPrs:true) {
        totalCount
        nodes {
          number title url state isDraft reviewDecision createdAt
          repository { nameWithOwner }
          commits(last:1) { nodes { commit { statusCheckRollup { state } } } }
        }
      }
      subIssues(first:100, after:$cursor) {
        totalCount
        pageInfo { hasNextPage endCursor }
        nodes {
          id number title url state stateReason
          repository { nameWithOwner }
          assignees(first:10) { nodes { login name avatarUrl } }
          closedByPullRequestsReferences(first:20, includeClosedPrs:true) {
            totalCount
            nodes {
              number title url state isDraft reviewDecision createdAt
              repository { nameWithOwner }
              commits(last:1) { nodes { commit { statusCheckRollup { state } } } }
            }
          }
        }
      }
    }
  }
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
	return document.String()
}

// refListTicketFragment is deliberately the same thin issue shape used for an
// Epic's child Tickets. It excludes hierarchy and Detail fields because an
// explicit Ref list names membership directly and remains on the polled path.
const refListTicketFragment = `fragment RefListTicketFields on Issue {
  id number title url state stateReason
  repository { nameWithOwner }
  assignees(first:10) { nodes { login name avatarUrl } }
  closedByPullRequestsReferences(first:20, includeClosedPrs:true) {
    totalCount
    nodes {
      number title url state isDraft reviewDecision createdAt
      repository { nameWithOwner }
      commits(last:1) { nodes { commit { statusCheckRollup { state } } } }
    }
  }
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
    ... on Issue {
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
    }
  }
}`
