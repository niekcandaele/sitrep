package model

import "time"

// WatchlistHeader is the display identity of a resolved Watchlist. Epic
// Watchlists copy their Epic identity here; other Selector kinds can supply a
// title without pretending to have an outer Epic.
type WatchlistHeader struct {
	Key   string
	Title string
	URL   string
}

// WatchlistSnapshot is one batched reading of a Watchlist produced by
// Provider.Resolve and re-read by the TUI on every refresh.
//
// Resolve answers what a Selector points at. A Ref that turns out to name a
// plain Ticket comes back as a snapshot with no Tickets, whose Epic field
// carries that Ticket's own identity and whose Parent carries its parent Ticket.
// Deciding what to do about that is the caller's job (internal/cli), not the
// Provider's: a Provider reports what the Tracker says and never picks a screen.
type WatchlistSnapshot struct {
	// Header identifies the Watchlist for every downstream renderer.
	Header WatchlistHeader
	// Epic is the outer root for an Epic Selector. It is zero for a Ref list.
	Epic Epic
	// Tickets are either the Epic's children or the exact Ref-list members, in the
	// Provider's stable Selector order. Grouping is a rendering concern; the model
	// never reorders.
	Tickets []Ticket
	// LimitReached is true only for a successful Query Resolve whose configured
	// membership budget was consumed while the Tracker indicated another ordered
	// result. It says nothing about the unknown total and remains false for every
	// non-Query Selector.
	LimitReached bool
	// Parent is the parent Ticket of the fetched node, when the Tracker exposes
	// one and declares the Hierarchy Capability. It is what a Ref
	// naming a plain Ticket is decoded through: the breadcrumb above that
	// Ticket's Detail and the Epic the monitor walks up into. Zero means "no
	// parent", which is a normal state.
	Parent Parent
	// Capabilities are copied from the serving Provider so a snapshot carries
	// everything a renderer needs to decide what to show.
	Capabilities Capabilities
	// FetchedAt is stamped by the caller from its clock, not by the Provider: a
	// Provider must leave it zero. One authoritative timestamp per snapshot is
	// what makes golden output deterministic and the TUI's "updated Ns ago"
	// indicator honest.
	FetchedAt time.Time
}
