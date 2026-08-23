# Links are hyperlinks, not browser launches

Sitrep exposes Tracker URLs with OSC 8 terminal hyperlinks. The terminal owns whether and how those hyperlinks are activated; sitrep does not launch a browser, shell command, or URL opener. This preserves the read-only boundary and works without assuming a GUI or a particular terminal emulator.

Only explicit, capability-visible Links returned in a Ticket's Detail can extend the in-app Trail. Following one by keyboard or mouse adds the current Ticket to the session-local Trail and opens the target by its Provider-scoped Ticket identity, fetching its Detail only on a cache miss. Esc pops that Trail; list-open and `u` move between the Watchlist and Detail without extending it. The same URL may also be emitted as OSC 8, but terminal activation is not an in-app Trail event.

OSC 8 hyperlinks emitted outside explicit Link rows—Ticket keys, header URLs, Trail breadcrumbs, and lead pull-request summaries—remain terminal hyperlinks only. They are never inferred as Ticket relationships and never create Trail entries. Arbitrary body fields outside the scoped description/comment renderer remain plain text.

## Amendment 1: Markdown in TUI Detail bodies

The TUI renders Ticket descriptions and comments as GitHub Flavored Markdown. Standard Markdown URLs and same-host GitHub issue references and mentions in those fields may therefore become OSC 8 terminal hyperlinks. The terminal still owns activation, and these hyperlinks never become explicit Link rows or extend the in-app Trail. Only capability-visible Links returned in Detail can drive in-app navigation. Plain and JSON output preserve the raw description and comment bodies.
