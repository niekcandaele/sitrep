// Package detailfanout is the shared policy for reading a whole Watchlist's
// Details at once.
//
// ADR-0003's split by view still holds: a list refresh never calls
// FetchDetail, and no Detail field migrates onto the thin Ticket. What this
// package permits is narrower — a caller may fan FetchDetail out across every
// member of a Watchlist in response to an explicit user action, never from a
// refresh, a poll or a render. The Frontier is the first such caller; a
// one-shot renderer asked for blocking data is the second.
//
// The policy lives here rather than in any one caller so that every consumer
// pays the same price and fails the same way: canonical order, cache skipping,
// a small parallelism budget, and the rule that only a successful fetch is ever
// recorded.
package detailfanout

import (
	"context"
	"sync"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
)

// Fetch reads one Ticket's Detail. It is the same shape as tui.DetailSource and
// as a provider.Provider's FetchDetail with Capabilities attached, so both the
// monitor and a one-shot renderer hand this package the seam they already hold.
type Fetch func(ctx context.Context, id model.TicketID) (model.Detail, model.Capabilities, error)

// FromProvider adapts a Provider to Fetch.
func FromProvider(p provider.Provider) Fetch {
	return func(ctx context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
		d, err := p.FetchDetail(ctx, id)
		if err != nil {
			return model.Detail{}, model.Capabilities{}, err
		}
		return d, p.Capabilities(), nil
	}
}

// Plan returns the Ticket IDs a caller still has to fetch, in canonical order:
// the Watchlist's own order, skipping empty IDs, skipping duplicates, and
// skipping anything have already reports holding. Determinism matters — it is
// what makes a progress count and a golden frame reproducible.
//
// A nil have is a caller holding nothing.
func Plan(tickets []model.Ticket, have func(model.TicketID) bool) []model.TicketID {
	var ids []model.TicketID
	planned := make(map[model.TicketID]bool, len(tickets))
	for _, t := range tickets {
		if t.ID == "" || planned[t.ID] {
			continue
		}
		if have != nil && have(t.ID) {
			continue
		}
		planned[t.ID] = true
		ids = append(ids, t.ID)
	}
	return ids
}

// Outcome is one resolved fetch.
type Outcome struct {
	// ID is the Ticket the fetch was issued for.
	ID model.TicketID
	// Detail is what the Provider returned; the zero value on failure.
	Detail model.Detail
	// Caps are the serving Provider's Capabilities; the zero value on failure.
	Caps model.Capabilities
	// Err is the read's failure, if any. A failed read is recorded nowhere:
	// see Links.
	Err error
}

// Parallelism is how many FetchDetail calls this package will have in flight at
// once. It is deliberately small: the whole point of ADR-0003 is not to
// multiply rate-limit pressure, and a fan-out the user asked for is still a
// fan-out.
const Parallelism = 4

// Run fetches every id with bounded concurrency, calling emit once per Outcome
// as it resolves, and returns when every id has resolved or ctx is done. It is
// for one-shot callers; an event-loop caller schedules its own commands and
// uses Plan and Links instead.
//
// emit is called from Run's own goroutine, one Outcome at a time, so a caller
// needs no lock of its own.
func Run(ctx context.Context, f Fetch, ids []model.TicketID, emit func(Outcome)) error {
	if len(ids) == 0 {
		return ctx.Err()
	}

	outcomes := make(chan Outcome, len(ids))
	var wg sync.WaitGroup
	slots := make(chan struct{}, Parallelism)

	go func() {
		defer close(outcomes)
		for _, id := range ids {
			select {
			case <-ctx.Done():
				// The remaining ids are simply un-emitted: an interrupted
				// fan-out records nothing for them, which is exactly the
				// fail-closed answer BuildBlockingGraph wants.
				wg.Wait()
				return
			case slots <- struct{}{}:
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				d, caps, err := f(ctx, id)
				outcomes <- Outcome{ID: id, Detail: d, Caps: caps, Err: err}
			}()
		}
		wg.Wait()
	}()

	for o := range outcomes {
		emit(o)
	}
	return ctx.Err()
}

// Links folds resolved Details into the map model.BuildBlockingGraph consumes.
// Only a successful fetch writes a key: key presence is the tri-state that
// makes fail-closed Actionable real, so an id that failed, was interrupted, or
// was never issued is simply absent. Never write an empty slice for a failure.
//
// It takes the caller's own cache map, so the monitor passes what it has cached
// this session and a one-shot caller passes what Run collected.
func Links(details map[model.TicketID]model.Detail) map[model.TicketID][]model.Link {
	links := make(map[model.TicketID][]model.Link, len(details))
	for id, d := range details {
		links[id] = d.Links
	}
	return links
}
