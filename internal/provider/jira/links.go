package jira

import (
	"context"
	"errors"
	"strings"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
)

// linkType is one of the instance's issue link types, classified: sitrep's Kind
// for each direction plus the instance's own wording for it.
type linkType struct {
	inwardKind  model.LinkKind
	outwardKind model.LinkKind
	inward      string
	outward     string
}

type linkTypesState uint8

const (
	linkTypesUninitialized linkTypesState = iota
	linkTypesLoading
	linkTypesSuccess
	linkTypesOrdinaryFallback
	linkTypesRateBarrier
)

type linkTypesAttempt struct {
	done    chan struct{}
	barrier error
}

// linkTypes returns the instance's coalesced issue link type catalogue.
// Discovery stays lazy so constructors and Resolve remain free of this Detail
// cost. A successful catalogue and an ordinary-failure inline-label fallback
// are terminal for the Provider session. A rate refusal instead forms a barrier:
// callers admitted in the refusing or an older policy epoch share it without
// I/O. A caller already waiting on the refusing attempt shares that barrier
// regardless of epoch; only a fresh entry may use newer admission to replace it.
// A known future deadline blocks every epoch until it expires; after that, as for
// an unknown reset, only a strictly newer admitted epoch may replace the retained
// barrier.
func (p *Provider) linkTypes(ctx context.Context) (map[string]linkType, error) {
	policy := provider.RequestPolicyFromContext(ctx)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p.linkTypesMu.Lock()
		switch p.linkTypesState {
		case linkTypesUninitialized:
			if p.linkTypesBarrier != nil && policy.Epoch <= p.linkTypesBarrierEpoch {
				err := p.linkTypesBarrier
				p.linkTypesMu.Unlock()
				return nil, err
			}
			attempt := p.startLinkTypesAttemptLocked()
			p.linkTypesMu.Unlock()
			return p.loadLinkTypes(ctx, policy, attempt)

		case linkTypesLoading:
			attempt := p.linkTypesAttempt
			p.linkTypesMu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-attempt.done:
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if attempt.barrier != nil {
					return nil, attempt.barrier
				}
				continue
			}

		case linkTypesSuccess:
			if p.linkTypesBarrier != nil && policy.Epoch <= p.linkTypesBarrierEpoch {
				err := p.linkTypesBarrier
				p.linkTypesMu.Unlock()
				return nil, err
			}
			types := p.linkTypesCache
			p.linkTypesMu.Unlock()
			return types, nil

		case linkTypesOrdinaryFallback:
			if p.linkTypesBarrier != nil && policy.Epoch <= p.linkTypesBarrierEpoch {
				err := p.linkTypesBarrier
				p.linkTypesMu.Unlock()
				return nil, err
			}
			p.linkTypesMu.Unlock()
			return nil, nil

		case linkTypesRateBarrier:
			if (p.linkTypesBarrierReset.IsZero() || !p.now().Before(p.linkTypesBarrierReset)) &&
				policy.Epoch > p.linkTypesBarrierEpoch {
				attempt := p.startLinkTypesAttemptLocked()
				p.linkTypesMu.Unlock()
				return p.loadLinkTypes(ctx, policy, attempt)
			}
			err := p.linkTypesBarrier
			p.linkTypesMu.Unlock()
			return nil, err
		}
	}
}

func (p *Provider) startLinkTypesAttemptLocked() *linkTypesAttempt {
	attempt := &linkTypesAttempt{done: make(chan struct{})}
	p.linkTypesState = linkTypesLoading
	p.linkTypesAttempt = attempt
	return attempt
}

func retainedLinkTypesBarrier(refusal provider.RateLimitRefusal) error {
	if !refusal.KnownReset {
		return refusal.Err
	}
	classified := refusal.Err.(*provider.Error) //nolint:errorlint // InspectRateLimitRefusal returns its direct classified representative.
	return &provider.Error{
		Kind: classified.Kind,
		Err:  classified.Err,
		RateLimit: provider.RateLimitMetadata{
			ResetAt: refusal.ResetAt.UTC(),
		},
	}
}

