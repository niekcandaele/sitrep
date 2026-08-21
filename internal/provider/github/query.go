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
