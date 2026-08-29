# Terminal text is sanitized where it enters a screen, not where it is rendered

Tracker-controlled text reaches a terminal through sitrep, and a title of
`hi\x1b[2J` clears the reader's screen while a comment body carrying OSC 52
writes their clipboard. On a public tracker anyone who can comment can write
one. The policy for that text — normalize malformed UTF-8, remove C0 (including
ESC), DEL and C1 — lives in `internal/termtext` and nowhere else. `Line` folds a
tab or a newline to a space so a measured field keeps its width; `Body` keeps
newlines and tabs because layout is the content of a description or a comment.

That policy is applied at two enforcement points, and only two:

- `provider.Sanitized`, applied once in `internal/cli`, cleans everything a
  Provider returns. It protects `--plain`, `--json` and the decoded one-shot
  path, which are terminal and agent sinks of their own.
- `internal/tui`'s intake cleans everything that enters `Model` state, whoever
  built it. The monitor takes model data through four funnels — `Options.Initial`,
  `Options.Open`, `Options.Source` and `Options.DetailSource` — and a `Source` is
  any closure at all, so a screen is safe regardless of whether its caller
  wrapped a Provider.

A model value becomes safe to draw at the moment it enters a screen's state, not
at the moment it is rendered, and not because of who produced it. Both points
call the same walkers, which take whole model values rather than hand-picked
fields, so one policy has one implementation across two sinks.

Identity is outside the boundary. `model.TicketID` is never drawn — **a screen
shows a `Key`, never an `ID`** — and cleaning it would corrupt the Detail cache
key and the Provider re-reads that use it. The exemption is one explicit
allowlist in `TestWalkersCleanEveryModelField`, which fills every string field of
every walked type by reflection and fails until a new field is given a policy.
A new screen inherits the boundary by consuming a `ListInput` or a `DetailInput`,
and asserts its own inputs with `termtexttest.AssertClean`.

We rejected sanitizing per field at render sites. That is what the TUI did
before this decision, opportunistically, in five places — and the row title, the
Native Status tag, the assignee logins, the pull-request summary and a `Source`
error's text were all drawn raw. Every renderer added afterwards is one more
place to remember, which is the same argument `provider.Sanitized` already made
against sanitizing in the one-shot renderers.

We accept one asymmetry: `internal/render/plain` and `internal/render/jsonout`
have no intake of their own and rely on their only production caller sanitizing
at the Provider seam. Giving them one is a follow-up, not a defect of this
decision.

A future rule about what text may reach a terminal — balancing unterminated
bidirectional overrides, say — lands in `termtext.Line` and `termtext.Body`, and
nowhere else.
