package cli_test

import (
	"context"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/cli"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
)

// hostileText is what a malicious ticket title, comment or link label looks
// like: an erase-display sequence, a window-title sequence, a bare carriage
// return and a DEL. On a public tracker anyone who can comment can write one.
const hostileText = "hi\x1b[2J\x1b]0;pwned\aback\rgone\x7f"

// hostileProvider serves tracker text with escape sequences in every field.
// children decides whether the Ref reads as a Watchlist or decodes to a
// Ticket, which is how the decoder path — descriptions, comments and links —
// is reached.
type hostileProvider struct{ children bool }

func (*hostileProvider) Name() string { return "hostile" }

func (*hostileProvider) Capabilities() model.Capabilities {
	return model.Capabilities{Hierarchy: true, BlockingLinks: true, Comments: true, PullRequests: true}
}

func (p *hostileProvider) Resolve(context.Context, provider.Selector) (model.WatchlistSnapshot, error) {
	user := model.User{Login: hostileText, DisplayName: hostileText}
	snap := model.WatchlistSnapshot{
		Epic: model.Epic{
			ID: "e1", Key: hostileText, Title: hostileText, URL: hostileText,
			Status: model.StatusInProgress, NativeStatus: hostileText,
			Assignees: []model.User{user}, Repository: hostileText,
		},
		Parent: model.Parent{ID: "p1", Key: hostileText, Title: hostileText, URL: hostileText},
	}
	if p.children {
		snap.Tickets = []model.Ticket{{
			ID: "t1", Key: hostileText, Title: hostileText, URL: hostileText,
			Status: model.StatusTodo, NativeStatus: hostileText,
			Assignees: []model.User{user}, Repository: hostileText,
			PullRequests: []model.PullRequest{{
				Number: 7, Title: hostileText, URL: hostileText, Repository: hostileText,
				State: model.PROpen, Review: model.ReviewPending, Checks: model.ChecksPassing,
			}},
		}}
	}
	return snap, nil
}

func (*hostileProvider) FetchDetail(context.Context, model.TicketID) (model.Detail, error) {
	return model.Detail{
		TicketID:    "e1",
		Description: "before\x1b[2Jafter\r\nsecond line",
		Comments: []model.Comment{{
			ID:     "c1",
			Author: model.User{Login: hostileText, DisplayName: hostileText},
			Body:   "clipboard\x1b]52;c;cHduZWQ=\x07theft",
		}},
		Links: []model.Link{{
			Kind:        model.LinkBlockedBy,
			NativeLabel: hostileText,
			Target: model.LinkTarget{
				ID: "t2", Key: hostileText, Title: hostileText, URL: hostileText,
				Status: model.StatusTodo, NativeStatus: hostileText,
			},
		}},
	}, nil
}

// internal/render/plain's package doc and README both promise that a --plain
// report contains no ANSI escape sequence of any kind. The promise has to hold
// for text an attacker wrote, not only for text nobody attacked, which is what
// this asserts.
func TestPlainNeverCarriesTrackerEscapeSequences(t *testing.T) {
	tests := []struct {
		name string
		p    *hostileProvider
	}{
		{name: "an epic with children", p: &hostileProvider{children: true}},
		{name: "a ref that decodes to a ticket", p: &hostileProvider{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, mode := range []string{"--plain", "--json"} {
				got := runWith([]string{"111", mode}, cli.Deps{Provider: tt.p})
				if got.code != 0 {
					t.Fatalf("%s: exit code = %d, want 0 (stderr: %q)", mode, got.code, got.stderr)
				}
				if strings.ContainsRune(got.stdout, 0x1b) {
					t.Errorf("%s output contains an escape byte: %q", mode, got.stdout)
				}
				for _, forbidden := range []string{"\x07", "\x7f", "\r"} {
					if strings.Contains(got.stdout, forbidden) {
						t.Errorf("%s output contains %q: %q", mode, forbidden, got.stdout)
					}
				}
				// Removal, not escaping: the text either side of a sequence is
				// still there to read.
				if !strings.Contains(got.stdout, "hi") {
					t.Errorf("%s output dropped the text around the sequence: %q", mode, got.stdout)
				}
			}
		})
	}
}

// Tracker-supplied error text takes the same treatment: it reaches the terminal
// through one funnel and is cleaned there.
func TestRuntimeErrorsCarryNoEscapeSequences(t *testing.T) {
	got := runWith([]string{"111", "--plain"}, cli.Deps{Provider: &failingProvider{}})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	if strings.ContainsRune(got.stderr, 0x1b) {
		t.Errorf("stderr contains an escape byte: %q", got.stderr)
	}
	if strings.Count(got.stderr, "\n") != 1 {
		t.Errorf("stderr = %q, want exactly one line", got.stderr)
	}
}

type failingProvider struct{ hostileProvider }

func (*failingProvider) Resolve(context.Context, provider.Selector) (model.WatchlistSnapshot, error) {
	return model.WatchlistSnapshot{}, errHostile
}

var errHostile = &plainError{"hostile: the tracker said \x1b[2Jboom\nand more"}

type plainError struct{ msg string }

func (e *plainError) Error() string { return e.msg }
