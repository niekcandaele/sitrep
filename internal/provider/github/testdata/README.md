# GitHub driver fixtures

These payloads are replayed by the local test server in `github_test.go`. They are the
GitHub driver's only test seam: assertions target the normalized `model.WatchlistSnapshot`, and
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

Only #90 carries a `totalCount`. Every other block omits it deliberately, so the recorded
payload covers both paths: a truncated connection that knows its own size, and a Tracker
answer with no total at all, which falls back to counting what was fetched.

The shapes and what each one proves:

| Ticket | pull requests | proves |
|---|---|---|
| #3, #4 | one `MERGED`, `APPROVED`, `SUCCESS` | merged; a closed Ticket stays Done |
| #5 | one `OPEN`, `REVIEW_REQUIRED`, `SUCCESS` | waiting on review; Ticket becomes InProgress |
| #6 | one `OPEN` `isDraft: true`, `reviewDecision: null`, `PENDING` | the agent is coding |
| #7, #92, #94 | `nodes: []` | nil `PullRequests`; #7 stays Todo |
| #90 | `CLOSED`, then `MERGED`, then a newer `OPEN`, under `totalCount: 34` | lead selection prefers merged; a truncated connection reports the Tracker's own total, so the row counts thirty-three it does not show |
| #91 | one `CLOSED` on a `NOT_PLANNED` Ticket | stays Cancelled, stays out of the denominator |
| #93 | `closedByPullRequestsReferences: null` | the whole connection can be absent |
| #95 | one `OPEN`, `CHANGES_REQUESTED`, `FAILURE` | changes requested and red checks |
| #96 | one `OPEN`, `APPROVED`, `ERROR` | `ERROR` is a red pipeline too |
| #97 | one `CLOSED` `isDraft: true` | closed beats the draft flag; Ticket stays Todo |
| #98 | two `OPEN` with different numbers | the newest leads |
| #99 | one `OPEN` in `acme/widgets` | a cross-repo pull request keeps its own repository |
| #100 | one with `commits.nodes: []`, one with `statusCheckRollup: null` | no CI, no panic |
| #101 | an unknown `state`, `reviewDecision` and rollup, and a null `repository` | unknown never reads as green |

### Cross-referenced pull requests

`cross_reference_prs.json` was added on **2026-08-23** and is a small,
**hand-written** aliased exact-Ref response, not a live recording. Its recognizable
row reproduces the observed relationship between `niekcandaele/sitrep#44` and PR
`#50`: the closing connection was empty while a PR-sourced
`CrossReferencedEvent` linked the two. In the observed case, PR #50 targeted
`epic/sitrep-v0.2`, a non-default integration branch. The fixture deliberately
omits `baseRefName` and every other branch field because the native relationship,
not a branch-name heuristic, is the behavior under test.

