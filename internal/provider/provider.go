// Package provider defines the seam between sitrep and the Trackers it reads.
//
// A Provider is a driver translating one Tracker's API into sitrep's model.
// Providers are compiled in and read-only (ADR-0002): no method mutates Tracker
// state, and none ever will — sitrep is a situation report, not a client.
//
// The interface is split by view, not by entity (ADR-0003). Resolve is the
// cheap batched hot path: one logical read returning a Watchlist's lightweight
// Tickets, re-run on every refresh. FetchDetail is the lazy per-ticket call made
// only when a human opens a Ticket. Detail data — descriptions, comments, links
// — must never migrate into Resolve's result, however convenient it looks: a
// list refresh that drags full descriptions along turns one request into N and
// is exactly what this split exists to prevent. If a view seems to need Detail
// while listing, that is a FetchDetail call, not a wider Ticket.
//
// A Resolve answers what the Selector points at, whatever that turns out to be.
// A Ref naming a plain Ticket comes back as a snapshot with no Tickets, carrying
// that Ticket's identity in Epic and its parent Ticket in Parent
// (model.WatchlistSnapshot). A Provider reports that and stops there: which
// screen opens is the caller's decision, and no third method exists to ask the
// question (ADR-0003).
//
// # A Provider's failures are part of its contract
//
// A driver is the only thing that knows why a Tracker said no, so the sentence
// the user reads is the driver's to write — and the class that sentence belongs
// to is the driver's to declare. errors.go holds that classification: a small
// Kind, the Errorf that attaches one without touching the message, and the
// prose rules every classified error satisfies. A caller reads a Kind to decide
// whether retrying is worth anything; it never prints one.
package provider

import (
	"context"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// Selector names a Watchlist. EpicSelector is the only implementation in v0.2
// after ticket #44; later tickets add other kinds without changing Provider.Resolve.
type Selector interface {
	selector()
}

// EpicSelector names a Watchlist by one Ref expected to identify an Epic.
// The existing decoder may discover that the Ref actually names a Ticket.
type EpicSelector struct {
	Ref ref.Ref
}

func (EpicSelector) selector() {}

// Provider translates one Tracker's API into sitrep's model. Implementations
// must be safe for concurrent use: the TUI polls from its own goroutines.
type Provider interface {
	// Name identifies the Provider for diagnostics and the --provider flag,
	// e.g. "github", "jira", "gitlab", "fake".
	Name() string

	// Capabilities declares which optional Tracker features this Provider
	// supports. It is cheap and pure: callers may ask on every render.
	Capabilities() model.Capabilities

	// Resolve returns one batched reading of the Watchlist named by selector.
	// The Selector is constructed after Ref resolution and reused on every poll,
	// so implementations must not repeat that work. Implementations must be safe
	// to call repeatedly and return Tickets in a stable order. The returned
	// snapshot's FetchedAt is left zero for the caller to stamp.
	Resolve(ctx context.Context, selector Selector) (model.WatchlistSnapshot, error)

	// FetchDetail returns the expensive per-ticket data for one Ticket. It is
	// called only on drill-in, never during a list refresh.
	FetchDetail(ctx context.Context, id model.TicketID) (model.Detail, error)
}

// StampSnapshot completes a snapshot a Provider just returned, so it is ready
// to render: FetchedAt is stamped from the caller's clock, because a Provider
// leaves it zero, and the Capabilities are back-filled from the Provider when
// it did not set them itself.
//
// Every read path goes through it — the one-shot --json and --plain renderers
// and the TUI's refresh — so "what turns a fetch into something displayable"
// has exactly one definition and the monitor and the snapshot cannot disagree
// about which Capabilities are in force.
func StampSnapshot(p Provider, snap model.WatchlistSnapshot, now time.Time) model.WatchlistSnapshot {
	snap.FetchedAt = now
	if snap.Capabilities == (model.Capabilities{}) {
		snap.Capabilities = p.Capabilities()
	}
	return snap
}
