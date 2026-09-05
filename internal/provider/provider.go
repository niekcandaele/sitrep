// Package provider defines the seam between sitrep and the Trackers it reads.
//
// A Provider is a driver translating one Tracker's API into sitrep's model.
// Providers are compiled in and read-only (ADR-0002): no method mutates Tracker
// state, and none ever will — sitrep is a ticket viewer, not a Tracker client.
//
// The interface is split by view, not by entity (ADR-0003). Resolve is the
// cheap batched hot path: one logical read returning a Watchlist's lightweight
// Tickets, re-run on every refresh. Detail data is lazy: FetchDetail reads one
// Ticket for drill-in, while FetchDetails serves an explicit whole-Watchlist
// request and may use FetchDetail as its generic fallback. Descriptions,
// comments, and links must never migrate into Resolve's result, however
// convenient it looks: a list refresh that drags full descriptions along turns
// one request into N and is exactly what this split exists to prevent. If a view
// seems to need Detail while listing, use the explicit Detail methods rather
// than widening Ticket.
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
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/ref"
	"github.com/niekcandaele/sitrep/internal/termtext"
)

// DefaultMaxTickets is the membership budget used for Query Selectors when a
// Profile does not override it. Concrete Providers own and enforce the budget;
// it never travels with a Selector.
const DefaultMaxTickets = 100

// Selector names a Watchlist. Implementations are closed to this package so a
// Provider can exhaustively choose the authoritative read for each kind.
type Selector interface {
	selector()
}

// EpicSelector names a Watchlist by one Ref expected to identify an Epic.
// The existing decoder may discover that the Ref actually names a Ticket.
type EpicSelector struct {
	Ref ref.Ref
}

func (EpicSelector) selector() {}

// RefListSelector names the exact Tickets to read, in display order. Refs have
// already been parsed, retagged, Profile-completed, de-duplicated, and checked
// to share one Tracker and host. Providers perform no cwd git or Profile
// resolution.
type RefListSelector struct {
	Refs []ref.Ref
}

func (RefListSelector) selector() {}

// QuerySelector names a Watchlist through one opaque Tracker-native query.
// Connection selection is complete before this value reaches a Provider.
type QuerySelector struct {
	Query string
}

func (QuerySelector) selector() {}

// EpicHeader copies an Epic's display identity into the generic Watchlist seam.
func EpicHeader(epic model.Epic) model.WatchlistHeader {
	return model.WatchlistHeader{Key: epic.Key, Title: epic.Title, URL: epic.URL}
}

// RefListHeader identifies an explicit Ref-list Watchlist by its member count.
func RefListHeader(count int) model.WatchlistHeader {
	noun := "tickets"
	if count == 1 {
		noun = "ticket"
	}
	return model.WatchlistHeader{Title: fmt.Sprintf("%d %s", count, noun)}
}

// QueryHeader identifies a Query Watchlist without inventing a Tracker key or
// URL. The query remains complete here; terminal-width truncation belongs to a
// renderer.
func QueryHeader(query string) model.WatchlistHeader {
	return model.WatchlistHeader{Title: query}
}

// CheckSelectorSupport rejects an undeclared Selector before a Provider
// validates values or performs I/O. Concrete Providers call it at the top of
// Resolve so direct callers receive the same contract as the CLI.
func CheckSelectorSupport(name string, caps model.Capabilities, selector Selector) error {
	var supported bool
	var label string
	switch selector.(type) {
	case EpicSelector:
		supported, label = caps.Selectors.Epic, "Epic Selector"
	case RefListSelector:
		supported, label = caps.Selectors.RefList, "Ref-list Selector"
	case QuerySelector:
		supported, label = caps.Selectors.Query, "Query Selector"
	default:
		return Errorf(KindBadRef, "%s: unsupported Watchlist selector %T", name, selector)
	}
	if !supported {
		return Errorf(KindBadRef, "%s: %s is not supported", name, label)
	}
	return nil
}

// Provider translates one Tracker's API into sitrep's model. Implementations
// must be safe for concurrent use: the TUI polls from its own goroutines.
type Provider interface {
	// Name identifies the Provider for diagnostics and the --provider flag,
	// e.g. "github", "jira", "gitlab", "fake".
	Name() string

	// Capabilities declares which optional Tracker features this Provider
	// supports. It is cheap and pure: callers may ask on every render.
	Capabilities() model.Capabilities

	// Resolve returns one logical batched reading of the Watchlist named by
	// selector. A REST Provider may make multiple HTTP requests inside that one
	// invocation. Selectors are constructed after Ref resolution and reused on
	// every poll, so implementations must not repeat cwd git or Profile work.
	// Every returned Ticket is thin list data; Detail reads remain exclusive
	// to FetchDetail and FetchDetails. Implementations return Tickets in stable
	// Selector order and leave the snapshot's FetchedAt zero for the caller to
	// stamp.
	Resolve(ctx context.Context, selector Selector) (model.WatchlistSnapshot, error)

	// FetchDetail returns the expensive per-ticket data for one Ticket. Drill-in
	// calls it directly, and the generic FetchDetails fallback calls it for each
	// canonical ID. Neither path runs during a list refresh.
	FetchDetail(ctx context.Context, id model.TicketID) (model.Detail, error)

	// FetchDetails returns expensive Detail data for requested Tickets. It may
	// use singular reads or a Tracker-native batch, but it never widens a list
	// refresh path. Implementations canonicalize first-seen, non-empty IDs;
	// empty input performs no I/O and returns a non-nil empty map. Successful
	// entries retain their requested TicketID. A partial result is reported with
	// a *DetailFailures error. A rate refusal stops internal fallback/chunk work
	// and is attributed to later unissued IDs. Cancellation returns completed
	// successes with an error matching the context and does not invent per-Ticket
	// cancellation failures for incomplete IDs. If a paid-for callback also returns
	// a rate refusal, that aggregate remains inspectable for Tracker admission.
	FetchDetails(ctx context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, error)
}

