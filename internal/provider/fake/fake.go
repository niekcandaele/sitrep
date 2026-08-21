// Package fake provides sitrep's built-in fake Provider: a deterministic,
// dependency-free implementation of provider.Provider serving a hand-written
// fixture epic.
//
// This is the standing test double for all renderer and TUI work in sitrep, not
// scaffolding for one package's tests. It is production code precisely so every
// other package — and `sitrep --provider fake` itself — can use it. When the
// Provider interface grows, grow the fake with it and keep it complete and
// deterministic: fixed timestamps, fixed ordering, no time.Now, no randomness,
// no map iteration reaching the output. Downstream goldens depend on the
// fixture, so change it deliberately.
package fake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
)

// allCapabilities is what New declares by default: everything on, so a test
// that cares about a capability has to say so.
var allCapabilities = model.Capabilities{
	Hierarchy:     true,
	BlockingLinks: true,
	Comments:      true,
	PullRequests:  true,
}

// Provider is a fake Provider serving a built-in fixture epic. The zero value
// is not usable; call New.
type Provider struct {
	mu             sync.Mutex
	snapshots      []model.EpicSnapshot
	details        map[model.TicketID]model.Detail
	caps           model.Capabilities
	epicErr        error
	detailErr      error
	delay          time.Duration
	cursor         int
	epicCalls      int
	detailCalls    int
	detailCallsFor map[model.TicketID]int
}

// Option configures a fake Provider.
type Option func(*Provider)

