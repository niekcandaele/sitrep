package tui

import (
	"strings"

	"github.com/niekcandaele/sitrep/internal/model"
)

// Filter narrows the Tickets the monitor lists. It is view state, not model
// state: it never reaches a Provider, never changes what was fetched, and never
// touches the progress header, which reports the whole collection whatever the
// list is showing.
//
// The zero Filter admits every Ticket.
type Filter struct {
	// HideFinished drops Tickets whose Status Category has come to rest —
	// Done and Cancelled, as model.StatusCategory.IsFinished defines it.
	HideFinished bool
	// Query is the fuzzy find over each Ticket's key and title. Empty admits
	// every Ticket.
	Query string
}

// Active reports whether the Filter narrows anything, i.e. whether the user
// needs to be told the list is not the whole collection.
func (f Filter) Active() bool {
	return f.HideFinished || len(strings.Fields(f.Query)) > 0
}

// Apply returns the Tickets f admits, in input order. It never reorders, never
// scores and never copies a Ticket: grouping and Provider order are decided
// elsewhere and filtering must not disturb either. A Filter that admits
// everything returns the input slice unchanged.
func (f Filter) Apply(tickets []model.Ticket) []model.Ticket {
	if !f.Active() {
		return tickets
	}

	kept := make([]model.Ticket, 0, len(tickets))
	for _, t := range tickets {
		// A StatusUnknown Ticket is not finished, so it survives the hide
		// toggle: a Provider that could not be understood stays on screen.
		if f.HideFinished && t.Status.IsFinished() {
			continue
		}
		if !matchesQuery(t, f.Query) {
			continue
		}
		kept = append(kept, t)
	}
	return kept
}

// matchesQuery reports whether a Ticket answers a fuzzy find. The haystack is
// the Ticket's key and title joined by a space — the two things a human knows a
// Ticket by — so "112 shard" matches Ticket #112 "Draft the shard sync
// protocol" and a bare "shard" matches it too.
//
// The haystack is key and title and nothing else. Native Status is deliberately
// excluded: CONTEXT.md has it displayed as-is and never filtered on, because
// every Tracker invents its own wording and a filter over it would mean
// something different on every one of them. Assignees, repository and URL are
// out for the same reason a find box is not a query language — they are the
// next thing someone will want to add, and this is the sentence that says no.
//
// The query is split on whitespace into terms; every term must appear in the
// haystack as a case-insensitive subsequence (its runes in order, gaps
// allowed). Subsequence rather than substring is what makes "dsp" find "Draft
// the shard protocol"; requiring every term is what lets a second word narrow
// rather than widen. There is no scoring and no ranking: the list's value is
// that it stays grouped by Status Category in a stable order.
//
// Matching is rune-based, and folds case but not diacritics: "métrique" finds
// the accented title, "metrique" does not. Folding would need
// golang.org/x/text, and one accent does not justify a dependency in a binary
// whose selling point is a single static download.
func matchesQuery(t model.Ticket, query string) bool {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return true
	}

	haystack := []rune(strings.ToLower(t.Key + " " + t.Title))
	for _, term := range terms {
		if !isSubsequence([]rune(term), haystack) {
			return false
		}
	}
	return true
}

// isSubsequence reports whether every rune of needle occurs in haystack in
// order, with any number of runes between them.
func isSubsequence(needle, haystack []rune) bool {
	i := 0
	for _, r := range haystack {
		if i == len(needle) {
			return true
		}
		if r == needle[i] {
			i++
		}
	}
	return i == len(needle)
}
