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
