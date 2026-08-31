# `internal/render/plain` is the shared terminal-shaping layer

`internal/render/plain` has two responsibilities. `RenderWatchlist` and
`RenderTicket` are the `--plain` report entry points. Its other exported
functions are renderer-independent terminal-shaping vocabulary that renderers
may share. The TUI consumes applicable members of that vocabulary. The two
report entry points are not shared rendering APIs.

The shared vocabulary has three categories:

- text and layout shaping: `LimitNotice`, `ProgressBar`, `PadKey`, `Truncate`,
  `PullRequestSummary`, and `PullRequestOverflow`;
- measurement: `BarFill` and `KeyColumnWidth`;
- display policy: `CategoryLabel`, `ShowsNativeStatus`, and `StatusField`.

The dependency direction is one way. The TUI consumes applicable members of the
vocabulary, but `plain` must never depend on TUI. The vocabulary may depend on
model values and terminal-text utilities, but does not own screens, styles,
input, screen geometry, or the TUI lifecycle.

Shared display policy requires caller-frame context. `ShowsNativeStatus` is
valid only for a Ticket row beneath a Status Category heading, because that
heading supplies the information it may suppress. A heading-less Ticket frame
uses `StatusField`, which always gives the reader status context. Link-target
rows remain deliberately exempt: they have no category heading.

These rules do not belong in `internal/model`. They are terminal presentation
policy, including suppression based on Tracker spelling, rather than domain
state or a Provider or JSON contract.

ADR-0006 is the neighboring boundary. It owns terminal-text sanitation at sink
boundaries and rebalancing after a renderer cuts text; this decision owns reuse
and rendering-policy ownership. `plain` does not independently sanitize its
inputs.

A future terminal display rule belongs in `plain` when it is
renderer-independent model-to-terminal shaping or policy and its required frame
context can be documented. It must not be duplicated in TUI or moved to model.
A rename such as `internal/render/policy` is a separate future decision; this
ADR changes neither the package name nor runtime behavior.
