package tui

import (
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
)

// Header is the block above the Ticket list: what this Watchlist is, and how
// far along it is.
type Header struct {
	// Key is the Watchlist's display identity, e.g. "#111". May be empty.
	Key string
	// Title is the Watchlist's one-line summary.
	Title string
	// URL points at the Watchlist in its Tracker. May be empty.
	URL string
}

// ListInput is everything the monitor's list screen renders: a Watchlist of
// Tickets plus a Header, the Capabilities that decide what may be shown, and
// the moment the Watchlist was read.
//
// Progress is deliberately absent: it is a function of the Tickets, and
// storing it alongside them invites the two to drift.
type ListInput struct {
	// Header identifies the Watchlist being shown.
	Header Header
	// Tickets are the Watchlist's members in the Provider's own order.
	Tickets []model.Ticket
	// Capabilities decide what a row may show; an undeclared Capability is
	// silently absent, never an error.
	Capabilities model.Capabilities
	// LimitReached reports that Query membership consumed its configured budget.
	LimitReached bool
	// FetchedAt is when this reading was taken, from the caller's clock.
	FetchedAt time.Time
	// RateLimitBudget is the optional request budget observed for this reading.
	RateLimitBudget model.RateLimitBudget
}

// ListFromWatchlistSnapshot adapts one Watchlist snapshot to the list-view
// contract. The Model and frame renderer see only a Header and Tickets.
func ListFromWatchlistSnapshot(s model.WatchlistSnapshot) ListInput {
	return ListInput{
		Header: Header{
			Key:   s.Header.Key,
			Title: s.Header.Title,
			URL:   s.Header.URL,
		},
		Tickets:         s.Tickets,
		Capabilities:    s.Capabilities,
		LimitReached:    s.LimitReached,
		FetchedAt:       s.FetchedAt,
		RateLimitBudget: s.RateLimitBudget,
	}
}

// RowKind distinguishes the two things that occupy a line in the list.
type RowKind int

const (
	// RowGroupHeader is a Status Category heading with its count.
	RowGroupHeader RowKind = iota
	// RowTicket is one Ticket.
	RowTicket
)

// Row is one selectable-or-not line of the list.
type Row struct {
	// Kind says whether this row is a heading or a Ticket.
	Kind RowKind
	// Category is the row's Status Category, set on both kinds.
	Category model.StatusCategory
	// Count is the group's Ticket count, set on RowGroupHeader.
	Count int
	// Ticket is the row's Ticket, set on RowTicket.
	Ticket model.Ticket
}

// Selectable reports whether the cursor may rest on this row. Group headings
// are not: moving down off the last Ticket of a group lands on the first
// Ticket of the next.
func (r Row) Selectable() bool { return r.Kind == RowTicket }

// BuildRows groups Tickets by Status Category and flattens them into the rows
// the list renders: a heading per non-empty category followed by its Tickets in
// Provider order. Grouping is model.GroupByCategory's job — it already returns
// display order and omits empty categories — so this function only flattens
// and never sorts, regroups or reorders.
func BuildRows(tickets []model.Ticket) []Row {
	groups := model.GroupByCategory(tickets)

	rows := make([]Row, 0, len(tickets)+len(groups))
	for _, g := range groups {
		rows = append(rows, Row{
			Kind:     RowGroupHeader,
			Category: g.Category,
			Count:    len(g.Tickets),
		})
		for _, t := range g.Tickets {
			rows = append(rows, Row{
				Kind:     RowTicket,
				Category: g.Category,
				Ticket:   t,
			})
		}
	}
	return rows
}