// New returns a fake Provider serving the built-in fixture epic with every
// Capability declared, no injected errors and no latency. Options tune it for
// specific test scenarios.
func New(opts ...Option) *Provider {
	p := &Provider{
		snapshots:      []model.EpicSnapshot{FixtureSnapshot()},
		details:        FixtureDetails(),
		caps:           allCapabilities,
		detailCallsFor: make(map[model.TicketID]int),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// WithCapabilities overrides the declared Capabilities. The fake then serves
// data consistent with them: with PullRequests off Tickets come back with nil
// PullRequests, with Hierarchy off they come back with an empty ParentID, with
// Comments off Details carry no comments, and with BlockingLinks off only
// Relates links survive. That consistency is what makes this a real test of
// capability-driven rendering rather than a label.
func WithCapabilities(c model.Capabilities) Option {
	return func(p *Provider) { p.caps = c }
}

// WithSnapshot replaces the fixture epic with s for every FetchEpic call.
func WithSnapshot(s model.EpicSnapshot) Option {
	return func(p *Provider) { p.snapshots = []model.EpicSnapshot{s} }
}

// WithSnapshots serves s in order, one per FetchEpic call, repeating the last
// one once they run out. This is how refresh behaviour is tested: give the
// Provider the world before and after a Ticket moves.
func WithSnapshots(s ...model.EpicSnapshot) Option {
	return func(p *Provider) {
		p.snapshots = make([]model.EpicSnapshot, len(s))
		copy(p.snapshots, s)
	}
}

// WithDetails replaces the fixture Details. A TicketID absent from d makes
// FetchDetail fail for that Ticket.
func WithDetails(d map[model.TicketID]model.Detail) Option {
	return func(p *Provider) {
		p.details = make(map[model.TicketID]model.Detail, len(d))
		for id, detail := range d {
			p.details[id] = detail
		}
	}
}

// WithEpicError makes every FetchEpic call fail with err.
func WithEpicError(err error) Option {
	return func(p *Provider) { p.epicErr = err }
}

// WithDetailError makes every FetchDetail call fail with err.
func WithDetailError(err error) Option {
	return func(p *Provider) { p.detailErr = err }
}

// WithDelay makes both fetch methods take d before returning, so loading states
// and spinners have something to spin for. The delay is cancellable through the
// call's context.
func WithDelay(d time.Duration) Option {
	return func(p *Provider) { p.delay = d }
}

// Name returns "fake".
func (p *Provider) Name() string { return "fake" }

// Capabilities returns the declared Capabilities.
func (p *Provider) Capabilities() model.Capabilities {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.caps
}

// FetchEpic returns the current fixture snapshot, ignoring the Epic Ref. The
// result is a deep copy filtered to the declared Capabilities, with FetchedAt
// left zero for the caller to stamp. Successive calls advance through the
// snapshots given to WithSnapshots.
func (p *Provider) FetchEpic(ctx context.Context, _ string) (model.EpicSnapshot, error) {
	if err := p.wait(ctx); err != nil {
		return model.EpicSnapshot{}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.epicCalls++
	if p.epicErr != nil {
		return model.EpicSnapshot{}, p.epicErr
	}

	snap := cloneSnapshot(p.snapshots[p.cursor])
	if p.cursor < len(p.snapshots)-1 {
		p.cursor++
	}

	snap.Capabilities = p.caps
	snap.FetchedAt = time.Time{}
	for i := range snap.Tickets {
		applyTicketCapabilities(&snap.Tickets[i], p.caps)
	}
	return snap, nil
}

// FetchDetail returns the fixture Detail for id, filtered to the declared
// Capabilities. An id with no fixture Detail is an error.
func (p *Provider) FetchDetail(ctx context.Context, id model.TicketID) (model.Detail, error) {
	if err := p.wait(ctx); err != nil {
		return model.Detail{}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.detailCalls++
	p.detailCallsFor[id]++
	if p.detailErr != nil {
		return model.Detail{}, p.detailErr
	}

	d, ok := p.details[id]
	if !ok {
		return model.Detail{}, fmt.Errorf("fake: no detail for ticket %q", id)
	}

	d = cloneDetail(d)
	applyDetailCapabilities(&d, p.caps)
	return d, nil
}

// EpicCalls returns how many times FetchEpic has been called, including calls
// that returned an injected error.
func (p *Provider) EpicCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.epicCalls
}

// DetailCalls returns how many times FetchDetail has been called. A list
// refresh must never move this number (ADR-0003).
func (p *Provider) DetailCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.detailCalls
}

// DetailCallsFor returns how many times FetchDetail has been called for one
// Ticket, so caching behaviour can be asserted per Ticket.
func (p *Provider) DetailCallsFor(id model.TicketID) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.detailCallsFor[id]
}

// wait honours an already-cancelled context and any configured delay.
func (p *Provider) wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	p.mu.Lock()
	delay := p.delay
	p.mu.Unlock()
	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// applyTicketCapabilities strips Ticket data the declared Capabilities say the
// Tracker cannot supply.
func applyTicketCapabilities(t *model.Ticket, caps model.Capabilities) {
	if !caps.PullRequests {
		t.PullRequests = nil
	}
	if !caps.Hierarchy {
		t.ParentID = ""
	}
}

// applyDetailCapabilities strips Detail data the declared Capabilities say the
// Tracker cannot supply. Relates links survive without the BlockingLinks
// capability; only the directed blocking ones go.
func applyDetailCapabilities(d *model.Detail, caps model.Capabilities) {
	if !caps.Comments {
		d.Comments = nil
	}
	if caps.BlockingLinks {
		return
	}
	kept := make([]model.Link, 0, len(d.Links))
	for _, l := range d.Links {
		if l.Kind == model.LinkRelates {
			kept = append(kept, l)
		}
	}
	if len(kept) == 0 {
		d.Links = nil
		return
	}
	d.Links = kept
}

// cloneSnapshot deep-copies a snapshot so a caller mutating what it got cannot
// corrupt the fixture for the next call.
func cloneSnapshot(s model.EpicSnapshot) model.EpicSnapshot {
	out := s
	if s.Tickets != nil {
		out.Tickets = make([]model.Ticket, len(s.Tickets))
		for i, t := range s.Tickets {
			out.Tickets[i] = cloneTicket(t)
		}
	}
	return out
}

func cloneTicket(t model.Ticket) model.Ticket {
	out := t
	if t.Assignees != nil {
		out.Assignees = make([]model.User, len(t.Assignees))
		copy(out.Assignees, t.Assignees)
	}
	if t.PullRequests != nil {
		out.PullRequests = make([]model.PullRequest, len(t.PullRequests))
		copy(out.PullRequests, t.PullRequests)
	}
	return out
}

func cloneDetail(d model.Detail) model.Detail {
	out := d
	if d.Comments != nil {
		out.Comments = make([]model.Comment, len(d.Comments))
		copy(out.Comments, d.Comments)
	}
	if d.Links != nil {
		out.Links = make([]model.Link, len(d.Links))
		copy(out.Links, d.Links)
	}
	return out
}

// The fake must always satisfy the interface it stands in for.
var _ provider.Provider = (*Provider)(nil)
