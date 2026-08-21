# Jira driver fixtures

These payloads are replayed by the local test server in `jira_test.go`, routed by request
path. They are the Jira driver's only test seam: assertions target the normalized
`model.EpicSnapshot` and `model.Detail`, and no test in this repository ever calls a live
Jira site, reads an environment variable, or needs a credential.

## Provenance

**Every file here is hand-written**, to the response shapes Atlassian's REST API v2
documentation specifies. Nothing in this directory is a recording: the author had no Jira
Cloud site to record from. Where a real payload carries more fields than the driver reads —
`self` URLs, `statusCategory.colorName`, avatar sizes, `startAt`/`total` on the comment
response — a representative sample of them is included, precisely so that the tests prove
the driver ignores what it does not read.

If you do have a site and want to replace a file with recorded bytes, record it with

```
curl -s -u "$EMAIL:$JIRA_API_TOKEN" -H 'Accept: application/json' \
  "https://<site>.atlassian.net/rest/api/2/issue/<KEY>?fields=summary,status,resolution,assignee,parent,project"
```

and **redact the site host, account ids, emails and avatar URLs before committing** — a
fixture must contain no real credential and no personal data. Then say so here, per file.
This note must never claim a recording that did not happen.

The site these fixtures describe is `acme.atlassian.net`, the project is `ABC`, and the epic
is `ABC-1`.

## The epic and its children

| File | What it proves |
|---|---|
| `epic_issue.json` | the Epic `ABC-1`: `To Do` in category `new`, one assignee, `parent: null`, project `ABC`; the browse URL is built from the site and the key, never from `self` |
| `epic_children_page1.json` | five children plus a `nextPageToken` and `isLast: false`, which is what makes this two pages; categories `new`, `indeterminate` and `done`; one unassigned child; `ABC-6`'s title carries an `&` and `« éclair »` and must survive verbatim |
| `epic_children_page2.json` | `isLast: true`; `ABC-7` resolved **Won't Do** → Cancelled and out of the progress denominator; `ABC-8` resolved `Duplicate` → Cancelled; `ABC-9` in category `done` with `resolution: null` → Done; `ABC-10` with an unknown `statusCategory.key` → Unknown, so a broken instance is visible; `DEF-4` in another project, so a cross-project child identifies itself |
| `epic_children_empty.json` | `issues: []`, `isLast: true` — a Ref that named a plain Ticket, which is not an error |
| `ticket_with_parent.json` | an issue carrying `fields.parent`: the decoder's breadcrumb and the `u` walk-up |

## Link types

| File | What it proves |
|---|---|
| `issue_link_types.json` | the stock catalogue — `Blocks` (`is blocked by` / `blocks`), `Cloners`, `Duplicate`, `Relates` |
| `issue_link_types_renamed.json` | the acceptance criterion: the blocking type renamed to `Dependency` but still phrased `is blocked by` / `blocks` still maps to BlockedBy/Blocks, and an invented `Causes` type falls back to Relates **carrying its own label** |

## Ticket detail

The Detail fixtures answer a **different** request from the polled one — more fields, plus
the comment endpoint — because the epic query is polled and the detail query is opened
(ADR-0003). That is why they are separate files.

| File | What it proves |
|---|---|
| `detail_full.json` | a description in wiki markup with an `&`, a code block and non-ASCII, carried across verbatim; five `issuelinks` entries: an **inward** blocking one, an **outward** blocking one, a recognized `Relates` one, one whose type id is absent from the stock catalogue (so the inline `type` object is the fallback), and one with **neither** side present, which is skipped |
| `detail_bare.json` | `description: null` and `issuelinks: []` — the ordinary freshly-filed Ticket, which must not read as an error |
| `comments_page.json` | three comments newest-first, so the reversal into oldest-first is proven; one with `author: null`, which must not drop the comment; one whose `created` carries Jira's `+0100` offset, which is not RFC3339 and proves the explicit layout |
| `comments_empty.json` | `comments: []` — no comments is not an error |

## Errors

| File | What it proves |
|---|---|
| `errors_not_found.json` | Jira's `{"errorMessages":[…],"errors":{}}` shape. Served with a 404 it proves sitrep's own "not found (or you lack access)" wording wins over the API's; served with a 400 it proves the `errorMessages` join is what a status with no wording of its own falls back to |
| `errors_auth.json` | the same shape for an unauthenticated read, behind the 401 wording |

The 403, 429, 500 and malformed-JSON paths need no file: the replay server produces them
directly.

## Extending

Extend **these** files rather than starting new ones, and keep this note honest about which
bytes are hand-written and which — if that ever changes — are recorded.
