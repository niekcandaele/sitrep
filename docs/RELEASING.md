# Releasing sitrep

Cutting a release is one command. Everything else on this page is what to check before it
and what to verify after it.

## Preconditions

- You are on `main`, up to date with `origin/main`, with a clean working tree.
- `make check` is green locally.
- CI is green on the commit you are about to tag.

## The command

```sh
git tag -a v0.1.0 -m "sitrep v0.1.0" && git push origin v0.1.0
```

That is the whole release. Nothing else is run by hand, and no secret beyond the default
`GITHUB_TOKEN` is involved.

## What happens next

Pushing a `v*` tag runs the `Release` workflow, which runs goreleaser. It builds four
static archives — linux and darwin, amd64 and arm64 — plus `checksums.txt`, and publishes
them as a GitHub Release with a changelog generated from the commits since the previous
tag. `CGO_ENABLED=0` and `-trimpath` are what make the binaries droppable onto a headless
box; the version, the short commit and the build date are linked in, so `sitrep --version`
on a published binary reports the tag.

CI already proves that pipeline on every pull request: `goreleaser check` validates the
config, a snapshot build produces the same four archives, and the linux/amd64 binary from
that build is run — `--version`, `--help`, and an assertion that it is statically linked.

## After the tag: verify the published binary

This is the half a machine cannot do. On a headless Linux box, with no Go toolchain:

```sh
v=0.1.0
curl -fsSLO "https://github.com/niekcandaele/sitrep/releases/download/v$v/sitrep_${v}_linux_amd64.tar.gz"
curl -fsSLO "https://github.com/niekcandaele/sitrep/releases/download/v$v/checksums.txt"
sha256sum --check --ignore-missing checksums.txt
tar -xzf "sitrep_${v}_linux_amd64.tar.gz" sitrep
./sitrep --version
```

`--version` must print `sitrep 0.1.0 (commit <short sha>, built <timestamp>)` — a `dev`
there means the link-time values did not reach the binary. Then, against a real epic on a
tracker you have access to:

```sh
./sitrep <a real epic ref> --plain
./sitrep <a real epic ref> --json | jq .schema_version
./sitrep <a real epic ref>          # open the monitor once, then q
```

## Versioning

Tags are `vMAJOR.MINOR.PATCH`. The `--json` `schema_version` is independent of the tag: it
stays `1` for additive changes and is bumped only by a breaking change to the wire format.

sitrep is pre-1.0. The CLI surface may still move between minor versions; the `--json`
schema is the part that carries a compatibility promise.
