package tui

import (
	"fmt"
	"math"
	"time"

	tea "charm.land/bubbletea/v2"
)

// heartbeatInterval is the monitor's single timer. One beat drives both the
// staleness label and the decision to refresh, so the two can never disagree
// about how old the data is (a 60s refresh timer alongside a 1s display timer
// can, and the disagreement is invisible until it is a bug report).
const heartbeatInterval = time.Second

// heartbeatMsg is one beat of that timer.
type heartbeatMsg time.Time

// refreshedMsg carries the outcome of one Source call. generation is the
// refresh's sequence number: a reading whose generation is no longer the
// Model's current one is stale and gets dropped, so a slow auto-refresh cannot
// land on top of the answer to a manual `r`.
type refreshedMsg struct {
	generation int
	input      ListInput
	err        error
}

// heartbeat schedules the next beat. tea.Tick rather than tea.Every: the beat
// should be one second after this one, not on the next wall-clock second
// boundary.
func heartbeat() tea.Cmd {
	return tea.Tick(heartbeatInterval, func(t time.Time) tea.Msg { return heartbeatMsg(t) })
}

// requestBackgroundColor asks the terminal what it is drawn on, so the palette
// is chosen from the answer rather than guessed from the environment. A
// terminal that never answers leaves the default palette in place.
func requestBackgroundColor() tea.Msg { return tea.RequestBackgroundColor() }

// Staleness renders how old a reading is, read from a fixed clock: "just now",
// "updated 12s ago", "updated 3m ago", "updated 1h 4m ago".
//
// An in-flight refresh wins over any age: what the user wants to know while
// bytes are moving is that they are moving.
func Staleness(fetchedAt, now time.Time, refreshing bool) string {
	if refreshing {
		return "refreshing…"
	}
	if fetchedAt.IsZero() {
		return "never updated"
	}

	age := now.Sub(fetchedAt)
	// A clock that stepped backwards between the fetch and the render is not
	// worth reporting as a negative age.
	if age < time.Second {
		return "just now"
	}
	switch {
	case age < time.Minute:
		return fmt.Sprintf("updated %ds ago", int(age.Seconds()))
	case age < time.Hour:
		return fmt.Sprintf("updated %dm ago", int(age.Minutes()))
	default:
		return fmt.Sprintf("updated %dh %dm ago", int(age.Hours()), int(age.Minutes())%60)
	}
}

// countdown renders how long until the next automatic refresh, e.g. "47s". It
// never counts below zero: a refresh that is overdue is "due", not negative.
func countdown(remaining time.Duration) string {
	if remaining <= 0 {
		return "due"
	}
	if remaining <= time.Minute {
		return fmt.Sprintf("%ds", int(remaining.Round(time.Second).Seconds()))
	}
	return fmt.Sprintf("%dm", int(math.Ceil(remaining.Minutes())))
}
