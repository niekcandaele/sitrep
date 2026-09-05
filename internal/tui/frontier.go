package tui

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/niekcandaele/sitrep/internal/detailfanout"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
)

// FrontierInput is everything the Frontier screen renders: the complete
// Watchlist, the Links read for it so far, and the Capabilities that decide
// whether there is a blocking graph at all.
//
// Tickets is the whole Watchlist and never the filtered subset: hiding a node
// deletes an edge, and a deleted edge can make a blocked Ticket look
// Actionable — the precise failure Actionable exists to prevent.
//
// The BlockingGraph and the layout are deliberately absent. Both are functions
// of these fields, and storing them here would invite the two to drift, exactly
// as ListInput refuses to carry Progress.
type FrontierInput struct {
	// Header identifies the Watchlist being drawn.
	Header Header
	// Tickets are the Watchlist's members in the Provider's own order, which is
	// the graph's canonical order.
	Tickets []model.Ticket
	// Links are the members' Links as read so far. Key presence is the
	// tri-state: a Ticket absent from this map had its Links fetch fail, get
	// interrupted or never get issued, so it is never Actionable.
	Links map[model.TicketID][]model.Link
	// Capabilities decide whether there is a blocking graph at all.
	Capabilities model.Capabilities
	// FetchedAt is when the Watchlist behind this screen was read.
	FetchedAt time.Time
	// RateLimitBudget is the optional request budget from the seated List reading.
	RateLimitBudget model.RateLimitBudget
}

// FrontierFromList adapts the list's reading and the session's Detail cache to
// the Frontier's contract. Like DetailFromTicket it is a state-entry funnel, so
// it crosses the terminal-visible-text boundary here (ADR-0006); the walkers
// are idempotent, so values already cleaned at intake pay nothing for crossing
// again.
func FrontierFromList(in ListInput, links map[model.TicketID][]model.Link) FrontierInput {
	return safeFrontierInput(FrontierInput{
		Header:          in.Header,
		Tickets:         in.Tickets,
		Links:           links,
		Capabilities:    in.Capabilities,
		FetchedAt:       in.FetchedAt,
		RateLimitBudget: in.RateLimitBudget,
	})
}

type frontierCanvasRefusal struct {
	direction          frontierRankDirection
	width, height      int
	projectedCells     string
	arithmeticOverflow bool
	nodes, ghosts      int
}

func refuseFrontierCandidate(candidate frontierLayoutCandidate, nodes, ghosts int) *frontierCanvasRefusal {
	if candidate.withinCellLimit(frontierCanvasCellLimit) {
		return nil
	}
	cells, ok := candidate.projectedCells()
	projected := "overflowed checked arithmetic"
	if ok {
		projected = fmt.Sprintf("%d", cells)
	} else if !candidate.overflow && candidate.width >= 0 && candidate.height >= 0 {
		projected = new(big.Int).Mul(
			big.NewInt(int64(candidate.width)), big.NewInt(int64(candidate.height))).String()
	}
	return &frontierCanvasRefusal{
		direction:          candidate.direction,
		width:              candidate.width,
		height:             candidate.height,
		projectedCells:     projected,
		arithmeticOverflow: candidate.overflow,
		nodes:              nodes,
		ghosts:             ghosts,
	}
}

// frontierState is the Frontier screen's own state, kept in one struct so that
// it can be seated whole: the list's state is never consulted to draw it.
type frontierState struct {
	input   FrontierInput
	graph   model.BlockingGraph
	layout  frontierLayout
	refusal *frontierCanvasRefusal
	// direction is seat-local. A fresh entry or Watchlist reseat leaves it unset;
	// every other rebuild applies strict hysteresis and retains it.
	direction    frontierRankDirection
	directionSet bool
	// focusID is the focused node and may name a Ghost Ticket.
	focusID  model.TicketID
	hasFocus bool
	// retainRefusedFocus keeps a density-toggle focus latent while the selected
	// density remains refused across resize, evidence, and coalesced rebuilds.
	// Initial #105 refusal leaves it false and exposes no focus geometry.
	retainRefusedFocus bool
	offsetX, offsetY   int
	// queued are the canonical Ticket IDs not yet issued as this generation's
	// plural command. The in-flight count lives on Model, not here: re-seating
	// frontierState while an older plural call unwinds must not reset the shared
	// four-generation bound.
	queued  []model.TicketID
	planned int
	done    int
	// failed names the Tickets whose Links could not be read. Their dependents
	// are not Actionable, and the footer says so rather than guessing. It is a
	// set rather than a count because adoption has to credit the one Ticket it
	// seated: a count cannot tell which Ticket it is clearing, so adopting a
	// still-queued Ticket's Detail would silently absolve one that really did
	// fail while its card still says UNVERIFIED.
	failed        map[model.TicketID]struct{}
	failureErrors map[model.TicketID]error
	// protocolWarning is a completed batch-level semantic error after every
	// requested outcome succeeded. It is dismissible and never a retry plan.
	protocolWarning error
	lastErr         error
}

// isResolved reports whether the current Watchlist seat is complete enough to
// publish Actionability. Plan counters describe one fan-out; resolution covers
// every identifiable current member, including successful reads with no Links.
// An empty ID is not fetchable and stays visibly unverified rather than keeping
// the whole seat in an inescapable loading state.
func (f frontierState) isResolved() bool {
	if !f.input.Capabilities.BlockingLinks || len(f.input.Tickets) == 0 {
		return true
	}
	for _, ticket := range f.input.Tickets {
		if ticket.ID == "" {
			continue
		}
		if _, seated := f.input.Links[ticket.ID]; seated {
			continue
		}
		if _, failed := f.failed[ticket.ID]; failed {
			continue
		}
		return false
	}
	return true
}

// frontierDetailsMsg carries one logical plural answer from a Frontier
// generation. Cancellation stops Provider work when a seat ends; the generation
// guard separately prevents a raced or cancellation-ignoring answer from
// mutating its replacement. rawErr retains the one Provider aggregate before
// detailfanout.Run classifies cancellation and malformed batch output, and supplies
// the dismissible warning when an ordinary response-wide diagnostic accompanies a
// complete set of valid Details.
type frontierDetailsMsg struct {
	generation            int
	detailEvidenceVersion int
	outcomes              []detailfanout.Outcome
	rawErr                error
	err                   error
	requestPolicy         provider.RequestPolicy
}

const frontierRebuildDelay = time.Second / 60

// frontierRebuildMsg is immutable ownership evidence for the sole outstanding
// deferred canvas replacement tick.
type frontierRebuildMsg struct {
	timerID         int
	observedVersion int
}

// frontierLayoutForModel keeps layout observation instance-local. Nil is the
// production default for model-literal tests.
func (m Model) frontierLayoutForModel(g model.BlockingGraph, nodes []frontierNode,
	opts frontierLayoutOptions) frontierLayout {
	if m.layoutFrontierFn == nil {
		return layoutFrontier(g, nodes, opts)
	}
	return m.layoutFrontierFn(g, nodes, opts)
}

