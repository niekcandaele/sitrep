# Releasing sitrep

Cutting a release is one command. Everything else on this page is what to check before it and
what to verify after it.

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

Pushing a `v*` tag runs the `Release` workflow, which runs goreleaser. It builds four static
archives — Linux and Darwin, amd64 and arm64 — plus `checksums.txt`, and publishes them as a
GitHub Release with a changelog generated from commits since the previous tag. `CGO_ENABLED=0`
and `-trimpath` make the binaries droppable onto a headless box. Version, short commit, and
build date are linked in, so `sitrep --version` on a published binary reports the tag.

CI proves that pipeline on every pull request: `goreleaser check` validates the config, a
snapshot build produces the four archives, and the Linux/amd64 binary is run with `--version`
and `--help` and checked for static linking.

## After the tag: verify the published binary

On a headless Linux box with no Go toolchain:

```sh
v=0.1.0
curl -fsSLO "https://github.com/niekcandaele/sitrep/releases/download/v$v/sitrep_${v}_linux_amd64.tar.gz"
curl -fsSLO "https://github.com/niekcandaele/sitrep/releases/download/v$v/checksums.txt"
sha256sum --check --ignore-missing checksums.txt
tar -xzf "sitrep_${v}_linux_amd64.tar.gz" sitrep
./sitrep --version
```

For `v0.1.0`, `--version` must print
`sitrep 0.1.0 (commit <short sha>, built <timestamp>)`; `dev` means link-time values did not
reach the binary. That tagged release predates generalized Watchlists, so its smoke test uses
a real Epic Ref and expects schema v1:

```sh
./sitrep <a real Epic Ref> --plain
./sitrep <a real Epic Ref> --json | jq -e '.schema_version == 1'
./sitrep <a real Epic Ref>          # open the monitor once, then q
```

For a future release containing the Watchlist source-tree behavior, exercise representative
real Selectors rather than only an Epic. Use Refs and Queries appropriate to the release's
Tracker/Profile:

```sh
./sitrep <ref> <ref> --plain
./sitrep <ref> <ref> --json |
  jq -e '.schema_version == 3 and .watchlist.selector.kind == "ref_list"'
printf '%s\n' <ref> <ref> | ./sitrep --plain -
./sitrep --profile <profile> --query '<native query>' --json |
  jq -e '.schema_version == 3 and .watchlist.selector.kind == "query"'
./sitrep <ref> <ref>              # open the Watchlist monitor once, then q
```

`--links` costs one Detail fetch per Ticket, so smoke it against a small Watchlist whose
blocking Links you can eyeball. Every Ticket must carry the three booleans, and a Provider
without the `blocking_links` Capability must instead emit none of them and exit `0`:

```sh
./sitrep <ref> <ref> --json --links |
  jq -e '.blocking.cycles != null and ([.tickets[] | has("actionable")] | all)'
```

Also test one Ref known to decode directly to a plain Ticket; its unchanged Ticket/Detail
JSON remains schema v1:

```sh
./sitrep <a plain Ticket Ref> --json |
  jq -e '.schema_version == 1 and has("ticket") and has("ticket_id")'
```

## Upgrade notes to carry into the release notes

The generated changelog lists commits; a config change a user has to make by hand needs its
own sentence in the GitHub Release body.

**v0.3 — GitLab Profile paths declare their scope.** A Profile's `project:` is a project
path unless it is written `groups/<path>`. A Profile that names a group must be rewritten,
or hostless `&N` Refs against it fail:

```yaml
# before
project: acme/platform
# after
project: groups/acme/platform
```

sitrep cannot detect this when the config is loaded — `acme/platform` is a valid project
path — so the failure surfaces at the first `&N` Ref, with a message naming the fix.

## Versioning

Tags are `vMAJOR.MINOR.PATCH`. The JSON `schema_version` is independent of the tag and of
other document families. Watchlist documents are schema v3 in the unreleased source tree;
decoded Ticket/Detail documents remain schema v1. Additive optional fields keep a document's
schema version, while a breaking change to that document's shape or tokens increments it.

Watchlist v3 is the documented exception to that additive rule: `--links` makes the
Watchlist field set invocation-dependent, so the version has to say which fields the binary
can emit at all rather than which ones this run did. It is `3` on every Watchlist document,
`--links` or not — a version that flipped with a flag would describe the invocation instead
of the schema.

sitrep is pre-1.0. The CLI surface may still move between minor versions; the JSON document
schemas carry the compatibility promise.
