package tui

import (
	"context"
	"time"

	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// Source produces one reading of the collection the monitor is watching. It is
// the seam between the TUI and the Provider: the TUI knows nothing about Epic
// Refs, auth or GraphQL, and a future Ticket-collection source (a query, an
// ad-hoc set) plugs in here without touching the screen.
type Source func(ctx context.Context) (ListInput, error)

// EpicSource returns a Source that reads one Epic from a Provider on every
// call, stamping the reading with now. Ref resolution has already happened:
// FetchEpic is polled and must never re-read a git remote (ADR-0003,
// provider.Provider.FetchEpic). One call is exactly one FetchEpic and never a
// FetchDetail.
func EpicSource(p provider.Provider, r ref.Ref, now func() time.Time) Source {
	return func(ctx context.Context) (ListInput, error) {
		snap, err := p.FetchEpic(ctx, r)
		if err != nil {
			return ListInput{}, err
		}
		return ListFromEpicSnapshot(provider.StampSnapshot(p, snap, now())), nil
	}
}
