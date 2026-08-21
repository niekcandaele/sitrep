# sitrep

A read-only terminal situation report on the work you delegated: one screen showing an
Epic's Tickets, their status, and the code moving them — across GitHub, Jira, and GitLab.
Agents write; sitrep watches. It never writes to a Tracker ([ADR-0002](docs/adr/0002-read-only-by-design.md)).

sitrep is early: today it reads an Epic from GitHub, Jira or GitLab, either as a live
monitor you leave open or as a one-shot report.

## Usage

```sh
sitrep <ref>           # the live monitor: a full-screen, auto-refreshing epic view
sitrep <ref> --plain   # a one-shot text report, for humans, dumb terminals and pipes
sitrep <ref> --json    # the same epic as a JSON document, for scripts and agents
```

`<ref>` is the Epic Ref: a bare number (`111`, resolved through the current clone's
`origin` remote), `owner/repo#111`, a Jira-style key (`ABC-123`, matched to a Profile by
its key prefix — see [Configuration](#configuration)), a GitLab epic or milestone reference
(`acme&12`, `acme/widgets%3`), or the issue's full URL.

With no mode flag, sitrep opens the **monitor**: the Epic's Tickets grouped by status with
a progress bar, assignees and the pull request moving each Ticket, refreshed every 60
seconds with an indicator saying how old the reading is. Move with `↑`/`↓` (or `j`/`k`),
refresh now with `r`, expand the key list with `?`, quit with `q`. `--interval` changes the
cadence — `sitrep 111 --interval 15s` — down to a floor of 5 seconds, because a Tracker's
API is rate-limited. A refresh that fails leaves the last good reading on screen and says
so in the footer rather than blanking or exiting.

`enter` opens the selected Ticket's Detail — its description, its comments and its links, each
with the tracker's own label — fetched at that moment and never during a list refresh. `esc`
returns to the list with the selection, the scroll position and any filters exactly as they
were.

A Ref with sub-tickets is an Epic; one without is a Ticket. Point sitrep at a Ticket —
`sitrep 112` — and it lands straight in that Ticket's Detail, which is how you decode a
number an agent just handed you. A breadcrumb above it names the Epic the Ticket belongs
to, and `u` opens that Epic in the full monitor, from where `enter` on the same Ticket is
instant. A Ticket with no parent opens the same way, without the breadcrumb and without
`u`. `--plain` and `--json` decode the same Ref the same way, printing that one Ticket
rather than an epic report.

Two keys narrow what the list shows. `d` hides Done and Cancelled Tickets, and the progress
bar keeps counting the whole Epic while they are hidden, so the header still says how far
along the work really is. `/` opens a fuzzy find over Ticket titles and keys — type to
narrow the list as you go, `enter` to keep the query and close the box. `esc` clears the
filter, and clears the find box first if it is open.

`--plain` prints the same information once. It emits no ANSI escape sequences and never
takes over the screen, so it is safe over SSH, in a log file, or piped into something else.
`--json` prints the same snapshot as a stable, documented wire format.

## Keybindings

The monitor's footer always shows the keys that apply right now, and `?` expands it to the
full list. This table is the lookup.

**The list**

| Key | Does |
|---|---|
| `↑` / `k`, `↓` / `j` | move the selection |
| `pgup`, `pgdn` | move one page |
| `g`, `G` | first Ticket, last Ticket |
| `enter` | open the selected Ticket's Detail |
| `d` | hide Done and Cancelled Tickets |
| `/` | open the fuzzy find |
| `esc` | clear the filters — and quit when none are active |
| `r` | refresh now |
| `?` | expand the help |
| `q`, `ctrl+c` | quit |

**The find box** — everything not listed here is text, so searching for `queue` types a `q`
rather than quitting.

| Key | Does |
|---|---|
| `enter` | keep the query and close the box |
| `esc` | abandon the query and close the box |
| `↑`, `↓`, `pgup`, `pgdn` | move the selection without leaving the box |
| `ctrl+c` | quit |

**A Ticket's Detail**

| Key | Does |
|---|---|
| `↑` / `k`, `↓` / `j` | scroll one line |
| `pgup`, `pgdn` | scroll one page |
| `g`, `G` | top, bottom |
| `esc` | back to the list — or quit, when the Ticket was opened directly |
| `u` | open the Epic this Ticket belongs to, when it has one |
| `r` | re-read this Ticket's Detail |
| `?` | expand the help |
| `q`, `ctrl+c` | quit |

## Configuration

**GitHub with `gh auth login` done needs no config file at all.** Everything below is for
connecting to something else, or for changing a default.

sitrep reads one file, `~/.config/sitrep/config.yml` (`$XDG_CONFIG_HOME/sitrep/config.yml`
when that is set, or the path in `$SITREP_CONFIG`). There is no per-repository config and
no search: one file, one place. sitrep never writes it — a missing file is silence, and a
malformed one is an error naming the file, the Profile and the problem.

The file holds named **Profiles**. A Profile selects a Provider and supplies what it needs
to connect to one Tracker:

```yaml
profiles:
  acme-jira:
    provider: jira              # github, gitlab or jira
    host: acme.atlassian.net    # the Tracker host; optional for github
    project: ABC                # the Jira project key — also the Epic Ref key prefix
    auth:
      user: me@acme.test        # the Atlassian account email
      token_env: JIRA_API_TOKEN # the NAME of the variable holding the token
    refresh_interval: 30s

  work-github:
    provider: github
    host: ghe.acme.test
    auth:
      token_env: GHE_TOKEN
```

**A Profile names a token; it never holds one.** `auth.token_env` is the name of an
environment variable, and sitrep reads the value from the environment when it connects. A
config file lives in dotfiles, in git, in backups, and in whatever an agent reads next, so
a literal token in it is rejected rather than used.

`ABC-123` on the command line is matched to the Profile whose `project` is `ABC`, which is
what turns a Jira-style key into something sitrep can resolve. Each Jira Profile serves one
project key; a site with several projects gets a Profile each. When two Profiles could
serve one ref, sitrep says so instead of guessing — pass `--profile <name>` to choose.

`refresh_interval` is that Profile's monitor cadence, subject to the same 5-second floor as
`--interval`, which overrides it when you actually type it.

### Jira

A Jira Profile is what turns `sitrep PROJ-123` into a situation report. It needs the site,
the project key, your Atlassian account email, and the name of the variable holding an API
token:

```yaml
profiles:
  acme-jira:
    provider: jira
    host: acme.atlassian.net    # the site, not a URL
    project: PROJ               # the project key — also the Epic Ref key prefix
    auth:
      user: me@acme.test        # your Atlassian account email
      token_env: JIRA_API_TOKEN # the NAME of the variable holding the token
```

Create the token at [id.atlassian.com](https://id.atlassian.com/manage-profile/security/api-tokens),
put it in `$JIRA_API_TOKEN`, and `sitrep PROJ-123` reads that Epic — the monitor, the
filters, and `enter` for a Ticket's description, comments and links, each with your
instance's own wording for the link ("is blocked by", or whatever your admin renamed it to).
An epic's children are the issues whose **parent** is that epic; sitrep does not read the
deprecated Epic Link field. Your own Atlassian email can also come from a variable, with
`auth.user_env` instead of `auth.user`.

Descriptions and comments come back as Jira wiki markup rather than markdown, because that
is what the REST API returns as text, and sitrep displays what the Tracker gave it without
rendering it.

**Jira shows no pull request information, by design.** That data lives behind an
undocumented internal endpoint and a per-instance application link, and sitrep will not
guess: the section is simply absent from a Jira report rather than empty or wrong.

### GitLab

**`glab auth login` alone is enough** on gitlab.com — no Profile and no config file. sitrep
reads `$GITLAB_TOKEN` (or `$GITLAB_ACCESS_TOKEN`, or `$OAUTH_TOKEN`) first and falls back to
`glab`'s stored login, which is `glab`'s own documented order.

A Profile is what pins a self-managed instance, names a default group or project, or points
at a different token variable:

```yaml
profiles:
  acme-gitlab:
    provider: gitlab
    host: git.acme.test    # the instance host, not a URL; gitlab.com for SaaS
    project: acme/platform # the group or project path
    auth:
      token_env: GITLAB_TOKEN
```

Two ref forms reach a GitLab epic: its URL,
`https://gitlab.com/groups/acme/-/epics/12`, and GitLab's own reference, `acme&12` — or
just `&12` when a Profile's `project` names the group. A project issue works the same way
by URL (`https://gitlab.com/acme/widgets/-/issues/7`, and the `/-/work_items/7` form GitLab
now serves), landing straight in that Ticket's Detail with its epic as the breadcrumb.
Inside a clone, a bare number resolves against the `origin` remote; on a self-managed host,
a `gitlab` Profile claiming that host is what tells sitrep it is not GitHub Enterprise.

**On GitLab Free, a milestone is what sitrep reports as an Epic.** Native epics need
Premium or Ultimate, but a milestone is Free tier and is the collection most teams actually
delegate into — so a milestone ref renders a full Epic view, with the same progress bar,
the same grouped Tickets, the same filters and the same drill-in. Three forms reach one:
its project URL `https://gitlab.com/acme/widgets/-/milestones/3`, its group URL
`https://gitlab.com/groups/acme/-/milestones/3`, and GitLab's own reference `acme/widgets%3`
— or `groups/acme%3` for a group milestone, or just `%3` when a Profile's `project` names
the project. A child issue's milestone becomes its breadcrumb when it has no epic.

sitrep does not guess which of the two you meant: an epic ref fetches an epic and a
milestone ref fetches a milestone. If you point it at an epic on a Free instance, the epic
endpoints answer `403` and sitrep says so — and points you at the milestone route.

**Merge requests show per Ticket.** Every Ticket carries the merge requests moving it, with
their state, their review and approval posture, and their CI pipeline status; an open
Ticket with an open or draft merge request is reported as in progress. Note that this costs
roughly one extra request per Ticket per refresh, so raise `--interval` on a large Epic
against a rate-limited instance.

## The `--json` document

`--json` is the machine-readable contract, and it is what an agent should read rather than
scraping `--plain`.

**Conventions.** `schema_version` is `1` and always comes first. Keys are snake_case. Times
are RFC 3339 in UTC, truncated to the second. `tickets` is always present and always an
array, never null. **Optional and capability-gated keys are omitted, never null** — absence
is how sitrep says "this Tracker does not do that", so a script must test for the key
rather than for an empty value.

There are **two documents**. An Epic Ref that names a collection emits the epic document;
one that names a plain Ticket emits the ticket document (the same Ref, decoded — see
[Usage](#usage)).

### The epic document

| Key | Type | Meaning |
|---|---|---|
| `schema_version` | number | always `1` |
| `generated_at` | string | when this snapshot was read |
| `provider.name` | string | `github`, `gitlab`, `jira` or `fake` |
| `provider.capabilities` | object | `hierarchy`, `blocking_links`, `comments`, `pull_requests`, each a bool — which optional keys can appear at all |
| `epic` | object | `id`, `key`, `title`, `url`, `status`, `native_status` |
| `progress` | object | the completion arithmetic, below |
| `tickets` | array | every Ticket, flat, in the Tracker's own order |

`progress` counts Tickets by Status Category: `todo`, `in_progress`, `done`, `cancelled`,
`unknown`, plus `total` (every Ticket), `denominator` (`total` minus `cancelled` — **the
Tickets that can still be finished**) and `percent_done` (`done` over `denominator`, 0–100,
rounded; `0` when `denominator` is `0`). Cancelled work is counted but excluded from the
denominator, so an epic reads "7/11 done, 1 cancelled" rather than being permanently short
of 100%.

Each entry in `tickets`:

| Key | Type | Meaning |
|---|---|---|
| `id` | string | the Provider's opaque handle — do not parse it |
| `key` | string | the display identity, e.g. `#113` or `acme/widgets#113` |
| `title`, `url` | string | the Ticket's summary and its Tracker page |
| `status` | string | the normalized Status Category |
| `native_status` | string | the Tracker's own word for it; omitted when there is none |
| `repository` | string | where the Ticket lives; omitted when the Tracker has no such notion |
| `parent_id` | string | present only with the `hierarchy` capability, and omitted when empty |
| `assignees` | array | `login`, plus optional `display_name` and `avatar_url` |
| `pull_requests` | array | present only with the `pull_requests` capability |

Each entry in `pull_requests` carries `number`, `title`, `url`, an optional `repository`,
and the three tokens below. A GitLab merge request uses this same object — `number` is its
iid — and sitrep renders every one of them as `#N`, on every Tracker.

### The ticket document

Emitted when the Ref named a plain Ticket. It carries `schema_version`, `generated_at` and
`provider` exactly as above, plus:

| Key | Type | Meaning |
|---|---|---|
| `ticket` | object | the Ticket itself, in the same shape as a `tickets` entry |
| `parent` | object | `id` and optional `key`, `title`, `url` — omitted when the Ticket hangs off nothing |
| `ticket_id` | string | the Ticket's opaque handle, matching `ticket.id` |
| `description` | string | the raw body, exactly as the Tracker returned it, unrendered |
| `comments` | array | `id`, `author`, `body`, `created_at`, optional `url`; needs the `comments` capability |
| `links` | array | `kind`, optional `native_label`, and a `target` object; blocking kinds need the `blocking_links` capability |

### Enumerations

Every one of these is a fixed token; anything else is a bug.

| Field | Values |
|---|---|
| `status`, `target.status` | `unknown`, `todo`, `in_progress`, `done`, `cancelled` |
| `pull_requests[].state` | `unknown`, `draft`, `open`, `merged`, `closed` |
| `pull_requests[].review` | `none`, `pending`, `approved`, `changes_requested` |
| `pull_requests[].checks` | `none`, `pending`, `passing`, `failing` |
| `links[].kind` | `relates`, `blocked_by`, `blocks` |

### An example

Trimmed from sitrep's own golden output:

```json
{
  "schema_version": 1,
  "generated_at": "2026-01-15T12:00:00Z",
  "provider": {
    "name": "fake",
    "capabilities": {
      "hierarchy": true,
      "blocking_links": true,
      "comments": true,
      "pull_requests": true
    }
  },
  "epic": {
    "id": "acme/widgets#111",
    "key": "#111",
    "title": "Widget sync v2: shards, retries & telemetry",
    "url": "https://tracker.example.test/acme/widgets/111",
    "status": "in_progress",
    "native_status": "open"
  },
  "progress": {
    "todo": 3,
    "in_progress": 3,
    "done": 3,
    "cancelled": 1,
    "unknown": 0,
    "total": 10,
    "denominator": 9,
    "percent_done": 33
  },
  "tickets": [
    {
      "id": "acme/widgets#113",
      "key": "#113",
      "title": "Retry & backoff for the sync worker",
      "url": "https://tracker.example.test/acme/widgets/113",
      "status": "in_progress",
      "native_status": "In Progress",
      "repository": "acme/widgets",
      "assignees": [
        { "login": "mara-vos", "display_name": "Mara Vos" },
        { "login": "tobias" }
      ],
      "pull_requests": [
        {
          "number": 502,
          "title": "Exponential backoff with jitter",
          "url": "https://tracker.example.test/acme/widgets/pull/502",
          "repository": "acme/widgets",
          "state": "open",
          "review": "pending",
          "checks": "failing"
        }
      ]
    }
  ]
}
```

Two one-liners an agent actually needs:

```sh
sitrep 111 --json | jq '.progress.percent_done'
sitrep 111 --json | jq -r '.tickets[] | select(.status != "done" and .status != "cancelled") | .key'
```

**The compatibility promise:** additive changes — a new optional key, a new Provider — keep
`schema_version` at `1`. A breaking change to the shape or to a token bumps it. Pin on
`schema_version` and ignore keys you do not know.

## When something fails

Every failure is one line on stderr, prefixed `sitrep:` and then the driver's name, and
sitrep exits `1`. There are three classes worth knowing, because the fix is different for
each.

**A bad ref** — `github: acme/widgets#999 not found (or you lack access)`. The ref named
nothing the Tracker has: a typo, the wrong repository, a deleted issue, or a private one
your token cannot see. The message quotes the ref as you typed it, or the key sitrep
derived from it, so the first thing to check is whether that is the thing you meant. A bare
number is resolved through the current clone's `origin` remote, so running it in the wrong
directory points it at the wrong repository — pass `owner/repo#111` or the full URL from
anywhere.

**An auth failure** — `github: authentication failed (401) — check "gh auth status" or
GITHUB_TOKEN`. The credential is missing, rejected, or lacks a scope. On GitHub, `gh auth
status`; on GitLab, `glab auth status` or `$GITLAB_TOKEN`; on Jira, the Atlassian email and
API token your Profile names, remembering that **a Profile names a token, it never holds
one** — `auth.token_env` is a variable name and sitrep reads the value from your
environment. A `403` is the same class: on GitHub it usually means a missing scope or an
organisation enforcing SAML SSO, on GitLab a token without `api` or `read_api`, on Jira a
project you cannot read.

**Rate limiting** — `github: API rate limit exceeded; resets at Mon, 01 Jan 2026 14:32:00
CET`. The Tracker said "not now" and the message says when the limit clears.
**sitrep never retries behind your back**: there is no backoff loop and no hidden second
request, so what you are seeing is one refresh's worth of traffic. Raise `--interval` (or a
Profile's `refresh_interval`) if a large Epic is hitting the limit — GitLab's per-Ticket
merge request correlation is the usual reason.

Two more things about failures, both deliberate:

- A **pre-flight failure you cannot retry away** — a bad ref, a missing or rejected
  credential — prints its line and exits instead of opening the monitor. An alt-screen
  retry loop is a bad way to read one sentence on a headless box. Anything that might
  recover — a network blip, a rate limit, a 5xx — still opens the monitor.
- **A monitor already open survives a failed refresh**, keeping its last good reading on
  screen and saying so in the footer, whatever the failure was.
- **`ctrl+c` is silent.** It prints nothing and exits `130`; a cancelled fetch is not the
  Tracker's fault and sitrep does not report it as one.

## Install

Download the archive for your OS/arch from the
[releases page](https://github.com/niekcandaele/sitrep/releases) and put `sitrep` on your
`PATH`. The binaries are static (linux and darwin, amd64 and arm64), so they drop onto an
SSH box without dependencies.

On a fresh headless linux/amd64 box, with the version you want:

```sh
v=0.1.0
curl -fsSLO "https://github.com/niekcandaele/sitrep/releases/download/v$v/sitrep_${v}_linux_amd64.tar.gz"
curl -fsSLO "https://github.com/niekcandaele/sitrep/releases/download/v$v/checksums.txt"
sha256sum --check --ignore-missing checksums.txt
tar -xzf "sitrep_${v}_linux_amd64.tar.gz" sitrep
install -m 0755 sitrep ~/.local/bin/sitrep
sitrep --version
```

`sitrep --version` is the "it works" check, and every release publishes `checksums.txt`
beside the archives so the download can be verified before it is run.

Or, with a Go toolchain:

```sh
go install github.com/niekcandaele/sitrep/cmd/sitrep@latest
```

A `go install` build reports the version the Go toolchain embedded from the module and the
commit it stamped, where a release archive reports the tag it was built from. Both are
enough to identify a binary in a bug report.

## Build from source

```sh
git clone https://github.com/niekcandaele/sitrep.git
cd sitrep
make build
./sitrep --version
```

## Development

`make check` runs the same gates CI does, in the same order:

| Target | Gate |
|---|---|
| `make fmt-check` | `gofmt -l .` reports nothing |
| `make vet` | `go vet ./...` |
| `make lint` | `golangci-lint run` |
| `make test` | `go test -race ./...` |

CI runs those four gates on every pull request and fails on any violation, plus a release
dry-run (`goreleaser check`, a snapshot build, and running the linux/amd64 binary that
build produced) that proves the release pipeline without cutting a release. Pushing a `v*`
tag publishes the archives to GitHub Releases — see [docs/RELEASING.md](docs/RELEASING.md)
for the one command that does it and what to verify afterwards.

`golangci-lint` is pinned to the version CI uses:

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.1
```

Tests assert observable behaviour at seams — exit codes, rendered reports and whole
terminal frames — with no assertion library: the standard library, plus `teatest` to drive
the monitor. The runtime dependencies are the Charm TUI stack the monitor is built on —
Bubble Tea, Bubbles and Lip Gloss ([ADR-0001](docs/adr/0001-go-and-bubble-tea.md)) — plus
`gopkg.in/yaml.v3` to read the config file, and nothing else. All are pure Go, so the
release binaries stay static.
