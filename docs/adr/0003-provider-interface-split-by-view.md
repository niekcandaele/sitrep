# Provider interface split by view, not by entity

Each Provider exposes two calls. `Resolve(ctx, Selector)` returns a thin reading of a
Watchlist for the list renderers, and `FetchDetail` returns the rich data for one Ticket only
when that Ticket is opened. A Provider may make several bounded Tracker requests inside one
logical `Resolve`; keeping descriptions, comments, and links out of that hot path avoids
multiplying rate-limit pressure on every refresh. The rejected alternative — one rich Ticket
entity carrying everything — would bloat the interface and make list polling proportional to
Detail size. Do not "fix" the thin Ticket by adding detail fields to it; add them to Detail.

## Amendment 1: Watchlist Selector seam

A closed Selector names the Watchlist; it does not imply that the Watchlist came from an
Epic. One `Resolve` remains one logical read per preflight or refresh, although a Provider
may need multiple Tracker requests within that call. The returned generic Header and thin
Tickets feed every list renderer. `FetchDetail` remains the only lazy per-Ticket Detail call.

## Amendment 2: Query membership and authoritative state

A Query Selector adds a membership stage without weakening the thin-list boundary. On every
`Resolve`, the Provider first sends the opaque tracker-native Query to search for current
member identities. Only those stable identities cross the stage boundary. The Provider then
uses its existing exact-root path to read each member's current thin Ticket state
authoritatively; search-result titles, statuses, and other list fields are never rendered.
Both stages rerun on every refresh, so membership may appear or disappear while current
member state still comes from direct reads. Opening a member continues to be the only
operation that calls `FetchDetail`.

## Amendment 3: bounded Query membership

Query membership may make multiple blocking page requests inside one logical `Resolve`. It
restarts from the first page on every refresh and stops at cursor exhaustion or the effective
`max_tickets` budget: the selected Profile's value, or 100 when no Profile is selected or the
key is omitted. Where the Tracker API permits, the Provider fetches one item of lookahead, so
reaching an exhausted exact boundary is not mislabeled as truncation. A successful cutoff
sets `WatchlistSnapshot.LimitReached`; it does not claim a known total. This budget applies
only to Tracker-discovered Query membership, never to an Epic's children or an exact Ref
list.

The Provider completes authoritative exact-root reads for the selected identities before any
snapshot reaches rendering. Internal paging exposes neither partial search state nor a new
Provider method.

## Amendment 4: an explicit bulk Detail fan-out

One screen needs Links for every Ticket at once. The Frontier answers "which Tickets can be
picked up right now?", `Actionable` is computed from BlockedBy Links, and Links live in
Detail. The split forbids the list refresh from paying for that, and it still does.

A caller may fan `FetchDetail` out across a whole Watchlist **only in response to an explicit
user action**, never from a refresh, a poll, or a render. The cost is visible and voluntary:
you pressed a key, you get a progress indicator, and you can interrupt it. Results land in the
caller's existing per-Ticket Detail cache, so a Ticket already opened this session costs
nothing. The policy — canonical order, cache skipping, bounded concurrency, and the rule that
only a successful fetch is recorded — lives in `internal/detailfanout` so that every consumer
pays the same price and fails the same way. A screen may also *read* the cache a fan-out
filled to render a derived property, provided it issues no fetch of its own and claims nothing
when the cache does not cover the whole Watchlist.

The rejected alternative is promoting Links onto the thin `Ticket`, which makes every refresh
slower forever to serve a screen that may never be opened. Do not "fix" the fan-out that way;
it is the thing this ADR exists to prevent.
