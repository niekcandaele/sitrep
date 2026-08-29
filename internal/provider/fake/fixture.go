package fake

import (
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
)

// The fixture's people. One has a display name, one is a bare login, so
// renderers meet both shapes.
var (
	userMara = model.User{
		Login:       "mara-vos",
		DisplayName: "Mara Vos",
		AvatarURL:   "https://avatars.example.test/u/mara-vos.png",
	}
	userTobias = model.User{Login: "tobias"}
)

// fixtureTime returns a fixed UTC instant. The fake never calls time.Now: every
// timestamp downstream tests see is written here.
func fixtureTime(day, hour, min int) time.Time {
	return time.Date(2026, time.January, day, hour, min, 0, 0, time.UTC)
}

// FixtureSnapshot returns the built-in fixture epic: one Epic and ten Tickets
// spanning every Status Category, differing Native Statuses inside a category,
// zero/one/many assignees, a parent-child pair, a cross-repo Ticket, all four
// pull request shapes, and Tickets with no pull request at all.
//
// It is safe to mutate the returned value: each call builds a fresh copy.
func FixtureSnapshot() model.WatchlistSnapshot {
	return model.WatchlistSnapshot{
		Header: model.WatchlistHeader{
			Key:   "#111",
			Title: "Widget sync v2: shards, retries & telemetry",
			URL:   "https://tracker.example.test/acme/widgets/111",
		},
		Epic: model.Epic{
			ID:           "acme/widgets#111",
			Key:          "#111",
			Title:        "Widget sync v2: shards, retries & telemetry",
			URL:          "https://tracker.example.test/acme/widgets/111",
			Status:       model.StatusInProgress,
			NativeStatus: "open",
		},
		Capabilities: allCapabilities,
		Tickets: []model.Ticket{
			{
				ID:           "acme/widgets#112",
				Key:          "#112",
				Title:        "Draft the shard sync protocol",
				URL:          "https://tracker.example.test/acme/widgets/112",
				Status:       model.StatusInProgress,
				NativeStatus: "In Review",
				Assignees:    []model.User{userMara},
				Repository:   "acme/widgets",
				PullRequests: []model.PullRequest{{
					Number:     501,
					Title:      "Shard sync protocol, first cut",
					URL:        "https://tracker.example.test/acme/widgets/pull/501",
					Repository: "acme/widgets",
					State:      model.PROpen,
					Review:     model.ReviewChangesRequested,
					Checks:     model.ChecksPassing,
				}},
				// The Tracker knows about four; the Provider fetched one. This
				// is the fixture's truncated Ticket, and the goldens read it.
				PullRequestTotal: 4,
			},
			{
				ID:           "acme/widgets#113",
				Key:          "#113",
				Title:        "Retry & backoff for the sync worker",
				URL:          "https://tracker.example.test/acme/widgets/113",
				Status:       model.StatusInProgress,
				NativeStatus: "In Progress",
				Assignees:    []model.User{userMara, userTobias},
				Repository:   "acme/widgets",
				PullRequests: []model.PullRequest{{
					Number:     502,
					Title:      "Exponential backoff with jitter",
					URL:        "https://tracker.example.test/acme/widgets/pull/502",
					Repository: "acme/widgets",
					State:      model.PROpen,
					Review:     model.ReviewPending,
					Checks:     model.ChecksFailing,
				}},
			},
			{
				ID:           "acme/gadgets#7",
				Key:          "#7",
				Title:        "Teach the gadget agent to speak sync v2",
				URL:          "https://tracker.example.test/acme/gadgets/7",
				Status:       model.StatusInProgress,
				NativeStatus: "In Progress",
				Repository:   "acme/gadgets",
				PullRequests: []model.PullRequest{{
					Number:     18,
					Title:      "WIP: sync v2 handshake",
					URL:        "https://tracker.example.test/acme/gadgets/pull/18",
					Repository: "acme/gadgets",
					State:      model.PRDraft,
					Review:     model.ReviewNone,
					Checks:     model.ChecksPending,
				}},
			},
			{
				ID:           "acme/widgets#115",
				Key:          "#115",
				Title:        "Reconcile widget IDs across shards",
				URL:          "https://tracker.example.test/acme/widgets/115",
				Status:       model.StatusTodo,
				NativeStatus: "open",
				Repository:   "acme/widgets",
			},
			{
				ID:           "acme/widgets#116",
				Key:          "#116",
				Title:        "Protocol conformance tests",
				URL:          "https://tracker.example.test/acme/widgets/116",
				Status:       model.StatusTodo,
				NativeStatus: "Selected for Development",
				Assignees:    []model.User{userTobias},
				ParentID:     "acme/widgets#112",
				Repository:   "acme/widgets",
			},
			{
				ID:           "acme/widgets#117",
				Key:          "#117",
				Title:        "Renseigner la métrique « éclair » du tableau de bord",
				URL:          "https://tracker.example.test/acme/widgets/117",
				Status:       model.StatusTodo,
				NativeStatus: "open",
				Repository:   "acme/widgets",
			},
			{
				ID:           "acme/widgets#118",
				Key:          "#118",
				Title:        "Cache shard lookups",
				URL:          "https://tracker.example.test/acme/widgets/118",
				Status:       model.StatusDone,
				NativeStatus: "closed",
				Assignees:    []model.User{userMara},
				Repository:   "acme/widgets",
				PullRequests: []model.PullRequest{{
					Number:     504,
					Title:      "Cache shard lookups for 30s",
					URL:        "https://tracker.example.test/acme/widgets/pull/504",
					Repository: "acme/widgets",
					State:      model.PRMerged,
					Review:     model.ReviewApproved,
					Checks:     model.ChecksPassing,
				}},
			},
			{
				ID:           "acme/widgets#119",
				Key:          "#119",
				Title:        "Emit sync telemetry",
				URL:          "https://tracker.example.test/acme/widgets/119",
				Status:       model.StatusDone,
				NativeStatus: "Done",
				Assignees:    []model.User{userTobias},
				Repository:   "acme/widgets",
			},
			{
				ID:           "acme/widgets#120",
				Key:          "#120",
				Title:        "Write the sync v2 runbook",
				URL:          "https://tracker.example.test/acme/widgets/120",
				Status:       model.StatusDone,
				NativeStatus: "closed",
				Repository:   "acme/widgets",
			},
			{
				ID:           "acme/widgets#121",
				Key:          "#121",
				Title:        "Mirror shards to the legacy feed",
				URL:          "https://tracker.example.test/acme/widgets/121",
				Status:       model.StatusCancelled,
				NativeStatus: "not planned",
				Repository:   "acme/widgets",
			},
		},
	}
}

