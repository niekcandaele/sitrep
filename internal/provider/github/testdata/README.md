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
  of, and **#94** is closed with `stateReason: null`.

`epic_empty.json` is the same real epic node with an empty `subIssues` page: an issue with
no sub-issues is not an error, it is a Ticket someone pointed sitrep at.

`issue_null.json`, `errors_not_found.json` and `errors_query.json` are hand-written to the
shapes GitHub's GraphQL API documents: a 200 with a null issue, a 200 carrying a
`NOT_FOUND` error entry, and a 200 carrying ordinary errors. The 401, rate-limit, 500 and
malformed-JSON cases need no file — the test server produces them directly.

## Extending

Pull request correlation and ticket detail will widen the query. Extend **these** files
rather than starting new ones, and keep this note honest about which bytes are recorded and
which are grafted.
