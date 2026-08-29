package gitlab

import (
	"strings"

	"github.com/niekcandaele/sitrep/internal/model"
)

// GitLab's own state values for an issue or an epic. They are the whole of what
// REST exposes: there is no resolution field and no closed-reason field.
const (
	stateOpened = "opened"
	stateClosed = "closed"
)

// defaultWontDoLabels are the label names that mean "nobody did this", as
// opposed to "this is finished". Counting work nobody did as finished flatters
// every epic containing one, so these become Cancelled and leave the progress
// denominator, exactly as GitHub's not_planned and Jira's Won't Do do.
//
// The list is a judgement call — GitLab lets anyone invent a label — and it is
// the only inference in this file. Names are matched after normalization:
// lower-cased, reduced to the segment after the last "::" so a scoped label such
// as "workflow::wontfix" is the same entry as a plain "wontfix", then stripped
// of everything that is not a letter or a digit.
//
// This is the default a Profile may replace: wont_do_labels on a gitlab Profile
// supplies the site's own wording, and this list is then not consulted at all.
// See newWontDoSet.
var defaultWontDoLabels = wontDoSet{
	"wontfix":         true,
	"wontdo":          true,
	"willnotfix":      true,
	"willnotdo":       true,
	"duplicate":       true,
	"invalid":         true,
	"declined":        true,
	"rejected":        true,
	"obsolete":        true,
	"notplanned":      true,
	"notreproducible": true,
	"cannotreproduce": true,
	"abandoned":       true,
}

// wontDoSet is the set of normalized label names that mean cancelled for one
// Provider: either a Profile's own list or the built-in default.
type wontDoSet map[string]bool

// newWontDoSet builds the set a Provider matches against. A nil or empty list —
// and a list whose entries all normalize away to nothing — leaves
// defaultWontDoLabels in place, because a site that wrote nothing usable has not
// asked for the inference to be turned off.
//
// Names are normalized exactly as a GitLab label is, so a Profile may write
// "Won't Fix", "wontfix" or "workflow::wontfix" and all three match a label
// spelled any of those ways.
func newWontDoSet(names []string) wontDoSet {
	set := make(wontDoSet, len(names))
	for _, name := range names {
		if normalized := normalizeLabel(name); normalized != "" {
			set[normalized] = true
		}
	}
	if len(set) == 0 {
		return defaultWontDoLabels
	}
	return set
}

// matches reports whether a GitLab label means cancelled on this site. A nil set
// is the built-in list: a zero-value Provider must not silently disable the
// rule. A label that normalizes to nothing matches nothing, whatever the set
// holds.
func (s wontDoSet) matches(label string) bool {
	normalized := normalizeLabel(label)
	if normalized == "" {
		return false
	}
	if s == nil {
		return defaultWontDoLabels[normalized]
	}
	return s[normalized]
}

// normalizeStatus maps a GitLab issue or epic state, its labels and its
// closed-as-duplicate link onto sitrep's Status Category and the Native Status
// shown to the user. It is the only place in sitrep allowed to look at GitLab's
// vocabulary; everything downstream reads Status.
//
// GitLab issues and epics are opened or closed in REST and nothing else, so
// this function never returns StatusInProgress, and that is intentional: the
// in-progress half of a GitLab situation report comes from the merge requests
// moving a Ticket, which statusWithMergeRequests infers afterwards. Do not guess
// it from a scoped label — a "workflow::in dev" label is a plan, and GitLab
// sites spell it a hundred ways.
//
// Won't-do work is Cancelled rather than Done. REST exposes no resolution
// field, so the two available signals are checked in order of how much they are
// a fact: _links.closed_as_duplicate_of is something GitLab states outright, and
// a label is an inference. A label on an open node is never Cancelled whatever
// it says — a label on live work is a plan, not an outcome.
//
// wontDo is the site's won't-do label set, passed explicitly rather than read
// from a package-level global so a Profile's configuration travels with the
// call. The zero value is sitrep's built-in list.
func normalizeStatus(state string, labels []string, closedAsDuplicate bool, wontDo wontDoSet) (model.StatusCategory, string) {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case stateOpened:
		return model.StatusTodo, "open"
	case stateClosed:
		if closedAsDuplicate {
			return model.StatusCancelled, "duplicate"
		}
		for _, label := range labels {
			if wontDo.matches(label) {
				// The label itself becomes the Native Status, which is what makes
				// the Cancelled group readable rather than a list of "closed".
				return model.StatusCancelled, strings.TrimSpace(label)
			}
		}
		return model.StatusDone, "closed"
	default:
		// StatusUnknown is the model's deliberate "a Provider forgot to map
		// something" signal: a broken or future instance should be visible rather
		// than quietly Todo.
		return model.StatusUnknown, strings.TrimSpace(state)
	}
}

// normalizeLabel reduces a GitLab label to its comparable form: the segment
// after the last "::" of a scoped label, lower-cased, with everything that is
// not a letter or a digit stripped. "workflow::Won't Fix" and "wontfix" are one
// entry.
func normalizeLabel(label string) string {
	if at := strings.LastIndex(label, "::"); at >= 0 {
		label = label[at+2:]
	}
	var b strings.Builder
	for _, c := range strings.ToLower(label) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}
