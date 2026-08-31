package fake

import (
	"strconv"

	"github.com/niekcandaele/sitrep/internal/model"
)

// The Ghost Tickets the blocking fixture's Links reach: never Watchlist
// members, so they exist only as LinkTargets. The 400 series keeps them
// visibly apart from the 200-series members.
var (
	ghostRFC = model.LinkTarget{
		ID:           "acme/widgets#401",
		Key:          "#401",
		Title:        "Approve the rebalancing RFC",
		URL:          "https://tracker.example.test/acme/widgets/401",
		Status:       model.StatusTodo,
		NativeStatus: "open",
	}
	// A Provider that could not map the Tracker's status: the blocker is unmet
	// and unverified, which is what makes Actionable fail closed.
	ghostVendor = model.LinkTarget{
		ID:     "acme/widgets#402",
		Key:    "#402",
		Title:  "Vendor capacity sign-off",
		URL:    "https://tracker.example.test/acme/widgets/402",
		Status: model.StatusUnknown,
	}
	ghostOps = model.LinkTarget{
		ID:           "acme/widgets#403",
		Key:          "#403",
		Title:        "Ops capacity ticket",
		URL:          "https://tracker.example.test/acme/widgets/403",
		Status:       model.StatusDone,
		NativeStatus: "closed",
	}
)

func blockingTicket(number int, title string, status model.StatusCategory, native string) model.Ticket {
	key := "#" + strconv.Itoa(number)
	return model.Ticket{
		ID:           model.TicketID("acme/widgets" + key),
		Key:          key,
		Title:        title,
		URL:          "https://tracker.example.test/acme/widgets/" + strconv.Itoa(number),
		Status:       status,
		NativeStatus: native,
		Repository:   "acme/widgets",
	}
}

// blockingMembers is the fixture Watchlist's membership, in Selector order.
// Every row exists to pin one case of the Actionable computation, so keep them
// all: a clean Actionable Ticket, blockers that are members, Ghosts and
// unverified Ghosts, an InProgress Ticket that is also blocked, a two-Ticket
// cycle, a self-blocking Ticket, a Ticket whose Detail is missing, a Relates
// link that must not be an edge, and a finished Ghost that satisfies.
func blockingMembers() []model.Ticket {
	return []model.Ticket{
		blockingTicket(201, "Ship the rebalancer CLI", model.StatusTodo, "open"),
		blockingTicket(202, "Wire the rebalancer into the scheduler", model.StatusTodo, "open"),
		blockingTicket(203, "Backfill shard weights", model.StatusTodo, "open"),
		blockingTicket(204, "Drain a shard safely", model.StatusTodo, "open"),
		blockingTicket(205, "Compute shard weights", model.StatusDone, "closed"),
		blockingTicket(206, "Legacy weight table", model.StatusCancelled, "not planned"),
		blockingTicket(207, "Rewrite the placement heuristic", model.StatusInProgress, "In Review"),
		blockingTicket(208, "Split the rebalancer queue", model.StatusTodo, "open"),
		blockingTicket(209, "Merge the rebalancer queue", model.StatusTodo, "open"),
		blockingTicket(210, "Deduplicate shard moves", model.StatusTodo, "open"),
		blockingTicket(211, "Publish the rollout runbook", model.StatusTodo, "open"),
		blockingTicket(212, "Chart rebalancer latency", model.StatusTodo, "open"),
		blockingTicket(213, "Rotate rebalancer credentials", model.StatusTodo, "open"),
	}
}

// FixtureBlockingSnapshot returns a second, purpose-built Watchlist whose
// Tickets exercise the blocking graph: Actionable and blocked Tickets, Ghost
// Tickets inside and outside the Watchlist, an unverifiable blocker status, a
// Ticket whose Detail cannot be fetched, a two-Ticket cycle and a self-blocking
// Ticket.
//
// It exists beside FixtureSnapshot rather than inside it because every existing
// golden reads that fixture byte-for-byte.
//
// It is safe to mutate the returned value: each call builds a fresh copy.
func FixtureBlockingSnapshot() model.WatchlistSnapshot {
	return model.WatchlistSnapshot{
		Header: model.WatchlistHeader{
			Key:   "#200",
			Title: "Shard rebalancer rollout",
			URL:   "https://tracker.example.test/acme/widgets/200",
		},
		Epic: model.Epic{
			ID:           "acme/widgets#200",
			Key:          "#200",
			Title:        "Shard rebalancer rollout",
			URL:          "https://tracker.example.test/acme/widgets/200",
			Status:       model.StatusInProgress,
			NativeStatus: "open",
		},
		Capabilities:    allCapabilities,
		RateLimitBudget: fixtureRateLimitBudget(),
		Tickets:         blockingMembers(),
	}
}

// FixtureBlockingDetails returns the Details for the blocking Watchlist, keyed
// by TicketID. Ticket #211 is deliberately absent: FetchDetail then fails for
// it, which is the "this Ticket's Links could not be read" case a consumer has
// to hold open rather than treating as "no blockers".
//
// It is safe to mutate the returned map: each call builds a fresh copy.
func FixtureBlockingDetails() map[model.TicketID]model.Detail {
	blockedBy := func(t model.LinkTarget) model.Link {
		return model.Link{Kind: model.LinkBlockedBy, NativeLabel: "is blocked by", Target: t}
	}
	blocksLink := func(t model.LinkTarget) model.Link {
		return model.Link{Kind: model.LinkBlocks, NativeLabel: "blocks", Target: t}
	}
	members := blockingMembers()
	targetOf := func(index int) model.LinkTarget {
		t := members[index]
		return model.LinkTarget{
			ID:           t.ID,
			Key:          t.Key,
			Title:        t.Title,
			URL:          t.URL,
			Status:       t.Status,
			NativeStatus: t.NativeStatus,
		}
	}
	detail := func(index int, links ...model.Link) model.Detail {
		return model.Detail{TicketID: members[index].ID, Links: links}
	}

	return map[model.TicketID]model.Detail{
		members[0].ID: detail(0, blockedBy(targetOf(4)), blockedBy(targetOf(5))),
		members[1].ID: detail(1, blockedBy(targetOf(0))),
		members[2].ID: detail(2, blockedBy(ghostRFC)),
		members[3].ID: detail(3, blockedBy(ghostVendor)),
		members[4].ID: detail(4, blocksLink(targetOf(0))),
		members[5].ID: detail(5),
		members[6].ID: detail(6, blockedBy(targetOf(1))),
		members[7].ID: detail(7, blockedBy(targetOf(8))),
		members[8].ID: detail(8, blockedBy(targetOf(7))),
		members[9].ID: detail(9, blockedBy(targetOf(9))),
		members[11].ID: detail(11, model.Link{
			Kind:        model.LinkRelates,
			NativeLabel: "relates to",
			Target:      targetOf(6),
		}),
		members[12].ID: detail(12, blockedBy(ghostOps)),
	}
}

// WithBlockingFixture serves the blocking Watchlist and its Details together. It
// is one Option rather than two because a snapshot without its Details is a
// Watchlist where nothing is knowable, and half-configuring the pair is the
// mistake worth making impossible.
func WithBlockingFixture() Option {
	return func(p *Provider) {
		WithSnapshot(FixtureBlockingSnapshot())(p)
		WithDetails(FixtureBlockingDetails())(p)
	}
}