func (p *Provider) loadLinkTypes(ctx context.Context, policy provider.RequestPolicy, attempt *linkTypesAttempt) (map[string]linkType, error) {
	var resp linkTypesResponse
	err := p.do(ctx, linkTypesPath, nil, "the issue link types", &resp)
	var types map[string]linkType
	if err == nil {
		types = make(map[string]linkType, len(resp.IssueLinkTypes))
		for _, candidate := range resp.IssueLinkTypes {
			if candidate.ID != "" {
				types[candidate.ID] = newLinkType(candidate)
			}
		}
	}
	refusal, rateLimited := provider.InspectRateLimitRefusal(err, p.now())

	p.linkTypesMu.Lock()
	switch {
	case err == nil:
		p.linkTypesState = linkTypesSuccess
		p.linkTypesCache = types
		p.linkTypesErr = nil
	case rateLimited:
		// Even an already-elapsed refusal stops this paid-for operation and
		// remains an epoch barrier. The Model advances its admission epoch after
		// observing the refusal; only that freshly authorized entry may replace
		// the attempt, while current waiters share this exact evidence.
		p.linkTypesState = linkTypesRateBarrier
		p.linkTypesCache = nil
		p.linkTypesErr = nil
		p.linkTypesBarrier = retainedLinkTypesBarrier(refusal)
		p.linkTypesBarrierEpoch = policy.Epoch
		p.linkTypesBarrierReset = refusal.ResetAt
		attempt.barrier = p.linkTypesBarrier
	case ctx.Err() != nil:
		p.linkTypesState = linkTypesUninitialized
	case err != nil:
		p.linkTypesState = linkTypesOrdinaryFallback
		p.linkTypesCache = nil
		p.linkTypesErr = err
	}
	p.linkTypesAttempt = nil
	close(attempt.done)
	p.linkTypesMu.Unlock()

	switch {
	case err == nil:
		return types, nil
	case ctx.Err() != nil && rateLimited:
		return nil, errors.Join(ctx.Err(), refusal.Err)
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case rateLimited:
		return nil, refusal.Err
	default:
		return nil, nil
	}
}

// newLinkType classifies one wire link type and keeps the instance's own
// wording alongside sitrep's Kinds, because the wording is what the renderer
// shows.
func newLinkType(t linkTypeWire) linkType {
	inward, outward := classify(t)
	return linkType{
		inwardKind:  inward,
		outwardKind: outward,
		inward:      t.Inward,
		outward:     t.Outward,
	}
}

// classify maps one of the instance's issue link types onto sitrep's LinkKinds.
// Jira lets an administrator rename any link type, so the wording is what is
// examined, not the id: a type whose directional phrasing speaks of blocking is
// a blocking type whatever it has been renamed to, and everything else is
// Relates — displayed through the instance's own label rather than dropped.
//
// Each direction is read in its own voice, because an administrator is free to
// swap them. "is blocked by" is passive and means that end is BlockedBy;
// "blocks" is active and means that end is Blocks. Whichever direction speaks
// first decides, and the other end takes the complement — a type cannot have
// both ends blocking the same way. Only when the directional phrases say
// nothing and the *name* mentions blocking does this fall back to Jira's
// built-in orientation, inward "is blocked by" / outward "blocks".
//
// Never key this on a numeric id such as 10000: ids differ per instance.
func classify(t linkTypeWire) (inward, outward model.LinkKind) {
	if kind, ok := blockingKind(t.Inward); ok {
		return kind, complementOf(kind)
	}
	if kind, ok := blockingKind(t.Outward); ok {
		return complementOf(kind), kind
	}
	if mentionsBlocking(t.Name) {
		return model.LinkBlockedBy, model.LinkBlocks
	}
	// Relates, Duplicate, Cloners, Causes and any invented type all land here.
	// LinkRelates is the model's zero value on purpose: the fallback is the
	// default, not a special case.
	return model.LinkRelates, model.LinkRelates
}

