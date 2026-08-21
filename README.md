# sitrep

A read-only terminal situation report on the work you delegated: one screen showing an
Epic's Tickets, their status, and the code moving them — across GitHub, Jira, and GitLab.
Agents write; sitrep watches. It never writes to a Tracker ([ADR-0002](docs/adr/0002-read-only-by-design.md)).

sitrep is early: today the binary reports its own version and usage. The Providers, the
Epic view, and the TUI land in later work.

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

Tests assert observable behaviour at seams — exit codes and terminal output — using the
standard library only; there is no assertion dependency and `go.mod` carries no runtime
dependencies.
