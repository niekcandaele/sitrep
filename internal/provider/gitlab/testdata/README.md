# GitLab driver fixtures

These payloads are replayed by the local test server in `gitlab_test.go`, routed by request
path. They are the GitLab driver's only test seam: assertions target the normalized
`model.WatchlistSnapshot` and `model.Detail`, and no test in this repository ever calls a live
GitLab instance, executes `glab` or `git`, reads an environment variable, or needs a
credential.

The instance these fixtures describe is `gitlab.com`, the group is `gitlab-org`, the epic is
`gitlab-org&23356`, the project is `gitlab-org/cli`, and the milestones are
`gitlab-org/cli%3` (project) and `groups/gitlab-org%3` (group).

The heterogeneous Ref-list replay uses the existing root payloads
`issue_with_epic.json`, `epic.json`, `milestone_project.json`, and
`milestone_group.json` in command-line order. There is deliberately no combined
or hand-shaped Ref-list payload: GitLab has no heterogeneous multi-get endpoint,
so the test replays the same direct resource responses described below and
asserts that none of their child endpoints is read.

## Provenance

Two files are **recorded**, live and unauthenticated, on **2026-08-21**:

```
curl -s "https://gitlab.com/api/v4/groups/gitlab-org/epics/23356"
curl -s "https://gitlab.com/api/v4/projects/gitlab-org%2Fcli/issues?per_page=1&state=all"
```

Both endpoints are public and need no token. The only redaction applied is `avatar_url`,
replaced with `https://avatar.example/redacted`; nothing else was altered, and both files
carry every field GitLab sent — including the many the driver never reads, precisely so the
tests prove it ignores them.

Everything else here is **hand-written**, to the response shapes GitLab's REST API
documentation specifies, grafted onto the recorded issue's field set so the shapes stay
real. That is not a stylistic choice: the authenticated endpoints — epic issues, notes,
links — answer 401 unauthenticated, so there was nothing to record. This note must never
claim a recording that did not happen.

If you have a token and want to replace a hand-written file with recorded bytes, record it
with

```
curl -s -H "Authorization: Bearer $GITLAB_TOKEN" -H 'Accept: application/json' \
  "https://gitlab.com/api/v4/groups/<group>/epics/<iid>/issues?per_page=100"
```

and **redact avatar URLs and any personal email before committing** — a fixture must
contain no real credential and no personal data. Then say so here, per file.

## Native queries

The Query fixtures were added on **2026-08-22** and are **hand-written** to GitLab's
issue-list and direct-issue response shapes. No native filter request was recorded. Their
project ids, issue ids, titles, and descriptions are synthetic. The author block in
`query_issue_cli_101.json` is copied from the existing public `issue.json` recording with
its avatar redaction retained; no credential or new personal data was added.

| File | What it proves |
|---|---|
| `query_membership.json` | two-project search order, a repeated `project_id`/IID identity, and stale list state/title that cannot reach output |
| `query_issue_cli_101.json` | the authoritative direct root for `gitlab-org/cli#101`, including current open state; its description is deliberately dropped from the thin Ticket |
| `query_issue_core_7.json` | the authoritative nested-project root for `gitlab-org/platform/core#7`, proving project-id-to-path routing and cross-project order |

The empty-Query response and malformed-filter error are emitted directly by the replay
server as compact `[]` and documented `message` shapes; they need no fixture files.

## The epic and its children