// discardPendingFrontierRebuild invalidates deferred work but deliberately
// retains the physical tick owner. Bubble Tea ticks cannot be cancelled; keeping
// ownership prevents a replacement request from stacking another one behind it.
func (m Model) discardPendingFrontierRebuild() Model {
	m.frontierRebuildVersion++
	m.frontierRebuildPendingVersion = 0
	m.frontierRebuildPendingGeneration = 0
	return m
}

func (m Model) armFrontierRebuildTimer() (Model, tea.Cmd) {
	if m.frontierRebuildTimerID != 0 || m.frontierRebuildPendingVersion == 0 {
		return m, nil
	}
	m.frontierRebuildNextTimerID++
	m.frontierRebuildTimerID = m.frontierRebuildNextTimerID
	timerID := m.frontierRebuildTimerID
	observedVersion := m.frontierRebuildPendingVersion
	return m, tea.Tick(frontierRebuildDelay, func(time.Time) tea.Msg {
		return frontierRebuildMsg{timerID: timerID, observedVersion: observedVersion}
	})
}

// requestFrontierRebuild records the newest visible incomplete evidence or
// width. It never materialises the canvas synchronously.
func (m Model) requestFrontierRebuild() (Model, tea.Cmd) {
	if m.mode != modeFrontier {
		return m.discardPendingFrontierRebuild(), nil
	}
	m.frontierRebuildVersion++
	m.frontierRebuildPendingVersion = m.frontierRebuildVersion
	m.frontierRebuildPendingGeneration = m.frontierGeneration
	return m.armFrontierRebuildTimer()
}

// onFrontierRebuild services exactly the quiet trailing edge of a request.
func (m Model) onFrontierRebuild(msg frontierRebuildMsg) (tea.Model, tea.Cmd) {
	if msg.timerID != m.frontierRebuildTimerID {
		return m, nil
	}
	m.frontierRebuildTimerID = 0
	if m.frontierRebuildPendingVersion == 0 ||
		m.frontierRebuildPendingGeneration != m.frontierGeneration ||
		m.mode != modeFrontier {
		return m.discardPendingFrontierRebuild(), nil
	}
	if msg.observedVersion != m.frontierRebuildPendingVersion {
		next, cmd := m.armFrontierRebuildTimer()
		return next, cmd
	}
	m.frontierRebuildPendingVersion = 0
	m.frontierRebuildPendingGeneration = 0
	m = m.rebuildFrontier()
	return m, repaint
}

// frontierEvidenceChanged preserves immediate state updates but chooses whether
// the expensive stored canvas is replaced now or at a quiet frame boundary.
func (m Model) frontierEvidenceChanged() (Model, tea.Cmd) {
	if m.mode != modeFrontier {
		if m.mode == modeDetail && m.detailReturn == modeFrontier && m.frontier.isResolved() {
			m.frontier.graph = model.BuildBlockingGraph(m.frontier.input.Tickets, m.frontier.input.Links,
				m.frontier.input.Capabilities)
			return m, repaint
		}
		return m, nil
	}
	if m.frontier.isResolved() {
		return m.rebuildFrontier(), repaint
	}
	return m.requestFrontierRebuild()
}

// linksFromCache builds the Links map from session Details eligible to be
// Watchlist evidence. It delegates the key-presence contract to detailfanout
// rather than open-coding it: only a Ticket whose Detail was actually read gets
// a key.
func (m Model) linksFromCache() map[model.TicketID][]model.Link {
	details := make(map[model.TicketID]model.Detail, len(m.details))
	for id, entry := range m.details {
		detail, eligible := entry.frontierLinks()
		if !eligible {
			continue
		}
		details[id] = detail
	}
	return detailfanout.Links(details)
}

// enterFrontier seats the Frontier on the current reading and starts the bulk
// Detail fan-out ADR-0003's Amendment 4 permits: an explicit user action, with
// a visible cost whose generation-scoped context the user can cancel.
func (m Model) enterFrontier() (tea.Model, tea.Cmd) {
	if !m.hasData {
		return m, nil
	}
	m = m.clearPendingClick()
	m = m.retireFrontierFanout()
	m.mouseEpoch++
	m.mode = modeFrontier
	m.frontier = frontierState{
		input:   FrontierFromList(m.input, m.linksFromCache()),
		focusID: m.selectedID,
	}

	if m.frontier.input.Capabilities.BlockingLinks {
		outstanding := detailfanout.Plan(m.frontier.input.Tickets, m.haveDetail)
		if len(outstanding) > 0 {
			next, policy, allowed := m.trackerRequestAdmission(trackerRequestExplicit)
			if allowed {
				m = next
				m.frontier.queued = outstanding
				m.frontier.planned = len(outstanding)
				m = m.startFrontierFanout(policy)
			} else {
				m = next
			}
		}
	}
	m = m.rebuildFrontier()
	// The footer changes shape on the way in, so the next frame is drawn whole.
	return m.issueFrontierFetches()
}

// haveDetail reports whether this session already read one Ticket's Detail. A
// Ticket opened earlier costs the Tracker nothing here.
func (m Model) haveDetail(id model.TicketID) bool {
	entry, hit := m.details[id]
	if !hit {
		return false
	}
	_, eligible := entry.frontierLinks()
	return eligible
}

// issueFrontierFetches adopts paid-for cache entries before dispatching all
// remaining canonical IDs in one logical plural command. Each generation uses
// one shared slot; retiring cancellation-ignoring generations therefore remain
// bounded by detailfanout.Parallelism without fragmenting native batches.
//
// It repaints because the footer and badges change the frame's shape as evidence
// lands. A fan-out still running behind an open Detail repaints nothing: that
// screen's shape did not move.
func (m Model) issueFrontierFetches() (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if m.mode == modeFrontier {
		cmds = append(cmds, repaint)
	}

	adopted := false
	remaining := make([]model.TicketID, 0, len(m.frontier.queued))
	for _, id := range m.frontier.queued {
		// The plan was made before these answers landed. A Ticket read since — by
		// a fan-out this seat replaced, or by hand — is already paid for.
		if entry, cached := m.details[id]; cached {
			if detail, eligible := entry.frontierLinks(); eligible {
				m = m.seatFanoutLinks(id, detail.Links)
				m.frontier.done++
				adopted = true
				continue
			}
		}
		remaining = append(remaining, id)
	}
	m.frontier.queued = remaining

	if len(m.frontier.queued) > 0 && m.detailFanoutInflight < detailfanout.Parallelism {
		if !m.frontierAdmissionReady {
			m.frontier.queued = nil
			m = m.finishFrontierFanout()
		} else {
			next, valid := m.trackerAdmissionStillValid(m.frontierRequestPolicy)
			m = next
			if !valid {
				m.frontier.queued = nil
				m = m.finishFrontierFanout()
			} else {
				ids := slices.Clone(m.frontier.queued)
				m.frontier.queued = nil
				m.frontierAdmissionReady = false
				m.detailFanoutInflight++
				cmds = append(cmds, m.frontierFetchCmd(
					m.frontierGeneration, m.detailEvidenceVersion, ids))
			}
		}
	}

	if adopted {
		m = m.settleFrontier()
		var rebuildCmd tea.Cmd
		m, rebuildCmd = m.frontierEvidenceChanged()
		cmds = append(cmds, rebuildCmd)
	}
	return m, tea.Batch(cmds...)
}

