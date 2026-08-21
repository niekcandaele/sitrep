# GitHub driver fixtures

These payloads are replayed by the local test server in `github_test.go`. They are the
GitHub driver's only test seam: assertions target the normalized `model.EpicSnapshot`, and
no test in this repository ever calls the live API.

## Provenance

`epic_page1.json` and `epic_page2.json` start from a **real recording**, captured with:

```
gh api graphql -H 'GraphQL-Features: sub_issues' \
  -F owner=niekcandaele -F repo=sitrep -F number=2 \
  -f query='<the query in query.go>'
```

That recording is the epic node itself (`niekcandaele/sitrep#2`) and the first five
sub-issues on page 1, byte for byte as GitHub answered.

Everything else is **grafted by hand**, because a healthy epic does not contain the cases
the driver has to survive:

- Page 1 has been truncated to five real children plus grafted issue **#90**: an `OPEN`
  issue with `stateReason: REOPENED`, three assignees (one with a null `name`), and a title
  carrying an ampersand and non-ASCII characters. Its `pageInfo` was rewritten to
  `hasNextPage: true` with cursor `Y3Vyc29yOjEwMA==`, which is what makes this two pages.
- Page 2 repeats the same epic node — GitHub sends it on every page — and carries only
  grafted children: **#91** is a cross-repo child in `acme/widgets` closed as `NOT_PLANNED`,
  **#92** is closed as `DUPLICATE`, **#93** is closed with a reason sitrep has never heard
  of, and **#94** is closed with `stateReason: null`. **#95** through **#101** are grafted
  for pull request correlation and are described below.

## Pull requests

Every `closedByPullRequestsReferences` block is **grafted by hand** to the shape GitHub's
GraphQL API documents — correct nesting, SCREAMING_CASE enums, `nodes` arrays. The pull
request numbers and titles on the real children (#3 to #6) are the epic's real ones; the
rest are invented, because one healthy epic cannot contain every situation the driver has
to survive at once.

The shapes and what each one proves:

| Ticket | pull requests | proves |
|---|---|---|
| #3, #4 | one `MERGED`, `APPROVED`, `SUCCESS` | merged; a closed Ticket stays Done |
| #5 | one `OPEN`, `REVIEW_REQUIRED`, `SUCCESS` | waiting on review; Ticket becomes InProgress |
| #6 | one `OPEN` `isDraft: true`, `reviewDecision: null`, `PENDING` | the agent is coding |
| #7, #92, #94 | `nodes: []` | nil `PullRequests`; #7 stays Todo |
| #90 | `CLOSED`, then `MERGED`, then a newer `OPEN` | lead selection prefers merged |
| #91 | one `CLOSED` on a `NOT_PLANNED` Ticket | stays Cancelled, stays out of the denominator |
| #93 | `closedByPullRequestsReferences: null` | the whole connection can be absent |
| #95 | one `OPEN`, `CHANGES_REQUESTED`, `FAILURE` | changes requested and red checks |
| #96 | one `OPEN`, `APPROVED`, `ERROR` | `ERROR` is a red pipeline too |
| #97 | one `CLOSED` `isDraft: true` | closed beats the draft flag; Ticket stays Todo |
| #98 | two `OPEN` with different numbers | the newest leads |
| #99 | one `OPEN` in `acme/widgets` | a cross-repo pull request keeps its own repository |
| #100 | one with `commits.nodes: []`, one with `statusCheckRollup: null` | no CI, no panic |
| #101 | an unknown `state`, `reviewDecision` and rollup, and a null `repository` | unknown never reads as green |

`epic_empty.json` is the same real epic node with an empty `subIssues` page: an issue with
no sub-issues is not an error, it is a Ticket someone pointed sitrep at.

`issue_null.json`, `errors_not_found.json` and `errors_query.json` are hand-written to the
shapes GitHub's GraphQL API documents: a 200 with a null issue, a 200 carrying a
`NOT_FOUND` error entry, and a 200 carrying ordinary errors. The 401, rate-limit, 500 and
malformed-JSON cases need no file — the test server produces them directly.

## Extending

Ticket detail will widen the query. Extend **these** files rather than starting new ones,
and keep this note honest about which bytes are recorded and which are grafted.