The #44/#50 payload is deliberately edited to an open, review-required state so
list normalization can be asserted after the historical PR merged. The remaining
rows (#144/#150, #244/#250, and #344/#350) are clearly numbered synthetic copies:
they add a cross-repository draft PR, an explicitly closed Ticket with a merged
PR, and the same PR in both relationships with repository casing changed. An
Issue-sourced event beside #50 proves non-PR sources are ignored. Aggregate
checks, review values, timestamps, timeline `totalCount`, and retained-window
`pageInfo` are all deliberate test values; no credentials or personal payloads
were captured.

`epic_empty.json` is the same real epic node with an empty `subIssues` page: an issue with
no sub-issues is not an error, it is a Ticket someone pointed sitrep at.

## Native queries

The Query fixtures were added on **2026-08-22** and are **hand-written** to GitHub's
GraphQL search response and error shapes; no live native query was recorded. They contain
no credentials or personal data. The authoritative second stage deliberately reuses the
existing aliased exact-Ref fixtures described below rather than introducing another copy of
those issue payloads.

| Fixture | deliberate shape and purpose |
|---|---|
| `query_membership.json` | cross-repository Issue identities in search order, `issueCount` evidence, a PullRequest node to ignore, a case-varied duplicate identity, and stale title data that cannot reach output |
| `query_empty.json` | an empty `search.nodes` array, proving a zero-match Query succeeds without an exact-root request |
| `query_invalid.json` | a documented GraphQL error envelope carrying `SEARCH_QUERY_ERROR` and an unclosed-quotation explanation, proving malformed native Query classification and prose |

## Explicit Ref lists

The Ref-list fixtures were added on **2026-08-22** and replay the dynamic aliased GraphQL
response shape. They are deliberately hand-edited fixture payloads, not fresh live recordings:
the successful nodes and pull-request shapes are derived from `epic_page1.json` and
`epic_page2.json`, while the second repository and failure identities are fictional. The
payload contains no credentials; avatar URLs are the already-grafted examples from the Epic
fixtures, so no additional redaction was needed.

`ref_list.json` orders its JSON members as `ref2`, `ref0`, `ref3`, `ref1` even though the
Selector order is `ref0` through `ref3`. That edit proves response-object order cannot change
Watchlist order. It combines two repositories and one open pull request so the direct-read
path also exercises qualified keys and derived InProgress status.

The failure payloads each include valid data before the failing member so returning a partial
Watchlist would be visible:

| Fixture | deliberate edit and purpose |
|---|---|
| `ref_list_missing.json` | `ref1` is a null repository, representing a missing or inaccessible target |
| `ref_list_pull_request.json` | `ref1.issue` is null while `issueOrPullRequest.__typename` is `PullRequest` |
| `ref_list_errors.json` | a `NOT_FOUND` GraphQL error carries path `["ref1"]`, alongside valid `ref0` data |

## Refs that name a Ticket

`ticket_with_parent.json`, `ticket_no_parent.json` and `ticket_cross_repo_parent.json` are
**hand-written** to the same `epicQuery` response shape, for the case where the Epic Ref turns
out to name a plain Ticket: no sub-issues, and the fetched issue's own `parent`. The schema
probe behind them is worth recording — `Issue.parent` is a field on the public schema, and
like `blockedBy` / `blocking` it needs no extra `GraphQL-Features` header beyond the
`sub_issues` one the driver already sends:

```
gh api graphql -f query='{ __type(name:"Issue") { fields { name } } }' | grep -i -E 'parent|sub'
```

| Fixture | proves |
|---|---|
| `ticket_with_parent.json` | no Tickets, a same-repo parent keyed `#2`, and the root issue's own assignees and open pull request landing on the Epic, whose `totalCount: 12` proves the total rides onto the Epic too |
| `ticket_with_parent_ratelimit.json` | the same hand-written Ticket response with a valid zero GraphQL `rateLimit`, proving that a successful Resolve preserves zero remaining and its UTC reset |
| `ticket_cross_repo_parent.json` | a parent in another repository keyed `owner/repo#N`, with a null `closedByPullRequestsReferences` and no assignees |
| `ticket_no_parent.json` | `parent: null` — a Ticket that hangs off nothing, which is an ordinary state and not an error |

`issue_null.json`, `errors_not_found.json`, `errors_query.json` and
`errors_rate_limited.json` are hand-written to the shapes GitHub's GraphQL API documents —
none of them is a recorded capture:

| Fixture | proves |
|---|---|
| `issue_null.json` | a 200 with a null issue: the ref names nothing |
| `errors_not_found.json` | a 200 carrying a `NOT_FOUND` error entry |
| `errors_query.json` | a 200 carrying two ordinary `FORBIDDEN` entries, joined into one line |
| `errors_rate_limited.json` | a 200 carrying a `RATE_LIMITED` entry — how GitHub reports an exhausted GraphQL **point budget**, which never appears as an HTTP status; the reset time comes from the response's `x-ratelimit-reset` header, which the test supplies |

The 401, 403, HTTP rate-limit, secondary-rate-limit, 500 and malformed-JSON cases need no
file — the test server produces those statuses and headers directly.

## Ticket detail

The Detail fixtures answer `detailQuery`, which is a **second** GraphQL document rather than
a widening of the epic one (ADR-0003: the epic query is polled, the detail query is opened).
That is why they are separate files: they are a different response shape, not more of the
same one.

`detail_full.json` starts from a **real recording** of `niekcandaele/sitrep#9`, captured with:

```
gh api graphql -H 'GraphQL-Features: sub_issues' \
  -F id=I_kwDOT_WhQ88AAAABNqYEOA \
  -f query='<the detailQuery in query.go>'
```

The `id`, `number`, `url`, `repository`, the first comment and the real `blockedBy` /
`blocking` entries are that recording byte for byte. The dependency probe is worth recording
here: `Issue.blockedBy` and `Issue.blocking` are ordinary `IssueConnection` fields on the
public schema and need **no** extra `GraphQL-Features` header — the driver's existing
`sub_issues` header is enough.

Grafted onto it by hand, because one healthy issue does not contain everything the driver has
to survive:

| Graft | proves |
|---|---|
| an `&` and `« éclair »` in the body | the description survives verbatim, unrendered and unescaped |
| a second comment from `sitrep-bot` with no `name` | an Actor that is not a User has no display name, and that is not an error |
| a third comment with `author: null` | a deleted account maps to the zero `model.User`, never a panic and never a dropped comment |
| a cross-repo `blockedBy` target in `acme/widgets`, closed `NOT_PLANNED` | the target key is qualified `owner/repo#N`, and not-planned is Cancelled |

`detail_bare.json` is a hand-written empty issue — no body, `comments.nodes: []`, both
dependency connections empty — because a freshly filed Ticket is the ordinary case and must
not read as an error.

`detail_node_null.json` and `detail_not_an_issue.json` are hand-written to the two shapes a
`node(id:)` lookup answers with when nothing came back: a null node, and a node that matched
no inline fragment and so decodes with no fields at all. The NOT_FOUND, 401, rate-limit and
malformed-JSON paths reuse the epic query's files and the test server, because `FetchDetail`
goes through exactly the same handling.

### Batched Ticket detail

`detail_batch_partial.json` was added on **2026-08-31** and is a wholly **hand-written**
aliased `node(id:)` response. It was never captured from GitHub and contains no token or
personal data. Its object members are deliberately ordered `detail2`, `detail0`, `detail1`
so decoding cannot depend on JSON object order. Aliases 0 and 2 are complete successful
Issues. Alias 1 carries recognizable partial data plus two nested `FORBIDDEN` errors, proving
the whole failed Detail is discarded, both errors stay attributed to the requested ID, and
successful siblings survive. Alias 0 also contains a synthetic ANSI escape so the
`provider.Sanitized` integration test can prove sanitation remains outside the GitHub driver.

The 100-alias and chunk-boundary responses are generated only by `httptest` from synthetic
node IDs. They are not fixtures because their purpose is request cardinality and deterministic
alias/variable alignment, not payload provenance. The benchmark uses the same localhost-only
response generator; it never resolves a real token or contacts GitHub.

## Extending

Extend **these** files rather than starting new ones, and keep this note honest about which
bytes are recorded and which are grafted.
