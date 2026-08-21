package model

import "math"

// Progress is the epic-level completion arithmetic. Cancelled Tickets count in
// Total but are excluded from Denominator, so an epic reads as "7/11 done, 1
// cancelled" rather than being permanently short of 100%.
type Progress struct {
	// Todo counts Tickets in StatusTodo.
	Todo int
	// InProgress counts Tickets in StatusInProgress.
	InProgress int
	// Done counts Tickets in StatusDone.
	Done int
	// Cancelled counts Tickets in StatusCancelled.
	Cancelled int
	// Unknown counts Tickets whose Status a Provider failed to map.
	Unknown int
	// Total counts every Ticket, including cancelled ones.
	Total int
	// Denominator is Total minus Cancelled: the Tickets that can still be
	// finished.
	Denominator int
	// PercentDone is Done over Denominator as a percentage, 0-100, rounded to
	// nearest. It is 0 when Denominator is 0.
	PercentDone int
}

// ComputeProgress counts Tickets by Status Category and derives the epic's
// completion. It reads only Ticket.Status: a Ticket's Native Status can say
// anything without changing the arithmetic.
func ComputeProgress(tickets []Ticket) Progress {
	var p Progress
	for _, t := range tickets {
		switch t.Status {
		case StatusTodo:
			p.Todo++
		case StatusInProgress:
			p.InProgress++
		case StatusDone:
			p.Done++
		case StatusCancelled:
			p.Cancelled++
		default:
			p.Unknown++
		}
	}
	p.Total = len(tickets)
	p.Denominator = p.Total - p.Cancelled
	if p.Denominator > 0 {
		p.PercentDone = int(math.Round(float64(p.Done) / float64(p.Denominator) * 100))
	}
	return p
}

// Group is one Status Category bucket plus its Tickets.
type Group struct {
	// Category is the bucket's Status Category.
	Category StatusCategory
	// Tickets are the bucket's Tickets in their original input order.
	Tickets []Ticket
}

// GroupByCategory buckets Tickets by Status Category in the order
// AllStatusCategories defines. Empty categories are omitted and Ticket order
// within a group is preserved. Like ComputeProgress it reads only
// Ticket.Status.
func GroupByCategory(tickets []Ticket) []Group {
	buckets := make(map[StatusCategory][]Ticket, len(displayOrder))
	for _, t := range tickets {
		c := t.Status
		if !statusTokens.has(c) {
			c = StatusUnknown
		}
		buckets[c] = append(buckets[c], t)
	}

	groups := make([]Group, 0, len(displayOrder))
	for _, c := range displayOrder {
		if len(buckets[c]) == 0 {
			continue
		}
		groups = append(groups, Group{Category: c, Tickets: buckets[c]})
	}
	return groups
}
