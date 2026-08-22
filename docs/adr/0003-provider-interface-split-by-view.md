# Provider interface split by view, not by entity

Each Provider exposes two calls: a cheap batched epic call returning the Epic plus lightweight Tickets (one request per refresh), and a lazy per-ticket Detail call (description, comments, links) made only when a ticket is opened. The rejected alternative — one rich Ticket entity carrying everything — would push comments and link data into the polled hot path, multiply rate-limit pressure (Jira Cloud especially), and bloat the interface every time a tracker feature lands. Do not "fix" the thin Ticket by adding detail fields to it; add them to Detail.

## Amendment 1: Watchlist Selector seam

The cheap batched call is now `Resolve(context.Context, Selector) (WatchlistSnapshot, error)`.
A closed Selector names the Watchlist; it does not imply that the Watchlist came from an
Epic. One `Resolve` remains one logical read per preflight or refresh, although a Provider
may need multiple Tracker requests within that call. The returned generic Header and thin
Tickets feed every list renderer. `FetchDetail` remains the only lazy per-Ticket Detail
call.

## Amendment 2: Query membership and authoritative state

A Query Selector adds a membership stage without weakening the thin-list boundary. On
every `Resolve`, the Provider first sends the opaque tracker-native Query to search for
current member identities. Only those stable identities cross the stage boundary. The
Provider then uses its existing exact-root path to read each member's current thin Ticket
state authoritatively; search-result titles, statuses, and other list fields are never
rendered. Both stages rerun on every refresh, so membership may appear or disappear while
current member state still comes from direct reads. Opening a member continues to be the
only operation that calls `FetchDetail`.

Query membership may make multiple blocking page requests inside that one logical
`Resolve`. It restarts from the first page on every refresh, stops at the Provider's
configured Ticket limit or cursor exhaustion, and completes the authoritative exact-root
stage before any snapshot reaches rendering. This internal paging exposes neither partial
search state nor a new Provider method.
