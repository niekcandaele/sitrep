package model

import "time"

// Comment is one comment on a Ticket.
type Comment struct {
	// ID is the Tracker's identifier for the comment.
	ID string
	// Author wrote the comment.
	Author User
	// Body is raw markdown as the Tracker returned it.
	Body string
	// CreatedAt is when the comment was posted, in UTC.
	CreatedAt time.Time
	// URL points at the comment in its Tracker.
	URL string
}

// LinkKind is sitrep's normalized relationship type between two Tickets.
type LinkKind int

// The link kinds. LinkRelates is the zero value on purpose: a Tracker link type
// sitrep does not recognize falls back to Relates and is displayed through its
// NativeLabel instead of being dropped.
const (
	LinkRelates LinkKind = iota
	LinkBlockedBy
	LinkBlocks
)

var linkKindTokens = enumTokens[LinkKind]{
	{LinkRelates, "relates"},
	{LinkBlockedBy, "blocked_by"},
	{LinkBlocks, "blocks"},
}

// String returns the wire and display token for the link kind.
func (k LinkKind) String() string { return linkKindTokens.token(k, "link_kind") }

// MarshalJSON encodes the link kind as its wire token.
func (k LinkKind) MarshalJSON() ([]byte, error) { return linkKindTokens.marshal(k) }

// UnmarshalJSON decodes a link kind from its wire token.
func (k *LinkKind) UnmarshalJSON(b []byte) error { return linkKindTokens.unmarshal(b, k, "link kind") }

// LinkTarget is the far end of a Link: enough to display and navigate, no more.
type LinkTarget struct {
	// ID is the Provider's opaque handle for the linked Ticket.
	ID TicketID
	// Key is the linked Ticket's display identity, e.g. "#113".
	Key string
	// Title is the linked Ticket's one-line summary.
	Title string
	// URL points at the linked Ticket in its Tracker.
	URL string
	// Status is the linked Ticket's normalized lifecycle bucket.
	Status StatusCategory
	// NativeStatus is the Tracker's own label for the linked Ticket.
	// Display-only.
	NativeStatus string
}

// Link is a directed relationship from a Ticket to another Ticket.
type Link struct {
	// Kind is the normalized relationship type.
	Kind LinkKind
	// Target is the Ticket at the far end.
	Target LinkTarget
	// NativeLabel is the Tracker's own wording, e.g. "is blocked by" or "is
	// duplicated by". Always displayed, never interpreted.
	NativeLabel string
}

// Detail is the expensive per-ticket data, fetched only when a Ticket is opened
// (ADR-0003). Nothing here may migrate onto Ticket, however convenient it looks
// from a list view.
type Detail struct {
	// TicketID identifies the Ticket this Detail belongs to.
	TicketID TicketID
	// Description is raw markdown exactly as the Tracker returned it. sitrep
	// stores it unrendered so each renderer can decide what to do with it.
	Description string
	// Comments are the Ticket's comments, oldest first. Empty when the serving
	// Provider does not declare the Comments Capability.
	Comments []Comment
	// Links are the Ticket's relationships to other Tickets. BlockedBy and
	// Blocks links appear only when the Provider declares the BlockingLinks
	// Capability.
	Links []Link
}
