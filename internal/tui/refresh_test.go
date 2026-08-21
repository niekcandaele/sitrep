package tui

import (
	"testing"
	"time"
)

// Staleness is arithmetic with edge cases a golden frame cannot reach: a clock
// that has not been read yet, one that stepped backwards, and the boundaries
// between the units.
func TestStaleness(t *testing.T) {
	now := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		fetchedAt  time.Time
		refreshing bool
		want       string
	}{
		{
			name:      "a reading that never happened says so",
			fetchedAt: time.Time{},
			want:      "never updated",
		},
		{
			name:       "an in-flight refresh wins over any age",
			fetchedAt:  now.Add(-90 * time.Minute),
			refreshing: true,
			want:       "refreshing…",
		},
		{
			name:       "refreshing wins over a never-read clock too",
			fetchedAt:  time.Time{},
			refreshing: true,
			want:       "refreshing…",
		},
		{
			name:      "sub-second is just now",
			fetchedAt: now.Add(-200 * time.Millisecond),
			want:      "just now",
		},
		{
			name:      "a clock that stepped backwards reads as just now, not a negative age",
			fetchedAt: now.Add(3 * time.Minute),
			want:      "just now",
		},
		{
			name:      "seconds",
			fetchedAt: now.Add(-12 * time.Second),
			want:      "updated 12s ago",
		},
		{
			name:      "the last second before a minute is still seconds",
			fetchedAt: now.Add(-59 * time.Second),
			want:      "updated 59s ago",
		},
		{
			name:      "a minute exactly rolls over to minutes",
			fetchedAt: now.Add(-time.Minute),
			want:      "updated 1m ago",
		},
		{
			name:      "minutes",
			fetchedAt: now.Add(-3*time.Minute - 40*time.Second),
			want:      "updated 3m ago",
		},
		{
			name:      "an hour and change reads as both units",
			fetchedAt: now.Add(-64 * time.Minute),
			want:      "updated 1h 4m ago",
		},
		{
			name:      "a whole hour omits nothing",
			fetchedAt: now.Add(-2 * time.Hour),
			want:      "updated 2h 0m ago",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Staleness(tt.fetchedAt, now, tt.refreshing); got != tt.want {
				t.Errorf("Staleness() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The footer's retry countdown never counts below zero: an overdue refresh is
// "due", not a negative number of seconds.
func TestCountdown(t *testing.T) {
	tests := []struct {
		remaining time.Duration
		want      string
	}{
		{-time.Minute, "due"},
		{0, "due"},
		{47 * time.Second, "47s"},
		{90 * time.Second, "2m"},
	}

	for _, tt := range tests {
		if got := countdown(tt.remaining); got != tt.want {
			t.Errorf("countdown(%s) = %q, want %q", tt.remaining, got, tt.want)
		}
	}
}
