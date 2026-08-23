# Links are hyperlinks, not browser launches

Sitrep exposes Tracker URLs with OSC 8 terminal hyperlinks. The terminal owns whether and how those hyperlinks are activated; sitrep does not launch a browser, shell command, or URL opener. This preserves the read-only boundary and works without assuming a GUI or a particular terminal emulator.

Only explicit, capability-visible Links returned in a Ticket's Detail can extend the in-app Trail. Following one by keyboard or mouse adds the current Ticket to the session-local Trail and opens the target by its Provider-scoped Ticket identity, fetching its Detail only on a cache miss. Esc pops that Trail; list-open and `u` move between the Watchlist and Detail without extending it. The same URL may also be emitted as OSC 8, but terminal activation is not an in-app Trail event.

OSC 8 hyperlinks emitted outside explicit Link rows—Ticket keys, header URLs, Trail breadcrumbs, and lead pull-request summaries—remain terminal hyperlinks only. They are never inferred as Ticket relationships and never create Trail entries. URLs in descriptions, comments, raw markdown, and arbitrary body text remain plain text in this decision. A future scoped renderer may emit them as terminal hyperlinks, but adding in-app body or markdown navigation would still require a separate decision rather than string-scanning rendered output.
