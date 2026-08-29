# Watchlist, not Epic, is the product boundary

sitrep is a ticket viewer. The subject shown by its list screen and one-shot reports is a
Watchlist, and a Selector says how to choose that Watchlist. An Epic remains a real parent
Ticket and a first-class Selector; users do not have to reshape unrelated work into an Epic
before they can watch it.

The accepted Selector forms are one Epic Ref, an exact Ref list supplied as positional
arguments or transported through stdin, and an opaque tracker-native Query. One Watchlist is
served by one Provider, host, connection, credential scope, capability set, and refresh
clock, with at most one selected Profile. Every Watchlist-producing resolution feeds the
same generic Header, thin Ticket list, renderers, and TUI.

A refresh resolves the same Selector again. Query membership is bounded by effective
`max_tickets`: the selected Profile's setting, or 100 when no Profile is selected or the key
is omitted. Each selected identity is reread through the Provider's authoritative exact-Ticket
path before rendering. Exact Ref lists preserve their first spelling and order and are not
capped by the Query budget.

The machine contract reflects that boundary: Watchlist documents use schema v2 and record
an `epic`, `ref_list`, or `query` Selector. Only the Epic variant carries a Watchlist Epic.
The existing single-Ref decoder remains compatible: when that Ref names a plain Ticket,
sitrep opens Detail and emits the unchanged schema-v1 Ticket document in one-shot JSON.

We rejected requiring every user to create an Epic, persistent named Watchlists, a
sitrep-native query language, deriving membership from pull requests, treating Epic as Query
sugar, mixed-Tracker Watchlists, and lazy scroll paging. Those alternatives either impose
Tracker writes, duplicate native query languages, weaken credential and capability
boundaries, or expose partial membership that changes with viewport position.
