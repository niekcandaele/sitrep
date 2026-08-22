package model

import "time"

// WatchlistSnapshot is one batched reading of a Watchlist produced by
// Provider.Resolve and re-read by the TUI on every refresh.
//
// Resolve answers what a Selector points at. A Ref that turns out to name a
// plain Ticket comes back as a snapshot with no Tickets, whose Epic field
// carries that Ticket's own identity and whose Parent carries its parent Ticket.
// Deciding what to do about that is the caller's job (internal/cli), not the
// Provider's: a Provider reports what the Tracker says and never picks a screen.
type WatchlistSnapshot struct {
	// Epic is the parent being monitored.
	Epic Epic
	// Tickets are the Epic's children in the Provider's own stable order.
	// Grouping is a rendering concern; the model never reorders.
	Tickets []Ticket
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
