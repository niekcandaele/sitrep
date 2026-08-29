# sitrep

sitrep is a read-only terminal ticket viewer across GitHub, Jira, and GitLab. A
**Watchlist** is the set of Tickets on screen or in a one-shot report; a **Selector** says
how to choose it. Agents write; sitrep watches. It never writes to a Tracker
([ADR-0002](docs/adr/0002-read-only-by-design.md)).

The Watchlist behavior documented in this source tree is being developed on the unreleased
v0.2 integration branch. The latest tagged release is still `v0.1.0`; the install example
below deliberately installs that release and does not include the new Watchlist forms yet.

## Choose a Watchlist

There are four ways into the CLI and three concrete Selector kinds:

```sh
sitrep 111                                  # one Ref: Epic Watchlist or Ticket Detail
sitrep 38 39 40 41                         # exact positional Ref-list Watchlist
producer | sitrep -                        # whitespace-separated Refs from stdin
sitrep --profile work-github \
  --query 'repo:acme/widgets is:issue label:agent'  # tracker-native Query Watchlist

sitrep 38 39 40 41 --plain                 # one-shot text
sitrep 38 39 40 41 --json                  # schema-v2 Watchlist JSON
```

A **Ref** points to one Ticket or Epic. Accepted forms are:

- a bare number or `#number`, resolved through the current clone's `origin` remote;
- a GitHub-style `owner/repository#number` or issue URL;
- a Jira key such as `PROJ-123`, matched to a Profile by project-key prefix, or a Jira
  `/browse/PROJ-123` URL;
- a GitLab issue or work-item URL;
- a GitLab native Epic Ref or URL such as `acme&12` or
  `https://gitlab.com/groups/acme/-/epics/12`;
- a GitLab milestone Ref or URL such as `acme/widgets%3` or
  `https://gitlab.com/acme/widgets/-/milestones/3`.

GitLab's `&N` names an actual native Epic. `%N` names the milestone-as-Epic fallback that is
available on GitLab Free.

The four entry forms behave as follows:

- **One positional Ref** uses the Epic Selector retained for v0.1 compatibility. If the
  resolved root has children, those children form an Epic Watchlist. Under the existing
  decoder contract, a root with no children is treated as a plain Ticket and opens its
  Detail directly.
- **Two or more positional Refs** form an exact Ref-list Watchlist. Roots are not expanded
  and no common Epic is discovered or required. Refs may cross repositories or projects
  when one Provider and Tracker connection can serve them; if routing selects a Profile,
  every Ref must use it. Mixed Trackers, connections, or Profile selections are refused.
  Effective duplicates are removed, retaining the first spelling and order.
- A sole **`-`** reads Refs from stdin, splitting on all Unicode whitespace. There is no
  comma, comment, or quote grammar. `-` must be the only positional argument, and even one
  stdin token keeps exact Ref-list semantics rather than becoming an Epic Selector. The
  default TUI consumes the pipe to EOF, then reads keys from the controlling terminal;
  `--plain` and `--json` need no controlling TTY.
- **`--query`** selects membership with the exact, opaque tracker-native Query. It is
  presence-sensitive, including an explicitly empty value, and cannot be combined with a
  positional or stdin form. Membership is searched again on every refresh; direct exact
  reads supply the current thin Ticket state that sitrep renders. An explicit Profile is the
  clearest route. Without one, GitHub and GitLab can use an unambiguous supported current git
  origin; Jira Query use requires a Profile.

stdin is transport for a Ref-list Selector, not a fourth schema kind. Watchlist JSON records
only `epic`, `ref_list`, or `query`.

## Output modes

For Watchlist-producing Selectors, all three modes read the same resolved Watchlist:

- with no mode flag, sitrep opens the full-screen, auto-refreshing TUI;
- `--plain` prints one unstyled Watchlist or Ticket report for humans, dumb terminals, logs,
  and pipes;
- `--json` prints the one-shot machine contract for scripts and agents. Consume it instead
  of scraping plain text.

The single-Ref decoder exception applies in every mode: a plain Ticket opens Detail in the
TUI, prints one Ticket report with `--plain`, and emits a schema-v1 Ticket/Detail document
with `--json`. Watchlist JSON uses the schema-v2 contract.