| File | Provenance | What it proves |
|---|---|---|
| `epic.json` | recorded | the epic `gitlab-org&23356`: `state: "opened"`, `references.full`, `parent_iid: null`, `web_url` under `/groups/…/-/epics/…`, and a description the polled path must not read |
| `epic_with_parent.json` | recorded + `parent_iid: 4200` grafted on | the breadcrumb an epic's own parent produces, with an **empty Title** — the payload does not carry one and `FetchEpic` is polled |
| `epic_children_page1.json` | hand-written | five children, served with `x-next-page: 2`; `opened` and `closed`; one unassigned; **two assignees** on `#102`, whose title carries an `&` and `« éclair »` verbatim; a child in **another project** (`gitlab-org/platform/core#7`), so `Repository` differs; every child carries a description, which must not reach a `model.Ticket` |
| `epic_children_page2.json` | hand-written | the last page: `#105` closed with a **scoped** `workflow::wontfix` label → Cancelled; `#106` closed with `_links.closed_as_duplicate_of` set → Cancelled, Native Status `duplicate`; `#107` closed plainly → Done; `#108` with an **unknown `state`** → Unknown, so a broken instance is visible; `#109` with **no `references` object**, proving the `#iid` fallback |
| `epic_children_empty.json` | hand-written | `[]` — an epic with no children is a Ticket someone pointed sitrep at, not an error |
| `epic_children_one.json` | hand-written | one child, `gitlab-org/cli#101` — so a test about merge request correlation talks about exactly one Ticket |

## Milestones

**None of these is recorded, and that is not laziness.** GitLab's milestone endpoints
require authentication even for public projects — verified on **2026-08-21**, where
`GET /projects/gitlab-org%2Fcli/milestones` and
`GET /groups/gitlab-org/milestones?iids[]=138` both answer `401` while the same projects'
issue endpoints answer `200`. Every file below is therefore **hand-written to the shape
GitLab's documentation specifies**, with the field set grafted from a real milestone object
embedded in a recorded `gitlab-org/gitlab` issue payload
(`{"id": 6239395, "iid": 138, "group_id": 9970, "title": "19.4", "state": "active",
"web_url": "https://gitlab.com/groups/gitlab-org/-/milestones/138"}`), so the shapes stay
honest.

