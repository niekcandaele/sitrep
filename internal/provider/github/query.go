package github

// epicQuery is the one GraphQL document the epic hot path sends. It is a
// `query` and always will be: sitrep is read-only by design (ADR-0002) and no
// mutation exists anywhere in this package.
//
// It lives alone in this file so the tickets that widen it — pull request
// correlation, ticket detail — extend one visible place. Per ADR-0003 it must
// never grow description, comments or links: those belong to FetchDetail.
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
        }
      }
    }
  }
}`