// seatFanoutLinks writes one Ticket's Links into the seated input. Key presence
// is the tri-state that makes fail-closed Actionable real, so this is the only
// way a Ticket becomes verified.
func (m Model) seatFanoutLinks(id model.TicketID, links []model.Link) Model {
	if m.frontier.input.Links == nil {
		m.frontier.input.Links = make(map[model.TicketID][]model.Link)
	}
	m.frontier.input.Links[id] = links
	return m
}

// refreshFrontierDetailEvidence folds a successful reread of a native Frontier
// member into the hidden Frontier seat. A followed Ghost or anonymous Detail is
// never evidence for a Watchlist member, and anonymous members are non-fetchable.
func (m Model) refreshFrontierDetailEvidence(links []model.Link) (Model, tea.Cmd) {
	if m.mode != modeDetail || m.detailReturn != modeFrontier || !m.detail.frontierMember || m.detail.ticket.ID == "" {
		return m, nil
	}
	id := m.detail.ticket.ID
	m = m.seatFanoutLinks(id, links)
	delete(m.frontier.failed, id)
	delete(m.frontier.failureErrors, id)
	m = m.reconcileFrontierFailureNotice()
	return m.frontierEvidenceChanged()
}

// reconcileFrontierFailureNotice keeps the detail line in the footer tied to a
// currently failed Ticket whenever per-Ticket error provenance is available.
func (m Model) reconcileFrontierFailureNotice() Model {
	if len(m.frontier.failed) == 0 {
		m.frontier.lastErr = nil
		return m
	}
	if _, err := m.frontierFailure(); err != nil {
		m.frontier.lastErr = err
	}
	return m
}

// frontierFailure returns one deterministic failed member and its error. The
// identity is kept separate from the error so the footer cannot imply that an
// arbitrary last error belongs to every failed Ticket.
func (m Model) frontierFailure() (model.TicketID, error) {
	ids := make([]model.TicketID, 0, len(m.frontier.failed))
	for id := range m.frontier.failed {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if err := m.frontier.failureErrors[id]; err != nil {
			return id, err
		}
	}
	return "", nil
}

func (m Model) frontierFailureKey(id model.TicketID) string {
	for _, ticket := range m.frontier.input.Tickets {
		if ticket.ID == id && ticket.Key != "" {
			return ticket.Key
		}
	}
	return "a Watchlist ticket"
}

// settleFrontier releases the child context once this seat's own plan has been
// answered in full, from the Tracker or session cache. Plan completion is not
// resolution: the latter is derived from full Watchlist Links-or-failure
// coverage. It counts seat bookkeeping rather than the shared in-flight counter
// because cancelled reads from an older generation may still be unwinding.
func (m Model) settleFrontier() Model {
	if m.frontier.done == m.frontier.planned && len(m.frontier.queued) == 0 {
		m = m.finishFrontierFanout()
	}
	return m
}

// frontierFetchCmd reads one canonical plural plan with the child context owned
// by this Frontier generation. It retains the sanitized raw Provider aggregate
// separately because detailfanout.Run may reduce a cancellation-racing response
// to control flow while later policy still needs the refusal evidence.
func (m Model) frontierFetchCmd(generation, detailEvidenceVersion int, ids []model.TicketID) tea.Cmd {
	fetch := m.fetchDetails
	ctx := m.frontierContext
	policy := m.frontierRequestPolicy
	runIDs := slices.Clone(ids)
	return func() tea.Msg {
		outcomes := make([]detailfanout.Outcome, 0, len(runIDs))
		var rawErr error
		runErr := detailfanout.Run(ctx, func(ctx context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
			details, caps, err := fetch(ctx, ids)
			err = detailfanout.NormalizeError(err)
			rawErr = err
			return details, caps, err
		}, runIDs, func(outcome detailfanout.Outcome) {
			outcome.Err = safeErr(outcome.Err)
			outcomes = append(outcomes, outcome)
		})
		return frontierDetailsMsg{
			generation: generation, detailEvidenceVersion: detailEvidenceVersion,
			outcomes: outcomes, rawErr: safeErr(rawErr), err: safeErr(runErr),
			requestPolicy: policy,
		}
	}
}