// DetailFailures reports failures for individual Tickets in a plural Detail
// read. Successful siblings remain in the result map. Consumers inspect
// Failures rather than parsing Error. errors.Is reaches every child in an
// aggregate of at most 64 failures; a larger untrusted aggregate exposes only a
// bounds sentinel through error traversal.
type DetailFailures struct {
	Failures map[model.TicketID]error
}

const maxDetailFailureUnwrapChildren = 64

var errDetailFailuresExceedSafeBounds = errors.New("provider: DetailFailures exceeds safe unwrap bounds")

// DetailFailuresByTicket exposes the constituent map to plural-boundary
// normalization without requiring errors.As to traverse a potentially malformed
// error graph first.
func (e *DetailFailures) DetailFailuresByTicket() map[model.TicketID]error {
	if e == nil {
		return nil
	}
	return e.Failures
}

func (e *DetailFailures) Error() string {
	if e == nil {
		return "provider: typed-nil DetailFailures"
	}
	if len(e.Failures) > maxDetailFailureUnwrapChildren {
		return errDetailFailuresExceedSafeBounds.Error()
	}
	return fmt.Sprintf("details could not be read for %s", strings.Join(e.sortedFailureIDs(), ", "))
}

// sortedFailureIDs orders the failed Ticket IDs so a message and its unwrapped
// children always agree on order.
func (e *DetailFailures) sortedFailureIDs() []string {
	ids := make([]string, 0, len(e.Failures))
	for id := range e.Failures {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	return ids
}

// Unwrap exposes every per-Ticket error for an ordinary bounded aggregate. An
// oversized third-party aggregate exposes only a deterministic sentinel; callers
// can inspect Failures directly when they already trust its provenance.
func (e *DetailFailures) Unwrap() []error {
	if e == nil {
		return nil
	}
	if len(e.Failures) > maxDetailFailureUnwrapChildren {
		return []error{errDetailFailuresExceedSafeBounds}
	}
	ids := e.sortedFailureIDs()
	errs := make([]error, 0, len(ids))
	for _, rawID := range ids {
		id := model.TicketID(rawID)
		err := e.Failures[id]
		switch {
		case termtext.IsTypedNilError(err):
			errs = append(errs, fmt.Errorf("provider: FetchDetails returned a typed-nil failure for %q", id))
		case err != nil:
			errs = append(errs, err)
		}
	}
	return errs
}

type detailFailuresWithRefusal struct {
	failures *DetailFailures
	refusal  error
}

func (e *detailFailuresWithRefusal) Error() string { return e.failures.Error() }
func (e *detailFailuresWithRefusal) Unwrap() []error {
	return []error{e.refusal, e.failures}
}

func newDetailFailuresError(failures map[model.TicketID]error, refusal error) error {
	aggregate := &DetailFailures{Failures: failures}
	if refusal == nil {
		return aggregate
	}
	return &detailFailuresWithRefusal{failures: aggregate, refusal: refusal}
}

// FetchDetailsDefault supplies the generic plural implementation for Providers
// without a Tracker-native batch. It canonicalizes first-seen non-empty IDs and
// invokes fetch sequentially in canonical order. An empty input performs no I/O
// and returns a non-nil empty map. Singular failures are accumulated as
// DetailFailures. Ordinary failures continue, while the first rate refusal is
// assigned to its attempted ID and all later unissued canonical IDs and stops
// the loop. Caller cancellation is direct control flow for consumers, while a
// rate refusal returned by the same paid-for callback remains in its aggregate
// error for Tracker admission policy.
func FetchDetailsDefault(ctx context.Context, ids []model.TicketID, fetch func(context.Context, model.TicketID) (model.Detail, error)) (map[model.TicketID]model.Detail, error) {
	canonical := CanonicalDetailIDs(ids)
	details := make(map[model.TicketID]model.Detail, len(canonical))
	failures := make(map[model.TicketID]error)
	var rateRefusal error
	for i, id := range canonical {
		if err := ctx.Err(); err != nil {
			return details, err
		}
		detail, err := fetch(ctx, id)
		if err != nil {
			if refusal, limited := InspectRateLimitRefusal(err, time.Time{}); limited {
				rateRefusal = err
				for _, unissued := range canonical[i:] {
					failures[unissued] = refusal.Err
				}
				if ctxErr := ctx.Err(); ctxErr != nil {
					return details, errors.Join(ctxErr, newDetailFailuresError(failures, rateRefusal))
				}
				break
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return details, ctxErr
			}
			failures[id] = err
			continue
		}
		if detail.TicketID != id {
			failures[id] = fmt.Errorf("provider: detail for %q returned TicketID %q", id, detail.TicketID)
			if err := ctx.Err(); err != nil {
				return details, err
			}
			continue
		}
		details[id] = detail
		if err := ctx.Err(); err != nil {
			return details, err
		}
	}
	if len(failures) != 0 {
		return details, newDetailFailuresError(failures, rateRefusal)
	}
	return details, nil
}

// CanonicalDetailIDs returns the requested IDs in input order, skipping empty
// IDs and duplicates. Every plural Detail path orders its work by this slice.
func CanonicalDetailIDs(ids []model.TicketID) []model.TicketID {
	canonical := make([]model.TicketID, 0, len(ids))
	seen := make(map[model.TicketID]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		canonical = append(canonical, id)
	}
	return canonical
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
