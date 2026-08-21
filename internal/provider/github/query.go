package github

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
// work was turned down" must not look like "no work started". The cap of five
// per Ticket and the head-commit statusCheckRollup — rather than per-check
// detail — keep the polled path cheap.
const epicQuery = `query($owner:String!, $repo:String!, $number:Int!, $cursor:String) {
  repository(owner:$owner, name:$repo) {
    issue(number:$number) {
      id number title url state stateReason
      repository { nameWithOwner }
      subIssues(first:100, after:$cursor) {
        totalCount
        pageInfo { hasNextPage endCursor }
        nodes {
          id number title url state stateReason
          repository { nameWithOwner }
          assignees(first:10) { nodes { login name avatarUrl } }
          closedByPullRequestsReferences(first:5, includeClosedPrs:true) {
            nodes {
              number title url state isDraft reviewDecision
              repository { nameWithOwner }
              commits(last:1) { nodes { commit { statusCheckRollup { state } } } }
            }
          }
        }
      }
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