// FixtureRefListSnapshot returns four explicit fixture Tickets with varied
// status and pull-request state. It has no outer Epic or Parent because every
// member is an ordinary entry in the Watchlist.
func FixtureRefListSnapshot() model.WatchlistSnapshot {
	epic := FixtureSnapshot()
	tickets := []model.Ticket{
		epic.Tickets[0],
		epic.Tickets[3],
		epic.Tickets[6],
		epic.Tickets[9],
	}
	return model.WatchlistSnapshot{
		Header:       provider.RefListHeader(len(tickets)),
		Tickets:      tickets,
		Capabilities: allCapabilities,
	}
}

// FixtureTicketSnapshot returns what a batched fetch answers when the Ref
// named a plain Ticket rather than a Watchlist: no Tickets, the Ticket's own
// identity in the Epic field, and the Watchlist it belongs to in Parent. It is
// the fixture Ticket #112, so the Details FixtureDetails already serves — a
// multi-paragraph description, three comments, all three Link kinds — are its
// Detail.
//
// It is safe to mutate the returned value: each call builds a fresh copy.
func FixtureTicketSnapshot() model.WatchlistSnapshot {
	epic := FixtureSnapshot()
	t := epic.Tickets[0]
	return model.WatchlistSnapshot{
		Header:  model.WatchlistHeader{Key: t.Key, Title: t.Title, URL: t.URL},
		Tickets: []model.Ticket{},
		Epic: model.Epic{
			ID:               t.ID,
			Key:              t.Key,
			Title:            t.Title,
			URL:              t.URL,
			Status:           t.Status,
			NativeStatus:     t.NativeStatus,
			Assignees:        t.Assignees,
			Repository:       t.Repository,
			PullRequests:     t.PullRequests,
			PullRequestTotal: t.PullRequestTotal,
		},
		Parent: model.Parent{
			ID:    epic.Epic.ID,
			Key:   epic.Epic.Key,
			Title: epic.Epic.Title,
			URL:   epic.Epic.URL,
		},
		Capabilities: allCapabilities,
	}
}

