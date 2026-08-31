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
	// Assignees are the people the Epic is assigned to; may be empty. It exists
	// for the Detail header a decoded Ticket is drawn with — a Ref that
	// named a Ticket comes back as this Epic — and the epic renderers
	// deliberately do not draw it.
	Assignees []User
	// Repository is the display origin, "owner/repo" on GitHub. May be empty.
	// Like Assignees it exists for the decoded Detail header only.
	Repository string
	// PullRequests is populated only when the serving Provider declares the
	// PullRequests Capability; nil otherwise. Lead pull request first. Like
	// Assignees it exists for the decoded Detail header only.
	PullRequests []PullRequest
	// PullRequestTotal is how many pull requests the Tracker says this node
	// has, which can exceed len(PullRequests) when the Provider capped what it
	// fetched. Zero means the serving Provider cannot supply a total, which is
	// a normal state and never an error. Like PullRequests it is meaningful
	// only when the Provider declares the PullRequests Capability.
	PullRequestTotal int
}

// Parent is the parent Ticket a Ticket belongs to: enough to draw a breadcrumb
// and to re-enter sitrep on. Its Key and URL are both written in forms the Ref
// grammar accepts, so navigating up is a re-parse rather than a second lookup.
// The zero value means "no parent", which is a normal state, never an error.
type Parent struct {
	// ID is the Provider's opaque handle for the parent.
	ID TicketID
	// Key is the parent's display identity, e.g. "#111" or "acme/widgets#111".
	Key string
	// Title is the parent's one-line summary.
	Title string
	// URL points at the parent in its Tracker.
	URL string
}

// IsZero reports whether there is no parent at all. It is the one question
// every call site asks — "is there a breadcrumb?" — so it is asked one way.
func (p Parent) IsZero() bool { return p == Parent{} }

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
	// PullRequestTotal is how many pull requests the Tracker says this Ticket
	// has, which can exceed len(PullRequests) when the Provider capped what it
	// fetched. Zero means the serving Provider cannot supply a total, which is
	// a normal state and never an error. Like PullRequests it is meaningful
	// only when the Provider declares the PullRequests Capability.
	PullRequestTotal int
}

// SelectorCapabilities declares which Watchlist subjects a Provider can
// resolve. Unlike an absent feature Capability, which remains silently absent
// from rendering, an absent Selector Capability is a loud error: the requested
// Selector is the command's entire subject.
type SelectorCapabilities struct {
	Epic    bool
	RefList bool
	Query   bool
}

// Capabilities declares what the serving Provider supports. Renderers and the
// TUI silently omit undeclared feature data; Selector support is checked by the
// Provider before it performs any work.
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
	// RateLimitBudget reports whether Resolve can observe a request budget.
	RateLimitBudget bool
	// Selectors declares the Watchlist subjects this Provider accepts.
	Selectors SelectorCapabilities
}