// blockingKind reads one direction's phrase in its own voice: "blocked" is
// passive and names the end that is being blocked, and "block" without it is
// active. A phrase that says nothing about blocking reports false.
func blockingKind(phrase string) (model.LinkKind, bool) {
	normalized := normalizePhrase(phrase)
	switch {
	case strings.Contains(normalized, "blocked"):
		return model.LinkBlockedBy, true
	case strings.Contains(normalized, "block"):
		return model.LinkBlocks, true
	default:
		return model.LinkRelates, false
	}
}

// complementOf is the other end of a blocking relationship.
func complementOf(kind model.LinkKind) model.LinkKind {
	if kind == model.LinkBlockedBy {
		return model.LinkBlocks
	}
	return model.LinkBlockedBy
}

// mentionsBlocking reports whether a phrase speaks of blocking, ignoring case
// and whatever whitespace an administrator typed.
func mentionsBlocking(phrase string) bool {
	return strings.Contains(normalizePhrase(phrase), "block")
}

// normalizePhrase lower-cases a phrase and collapses whatever whitespace an
// administrator typed.
func normalizePhrase(phrase string) string {
	return strings.ToLower(strings.Join(strings.Fields(phrase), " "))
}

// newLinks maps an issue's links onto sitrep's, in Jira's own order within
// issuelinks. Nothing is sorted here: the model never reorders and the renderer
// aligns whatever it is given. (The GitHub driver's blocked-by-then-blocks
// ordering is a consequence of its two separate connections, not a rule the
// model imposes.)
//
// types is the discovered catalogue, which may be nil when discovery failed.
func newLinks(types map[string]linkType, entries []issueLinkWire, host string) []model.Link {
	if len(entries) == 0 {
		return nil
	}

	links := make([]model.Link, 0, len(entries))
	for _, entry := range entries {
		lt, ok := types[entry.Type.ID]
		if !ok {
			// A type created after discovery, or discovery that failed: Jira
			// inlines the whole type object on every entry, so the fallback is
			// exact rather than a guess.
			lt = newLinkType(entry.Type)
		}

		// An entry carries either outwardIssue or inwardIssue, never both.
		// outwardIssue means *this* Ticket points outward at the target ("blocks
		// PROJ-9"); inwardIssue means the target points at this Ticket ("is
		// blocked by PROJ-9"). Getting this backwards inverts every dependency on
		// screen.
		switch {
		case entry.OutwardIssue != nil:
			links = append(links, newLink(lt.outwardKind, lt.outward, *entry.OutwardIssue, host))
		case entry.InwardIssue != nil:
			links = append(links, newLink(lt.inwardKind, lt.inward, *entry.InwardIssue, host))
		default:
			// A link whose far end the user cannot see comes back with neither
			// side. It names nothing that can be displayed or navigated to, so it
			// is skipped rather than rendered as a blank row.
			continue
		}
	}
	if len(links) == 0 {
		return nil
	}
	return links
}

// newLink maps one linked issue onto a Link. The target runs through the same
// normalizeStatus every Ticket does, so a linked Ticket's Native Status reads in
// the links table exactly as it does in the list.
func newLink(kind model.LinkKind, label string, target issueWire, host string) model.Link {
	status, native := normalizeStatus(
		target.Fields.categoryKey(), target.Fields.statusName(), target.Fields.resolutionName())
	return model.Link{
		Kind:        kind,
		NativeLabel: label,
		Target: model.LinkTarget{
			ID:           model.TicketID(target.Key),
			Key:          target.Key,
			Title:        target.Fields.Summary,
			URL:          browseURL(host, target.Key),
			Status:       status,
			NativeStatus: native,
		},
	}
}
