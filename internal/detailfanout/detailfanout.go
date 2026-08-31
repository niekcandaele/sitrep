// Package detailfanout is the shared policy for reading a whole Watchlist's
// Details at once.
//
// ADR-0003's split by view still holds: a list refresh never calls FetchDetail,
// and no Detail field migrates onto the thin Ticket. What this package permits
// is narrower — a caller may read Details for every member of a Watchlist only
// in response to an explicit user action, never from a refresh, poll or render.
package detailfanout

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
)

// Fetch reads Details for a complete planned slice. Its result map is folded by
// Run in input order, so a Provider's map iteration can never affect progress or
// rendered output.
type Fetch func(ctx context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error)

// FromProvider adapts a Provider to Fetch with its declared Capabilities.
func FromProvider(p provider.Provider) Fetch {
	return func(ctx context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
		details, err := p.FetchDetails(ctx, ids)
		return details, p.Capabilities(), err
	}
}

// Plan returns the Ticket IDs a caller still has to fetch, in canonical order:
// the Watchlist's own order, skipping empty IDs, duplicates, and anything the
// have predicate already reports holding. A nil have means the caller holds
// nothing.
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

// Outcome is one requested Ticket's successful Detail or per-Ticket failure.
type Outcome struct {
	ID     model.TicketID
	Detail model.Detail
	Caps   model.Capabilities
	Err    error
}

// Parallelism is the Frontier scheduler's shared-slot budget. Run uses plural
// Provider transport and does not consume this constant.
const Parallelism = 4

// UnreadableLinksNotice describes how unreadable member Links constrain the
// blocking graph. An empty string means every planned read succeeded.
func UnreadableLinksNotice(failed int) string {
	switch failed {
	case 0:
		return ""
	case 1:
		return "1 Ticket's Links could not be read; anything it blocks is not Actionable"
	default:
		return fmt.Sprintf("%d Tickets' Links could not be read; anything they block is not Actionable", failed)
	}
}

// Run returns before dispatch when the planned slice is empty or ctx.Err() is
// non-nil at the pre-dispatch check. Otherwise, it calls f once with the full
// slice and emits outcomes in canonical input order. DetailFailures supply
// per-Ticket failures; an ordinary response-wide error preserves completed
// Details and fails each incomplete ID. Cancellation is control flow: completed
// successes are emitted, incomplete IDs are left unknown, and no unreadable-Links
// failures are manufactured for them.
func Run(ctx context.Context, f Fetch, ids []model.TicketID, emit func(Outcome)) error {
	if len(ids) == 0 {
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	details, caps, err := f(ctx, ids)
	if details == nil {
		details = map[model.TicketID]model.Detail{}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		failures, _ := detailFailures(err)
		for _, id := range ids {
			if _, failed := failures[id]; failed {
				continue
			}
			if detail, ok := details[id]; ok && detail.TicketID == id {
				emit(Outcome{ID: id, Detail: detail, Caps: caps})
			}
		}
		return ctxErr
	}

	failures, ordinary := detailFailures(err)
	invalid, batchErr := invalidResults(ids, details, failures)
	for _, id := range ids {
		if failure, ok := invalid[id]; ok {
			emit(Outcome{ID: id, Err: failure})
			continue
		}
		if failure, ok := failures[id]; ok {
			emit(Outcome{ID: id, Err: failure})
			continue
		}
		if detail, ok := details[id]; ok {
			emit(Outcome{ID: id, Detail: detail, Caps: caps})
			continue
		}
		if ordinary != nil {
			emit(Outcome{ID: id, Err: ordinary})
			continue
		}
		emit(Outcome{ID: id, Err: fmt.Errorf("provider: FetchDetails omitted detail for %q", id)})
	}
	return batchErr
}

func detailFailures(err error) (map[model.TicketID]error, error) {
	var typed *provider.DetailFailures
	if errors.As(err, &typed) {
		if typed == nil {
			return nil, errors.New("provider: FetchDetails returned a typed-nil DetailFailures error")
		}
		return typed.Failures, nil
	}
	return nil, err
}

// invalidResults turns malformed native-batch output into per-ID failures or a
// deterministic batch error without letting an unrequested key poison a valid ID.
func invalidResults(ids []model.TicketID, details map[model.TicketID]model.Detail, failures map[model.TicketID]error) (map[model.TicketID]error, error) {
	invalid := make(map[model.TicketID]error)
	requested := make(map[model.TicketID]bool, len(ids))
	for _, id := range ids {
		requested[id] = true
		if detail, ok := details[id]; ok && detail.TicketID != id {
			invalid[id] = fmt.Errorf("provider: detail for %q returned TicketID %q", id, detail.TicketID)
		}
		if failure, ok := failures[id]; ok && failure == nil {
			invalid[id] = fmt.Errorf("provider: FetchDetails returned nil failure for %q", id)
		}
		if _, hasDetail := details[id]; hasDetail {
			if _, hasFailure := failures[id]; hasFailure {
				invalid[id] = fmt.Errorf("provider: FetchDetails returned both detail and failure for %q", id)
			}
		}
	}
	keys := make([]string, 0)
	for id := range details {
		if !requested[id] {
			keys = append(keys, string(id))
		}
	}
	for id := range failures {
		if !requested[id] {
			keys = append(keys, string(id))
		}
	}
	if len(keys) != 0 {
		sort.Strings(keys)
		return invalid, fmt.Errorf("provider: FetchDetails returned unrequested TicketID %q", keys[0])
	}
	return invalid, nil
}

// Links folds resolved Details into the map model.BuildBlockingGraph consumes.
// Only a successful fetch writes a key: key presence is the tri-state that makes
// fail-closed Actionable real.
func Links(details map[model.TicketID]model.Detail) map[model.TicketID][]model.Link {
	links := make(map[model.TicketID][]model.Link, len(details))
	for id, d := range details {
		links[id] = d.Links
	}
	return links
}
