// This file maps GET /projects/:id/issues/:issue_iid/links onto model.Link.
//
// The endpoint carried a reputation worth retiring: gitlab-org/gitlab issues
// #271168, #299444, #365796 and #412835 all report is_blocked_by misbehaving.
// Every one of them is about *creating* a link — POST .../links with
// link_type=is_blocked_by answering 404 or 400 — and they were fixed by MR
// !110067. The read path carries none of those defects, is Free tier, and is not
// deprecated. sitrep only ever GETs (ADR-0002), so the risk does not apply to
// this driver and does not need re-litigating.
//
// What remains is an ordinary unknown: a link_type value sitrep has never heard
// of, which falls back to Relates displaying GitLab's own wording.

package gitlab

import (
	"strings"

	"github.com/niekcandaele/sitrep/internal/model"
)

// GitLab's link_type values, as the issue links API documents them.
const (
	linkTypeRelatesTo   = "relates_to"
	linkTypeBlocks      = "blocks"
	linkTypeIsBlockedBy = "is_blocked_by"
)

// linkKind maps GitLab's link_type onto sitrep's Kind and the native label the
// renderer shows.
//
// link_type is expressed from the *queried* issue's perspective — "blocks"
// means this Ticket blocks the target — which is exactly model.Link's own
// direction, so this is a table and not an inversion. Getting it backwards
// inverts every dependency on screen, which is why each row has its own test.
//
// LinkRelates is the model's zero value on purpose: the fallback is the
// default, not a special case, and an unrecognized type is displayed through
// GitLab's own wording rather than dropped.
func linkKind(linkType string) (model.LinkKind, string) {
	switch strings.ToLower(strings.TrimSpace(linkType)) {
	case linkTypeIsBlockedBy:
		return model.LinkBlockedBy, "is blocked by"
	case linkTypeBlocks:
		return model.LinkBlocks, "blocks"
	case linkTypeRelatesTo:
		return model.LinkRelates, "relates to"
	default:
		return model.LinkRelates, strings.ReplaceAll(strings.TrimSpace(linkType), "_", " ")
	}
}

// newLinks maps an issue's links onto sitrep's, in GitLab's own order. Nothing
// is sorted here: the model never reorders and the renderer aligns whatever it
// is given. (The GitHub driver's blocked-by-then-blocks ordering is a
// consequence of its two separate GraphQL connections, not a rule the model
// imposes.)
func newLinks(entries []issueLinkWire, wontDo wontDoSet) []model.Link {
	if len(entries) == 0 {
		return nil
	}
	links := make([]model.Link, 0, len(entries))
	for _, entry := range entries {
		kind, label := linkKind(entry.LinkType)
		links = append(links, model.Link{
			Kind:        kind,
			NativeLabel: label,
			Target:      newLinkTarget(entry.issueWire, wontDo),
		})
	}
	return links
}

// newLinkTarget maps the linked issue onto a Link's far end. It runs through
// the same normalizeStatus every Ticket does, so a linked Ticket's Native Status
// reads in the links table exactly as it does in the list.
func newLinkTarget(issue issueWire, wontDo wontDoSet) model.LinkTarget {
	status, native := normalizeStatus(issue.State, issue.Labels, issue.closedAsDuplicate(), wontDo)
	return model.LinkTarget{
		ID:           issue.ticketID(),
		Key:          issue.key(),
		Title:        issue.Title,
		URL:          issue.WebURL,
		Status:       status,
		NativeStatus: native,
	}
}
