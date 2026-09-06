# GitHub pull requests correlate through closing links or matching head branches

A GitHub PR belongs to a Ticket when GitHub returns it in
`closedByPullRequestsReferences`, or when a PR-sourced cross-reference timeline
candidate names the Ticket in its head branch. Prose-only mentions are dropped
entirely: they neither appear in the PR list nor promote a Ticket to In Progress.

GitHub interprets closing keywords only for PRs targeting the repository’s default
branch. Closing-references-only correlation therefore loses work targeting `epic/*`
branches. Timeline mentions recover those candidates, but also include incidental
references to sibling tickets. `willCloseTarget` cannot distinguish implementation
work from prose when both target non-default branches. A display-only tier would
retain that noise and add model and renderer complexity without identifying work.

The GitLab driver provides the precedent: `closed_by` decides membership, while
`related_merge_requests` only supplies pipeline data. A mention alone must not
promote a Ticket. GitHub applies the same principle with a branch signal because
its authoritative closing links are unavailable for non-default bases.

The GitHub-local matcher compares every maximal ASCII digit run in `headRefName`
with the Ticket’s decimal number using string equality. Leading zeros and numeric
substrings do not match. Runs immediately preceded by `.`, `v`, or `V`, or followed
by `.`, are skipped as version-like. Numbers may appear anywhere, and a branch may
name multiple Tickets. Prefix-only and first-number-only rules reject existing
branch conventions.

Ordinal suffixes such as `remediation-2` can still collide with low Ticket numbers.
This is an accepted limitation; an ordinal-prefix denylist would be brittle.
A matching branch is considered only among fetched timeline candidates, so a
branch name alone does not discover a PR without a cross-reference event.

Both connections remain bounded at twenty, with no pagination. Closing candidates
are kept unconditionally, then matching timeline candidates are deduplicated by
repository and PR number, preserving first occurrence and existing lead selection.
`pull_request_total` remains the larger of the closing count and retained union
size: an honest lower bound when closing references are truncated. Timeline totals
include unrelated events and cannot provide the union’s true size.
