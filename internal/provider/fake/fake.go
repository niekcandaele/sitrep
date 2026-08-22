// Package fake provides sitrep's built-in fake Provider: a deterministic,
// dependency-free implementation of provider.Provider serving hand-written
// fixture Watchlists.
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
	"strings"
	"sync"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// allCapabilities is what New declares by default: everything on, so a test
// that cares about a capability has to say so.
var allCapabilities = model.Capabilities{
	Hierarchy:     true,
	BlockingLinks: true,
	Comments:      true,
	PullRequests:  true,
	Selectors: model.SelectorCapabilities{
		Epic: true, RefList: true, Query: true,
	},
}

// Provider is a fake Provider serving built-in Watchlist fixtures. The zero value
// is not usable; call New.
type Provider struct {
	mu             sync.Mutex
	snapshots      []model.WatchlistSnapshot
	details        map[model.TicketID]model.Detail
	caps           model.Capabilities
	maxTickets     int
	resolveErr     error
	detailErr      error
	delay          time.Duration
	cursor         int
	lastSelector   provider.Selector
	resolveCalls   int
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
		snapshots:      []model.WatchlistSnapshot{FixtureSnapshot()},
		details:        FixtureDetails(),
		caps:           allCapabilities,
		maxTickets:     provider.DefaultMaxTickets,
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

// WithMaxTickets sets the Query membership budget. Non-Query Selectors are
// unaffected; non-positive values leave the default in place.
func WithMaxTickets(maxTickets int) Option {
	return func(p *Provider) {
		if maxTickets > 0 {
			p.maxTickets = maxTickets
		}
	}
}

// WithSnapshot replaces the fixture epic with s for every Resolve call.
func WithSnapshot(s model.WatchlistSnapshot) Option {
	return func(p *Provider) { p.snapshots = []model.WatchlistSnapshot{s} }
}

// WithSnapshots serves s in order, one per Resolve call, repeating the last
// one once they run out. This is how refresh behaviour is tested: give the
// Provider the world before and after a Ticket moves.
func WithSnapshots(s ...model.WatchlistSnapshot) Option {
	return func(p *Provider) {
		p.snapshots = make([]model.WatchlistSnapshot, len(s))
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

// WithResolveError makes every Resolve call fail with err.
func WithResolveError(err error) Option {
	return func(p *Provider) { p.resolveErr = err }
}

// WithDetailError makes every FetchDetail call fail with err.
func WithDetailError(err error) Option {
	return func(p *Provider) { p.detailErr = err }
}

// WithDelay makes both read methods take d before returning, so loading states
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

// Resolve returns the current fixture snapshot for selector. The result is a
// deep copy filtered to the declared Capabilities, with FetchedAt left zero for
// the caller to stamp. Successive calls advance through snapshots given to
// WithSnapshots. Ref-list selectors keep only their exact members in Selector
// order and never expose an outer Epic or Parent.
func (p *Provider) Resolve(ctx context.Context, selector provider.Selector) (model.WatchlistSnapshot, error) {
	selector = cloneSelector(selector)
	p.mu.Lock()
	p.resolveCalls++
	p.lastSelector = selector
	caps := p.caps
	p.mu.Unlock()

	if err := provider.CheckSelectorSupport(p.Name(), caps, selector); err != nil {
		return model.WatchlistSnapshot{}, err
	}
	if err := p.wait(ctx); err != nil {
		return model.WatchlistSnapshot{}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.resolveErr != nil {
		return model.WatchlistSnapshot{}, p.resolveErr
	}

	snap := cloneSnapshot(p.snapshots[p.cursor])
	if p.cursor < len(p.snapshots)-1 {
		p.cursor++
	}

	switch s := selector.(type) {
	case provider.EpicSelector:
		snap.Header = provider.EpicHeader(snap.Epic)
		snap.LimitReached = false
	case provider.RefListSelector:
		if len(s.Refs) == 0 {
			return model.WatchlistSnapshot{}, provider.Errorf(provider.KindBadRef,
				"fake: Ref-list Selector is empty")
		}
		var err error
		snap, err = selectRefListSnapshot(snap, s.Refs)
		if err != nil {
			return model.WatchlistSnapshot{}, err
		}
	case provider.QuerySelector:
		snap.Header = provider.QueryHeader(s.Query)
		snap.Epic = model.Epic{}
		snap.Parent = model.Parent{}
		snap.LimitReached = len(snap.Tickets) > p.maxTickets
		if snap.LimitReached {
			snap.Tickets = snap.Tickets[:p.maxTickets]
		}
	default:
		panic("provider.CheckSelectorSupport accepted an unknown Selector")
	}

	if snap.Tickets == nil {
		snap.Tickets = []model.Ticket{}
	}
	snap.Capabilities = p.caps
	snap.FetchedAt = time.Time{}
	applyEpicCapabilities(&snap.Epic, p.caps)
	if !p.caps.Hierarchy {
		snap.Parent = model.Parent{}
	}
	for i := range snap.Tickets {
		applyTicketCapabilities(&snap.Tickets[i], p.caps)
	}
	return snap, nil
}

func selectRefListSnapshot(snap model.WatchlistSnapshot, refs []ref.Ref) (model.WatchlistSnapshot, error) {
	tickets := make([]model.Ticket, 0, len(refs))
	for _, r := range refs {
		index := -1
		for i := range snap.Tickets {
			if fakeTicketMatchesRef(snap.Tickets[i], r) {
				index = i
				break
			}
		}
		if index < 0 {
			name := strings.TrimSpace(r.Raw)
			if name == "" {
				name = r.String()
			}
			return model.WatchlistSnapshot{}, provider.Errorf(provider.KindBadRef,
				"fake: ticket %q was not found (or you lack access)", name)
		}
		tickets = append(tickets, snap.Tickets[index])
	}
	return model.WatchlistSnapshot{
		Header:       provider.RefListHeader(len(tickets)),
		Tickets:      tickets,
		Capabilities: snap.Capabilities,
	}, nil
}

func fakeTicketMatchesRef(ticket model.Ticket, r ref.Ref) bool {
	if r.Owner != "" && r.Repo != "" && r.Number > 0 {
		return string(ticket.ID) == fmt.Sprintf("%s/%s#%d", r.Owner, r.Repo, r.Number)
	}
	if r.Key != "" {
		return strings.EqualFold(string(ticket.ID), r.Key) || strings.EqualFold(ticket.Key, r.Key)
	}
	name := strings.TrimSpace(r.Raw)
	return name != "" && (string(ticket.ID) == name || ticket.Key == name)
}

func cloneSelector(selector provider.Selector) provider.Selector {
	switch s := selector.(type) {
	case provider.RefListSelector:
		s.Refs = append([]ref.Ref(nil), s.Refs...)
		return s
	case provider.QuerySelector:
		s.Query = strings.Clone(s.Query)
		return s
	default:
		return selector
	}
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

// ResolveCalls returns how many times Resolve has been called, including calls
// that returned an injected error.
func (p *Provider) ResolveCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resolveCalls
}

// LastSelector returns the Selector passed to the most recent Resolve call, so
// caller-side selector construction can be asserted at the seam where it lands.
func (p *Provider) LastSelector() provider.Selector {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneSelector(p.lastSelector)
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

// applyEpicCapabilities strips Epic data the declared Capabilities say the
// Tracker cannot supply. The Epic carries pull requests only because a Ref that
// named a Ticket comes back in this field, and the same Capability gates them
// there as on a Ticket.
func applyEpicCapabilities(e *model.Epic, caps model.Capabilities) {
	if !caps.PullRequests {
		e.PullRequests = nil
	}
}

// applyDetailCapabilities strips Detail data the declared Capabilities say the
// Tracker cannot supply, so the fake behaves like a real driver whose Tracker
// cannot answer those questions.
func applyDetailCapabilities(d *model.Detail, caps model.Capabilities) {
	if !caps.Comments {
		d.Comments = nil
	}
	d.Links = model.VisibleLinks(d.Links, caps)
}

// cloneSnapshot deep-copies a snapshot so a caller mutating what it got cannot
// corrupt the fixture for the next call.
func cloneSnapshot(s model.WatchlistSnapshot) model.WatchlistSnapshot {
	out := s
	out.Epic = cloneEpic(s.Epic)
	if s.Tickets != nil {
		out.Tickets = make([]model.Ticket, len(s.Tickets))
		for i, t := range s.Tickets {
			out.Tickets[i] = cloneTicket(t)
		}
	}
	return out
}

func cloneEpic(e model.Epic) model.Epic {
	out := e
	if e.Assignees != nil {
		out.Assignees = make([]model.User, len(e.Assignees))
		copy(out.Assignees, e.Assignees)
	}
	if e.PullRequests != nil {
		out.PullRequests = make([]model.PullRequest, len(e.PullRequests))
		copy(out.PullRequests, e.PullRequests)
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
