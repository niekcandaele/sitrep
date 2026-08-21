// Package model holds sitrep's normalized domain: the Epic, its Tickets, the
// Status Categories they fall into, and the expensive per-ticket Detail that is
// fetched only on drill-in.
//
// The package is pure data and pure functions. It knows nothing about any
// Tracker, performs no I/O, and imports nothing outside the standard library.
// Providers translate a Tracker's vocabulary into these types; renderers
// translate these types into bytes.
//
// Per ADR-0003 the model is split by view: Ticket is the lightweight list model
// re-fetched on every refresh, and Detail is the expensive half. Description,
// comments and links belong on Detail and must never migrate onto Ticket.
package model

// TicketID is a Provider-scoped opaque identifier for a Ticket. Its format is
// the Provider's business (a GitHub node ID, a Jira issue key, ...); nothing
// outside a Provider may parse it.
type TicketID string

// User is a person referenced by a Ticket, normalized across Trackers.
type User struct {
	// Login is the Tracker handle, e.g. "niekcandaele".
	Login string
	// DisplayName is the human name if the Tracker exposes one; may be empty.
	DisplayName string
	// AvatarURL is optional; may be empty.
	AvatarURL string
}

// Epic is the normalized parent ticket whose children sitrep monitors.
type Epic struct {
	// ID is the Provider's opaque handle for this Epic.
	ID TicketID
	// Key is the display identity a human types or reads, e.g. "#111".
	Key string
	// Title is the Epic's one-line summary.
	Title string
	// URL points at the Epic in its Tracker.
	URL string
	// Status is the normalized lifecycle bucket, the only field grouping,
	// filtering and progress math may read.
	Status StatusCategory
	// NativeStatus is the Tracker's own label. Display-only: nothing in this
	// package may branch on it.
	NativeStatus string
}

// Ticket is the lightweight list-model view of one work item. Per ADR-0003 it
// must never carry description, comments or links: those live in Detail.
type Ticket struct {
	// ID is the Provider's opaque handle for this Ticket.
	ID TicketID
	// Key is the display identity a human types or reads, e.g. "#112".
	Key string
	// Title is the Ticket's one-line summary.
	Title string
	// URL points at the Ticket in its Tracker.
	URL string
	// Status is the normalized lifecycle bucket, the only field grouping,
	// filtering and progress math may read.
	Status StatusCategory
	// NativeStatus is the Tracker's own label, e.g. "In Review". Display-only:
	// nothing in this package may branch on it.
	NativeStatus string
	// Assignees are the people the Ticket is assigned to; may be empty.
	Assignees []User
	// ParentID is the Ticket's parent within the Epic's hierarchy. Empty when
	// the Ticket hangs directly off the Epic.
	ParentID TicketID
	// Repository is the display origin for cross-repo children: "owner/repo" on
	// GitHub, the project on Jira. May be empty.
	Repository string
	// PullRequests is populated only when the serving Provider declares the
	// PullRequests Capability; nil otherwise. Providers list the lead pull
	// request — the one that best represents the Ticket's current state —
	// first, so a renderer showing a single pull request per row can take the
	// first element.
	PullRequests []PullRequest
}

// Capabilities declares which optional Tracker features the serving Provider
// supports. Renderers and the TUI show only what is declared; an undeclared
// feature is silently absent, never an error.
type Capabilities struct {
	// Hierarchy reports whether the Tracker exposes sub-tickets / parent links.
	Hierarchy bool
	// BlockingLinks reports whether the Tracker exposes BlockedBy / Blocks
	// relationships.
	BlockingLinks bool
	// Comments reports whether the Tracker exposes ticket comments in Detail.
	Comments bool
	// PullRequests reports whether the Provider correlates pull or merge
	// requests to Tickets.
	PullRequests bool
}