// onFrontierDetails folds one logical plural answer in. Every command releases
// exactly one shared slot. Stale successes may warm the session cache without
// overwriting newer evidence, and stale refusals still update Tracker-wide
// policy. The generation guard protects only per-seat progress, failures, Links,
// graph, and layout from raced or cancellation-ignoring answers.
func (m Model) onFrontierDetails(msg frontierDetailsMsg) (tea.Model, tea.Cmd) {
	msg.rawErr = safeDetailFanoutError(msg.rawErr)
	msg.err = safeDetailFanoutError(msg.err)
	requestErrors := make([]error, 0, len(msg.outcomes)+2)
	requestErrors = append(requestErrors, msg.rawErr)
	for index := range msg.outcomes {
		msg.outcomes[index].Err = safeDetailFanoutError(msg.outcomes[index].Err)
		requestErrors = append(requestErrors, msg.outcomes[index].Err)
	}
	requestErrors = append(requestErrors, msg.err)
	requestErr := errors.Join(requestErrors...)
	var refused bool
	m, refused = m.settleTrackerRequest(msg.requestPolicy, requestErr, requestErr == nil)
	m.detailFanoutInflight--

	stale := msg.generation != m.frontierGeneration

	protected := make(map[model.TicketID]bool)
	var acceptedAt time.Time
	for _, outcome := range msg.outcomes {
		outcome.Err = safeErr(outcome.Err)
		entry, cached := m.details[outcome.ID]
		if cached && entry.directDetailVersion > msg.detailEvidenceVersion {
			protected[outcome.ID] = true
			continue
		}
		if outcome.Err != nil {
			continue
		}
		if cached && entry.lastFanoutGeneration > msg.generation {
			protected[outcome.ID] = true
			continue
		}
		if acceptedAt.IsZero() {
			acceptedAt = m.now()
		}
		entry.detail = outcome.Detail
		entry.caps = outcome.Caps
		entry.fetchedAt = acceptedAt
		entry.frontierIneligible = false
		entry.frontierDetail = outcome.Detail
		entry.frontierFetchedAt = acceptedAt
		entry.frontierEvidence = true
		entry.lastFanoutGeneration = msg.generation
		m.details[outcome.ID] = entry
	}

	if stale {
		if refused {
			// The successful siblings above remain paid-for Detail evidence, but a
			// refusal from the abandoned command invalidates any queued admission on
			// the current seat without changing its progress, failures, or Links.
			if len(m.frontier.queued) > 0 {
				next, valid := m.trackerAdmissionStillValid(m.frontierRequestPolicy)
				m = next
				if !valid {
					m.frontier.queued = nil
					m = m.finishFrontierFanout()
				}
			}
			return m, nil
		}
		// An abandoned command released a shared slot. The current generation may
		// now adopt an accepted paid-for success or issue its distinct plural plan.
		return m.issueFrontierFetches()
	}
	// One generation owns one plural command. Its Provider work is over even when
	// cancellation emitted only a completed subset; clearing the child lifetime
	// leaves the unknown IDs visibly pending and available to an explicit retry.
	m = m.finishFrontierFanout()
	m.frontier.protocolWarning = nil

	m.frontier.done += len(msg.outcomes)
	outcomeFailures := 0
	for _, outcome := range msg.outcomes {
		err := safeErr(outcome.Err)
		if protected[outcome.ID] {
			// A newer singular read or accepted fan-out success owns every visible
			// and semantic outcome. This command still completes bookkeeping.
			continue
		}
		if err != nil {
			outcomeFailures++
			if m.frontier.failed == nil {
				m.frontier.failed = make(map[model.TicketID]struct{})
			}
			if m.frontier.failureErrors == nil {
				m.frontier.failureErrors = make(map[model.TicketID]error)
			}
			m.frontier.failed[outcome.ID] = struct{}{}
			m.frontier.failureErrors[outcome.ID] = err
			m.frontier.lastErr = err
			continue
		}
		m = m.seatFanoutLinks(outcome.ID, outcome.Detail.Links)
	}
	warningErr := msg.err
	if warningErr == nil && outcomeFailures == 0 && msg.rawErr != nil &&
		!errors.Is(msg.rawErr, context.Canceled) && !errors.Is(msg.rawErr, context.DeadlineExceeded) {
		if _, rateLimited := provider.InspectRateLimitRefusal(msg.rawErr, time.Time{}); !rateLimited {
			warningErr = msg.rawErr
		}
	}
	if warningErr != nil && !errors.Is(warningErr, context.Canceled) &&
		!errors.Is(warningErr, context.DeadlineExceeded) {
		if outcomeFailures == 0 {
			m.frontier.lastErr = nil
			m.frontier.protocolWarning = safeErr(warningErr)
		} else {
			m.frontier.lastErr = safeErr(warningErr)
		}
	}

	m = m.settleFrontier()
	m, rebuildCmd := m.frontierEvidenceChanged()
	next, fetchCmd := m.issueFrontierFetches()
	return next, tea.Batch(rebuildCmd, fetchCmd)
}

// adoptCachedLinks folds Details read since the fan-out into the seated input.
// One Ticket, one answer: a member whose fan-out read failed and was then
// opened by hand has Links in the session cache, and leaving the Frontier's own
// map without the key would keep the card UNVERIFIED and the footer claiming a
// read that has since succeeded could not be made — while the list, which reads
// the cache directly, draws the Ticket as Actionable.
func (m Model) adoptCachedLinks() Model {
	for _, t := range m.frontier.input.Tickets {
		if _, seated := m.frontier.input.Links[t.ID]; seated {
			continue
		}
		entry, cached := m.details[t.ID]
		if !cached {
			continue
		}
		detail, eligible := entry.frontierLinks()
		if !eligible {
			continue
		}
		m = m.seatFanoutLinks(t.ID, detail.Links)
		delete(m.frontier.failed, t.ID)
		delete(m.frontier.failureErrors, t.ID)
	}
	return m.reconcileFrontierFailureNotice()
}

// rebuildFrontier recomputes everything derived from the seated input. It owns
// the only production installation of a materialized canvas. Projection retains
// no dummy, segment, route, incident, stroke, hit-map, or grid arena; admission
// at this boundary precedes the transient route raster, node hit metadata, and
// cell grid allocations.
func (m Model) rebuildFrontier() Model {
	return m.rebuildFrontierWithRefusalFocus(false)
}

func (m Model) rebuildFrontierPreservingRefusalFocus() Model {
	return m.rebuildFrontierWithRefusalFocus(true)
}

func (m Model) rebuildFrontierWithRefusalFocus(preserveRefusalFocus bool) Model {
	m = m.discardPendingFrontierRebuild()
	preserveRefusalFocus = preserveRefusalFocus || m.frontier.retainRefusedFocus
	preferredFocus := m.frontier.focusID
	previous := indexOfNode(m.frontier.layout.order, preferredFocus)
	if preferredFocus == "" && !m.frontier.hasFocus {
		preferredFocus = m.selectedID
	}
	g := model.BuildBlockingGraph(m.frontier.input.Tickets, m.frontier.input.Links,
		m.frontier.input.Capabilities)
	nodes := frontierNodes(g, m.frontier.input.Tickets, m.frontier.isResolved(), m.frontier.failed)
	inner := m.frontierCanvasRectForNodes(nodes, preferredFocus)
	projection := projectFrontierRanks(g, nodes, inner.W, m.frontierDensity)
	candidates := projection.candidates
	direction := m.frontier.direction
	if !m.frontier.directionSet {
		direction = chooseFrontierDirection(candidates, inner.W, inner.H)
	} else {
		direction = frontierDirectionAfterResize(direction, candidates, inner.W, inner.H)
	}

	m.frontier.graph = g
	m.frontier.direction = direction
	m.frontier.directionSet = true
	m.mouseEpoch++
	if refusal := refuseFrontierCandidate(candidates[direction], len(nodes), len(g.Ghosts())); refusal != nil {
		m = m.clearPendingClick()
		m.frontier.layout = frontierLayout{}
		m.frontier.refusal = refusal
		m.frontier.retainRefusedFocus = preserveRefusalFocus
		if !preserveRefusalFocus {
			m.frontier.focusID = ""
			m.frontier.hasFocus = false
		}
		m.frontier.offsetX = 0
		m.frontier.offsetY = 0
		return m
	}

	m.frontier.refusal = nil
	m.frontier.retainRefusedFocus = false
	m.frontier.layout = m.frontierLayoutForModel(g, nodes, frontierLayoutOptions{
		innerWidth: inner.W,
		direction:  direction,
		density:    m.frontierDensity,
		projection: &projection,
	})

	l := m.frontier.layout
	m.frontier.focusID = preferredFocus
	if _, drawn := l.nodeAt[preferredFocus]; !drawn {
		// The focused Ticket left the Watchlist under a refresh. Focus lands on
		// the nearest node in canonical order rather than jumping to the top.
		m.frontier.hasFocus = false
		if len(l.order) > 0 {
			m.frontier.focusID = l.order[min(max(previous, 0), len(l.order)-1)]
			m.frontier.hasFocus = true
		}
	} else {
		m.frontier.hasFocus = true
	}
	if !m.effectiveFrontierKeys().Open.Enabled() {
		m.frontier.hasFocus = false
		return m.reconcileFrontier(false)
	}
	return m.reconcileFrontier(true)
}