| File | Provenance | What it proves |
|---|---|---|
| `milestone_project.json` | hand-written | the `iids[]` **array** response for a project milestone — the id/iid bridge (`id: 6239395`, `iid: 3`) that is the whole reason this driver never reads `/milestones/:milestone_id`; a `web_url` present; and a description with an `&` and non-ASCII that `FetchDetail` carries verbatim |
| `milestone_project_no_web_url.json` | hand-written | the same shape with **no `web_url`** — the URL sitrep builds instead, which the project-milestone example in GitLab's own docs shows is a real case; also `description: null` and `expired: true`, neither of which may become a Status |
| `milestone_group.json` | hand-written | a **group** milestone: `group_id` set, `project_id` absent, `state: "closed"` → Done, `web_url` under `/groups/…/-/milestones/…` |
| `milestone_empty.json` | hand-written | `[]` — an iid that resolves to nothing, so the not-found wording is proven |
| `milestone_duplicate.json` | hand-written | two objects — the impossible answer to a filter documented to be unique, which must error rather than first-wins |
| `milestone_issues_page1.json` | hand-written | five children with `x-next-page: 2`, **from two projects** (so a group milestone's cross-project `Repository` and project-qualified `Key` are proven), one unassigned, one with two assignees whose title carries an `&` and `« éclair »` verbatim |
| `milestone_issues_page2.json` | hand-written | the last page: `#205` closed plainly → Done; `#206` closed with a **scoped** `workflow::wontfix` label → Cancelled, out of the denominator |
| `milestone_issues_empty.json` | hand-written | `[]` — a milestone with no issues decodes to a Ticket, not an error |
| `issue_with_milestone.json` | recorded issue + a `milestone` object grafted on | the milestone breadcrumb, with `epic: null` — a complete Parent for no extra request |
| `issue_with_epic_and_milestone.json` | recorded issue + both objects grafted on | the win order: an epic beats a milestone |

## Merge requests

| File | Provenance | What it proves |
|---|---|---|
| `closed_by.json` | **recorded** — `curl -s "https://gitlab.com/api/v4/projects/gitlab-org%2Fcli/issues/8509/related_merge_requests"`, 2026-08-21, avatar URLs redacted. The two endpoints return the same merge-request shape and only the related one fills in `head_pipeline`, so this one payload stands in for both; the replay server serves it for either path | `state: "opened"`, `draft: false`, `detailed_merge_status: "not_approved"`, `references.full`, `head_pipeline.status: "success"`, one reviewer |
| `closed_by_states.json` | hand-written to the recorded file's shape, trimmed to the fields under test | one row per mapping rule, because one healthy issue cannot hold them at once: `merged`; `closed`; an open `draft`; a **`locked`** one; an **unknown state**; `head_pipeline: null`; `failed`; `canceled`; `skipped`; an **unknown pipeline status**; `requested_changes`; one in **another project** (`references.full` differs → `Repository` differs); and one with **no `references` object** (the `web_url` fallback) |
| `closed_by_lead.json` | hand-written to the recorded file's shape | three merge requests on one issue — a `closed`, a `merged`, then a newer `opened` — proving the merged one leads and that nothing is dropped |
| `closed_by_empty.json` | hand-written | `[]` — nil `PullRequests`, and the Ticket stays where it was |
| `approvals_pending.json` | **recorded** — `curl -s "https://gitlab.com/api/v4/projects/gitlab-org%2Fcli/merge_requests/3761/approvals"`, 2026-08-21, avatar URLs redacted, otherwise verbatim | `approved_by: []`, `approvals_left: 1`, `approved: false` — an approval is still owed |
| `approvals_approved.json` | the recorded file with `approved_by` grafted non-empty and `approvals_required: 0` | somebody clicked Approve **while the requirement is zero** — the `approved_by`-not-`approved` rule made visible |
| `approvals_partial.json` | the recorded file with one `approved_by` entry, `approvals_required: 2`, `approvals_left: 1` | one approval of two is **not** approval: the merge request is still waiting |
| `approvals_zero_required.json` | the recorded file with `approved: true`, `approvals_required: 0` | the trap: `approved` is true because nothing is required and nobody has approved anything, so it must **not** read as Approved |

## The issue

| File | Provenance | What it proves |
|---|---|---|
| `issue.json` | recorded | `web_url` under **`/-/work_items/`**, which is what GitLab serves today; `references.full`; `epic: null`; `_links.closed_as_duplicate_of: null` |
| `issue_with_epic.json` | recorded + an `epic` object grafted on | the decoder's breadcrumb and #11's walk-up: a complete Parent with Key, Title and URL, for no extra request |
| `issue_detail.json` | recorded + a written description | a description with an `&`, a fenced code block and non-ASCII, carried across verbatim |
| `issue_detail_bare.json` | recorded + `description: null` | the ordinary freshly-filed Ticket, which must not read as an error |

## Notes and links

| File | Provenance | What it proves |
|---|---|---|
| `notes_page.json` | hand-written | four notes newest-first, so the reversal into oldest-first is proven: two human comments, one **`system: true`** (activity, dropped), and one with `author: null` and `internal: true` (a real confidential comment, kept with a zero `model.User`) |
| `notes_empty.json` | hand-written | `[]` — no comments is not an error |
| `links.json` | hand-written | one `is_blocked_by`, one `blocks`, one `relates_to` in another project, one with an **invented `link_type`** (→ Relates carrying its own wording), and one target closed as a duplicate, proving the target's own Native Status |
| `links_empty.json` | hand-written | `[]` |

## Errors

| File | Provenance | What it proves |
|---|---|---|
| `error_message.json` | hand-written | `{"message": "…"}` in the **string** shape, which is what a status with no wording of its own falls back to |
| `error_forbidden.json` | hand-written | `{"message": {…}}` in the **object** shape, served with a 403 on an epic path so that sitrep's Premium wording wins over the API's |

The 401 / 404 / 429 / 500 / malformed-JSON paths need no file: the replay server produces
them directly.

## Extending

Extend **these** files rather than starting new ones, and keep this note honest about which
bytes are recorded and which are hand-written.