Progress, grouping, and filters cover the complete fetched Watchlist. Hiding rows does not
change the progress denominator. When a Query reaches its configured membership cutoff,
"complete fetched Watchlist" means the fetched subset, not a known total result count.

## Compose with tracker CLIs

A producer can select exact Tickets and hand their Refs to stdin. These commands emit one
accepted Ref token per line:

```sh
# GitHub CLI: full URLs work outside a clone
gh issue list --repo acme/widgets --label agent --state all \
  --json url --jq '.[].url' |
  sitrep -

# GitLab CLI: URL output is one issue/work-item URL per line
glab issue list --repo acme/widgets --label agent --all --output-format urls |
  sitrep -

# Atlassian ACLI (official; Jira Cloud)
acli jira workitem search \
  --jql 'project = PROJ AND labels = agent' \
  --fields key --paginate --csv |
  tr -d '"\r' |
  grep -E '^[A-Za-z][A-Za-z0-9_]*-[0-9]+$' |
  sitrep --profile acme-jira -

# ankitpokhrel/jira-cli (community; Jira Cloud and Data Center)
jira issue list --jql 'project = PROJ AND labels = agent' \
  --plain --no-headers --columns key |
  sitrep --profile acme-jira -
```

Command references: [GitHub `gh issue list`](https://cli.github.com/manual/gh_issue_list),
[GitLab `glab issue list`](https://docs.gitlab.com/cli/issue/list/),
[Atlassian `acli jira workitem search`](https://developer.atlassian.com/cloud/acli/reference/commands/jira-workitem-search/),
and [ankitpokhrel/jira-cli](https://github.com/ankitpokhrel/jira-cli).

ACLI's `--fields key --csv` requests one CSV column; the following filter removes its header
and admits only Jira keys accepted by sitrep. ACLI's `--paginate` belongs to the producer.
sitrep's `max_tickets` belongs only to native Query Selectors; it intentionally does not cap
positional or stdin exact lists.

## Native Queries

Each real Provider accepts its Tracker's own language, without a sitrep query dialect:

```sh
sitrep --profile work-github \
  --query 'repo:acme/widgets is:issue label:agent'

sitrep --profile acme-jira \
  --query 'project = PROJ AND statusCategory != Done'

sitrep --profile acme-gitlab \
  --query 'scope=all&state=opened&labels=agent'
```

- **GitHub** receives issue-search syntax exactly. Include repository or organization scope
  when required; a Profile does not add it.
- **Jira** receives exact JQL. The Profile supplies site, credentials, and routing, but sitrep
  does not insert a `project = ...` clause.
- **GitLab** receives a raw issue-API query component. The Profile's `project` selects the
  endpoint it is searched against: an unprefixed path such as `acme/widgets` uses the
  project-scoped issues endpoint, a `groups/`-prefixed path such as `groups/acme` uses the
  group-scoped one, and an empty project uses the global issues endpoint.

Search selects identities. The Provider then rereads each identity through its exact Ticket
path, so rendered title, status, assignees, and code state do not come from a search-result
projection. Malformed Queries are reported as Tracker/Provider failures; sitrep does not
normalize or retry them as another language.

## Monitor and keybindings

The monitor groups Tickets by Status Category with a progress bar, assignees, and correlated
pull or merge requests. It refreshes every 60 seconds by default and shows how old the last
good reading is. `--interval` overrides the cadence down to a 5-second floor. A failed
refresh leaves the last good Watchlist on screen and reports the failure in the footer.

Mouse capture is on by default. Click a Ticket row to select it, double-click one to open its
Detail, and use the wheel to move one Ticket at a time in the list or three lines at a time in
Detail. Press `m` to turn capture off or on. Shift-drag is the common terminal escape hatch
for selecting text while capture is on; if a terminal or multiplexer does not honor it, use
`m`, or start with `--no-mouse` and enable capture only when wanted.

`enter` opens the selected Ticket's Detail — description, comments, and explicit Links
carrying the Tracker's native labels — fetched at that moment and never during list refresh.
Use `tab` and `shift+tab` to focus Links and `enter` to follow the focused target. With mouse
capture on, clicking an explicit Link row follows it directly. Following a Link opens its
Ticket Detail and appends the source Ticket to the session-local **Trail**. `esc` pops one
Trail step and restores the preceding Ticket; at the Trail root it returns to the list. When
the root Ticket was opened directly with no list behind it, `esc` quits. `u` leaves the Trail
for its root Watchlist when one is available. List selection, scroll position, and filters
remain preserved.

Tracker URLs on Link rows, Ticket identities, breadcrumbs, and lead pull/merge-request
summaries are also OSC 8 terminal hyperlinks. The terminal owns whether and how those URLs
open; sitrep never launches a browser or shell command. Terminal hyperlink activation does
not extend the in-app Trail ([ADR-0004](docs/adr/0004-links-are-hyperlinks-not-browser-launches.md)).
Description and comment text is shown as returned by the Tracker, without inferred Link
navigation.

`d` hides Done and Cancelled Tickets without changing progress arithmetic. `/` opens fuzzy
find over Ticket titles and keys; type to narrow, `enter` keeps the query, and `esc` first
clears the active find or visibility filter.

The footer shows currently applicable keys, and `?` expands it.

**The list**

| Key | Does |
|---|---|
| `↑` / `k`, `↓` / `j` | move the selection |
| click | select a Ticket |
| double-click | open a Ticket's Detail |
| wheel | move one Ticket |
| `pgup`, `pgdn` | move one page |
| `g`, `G` | first Ticket, last Ticket |
| `enter` | open the selected Ticket's Detail |
| `d` | hide Done and Cancelled Tickets |
| `/` | open fuzzy find |
| `esc` | clear filters, or quit when none are active |
| `r` | refresh now |
| `m` | turn mouse capture off or on |
| `?` | expand help |
| `q`, `ctrl+c` | quit |

**The find box** — everything not listed is text, so searching for `queue` types a `q`
rather than quitting.

| Key | Does |
|---|---|
| `enter` | keep the query and close the box |
| `esc` | abandon the query and close the box |
| `↑`, `↓`, `pgup`, `pgdn` | move selection without leaving the box |
| shift-drag | select terminal text while mouse capture is on |
| `ctrl+c` | quit |

**A Ticket's Detail**

| Key | Does |
|---|---|
| `tab`, `shift+tab` | focus the next or previous explicit Link |
| `enter` | follow the focused Link |
| click an explicit Link | follow its target |
| `↑` / `k`, `↓` / `j` | scroll one line |
| wheel | scroll three lines |
| `pgup`, `pgdn` | scroll one page |
| `g`, `G` | top, bottom |
| `esc` | back one Trail step, then to the list; quit when opened directly |
| `u` | open the root Watchlist when available, clearing the Trail |
| `r` | reread this Ticket's Detail |
| `m` | turn mouse capture off or on |
| `?` | expand help |
| `q`, `ctrl+c` | quit |

## Profiles, Providers, and configuration

**GitHub with `gh auth login` done needs no config file.** GitLab.com has the equivalent
zero-config path through `glab auth login`. Jira always needs a Profile.

sitrep reads one file: `~/.config/sitrep/config.yml`,
`$XDG_CONFIG_HOME/sitrep/config.yml`, or the exact path in `$SITREP_CONFIG`. There is no
per-repository config and no search or merge. A missing file is silence; malformed YAML or
an invalid Profile is an error naming the file, Profile, and problem.

```yaml
profiles:
  work-github:
    provider: github
    host: ghe.acme.test
    auth:
      token_env: GHE_TOKEN
    max_tickets: 250
    refresh_interval: 5m

  acme-jira:
    provider: jira
    host: acme.atlassian.net
    project: PROJ
    auth:
      user: me@acme.test
      token_env: JIRA_API_TOKEN
    max_tickets: 100
    refresh_interval: 30s

  acme-gitlab:
    provider: gitlab
    host: git.acme.test
    project: groups/acme/platform
    auth:
      token_env: GITLAB_TOKEN
```

`provider: github|gitlab|jira` chooses the driver for that Profile. A GitHub Profile must not
set `project`, because a GitHub Ref or native Query carries its own scope. A Jira Profile
requires `host`, `project`, and `auth.token_env`; `auth.user` or `auth.user_env` supplies the
Atlassian identity. A GitLab Profile requires `host` and may set a group or project path.

A GitLab `project` declares which of the two it is, and sitrep never guesses: `project:
acme/widgets` is a project, `project: groups/acme` is a group. That scope decides both which
issues endpoint a `--query` reads and which hostless Refs the Profile can complete — `&N`
needs a group, `N` or `#N` needs a project, and `%N` works under either and follows the
Profile's scope. A Ref the Profile's scope cannot complete is rejected before any request,
with the spelling to use instead.

**A Profile names a token; it never holds one.** `auth.token_env` is the environment-variable
name. Literal tokens in config are rejected. A credential is sent only to the Profile host
or to the host claimed by the corresponding `gh`/`glab` login. sitrep will not send a
GitHub.com or GitLab.com ambient token to a host inferred from an arbitrary URL or remote.

`--provider auto` is the default and infers a Provider from Refs or the current origin when
possible. `--provider` can correct an otherwise ambiguous self-managed origin, but cannot
contradict a Ref whose Tracker is known. `--profile` resolves routing ambiguity and is the
recommended explicit route for native Queries. One Watchlist has at most one selected Profile
and uses one Provider, host, connection, credential scope, Capability set, and staleness clock.

### Independent membership and cadence knobs

`max_tickets` and `refresh_interval` are independent settings. A selected Profile supplies
its configured values; profileless routes use the defaults below:

- effective `max_tickets` defaults to `100` when no Profile is selected or the selected
  Profile omits it;
- it must be a positive integer and has no arbitrary upper ceiling;
- it caps only Tracker-discovered Query membership — never Epic children, positional Refs,
  or stdin Refs;
- hitting the cutoff is successful truncation, not a known total. Plain/TUI output says
  `Limit reached — showing N ticket(s).`; schema-v2 JSON adds
  `watchlist.limit_reached: true`;
- exhausting results exactly at the boundary is not labeled as truncated;
- `refresh_interval` controls monitor cadence independently. It defaults to 60 seconds,
  has a 5-second floor, and is overridden by `--interval`;
- one-shot modes honor Query `max_tickets` but do not use monitor cadence.

sitrep does not slow, disable, or otherwise tune refresh based on `max_tickets`. A large
Watchlist and a long cadence remain user choices. `max_tickets` is not a CLI flag or a cap on
exact lists.

## Provider notes

### GitHub

GitHub issue sub-issues form an Epic Watchlist. A bare number is completed from the current
origin; full issue URLs and `owner/repository#number` work elsewhere. GitHub pull request
information comes from the closing pull-request references GitHub returns for each issue,
including state, review decision, and head checks. sitrep does not infer additional
relationships from branch names.

For github.com, an ambient `$GH_TOKEN` or `$GITHUB_TOKEN` is scoped to github.com and sitrep
falls back to `gh auth token`. An Enterprise host needs its own `gh auth login --hostname`
or Profile/token reference.

### Jira

A Jira key is matched to the Profile whose `project` is its prefix. Multiple sites using the
same prefix require `--profile`. For Jira Cloud, create an API token at
[id.atlassian.com](https://id.atlassian.com/manage-profile/security/api-tokens) and expose it
through the environment variable named by `auth.token_env`. Epic children are issues whose
modern `parent` names that Epic; sitrep does not use the deprecated native "Epic Link" field.

Descriptions and comments arrive as Jira wiki markup because that is what the REST API
returns as text. Jira pull request data is absent: the relevant data is behind undocumented,
instance-specific application-link endpoints, so the Capability is not declared rather than
rendered empty or guessed.

### GitLab

GitLab.com can use ambient GitLab token variables or `glab auth login`; a self-managed host
needs `$GITLAB_HOST`, a host-specific `glab` login, or a Profile. A Profile can also provide
the group/project needed to complete hostless `&N` and `%N` Refs. A Profile naming a group
must write it `groups/<path>`; anything unprefixed is a project path. `&N` therefore needs a
group Profile, a bare `N` or `#N` needs a project Profile, and `%N` reads the milestones of
whichever scope the Profile declared.

Native epics require Premium or Ultimate. On GitLab Free, a project or group milestone is the
Epic fallback, with the same Watchlist renderers and Ticket drill-in. sitrep does not guess
between them: an `&N` Ref reads a native Epic and a `%N` Ref reads a milestone. A child
issue's native Epic, or its milestone when no Epic exists, becomes its parent breadcrumb.

Merge requests show per Ticket with state, review/approval posture, and pipeline status. An
open Ticket with an open or draft merge request is categorized InProgress. Correlation costs
roughly one extra request per Ticket per refresh, so Watchlist size matters on rate-limited
instances.

## The `--json` contract

`--json` is the machine-readable contract. Keys are snake_case; timestamps are RFC 3339 UTC
at second precision; `tickets` is always an array. Optional and Capability-gated fields are
omitted, never `null`.

There are two independently versioned document families:

- **Watchlist documents use schema version 2** for Epic, Ref-list/stdin, and Query
  Watchlists.
- **Decoded Ticket/Detail documents remain schema version 1** when one positional Ref opens a
  plain Ticket.

### Watchlist document: schema v2

| Key | Meaning |
|---|---|
| `schema_version` | `2` |
| `generated_at` | when this Watchlist was resolved |
| `provider.name` | `github`, `gitlab`, `jira`, or `fake` |
| `provider.capabilities` | optional data Capabilities plus `selectors.epic`, `ref_list`, and `query` |
| `watchlist.selector` | Selector `kind` plus kind-specific `ref`, `refs`, or `query` |
| `watchlist.epic` | Epic identity, present only for an Epic Selector |
| `watchlist.limit_reached` | optional `true`, present only for a truncated Query |
| `progress` | Status Category arithmetic over the fetched Tickets |
| `tickets` | current thin Tickets, flat and in Provider order |

The three Selector variants are:

| `watchlist.selector.kind` | Kind-specific fields |
|---|---|
| `epic` | `selector.ref` and `watchlist.epic` |
| `ref_list` | ordered `selector.refs`; no fabricated Epic |
| `query` | exact `selector.query`; no fabricated Epic; optional `limit_reached` |

A one-Ticket stdin Ref list, copied from deterministic fake output, demonstrates the
non-Epic shape:

```json
{
  "schema_version": 2,
  "generated_at": "2026-01-15T12:00:00Z",
  "provider": {
    "name": "fake",
    "capabilities": {
      "hierarchy": true,
      "blocking_links": true,
      "comments": true,
      "pull_requests": true,
      "selectors": {
        "epic": true,
        "ref_list": true,
        "query": true
      }
    }
  },
  "watchlist": {
    "selector": {
      "kind": "ref_list",
      "refs": ["acme/widgets#115"]
    }
  },
  "progress": {
    "todo": 1,
    "in_progress": 0,
    "done": 0,
    "cancelled": 0,
    "unknown": 0,
    "total": 1,
    "denominator": 1,
    "percent_done": 0
  },
  "tickets": [
    {
      "id": "acme/widgets#115",
      "key": "#115",
      "title": "Reconcile widget IDs across shards",
      "url": "https://tracker.example.test/acme/widgets/115",
      "status": "todo",
      "native_status": "open",
      "repository": "acme/widgets"
    }
  ]
}
```

`progress` counts `todo`, `in_progress`, `done`, `cancelled`, and `unknown`, plus `total`,
`denominator` (`total - cancelled`), and rounded `percent_done`. A Ticket carries `id`, `key`,
`title`, `url`, `status`, and optional `native_status`, `repository`, hierarchy `parent_id`,
`assignees`, and `pull_requests`. Each pull request has `number`, `title`, `url`, optional
`repository`, and the fixed `state`, `review`, and `checks` tokens listed below.

Two useful Watchlist queries:

```sh
sitrep 38 39 40 41 --json | jq '.progress.percent_done'
sitrep --profile work-github --query 'repo:acme/widgets is:issue' --json |
  jq -r '.tickets[] | select(.status != "done" and .status != "cancelled") | .key'
```

### Decoded Ticket/Detail document: schema v1

A plain Ticket document has `schema_version`, `generated_at`, and `provider` (without the
schema-v2 Selector Capability object), plus:

| Key | Meaning |
|---|---|
| `ticket` | the Ticket in the same thin shape as a Watchlist member |
| `parent` | optional `id`, `key`, `title`, and `url` breadcrumb |
| `ticket_id` | the opaque Ticket handle, equal to `ticket.id` |
| `description` | raw Tracker text |
| `comments` | Capability-gated `id`, `author`, `body`, `created_at`, optional `url` |
| `links` | visible `kind`, optional `native_label`, and target Ticket object |

### Enumerations and compatibility

| Field | Values |
|---|---|
| `status`, `target.status` | `unknown`, `todo`, `in_progress`, `done`, `cancelled` |
| `pull_requests[].state` | `unknown`, `draft`, `open`, `merged`, `closed` |
| `pull_requests[].review` | `none`, `pending`, `approved`, `changes_requested` |
| `pull_requests[].checks` | `none`, `pending`, `passing`, `failing` |
| `links[].kind` | `relates`, `blocked_by`, `blocks` |

Compatibility is per document schema. Additive optional fields do not require a bump. A
breaking Watchlist change increments schema v2; an unchanged decoded Ticket/Detail document
remains schema v1. Consumers should pin the schema version for the document family they read
and ignore unknown keys.

## When something fails

Every runtime failure is one sanitized line on stderr prefixed `sitrep:` and, for Tracker
failures, the Provider name. sitrep exits `1`. CLI usage failures print help and exit `2`.

- **Bad Ref or membership**: check the quoted Ref, exact-list member, or native Query. A bare
  number uses the current clone's origin; use an owner/repository Ref or full URL elsewhere.
  One invalid exact member fails the Watchlist rather than rendering a partial list.
- **Authentication or authorization**: use `gh auth status`, `glab auth status`, or verify the
  identity and token environment variable named by the Jira Profile. A Profile contains a
  variable name, never the token value.
- **Rate limiting**: the Tracker decides when traffic can resume. sitrep performs no hidden
  retry or backoff loop. One logical Query Resolve can include bounded search pages and
  authoritative member reads; GitLab merge-request correlation is also proportional to
  Watchlist size. Raise `--interval` or a Profile's `refresh_interval` when needed.

A non-retryable preflight failure (bad Ref, malformed Query, missing credential) exits before
opening the TUI. A retryable network/rate-limit/5xx preflight can still open the monitor; an
already-open monitor always keeps its last good reading after a failed refresh. `ctrl+c` is
silent and exits `130`.

## Install the tagged release

The Watchlist forms above are unreleased. To install the currently tagged `v0.1.0`, download
an archive from the [releases page](https://github.com/niekcandaele/sitrep/releases):

```sh
v=0.1.0
curl -fsSLO "https://github.com/niekcandaele/sitrep/releases/download/v$v/sitrep_${v}_linux_amd64.tar.gz"
curl -fsSLO "https://github.com/niekcandaele/sitrep/releases/download/v$v/checksums.txt"
sha256sum --check --ignore-missing checksums.txt
tar -xzf "sitrep_${v}_linux_amd64.tar.gz" sitrep
install -m 0755 sitrep ~/.local/bin/sitrep
sitrep --version
```

The release publishes static Linux and Darwin archives for amd64 and arm64 plus
`checksums.txt`. A Go toolchain can install the latest tagged module version:

```sh
go install github.com/niekcandaele/sitrep/cmd/sitrep@latest
```

That command follows tagged releases; it does not install unreleased integration-branch
behavior.

## Build from source

```sh
git clone https://github.com/niekcandaele/sitrep.git
cd sitrep
make build
./sitrep --version
```

Building the v0.2 integration branch is the way to exercise the unreleased Watchlist work in
this source tree.

## Development

`make check` runs the same four gates as CI:

| Target | Gate |
|---|---|
| `make fmt-check` | `gofmt -l .` reports nothing |
| `make vet` | `go vet ./...` |
| `make lint` | `golangci-lint run` |
| `make test` | `go test -race ./...` |

CI also runs a release dry-run: `goreleaser check`, a snapshot build, and the generated
Linux/amd64 binary's `--version` and `--help`. Pushing a `v*` tag publishes archives — see
[docs/RELEASING.md](docs/RELEASING.md).

`golangci-lint` is pinned to the version CI uses:

```sh
go install github.com/golangci-lint/v2/cmd/golangci-lint@v2.13.1
```

Tests assert observable behavior at seams: exit codes, reports, JSON, and complete terminal
frames. Runtime dependencies are the pure-Go Charm TUI stack
([ADR-0001](docs/adr/0001-go-and-bubble-tea.md)) and `gopkg.in/yaml.v3`; static builds need no
runtime installation. The Watchlist product/seam decision is recorded in
[ADR-0005](docs/adr/0005-watchlist-not-epic.md).