type frontierDenseInspection uint8

const (
	frontierDenseInspectionNone frontierDenseInspection = iota
	frontierDenseInspectionSummary
	frontierDenseInspectionCard
)

func frontierNodeForInspection(nodes []frontierNode, focus model.TicketID) (frontierNode, bool) {
	for _, node := range nodes {
		if node.id == focus {
			return node, true
		}
	}
	if len(nodes) == 0 {
		return frontierNode{}, false
	}
	return nodes[0], true
}

func frontierDenseSummaryText(node frontierNode) string {
	switch {
	case node.key == "":
		return node.title
	case node.title == "":
		return node.key
	default:
		return node.key + " " + node.title
	}
}

func selectFrontierDenseInspection(node frontierNode, ok bool,
	width, height int) (frontierDenseInspection, frontierNode) {
	if !ok {
		return frontierDenseInspectionNone, frontierNode{}
	}
	if height >= frontierCardHeight+1 && frontierCardWidthFor(width)+2 <= width {
		return frontierDenseInspectionCard, node
	}
	summary := frontierDenseSummaryText(node)
	if height >= 2 && summary != "" && lipgloss.Width(summary) <= width {
		return frontierDenseInspectionSummary, node
	}
	return frontierDenseInspectionNone, node
}

func frontierDenseInspectionFor(nodes []frontierNode, focus model.TicketID,
	width, height int) (frontierDenseInspection, frontierNode) {
	node, ok := frontierNodeForInspection(nodes, focus)
	return selectFrontierDenseInspection(node, ok, width, height)
}

func frontierDenseInspectionForLayout(layout frontierLayout, focus model.TicketID,
	width, height int) (frontierDenseInspection, frontierNode) {
	node, ok := layout.nodes[focus]
	if !ok && len(layout.order) > 0 {
		node, ok = layout.nodes[layout.order[0]]
	}
	return selectFrontierDenseInspection(node, ok, width, height)
}

func frontierDenseInspectionHeight(kind frontierDenseInspection) int {
	switch kind {
	case frontierDenseInspectionCard:
		return frontierCardHeight
	case frontierDenseInspectionSummary:
		return 1
	default:
		return 0
	}
}

func (m Model) frontierCanvasRectForNodes(nodes []frontierNode, focus model.TicketID) frontierRect {
	height := m.frontierBodyHeight()
	if m.frontierDensity == frontierDensityDense {
		kind, _ := frontierDenseInspectionFor(nodes, focus, m.width, height)
		height -= frontierDenseInspectionHeight(kind)
	}
	return frontierInnerRect(m.width, max(height, 0))
}

func (m Model) frontierCanvasOuterHeight() int {
	height := m.frontierBodyHeight()
	if m.frontierDensity != frontierDensityDense {
		return height
	}
	kind, _ := frontierDenseInspectionForLayout(
		m.frontier.layout, m.frontier.focusID, m.width, height)
	available := max(height-frontierDenseInspectionHeight(kind), 0)
	if available == 0 || m.frontier.layout.height == 0 {
		return available
	}
	maxInner := frontierInnerRect(m.width, available)
	if m.frontier.layout.height <= maxInner.H {
		return min(available, m.frontier.layout.height+2)
	}
	return available
}

func (m Model) frontierCanvasRect() frontierRect {
	return frontierInnerRect(m.width, m.frontierCanvasOuterHeight())
}

func (m Model) frontierResizeNeedsDirectionFlip() bool {
	if !m.frontier.directionSet {
		return false
	}
	if m.frontier.refusal != nil {
		// A refused seat retains no candidate layout metadata. Re-projecting on a
		// height change is the only honest way to discover a newly fitting admitted
		// orientation, and still settles through #101's replacement boundary.
		return true
	}
	if !m.frontierShowsCanvas() {
		return false
	}
	inner := m.frontierCanvasRect()
	return frontierDirectionAfterResize(m.frontier.direction, m.frontier.layout.candidates,
		inner.W, inner.H) != m.frontier.direction
}

// reconcileFrontier clamps the window into the canvas, optionally bringing the
// focused card back into view first.
func (m Model) reconcileFrontier(ensureFocus bool) Model {
	inner := m.frontierCanvasRect()
	if ensureFocus && m.frontier.hasFocus {
		if rect, ok := m.frontier.layout.nodeAt[m.frontier.focusID]; ok {
			m.frontier.offsetX, m.frontier.offsetY = ensureNodeVisible(
				rect, m.frontier.offsetX, m.frontier.offsetY, inner.W, inner.H)
		}
	}
	m.frontier.offsetX = clampFrontierOffset(m.frontier.offsetX, m.frontier.layout.width, inner.W)
	m.frontier.offsetY = clampFrontierOffset(m.frontier.offsetY, m.frontier.layout.height, inner.H)
	return m
}

// indexOfNode is where id sits in canonical order, or -1.
func indexOfNode(order []model.TicketID, id model.TicketID) int {
	for i, candidate := range order {
		if candidate == id {
			return i
		}
	}
	return -1
}

// leaveFrontier returns to the list, cancels every issued read owned by this
// seat, and clears its unissued queue. The generation advance separately rejects
// any Provider outcome that races with cancellation.
func (m Model) leaveFrontier() (tea.Model, tea.Cmd) {
	m = m.clearPendingClick()
	m = m.retireFrontierFanout()
	m.mouseEpoch++
	m.mode = modeList
	// Selection survives the toggle in both directions. Only visible focus may
	// replace it: the no-Capability screen retains a hidden layout for internal
	// consistency, but must not move selection to a node the user never saw.
	if m.frontier.hasFocus {
		if row, ok := rowOf(m.rows, m.frontier.focusID); ok {
			m = m.selectRow(row)
		}
	}
	m.offset = ensureVisible(rowHeights(m.rows, m.input.Capabilities), m.selected, m.offset, m.bodyHeight())
	return m, repaint
}

// openFrontierNode opens the focused node's Detail: a member's exactly as the
// list does, and a Ghost Ticket's through the same deliberately thin seat a
// followed Link gets.
//
// The Frontier is a second rendering of the Watchlist rather than a Trail
// entry, so opening from it clears the Trail exactly as a list-open does and
// records the Frontier as where root esc returns to.
func (m Model) openFrontierNode() (tea.Model, tea.Cmd) {
	if !m.frontier.hasFocus {
		return m, nil
	}
	t, ok := m.frontierTicket(m.frontier.focusID)
	if !ok {
		return m, nil
	}
	m = m.clearPendingClick()
	m.trail = nil
	m.detailReturn = modeFrontier
	member := false
	for _, candidate := range m.frontier.input.Tickets {
		if candidate.ID == m.frontier.focusID {
			member = true
			break
		}
	}
	updated, cmd := m.seatDetail(t, m.frontier.input.Header, m.frontier.input.Capabilities, member)
	next := updated.(Model)
	next.detail.frontierMember = member
	return next, cmd
}

