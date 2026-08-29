package plain

import (
	"strings"

	"github.com/niekcandaele/sitrep/internal/model"
)

// degenerateNativeStatus lists, per Status Category, the Native Status
// spellings that say nothing the Category heading above the row does not
// already say. It is a table of Tracker vocabulary living in a renderer on
// purpose: the alternative is a Provider deciding presentation, or a new field
// on model.Ticket that would change the Provider contract and the --json
// schema just to decide whether to draw four characters. Comparing the words
// rather than testing equality is what makes the rule fire at all — GitHub
// says "open"/"closed" where the Categories are spelled Todo/Done.
//
// A fourth Tracker's word is a one-line addition here. Entries are normalised
// spellings: lower-case, whitespace-collapsed. model.StatusUnknown has no
// entry, so a Provider that failed to categorise still shows whatever word it
// produced.
var degenerateNativeStatus = map[model.StatusCategory][]string{
	model.StatusTodo:       {"todo", "to do", "open"},
	model.StatusInProgress: {"in progress"},
	model.StatusDone:       {"done", "closed", "completed"},
	// Both apostrophes: Jira sites emit the ASCII and the typographic form.
	model.StatusCancelled: {"cancelled", "canceled", "not planned", "won't do", "won’t do"},
}

// ShowsNativeStatus reports whether t's Native Status is worth a line: it is
// false when the Ticket has none, or when its spelling only restates t's own
// Status Category. A word absent from that Category's list is kept — "[open]"
// on an InProgress Ticket differs from the bucket's default, which is exactly
// when a Native Status earns its space.
//
// It takes a model.Ticket rather than a (string, StatusCategory) pair so that
// model.LinkTarget, which carries both fields, cannot be passed: the LINKS
// table has no Category heading supplying the context and is deliberately
// exempt from this rule.
//
// This is only half the rule. A renderer that draws a Ticket with no Category
// heading above it must call StatusField instead, which never renders nothing.
func ShowsNativeStatus(t model.Ticket) bool {
	if t.NativeStatus == "" {
		return false
	}
	native := normalizeNativeStatus(t.NativeStatus)
	for _, degenerate := range degenerateNativeStatus[t.Status] {
		if native == degenerate {
			return false
		}
	}
	return true
}

// normalizeNativeStatus lower-cases a Native Status and collapses every run of
// whitespace to a single space. It folds no punctuation: "todo" and "to do"
// are separate table entries precisely because the rule does not equate them.
func normalizeNativeStatus(native string) string {
	return strings.ToLower(strings.Join(strings.Fields(native), " "))
}

// StatusField is the bracketed status a renderer draws when no Status Category
// heading stands above the Ticket — the Detail header and the single-Ticket
// report. It is the Tracker's own word when that word adds something, and the
// Status Category when it does not, so the reader is never told nothing at all
// about a Ticket's status.
//
// A renderer whose rows already sit under a Category heading wants
// ShowsNativeStatus instead: there, suppression is the whole point.
func StatusField(t model.Ticket) string {
	if ShowsNativeStatus(t) {
		return "[" + t.NativeStatus + "]"
	}
	return "[" + CategoryLabel(t.Status) + "]"
}
