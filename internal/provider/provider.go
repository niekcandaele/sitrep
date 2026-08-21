// Package provider defines the seam between sitrep and the Trackers it reads.
//
// A Provider is a driver translating one Tracker's API into sitrep's model.
// Providers are compiled in and read-only (ADR-0002): no method mutates Tracker
// state, and none ever will — sitrep is a situation report, not a client.
//
// The interface is split by view, not by entity (ADR-0003). FetchEpic is the
// cheap batched hot path: one logical fetch returning the Epic plus its
// lightweight Tickets, re-run on every refresh. FetchDetail is the lazy
// per-ticket call made only when a human opens a Ticket. Detail data —
// descriptions, comments, links — must never migrate into FetchEpic's result,
// however convenient it looks: a list refresh that drags full descriptions
// along turns one request into N and is exactly what this split exists to
// prevent. If a view seems to need Detail while listing, that is a FetchDetail
// call, not a wider Ticket.
package provider

import (
	"context"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// Provider translates one Tracker's API into sitrep's model. Implementations
// must be safe for concurrent use: the TUI polls from its own goroutines.
type Provider interface {
	// Name identifies the Provider for diagnostics and the --provider flag,
	// e.g. "github", "jira", "gitlab", "fake".
	Name() string

	// Capabilities declares which optional Tracker features this Provider
	// supports. It is cheap and pure: callers may ask on every render.
	Capabilities() model.Capabilities

	// FetchEpic returns the Epic named by an Epic Ref plus its Tickets in one
	// logical fetch. The Ref is resolved once by the caller; FetchEpic is
	// polled and must not repeat ref resolution — re-reading a git remote on
	// every refresh is exactly the cost this value type exists to avoid.
	// Implementations must be safe to call repeatedly for polling and must
	// return Tickets in a stable order. The returned snapshot's FetchedAt is
	// left zero for the caller to stamp.
	FetchEpic(ctx context.Context, r ref.Ref) (model.EpicSnapshot, error)

	// FetchDetail returns the expensive per-ticket data for one Ticket. It is
	// called only on drill-in, never during a list refresh.
	FetchDetail(ctx context.Context, id model.TicketID) (model.Detail, error)
}