// FixtureOrphanTicketSnapshot returns the same shape for a Ticket that hangs off
// nothing: a zero Parent, which is an ordinary state and never an error. It is
// the fixture Ticket #115, whose Detail is description-only.
//
// It is safe to mutate the returned value: each call builds a fresh copy.
func FixtureOrphanTicketSnapshot() model.WatchlistSnapshot {
	t := FixtureSnapshot().Tickets[3]
	return model.WatchlistSnapshot{
		Header:  model.WatchlistHeader{Key: t.Key, Title: t.Title, URL: t.URL},
		Tickets: []model.Ticket{},
		Epic: model.Epic{
			ID:               t.ID,
			Key:              t.Key,
			Title:            t.Title,
			URL:              t.URL,
			Status:           t.Status,
			NativeStatus:     t.NativeStatus,
			Assignees:        t.Assignees,
			Repository:       t.Repository,
			PullRequests:     t.PullRequests,
			PullRequestTotal: t.PullRequestTotal,
		},
		Capabilities: allCapabilities,
	}
}

// FixtureDetails returns the built-in Details, keyed by TicketID: one rich
// (multi-paragraph description, three comments, all three Link kinds), one
// minimal (description only), and one with an empty description. Tickets absent
// from the map have no Detail and make FetchDetail fail, which is what tests of
// the unhappy path want.
//
// It is safe to mutate the returned map: each call builds a fresh copy.
func FixtureDetails() map[model.TicketID]model.Detail {
	return map[model.TicketID]model.Detail{
		"acme/widgets#112": {
			TicketID: "acme/widgets#112",
			Description: "The shard sync protocol has to survive a shard going away " +
				"mid-transfer & coming back with a different generation number.\n\n" +
				"## Shape\n\n" +
				"1. The sender announces `(shard, generation, offset)`.\n" +
				"2. The receiver either accepts or replies with the offset it wants.\n" +
				"3. Either side may abort; an aborted transfer leaves no partial state.\n\n" +
				"Open question: do we version the handshake itself, or rely on the " +
				"generation number to carry it? Leaning towards an explicit version — " +
				"see the linked ticket.",
			Comments: []model.Comment{
				{
					ID:        "c-1001",
					Author:    userMara,
					Body:      "Sketched the state machine; the abort path is the one that bites.",
					CreatedAt: fixtureTime(12, 9, 14),
					URL:       "https://tracker.example.test/acme/widgets/112#comment-1001",
				},
				{
					ID:     "c-1002",
					Author: userTobias,
					Body: "Agreed on an explicit version byte. Costs nothing now, " +
						"saves a migration later.",
					CreatedAt: fixtureTime(12, 16, 2),
					URL:       "https://tracker.example.test/acme/widgets/112#comment-1002",
				},
				{
					ID:        "c-1003",
					Author:    userMara,
					Body:      "Version byte it is. Updating the draft.",
					CreatedAt: fixtureTime(13, 8, 30),
					URL:       "https://tracker.example.test/acme/widgets/112#comment-1003",
				},
			},
			Links: []model.Link{
				{
					Kind:        model.LinkBlockedBy,
					NativeLabel: "is blocked by",
					Target: model.LinkTarget{
						ID:           "acme/widgets#115",
						Key:          "#115",
						Title:        "Reconcile widget IDs across shards",
						URL:          "https://tracker.example.test/acme/widgets/115",
						Status:       model.StatusTodo,
						NativeStatus: "open",
					},
				},
				{
					Kind:        model.LinkBlocks,
					NativeLabel: "blocks",
					Target: model.LinkTarget{
						ID:           "acme/widgets#116",
						Key:          "#116",
						Title:        "Protocol conformance tests",
						URL:          "https://tracker.example.test/acme/widgets/116",
						Status:       model.StatusTodo,
						NativeStatus: "Selected for Development",
					},
				},
				{
					// An unrecognized Tracker link type falls back to Relates
					// and keeps its native wording.
					Kind:        model.LinkRelates,
					NativeLabel: "is duplicated by",
					Target: model.LinkTarget{
						ID:           "acme/gadgets#7",
						Key:          "#7",
						Title:        "Teach the gadget agent to speak sync v2",
						URL:          "https://tracker.example.test/acme/gadgets/7",
						Status:       model.StatusInProgress,
						NativeStatus: "In Progress",
					},
				},
			},
		},
		"acme/widgets#115": {
			TicketID:    "acme/widgets#115",
			Description: "Two shards can hand out the same widget ID after a split. Pick a winner deterministically.",
		},
		"acme/widgets#119": {
			TicketID: "acme/widgets#119",
			Comments: []model.Comment{{
				ID:        "c-1004",
				Author:    userTobias,
				Body:      "Dashboards are live.",
				CreatedAt: fixtureTime(14, 11, 45),
				URL:       "https://tracker.example.test/acme/widgets/119#comment-1004",
			}},
		},
	}
}