// frontierTicket resolves a node to the Ticket its Detail is seated from.
func (m Model) frontierTicket(id model.TicketID) (model.Ticket, bool) {
	for _, t := range m.frontier.input.Tickets {
		if t.ID == id {
			return t, true
		}
	}
	for _, ghost := range m.frontier.graph.Ghosts() {
		if ghost.Target.ID == id {
			return ticketFromLinkTarget(ghost.Target), true
		}
	}
	return model.Ticket{}, false
}

// refreshFrontier first reconciles paid-for cached reads into the current seat,
// then re-issues only the reads that never succeeded. Re-reading a warm cache
// would turn a recovery key into a fan-out, which is what Amendment 4 says must
// be deliberate; one Ticket's r in Detail remains the way to re-read one Ticket.
func (m Model) refreshFrontier() (tea.Model, tea.Cmd) {
	var outstanding []model.TicketID
	if m.frontier.input.Capabilities.BlockingLinks {
		outstanding = detailfanout.Plan(m.frontier.input.Tickets, m.haveDetail)
	}
	active := m.frontierContext != nil || len(m.frontier.queued) > 0
	if len(outstanding) > 0 && active {
		now := m.now()
		next := m.expireRatePolicy(now)
		if next.rateHold.active(now) {
			return next, nil
		}
		m = next
	}

	var policy provider.RequestPolicy
	if len(outstanding) > 0 && !active {
		next, admittedPolicy, allowed := m.trackerRequestAdmission(trackerRequestExplicit)
		if !allowed {
			return next, nil
		}
		m = next
		policy = admittedPolicy
	}

	// A completed protocol-only warning is dismissible state, not failed evidence.
	// Clear it before rebuilding so the footer height and canvas geometry settle in
	// the same frame as the r action.
	m.frontier.protocolWarning = nil
	seated := len(m.frontier.input.Links)
	m = m.adoptCachedLinks()
	if len(m.frontier.input.Links) > seated {
		m.mouseEpoch++
	}
	m = m.rebuildFrontier()
	if !m.frontier.input.Capabilities.BlockingLinks {
		return m, repaint
	}
	if len(outstanding) == 0 {
		// Every requested Detail is already warm. The r action may have dismissed a
		// completed protocol warning, but it must never duplicate that paid-for I/O.
		m.frontier.lastErr = nil
		return m, repaint
	}
	if active {
		return m, repaint
	}

	m = m.retireFrontierFanout()
	m.frontier.queued = outstanding
	m.frontier.planned = len(outstanding)
	m.frontier.done = 0
	m.frontier.failed = nil
	m.frontier.failureErrors = nil
	m.frontier.lastErr = nil
	m = m.startFrontierFanout(policy)
	m = m.rebuildFrontier()
	return m.issueFrontierFetches()
}

// onFrontierKey dispatches a key press on the Frontier. Neither d nor / is
// bound here: filters do not apply to this screen and the footer says so.
func (m Model) onFrontierKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	keys := m.effectiveFrontierKeys()
	switch {
	case key.Matches(msg, keys.Quit):
		return m.quit(msg), tea.Quit

	case key.Matches(msg, keys.Toggle):
		return m.leaveFrontier()

	case key.Matches(msg, keys.Density):
		m.frontierDensity = m.frontierDensity.toggled()
		m.frontier.directionSet = false
		return m.rebuildFrontierPreservingRefusalFocus(), repaint

	case key.Matches(msg, keys.Open):
		return m.openFrontierNode()

	case key.Matches(msg, keys.Refresh):
		return m.refreshFrontier()

	case key.Matches(msg, keys.ToggleMouse):
		return m.toggleMouse(), nil

	case key.Matches(msg, keys.Legend):
		m.legendVisible = !m.legendVisible
		return m, repaint

	case key.Matches(msg, keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		return m.reconcileFrontier(true), nil

	case key.Matches(msg, keys.Up):
		return m.moveFrontierFocus(0, -1), nil
	case key.Matches(msg, keys.Down):
		return m.moveFrontierFocus(0, 1), nil
	case key.Matches(msg, keys.Left):
		return m.moveFrontierFocus(-1, 0), nil
	case key.Matches(msg, keys.Right):
		return m.moveFrontierFocus(1, 0), nil
	case key.Matches(msg, keys.PageUp):
		return m.pageFrontier(-1), nil
	case key.Matches(msg, keys.PageDown):
		return m.pageFrontier(1), nil
	case key.Matches(msg, keys.Home):
		return m.focusFrontierNodeAt(0), nil
	case key.Matches(msg, keys.End):
		return m.focusFrontierNodeAt(len(m.frontier.layout.order) - 1), nil
	}
	return m, nil
}

func (m Model) moveFrontierFocus(dx, dy int) Model {
	if !m.frontier.hasFocus {
		return m
	}
	next, ok := m.frontier.layout.moveFocus(m.frontier.focusID, dx, dy)
	if !ok {
		return m
	}
	m.frontier.focusID = next
	return m.reconcileFrontier(true)
}

// pageFrontier moves the canvas window by the current inner-canvas height.
func (m Model) pageFrontier(pages int) Model {
	m.frontier.offsetY += pages * m.frontierCanvasRect().H
	return m.reconcileFrontier(false)
}

func (m Model) focusFrontierNodeAt(i int) Model {
	if i < 0 || i >= len(m.frontier.layout.order) {
		return m
	}
	m.frontier.focusID = m.frontier.layout.order[i]
	m.frontier.hasFocus = true
	return m.reconcileFrontier(true)
}

// frontierFrame renders the whole screen: a header that never scrolls, the
// canvas window, and a footer that says what this screen is not showing.
func (m Model) frontierFrame() string {
	return m.frontierFrameWithCycles(m.frontierCyclesForFrame())
}

func (m Model) frontierFrameWithCycles(cycles [][]model.TicketID) string {
	footer := m.frontierFooterLines()
	body := m.frontierBodyWithCycles(m.frontierBodyHeight(), cycles)
	return strings.Join(append([]string{m.frontierHeaderWithCycles(cycles)}, append(body, footer...)...), "\n")
}

func (m Model) frontierCyclesForFrame() [][]model.TicketID {
	if !m.frontierShowsCanvas() {
		return nil
	}
	return m.frontier.graph.Cycles()
}

func (m Model) frontierRetryBlocked() bool {
	if !m.frontier.input.Capabilities.BlockingLinks || m.frontier.protocolWarning != nil {
		return false
	}
	return !m.trackerExplicitRequestAvailable()
}

// frontierHeader is the same three lines the list's header occupies, so the
// body arithmetic is shared: identity, the graph's counts, and a blank.
func (m Model) frontierHeader() string {
	return m.frontierHeaderWithCycles(m.frontierCyclesForFrame())
}

