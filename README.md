# sitrep

A read-only terminal situation report on the work you delegated: one screen showing an
Epic's Tickets, their status, and the code moving them — across GitHub, Jira, and GitLab.
Agents write; sitrep watches. It never writes to a Tracker ([ADR-0002](docs/adr/0002-read-only-by-design.md)).

sitrep is early: today it reads an Epic from GitHub, either as a live monitor you leave
open or as a one-shot report. Jira and GitLab land in later work.

## Usage

```sh
sitrep <ref>           # the live monitor: a full-screen, auto-refreshing epic view
sitrep <ref> --plain   # a one-shot text report, for humans, dumb terminals and pipes
sitrep <ref> --json    # the same epic as a JSON document, for scripts and agents
```

`<ref>` is the Epic Ref: a bare number (`111`, resolved through the current clone's
`origin` remote), `owner/repo#111`, a Jira-style key (`ABC-123`, matched to a Profile by
its key prefix — see [Configuration](#configuration)), or the issue's full URL.

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

*Jira and GitLab Profiles validate today but their drivers are not written yet: such a run
ends in "not supported yet".*

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
