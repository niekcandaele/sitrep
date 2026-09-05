package tui

import (
	"time"

	"github.com/niekcandaele/sitrep/internal/detailfanout"
	"github.com/niekcandaele/sitrep/internal/model"
)

// listMarkers is what the list knows about Actionability for the frame being
// drawn. Its zero value is the honest default: nothing is known, so nothing is
// claimed. Ticket rows reserve the marker gutter independently of this state.
//
// The list never fetches to fill this in. ADR-0003 keeps one batched Resolve
// per refresh and Amendment 4 permits a Detail fan-out only in response to an
// explicit user action, so the markers read the cache that fan-out already
// filled and claim nothing when it does not cover the whole Watchlist.
type listMarkers struct {
	active     bool
	actionable map[model.TicketID]bool
	count      int
}

// has reports whether one Ticket earns the Actionable marker in this frame.
func (l listMarkers) has(id model.TicketID) bool {
	return l.active && l.actionable[id]
}

// actionableMarkers derives the frame's markers from one reading and the Links
// the session happens to hold. It is pure: same Watchlist, same Links, same
// Capabilities, same answer. It issues no fetch and reads no clock.
//
// Warmth is all-or-nothing. A member whose Detail was never read — or whose
// read failed, which is the same absence — leaves the whole list cold, because
// one unreadable Ticket can block anything and a partial answer is a wrong
// answer. Cached Details for non-members cannot create warmth: the plan is
// taken over the members alone.
func actionableMarkers(tickets []model.Ticket, links map[model.TicketID][]model.Link,
	caps model.Capabilities) listMarkers {
	if !caps.BlockingLinks || len(tickets) == 0 {
		return listMarkers{}
	}
	missing := detailfanout.Plan(tickets, func(id model.TicketID) bool {
		_, ok := links[id]
		return ok
	})
	if len(missing) > 0 {
		return listMarkers{}
	}

	graph := model.BuildBlockingGraph(tickets, links, caps)
	marked := make(map[model.TicketID]bool, len(tickets))
	for _, member := range graph.Members() {
		if member.Actionable {
			marked[member.TicketID] = true
		}
	}
	return listMarkers{active: true, actionable: marked, count: len(marked)}
}

// hasActionabilityMembers reports whether the current Watchlist has at least
// one canonical member whose Detail the Frontier can read. An empty Watchlist
// and rows without identity have no computation for expanded Help to promise.
func hasActionabilityMembers(tickets []model.Ticket) bool {
	return detailfanout.HasPlan(tickets, nil)
}

// actionableEvidenceSnapshot returns when the last required current member's
// eligible Links evidence landed. It accepts only the same all-or-nothing marker
// state and canonical member set; duplicate, empty, and non-member cache entries
// cannot move the anchor.
func actionableEvidenceSnapshot(tickets []model.Ticket, details map[model.TicketID]detailEntry,
	markers listMarkers) (time.Time, bool) {
	if !markers.active {
		return time.Time{}, false
	}
	ids := detailfanout.Plan(tickets, nil)
	if len(ids) == 0 {
		return time.Time{}, false
	}

	var latest time.Time
	for _, id := range ids {
		entry, cached := details[id]
		if !cached {
			return time.Time{}, false
		}
		fetchedAt, eligible := entry.frontierEvidenceStamp()
		if !eligible {
			return time.Time{}, false
		}
		if fetchedAt.After(latest) {
			latest = fetchedAt
		}
	}
	return latest, true
}

// listMarkers derives the frame's markers from the session's Detail cache.
// It is deliberately given the whole reading and never the filtered subset:
// hiding a row deletes an edge, and a deleted edge can make a blocked Ticket
// look Actionable (CONTEXT.md).
func (m Model) listMarkers() listMarkers {
	return actionableMarkers(m.input.Tickets, m.linksFromCache(), m.input.Capabilities)
}