func (m Model) frontierHeaderWithCycles(cycles [][]model.TicketID) string {
	f := m.frontier
	nodes := len(f.layout.order)
	ghosts := len(f.graph.Ghosts())
	if f.refusal != nil {
		nodes = f.refusal.nodes
		ghosts = f.refusal.ghosts
	}

	var counts string
	switch {
	case !f.input.Capabilities.BlockingLinks:
		counts = "frontier" + separator + "this Provider reports no blocking links"
	case f.refusal != nil:
		counts = "frontier" + separator + plural(nodes, "node", "nodes")
		if ghosts > 0 {
			counts += separator + plural(ghosts, "ghost", "ghosts")
		}
		counts += separator + "canvas refused"
		switch {
		case f.isResolved():
			counts += separator + "Details complete"
		case m.frontierContext != nil:
			counts += separator + "reading Details…"
		default:
			counts += separator + "Details pending"
		}
	case f.isResolved():
		// Counted over the layout's order rather than the graph's members: a
		// Watchlist may name one Ticket twice, and a header claiming more
		// Actionable Tickets than there are cards is not a count of anything.
		//
		// Only members whose Links were readable are counted, and when no
		// member's were the count is withheld outright. "0 actionable" is a
		// computed answer, and a Frontier where nothing could be computed must
		// not be byte-identical to one where everything is genuinely blocked.
		actionable, known := 0, 0
		for _, id := range f.layout.order {
			a, member := f.graph.For(id)
			if !member || !a.LinksKnown {
				continue
			}
			known++
			if a.Actionable {
				actionable++
			}
		}
		counts = m.frontierCycleHeaderCountsWithCycles(nodes, ghosts, actionable, known, cycles)
	case m.frontierContext != nil:
		counts = fmt.Sprintf("frontier%s%s%sreading Details…",
			separator, plural(nodes, "node", "nodes"), separator)
	default:
		status := "Details pending · press r"
		if m.frontierRetryBlocked() {
			status = "Details pending · r held"
		}
		counts = fmt.Sprintf("frontier%s%s%s%s",
			separator, plural(nodes, "node", "nodes"), separator, status)
	}

	counts = rateLimitHeader(f.input.Capabilities, f.input.RateLimitBudget).
		appendToLine(counts, m.staleness(), m.width)

	return strings.Join([]string{
		headerIdentity(f.input.Header, m.width, m.styles),
		// The staleness clock is reserved rather than truncated away: how old
		// this reading is answers "why does it say that", and a counts line
		// clipped mid-word answers nothing. The list header makes the same
		// trade.
		pairLineReserved(m.styles.Counts.Render(counts),
			m.styles.Staleness.Render(m.staleness()), m.width),
		"",
	}, "\n")
}

// frontierBody draws the canvas window, or the state that stands in for it.
func (m Model) frontierBody(height int) []string {
	return m.frontierBodyWithCycles(height, m.frontierCyclesForFrame())
}

func (m Model) frontierBodyWithCycles(height int, cycles [][]model.TicketID) []string {
	switch {
	case !m.frontier.input.Capabilities.BlockingLinks:
		// No fetch was issued at all: a screen that cannot draw a graph must not
		// pay for one.
		return padLines(m.wrappedMuted(
			"This Provider does not report blocking links, so sitrep cannot tell which "+
				"Tickets are blocked or which can be picked up. The Watchlist itself is "+
				"unaffected — press v or esc to go back to it."), height)
	case m.frontier.refusal != nil:
		refusal := m.frontier.refusal
		return padLines(m.wrappedMuted(fmt.Sprintf(
			"The selected Frontier canvas projects to %d × %d cells (%s projected cells), "+
				"above the permitted 500,000 cells. sitrep refused the entire canvas without "+
				"clipping: no Frontier graph or subset was drawn, while the fetched Watchlist "+
				"evidence remains intact. On linux/amd64, the raw frontierCell grid payload for "+
				"500,000 cells is 24,000,000 bytes only. Row slices, projection metadata, "+
				"transient route rasterization, Go/runtime overhead, and total process memory "+
				"are additional and architecture-dependent. Press v or esc to return to the Watchlist.",
			refusal.width, refusal.height, refusal.projectedCells)), height)
	case len(m.frontier.input.Tickets) == 0:
		return padLines([]string{m.styles.Muted.Render("This Watchlist has no Tickets.")}, height)
	}
	// A graph with nodes but no edges is not an error state: every Todo node
	// whose Links were readable is then Actionable, which is the truth.
	if m.frontierDensity == frontierDensityDense {
		return m.frontierDenseBody(height, cycles)
	}
	body := renderFrontierBody(m.frontier.layout, m.frontier.focusID, m.frontier.hasFocus,
		m.frontier.offsetX, m.frontier.offsetY, m.width, height, m.styles)
	return m.frontierLegendBody(body, height, cycles)
}

func (m Model) frontierDenseBody(height int, cycles [][]model.TicketID) []string {
	canvasHeight := m.frontierCanvasOuterHeight()
	body := renderFrontierBody(m.frontier.layout, m.frontier.focusID, m.frontier.hasFocus,
		m.frontier.offsetX, m.frontier.offsetY, m.width, canvasHeight, m.styles)

	kind, node := frontierDenseInspectionForLayout(
		m.frontier.layout, m.frontier.focusID, m.width, height)
	switch kind {
	case frontierDenseInspectionCard:
		indent := frontierInnerRect(m.width, height).X
		for _, line := range renderFrontierInspection(node, m.frontier.graph, m.width, m.styles) {
			body = append(body, strings.Repeat(" ", indent)+line)
		}
	case frontierDenseInspectionSummary:
		var summary strings.Builder
		category := m.styles.frontierStyle(frontierStyle{Role: frontierRoleCategory, Category: node.status})
		if node.key != "" {
			summary.WriteString(renderHyperlink(category, node.key, node.url))
		}
		if node.key != "" && node.title != "" {
			summary.WriteByte(' ')
		}
		if node.title != "" {
			summary.WriteString(renderHyperlink(
				m.styles.frontierStyle(frontierStyle{Role: node.emphasis.titleRole}), node.title, node.url))
		}
		body = append(body, summary.String())
	}
	primaryEnd := len(body)
	body = padLines(body, height)
	return m.frontierDenseLegendBody(body, height, canvasHeight, primaryEnd, kind, cycles)
}

func (m Model) frontierDenseLegendBody(lines []string, height, canvasHeight, primaryEnd int,
	kind frontierDenseInspection, cycles [][]model.TicketID) []string {
	if !m.legendVisible || kind == frontierDenseInspectionNone ||
		m.frontier.offsetX != 0 || m.frontier.offsetY != 0 {
		return lines
	}
	inner := frontierInnerRect(m.width, canvasHeight)
	layout := m.frontier.layout
	if layout.width > inner.W || layout.height > inner.H {
		return lines
	}
	legendCycles := cycles
	if !m.frontier.isResolved() {
		legendCycles = nil
	}
	var legendKeys map[model.TicketID]string
	if len(legendCycles) > 0 {
		legendKeys = frontierLegendKeysForLayout(layout)
	}
	legend := frontierDenseLegendLinesForLayout(layout, legendCycles, legendKeys, inner.W)
	startY := primaryEnd + 1
	if len(legend) == 0 || startY+len(legend) > height {
		return lines
	}
	for i, line := range legend {
		lines[startY+i] = strings.Repeat(" ", inner.X) + m.styles.Muted.Render(line)
	}
	return lines
}

