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

## Install

Download the archive for your OS/arch from the
[releases page](https://github.com/niekcandaele/sitrep/releases) and put `sitrep` on your
`PATH`. The binaries are static (linux and darwin, amd64 and arm64), so they drop onto an
SSH box without dependencies.

Or, with a Go toolchain:

```sh
go install github.com/niekcandaele/sitrep/cmd/sitrep@latest
```

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
dry-run (`goreleaser check` and a snapshot build) that proves the release pipeline without
cutting a release. Pushing a `v*` tag publishes the archives to GitHub Releases.

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
