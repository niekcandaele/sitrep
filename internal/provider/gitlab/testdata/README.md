# GitLab driver fixtures

These payloads are replayed by the local test server in `gitlab_test.go`, routed by request
path. They are the GitLab driver's only test seam: assertions target the normalized
`model.EpicSnapshot` and `model.Detail`, and no test in this repository ever calls a live
GitLab instance, executes `glab` or `git`, reads an environment variable, or needs a
credential.

The instance these fixtures describe is `gitlab.com`, the group is `gitlab-org`, the epic is
`gitlab-org&23356` and the project is `gitlab-org/cli`.

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

## The epic and its children

| File | Provenance | What it proves |
|---|---|---|
| `epic.json` | recorded | the epic `gitlab-org&23356`: `state: "opened"`, `references.full`, `parent_iid: null`, `web_url` under `/groups/…/-/epics/…`, and a description the polled path must not read |
| `epic_with_parent.json` | recorded + `parent_iid: 4200` grafted on | the breadcrumb an epic's own parent produces, with an **empty Title** — the payload does not carry one and `FetchEpic` is polled |
| `epic_children_page1.json` | hand-written | five children, served with `x-next-page: 2`; `opened` and `closed`; one unassigned; **two assignees** on `#102`, whose title carries an `&` and `« éclair »` verbatim; a child in **another project** (`gitlab-org/platform/core#7`), so `Repository` differs; every child carries a description, which must not reach a `model.Ticket` |
| `epic_children_page2.json` | hand-written | the last page: `#105` closed with a **scoped** `workflow::wontfix` label → Cancelled; `#106` closed with `_links.closed_as_duplicate_of` set → Cancelled, Native Status `duplicate`; `#107` closed plainly → Done; `#108` with an **unknown `state`** → Unknown, so a broken instance is visible; `#109` with **no `references` object**, proving the `#iid` fallback |
| `epic_children_empty.json` | hand-written | `[]` — an epic with no children is a Ticket someone pointed sitrep at, not an error |

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