func (m Model) wrappedMuted(text string) []string {
	lines := wrapText(text, m.width)
	for i, line := range lines {
		lines[i] = m.styles.Muted.Render(truncateLine(line, m.width))
	}
	return lines
}

// padLines grows a block to exactly height lines so the footer stays at the
// bottom of the screen.
func padLines(lines []string, height int) []string {
	if height <= 0 {
		return nil
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// frontierFooterLines is the Frontier's bottom block with the scroll position
// written onto it. The position is an overlay rather than a line of its own,
// which is what lets frontierFooterHeight measure the block instead of
// hand-counting it.
func (m Model) frontierFooterLines() []string {
	lines := m.frontierFooterBlock()
	if pos := m.frontierScrollPosition(); pos != "" {
		last := len(lines) - 1
		position := m.styles.Staleness.Render(pos)
		if lipgloss.Width(lines[last])+1+lipgloss.Width(position) <= m.width {
			lines[last] = pairLineReserved(lines[last], position, m.width)
		} else {
			lines[0] = pairLineReserved(lines[0], position, m.width)
		}
	}
	return lines
}

// frontierFooterBlock is every footer line the screen's own state produces: the
// spacer, the notices, and the help listing. It never consults the canvas, so
// frontierBodyHeight can measure it without asking the canvas how tall it is.
//
// The filter notice is mandatory whenever a filter is on: hidden work that looks
// like missing work is this feature's failure mode, and here it would also
// delete edges.
func (m Model) frontierFooterBlock() []string {
	lines := []string{""}
	policy := m.trackerHoldNotice()
	if m.filter.Active() {
		lines = append(lines, m.styles.Muted.Render(truncateLine(
			"filters do not apply here: the Frontier renders the whole Watchlist", m.width)))
	}
	if failed := len(m.frontier.failed); m.frontier.isResolved() && failed > 0 {
		notice := detailfanout.UnreadableLinksNotice(failed)
		lines = append(lines, m.styles.Error.Render(balancedTruncate(notice, m.width, "…")))
	}
	if failedID, failedErr := m.frontierFailure(); failedErr != nil {
		// One currently failed Ticket is named as an example, rather than as the
		// cause of everything on screen. Tracker-wide policy owns rate-refusal
		// wording, so preserve unrelated failed evidence but do not duplicate it.
		if policy == "" || !m.rateRefusal(failedErr) {
			remedy := "press r to retry the Tickets that failed"
			if m.frontierRetryBlocked() {
				remedy = "retry held by Tracker request policy"
			}
			message := fmt.Sprintf("read failed for %s: %s — %s",
				m.frontierFailureKey(failedID), failedErr.Error(), remedy)
			if m.frontier.refusal != nil {
				message = fmt.Sprintf("read failed for %s: %s — the fetched evidence remains recorded",
					m.frontierFailureKey(failedID), failedErr.Error())
			}
			lines = append(lines, m.styles.Error.Render(balancedTruncate(message, m.width, "…")))
		}
	} else if m.frontier.protocolWarning != nil {
		if policy == "" || !m.rateRefusal(m.frontier.protocolWarning) {
			remedy := "press r to dismiss warning"
			if m.frontierRetryBlocked() {
				remedy = "warning dismissal held by Tracker request policy"
			}
			message := fmt.Sprintf("provider response warning: %s — %s",
				m.frontier.protocolWarning.Error(), remedy)
			if m.frontier.refusal != nil {
				message = fmt.Sprintf("provider response warning: %s — the fetched evidence remains recorded",
					m.frontier.protocolWarning.Error())
			}
			lines = append(lines, m.styles.Error.Render(balancedTruncate(message, m.width, "…")))
		}
	} else if m.frontier.lastErr != nil && (policy == "" || !m.rateRefusal(m.frontier.lastErr)) {
		remedy := "press r to retry the Tickets that failed"
		if m.frontierRetryBlocked() {
			remedy = "retry held by Tracker request policy"
		}
		message := fmt.Sprintf("read failed: %s — %s",
			m.frontier.lastErr.Error(), remedy)
		if m.frontier.refusal != nil {
			message = fmt.Sprintf("read failed: %s — the fetched evidence remains recorded",
				m.frontier.lastErr.Error())
		}
		lines = append(lines, m.styles.Error.Render(balancedTruncate(message, m.width, "…")))
	}
	if policy != "" {
		lines = append(lines, m.styles.Error.Render(truncateLine(policy, m.width)))
	}

	return append(lines, strings.Split(truncateBlock(m.help.View(m.helpKeys()), m.width), "\n")...)
}

// frontierShowsCanvas reports whether the body is the graph rather than one of
// the states that stands in for it.
func (m Model) frontierShowsCanvas() bool {
	return m.frontier.input.Capabilities.BlockingLinks &&
		len(m.frontier.input.Tickets) > 0 && m.frontier.refusal == nil
}

// frontierScrollPosition reports where the window sits on a canvas bigger than
// it, one axis per direction that actually has somewhere to go. A canvas that
// fits, or a body that is not a canvas at all, reports nothing.
//
// The axes are labelled x and y, in character cells. "col" would collide with
// the graph columns the blocker-side and dependent-side keys move between,
// which are counted in nodes and are not what this reports.
func (m Model) frontierScrollPosition() string {
	if !m.frontierShowsCanvas() {
		return ""
	}
	l := m.frontier.layout
	inner := m.frontierCanvasRect()
	var parts []string
	if l.width > inner.W {
		parts = append(parts, fmt.Sprintf("x %d/%d", m.frontier.offsetX, l.width-inner.W))
	}
	if l.height > inner.H {
		parts = append(parts, fmt.Sprintf("y %d/%d", m.frontier.offsetY, l.height-inner.H))
	}
	return strings.Join(parts, separator)
}

// frontierFooterHeight is how many lines the footer occupies. Measuring the
// block would recurse — the canvas needs the height to know its own size, and
// the scroll position needs the canvas — so the block is built without the
// scroll-position overlay, which is what needs the canvas and which never adds
// a line: it is written onto a line the footer already has.
func (m Model) frontierFooterHeight() int {
	return len(m.frontierFooterBlock())
}

// frontierBodyHeight is the non-negative room left for the canvas once the
// header and footer have taken theirs. A positive remainder keeps its full
// extent; no synthetic body row is created when none remains.
func (m Model) frontierBodyHeight() int {
	return max(m.height-headerHeight-m.frontierFooterHeight(), 0)
}
