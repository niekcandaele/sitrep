package model

import "fmt"

// StatusCategory is sitrep's normalized lifecycle bucket for a Ticket. It is
// the only thing grouping, filtering and progress math may read; a Tracker's
// own wording lives in NativeStatus and is display-only.
type StatusCategory int

// The Status Categories. StatusUnknown is the zero value on purpose: a Provider
// that forgets to map a Tracker status produces something visibly wrong rather
// than a silent "todo". Providers never emit it deliberately.
const (
	StatusUnknown StatusCategory = iota
	StatusTodo
	StatusInProgress
	StatusDone
	StatusCancelled
)

var statusTokens = enumTokens[StatusCategory]{
	{StatusUnknown, "unknown"},
	{StatusTodo, "todo"},
	{StatusInProgress, "in_progress"},
	{StatusDone, "done"},
	{StatusCancelled, "cancelled"},
}

// String returns the wire and display token for the Status Category.
func (s StatusCategory) String() string { return statusTokens.token(s, "status_category") }

// MarshalJSON encodes the Status Category as its wire token.
func (s StatusCategory) MarshalJSON() ([]byte, error) { return statusTokens.marshal(s) }

// UnmarshalJSON decodes a Status Category from its wire token.
func (s *StatusCategory) UnmarshalJSON(b []byte) error {
	return statusTokens.unmarshal(b, s, "status category")
}

// ParseStatusCategory returns the Status Category for a wire token, or an error
// if the token is not one of them.
func ParseStatusCategory(s string) (StatusCategory, error) {
	v, ok := statusTokens.value(s)
	if !ok {
		return StatusUnknown, fmt.Errorf("unknown status category %q", s)
	}
	return v, nil
}

// IsFinished reports whether work in this category has come to rest: Done and
// Cancelled are finished, everything else is not. This is the single predicate
// filtering and progress math read.
func (s StatusCategory) IsFinished() bool {
	return s == StatusDone || s == StatusCancelled
}

// displayOrder is the order humans read Status Categories in: what they are
// watching first, finished work last. StatusUnknown trails everything because
// it only appears when a Provider is broken.
var displayOrder = []StatusCategory{
	StatusInProgress,
	StatusTodo,
	StatusDone,
	StatusCancelled,
	StatusUnknown,
}

// AllStatusCategories returns every Status Category in display order:
// InProgress, Todo, Done, Cancelled, then Unknown. Renderers and the TUI share
// this ordering so grouped output looks the same everywhere.
func AllStatusCategories() []StatusCategory {
	out := make([]StatusCategory, len(displayOrder))
	copy(out, displayOrder)
	return out
}
