package provider

import (
	"context"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/termtext"
)

// Sanitized wraps a Provider so that every piece of tracker-controlled text it
// returns has crossed the terminal-visible-text boundary before anything
// renders it.
//
// This is one of that boundary's two enforcement points (ADR-0006), and the one
// that protects the sinks a Provider feeds directly: --plain, --json and the
// decoded one-shot path. Lip Gloss and sitrep's ANSI-aware wrapping preserve
// escape sequences they recognize, and internal/render/plain writes strings
// with fmt.Fprintf, so a title of "hi\x1b[2J" clears the reader's screen and a
// comment body carrying OSC 52 writes their clipboard — and on a public tracker
// anyone who can comment can write one.
//
// It is deliberately not the only enforcement point. internal/tui accepts model
// data through funnels a Provider never touched — a caller-built ListInput, a
// Source that is any closure at all — so a screen makes its own inputs safe at
// intake rather than trusting whoever built them. Both call the same walkers in
// internal/termtext: one policy, one implementation, two sinks.
func Sanitized(p Provider) Provider {
	if p == nil {
		return nil
	}
	if _, already := p.(sanitized); already {
		return p
	}
	return sanitized{Provider: p}
}

type sanitized struct{ Provider }

func (s sanitized) Resolve(ctx context.Context, selector Selector) (model.WatchlistSnapshot, error) {
	snap, err := s.Provider.Resolve(ctx, selector)
	return termtext.Snapshot(snap), err
}

func (s sanitized) FetchDetail(ctx context.Context, id model.TicketID) (model.Detail, error) {
	detail, err := s.Provider.FetchDetail(ctx, id)
	return termtext.Detail(detail), err
}

func (s sanitized) FetchDetails(ctx context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, error) {
	details, err := s.Provider.FetchDetails(ctx, ids)
	sanitizedDetails := make(map[model.TicketID]model.Detail, len(details))
	for id, detail := range details {
		sanitizedDetails[id] = termtext.Detail(detail)
	}
	return sanitizedDetails, err
}
