package model

import "time"

// EpicSnapshot is one batched read of an Epic and its Tickets: the result of a
// single Provider.FetchEpic call and the unit the TUI re-fetches on every
// refresh.
type EpicSnapshot struct {
	// Epic is the parent being monitored.
	Epic Epic
	// Tickets are the Epic's children in the Provider's own stable order.
	// Grouping is a rendering concern; the model never reorders.
	Tickets []Ticket
	// Capabilities are copied from the serving Provider so a snapshot carries
	// everything a renderer needs to decide what to show.
	Capabilities Capabilities
	// FetchedAt is stamped by the caller from its clock, not by the Provider: a
	// Provider must leave it zero. One authoritative timestamp per snapshot is
	// what makes golden output deterministic and the TUI's "updated Ns ago"
	// indicator honest.
	FetchedAt time.Time
}
