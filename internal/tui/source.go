package tui

import (
	"context"
	"time"

	"github.com/niekcandaele/sitrep/internal/provider"
)

// Source produces one reading of the Watchlist the monitor is showing. It is
// the seam between the TUI and the Provider: the TUI knows nothing about
// Selectors, auth or Tracker APIs.
type Source func(ctx context.Context) (ListInput, error)

// SelectorSource returns a Source that resolves one Selector on every call,
// stamping the reading with now. Ref resolution has already happened: Resolve
// is polled and must never re-read a git remote (ADR-0003,
// provider.Provider.Resolve). One call is exactly one Resolve and never a
// FetchDetail.
func SelectorSource(p provider.Provider, selector provider.Selector, now func() time.Time) Source {
	return func(ctx context.Context) (ListInput, error) {
		snap, err := p.Resolve(ctx, selector)
		if err != nil {
			return ListInput{}, err
		}
		return ListFromWatchlistSnapshot(provider.StampSnapshot(p, snap, now())), nil
	}
}
