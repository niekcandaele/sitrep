package jira

import (
	"context"
	"strings"

	"github.com/niekcandaele/sitrep/internal/model"
)

// linkType is one of the instance's issue link types, classified: sitrep's Kind
// for each direction plus the instance's own wording for it.
type linkType struct {
	inwardKind  model.LinkKind
	outwardKind model.LinkKind
	inward      string
	outward     string
}

// linkTypes returns the instance's issue link type catalogue, discovered once
// per process and read-only afterwards.
//
// Discovery is lazy, on the first FetchDetail, rather than in the constructor:
// construction must stay free of side effects (`sitrep --help` may not call
// Jira) and FetchEpic is the polled hot path, which must not carry a catalogue
// request. Once per process, before the first link is mapped, is the only
// moment that satisfies both — do not "fix" this into a constructor request.
//
// A failed discovery is not a failed Detail. The error is recorded and the
// caller falls back to the type object Jira inlines on every issuelinks entry:
// the catalogue's job is consistency, not availability, and a Detail rendering
// its links through their inline labels is strictly better than one that
// refuses to open.
func (p *Provider) linkTypes(ctx context.Context) (map[string]linkType, error) {
	p.linkTypesOnce.Do(func() {
		var resp linkTypesResponse
		if err := p.do(ctx, linkTypesPath, nil, "the issue link types", &resp); err != nil {
			p.linkTypesErr = err
			return
		}
		types := make(map[string]linkType, len(resp.IssueLinkTypes))
		for _, t := range resp.IssueLinkTypes {
			if t.ID == "" {
				continue
			}
			types[t.ID] = newLinkType(t)
		}
		p.linkTypesCache = types
	})
	return p.linkTypesCache, p.linkTypesErr
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
