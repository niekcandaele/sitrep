package cli_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/cli"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
	"github.com/niekcandaele/sitrep/internal/ref"
	"github.com/niekcandaele/sitrep/internal/render/plain"
)

// The headline test: the whole program, run against the fake Provider, emits a
// text report that is byte-for-byte what the golden says. The fixture epic
// spans every Status Category, zero/one/many assignees, a cross-repo Ticket, a
// unicode title, all four pull request shapes and Tickets with no pull request.
func TestPlainEpicReport(t *testing.T) {
	p := fake.New()

	got := run([]string{"111", "--plain"}, p)

	if got.code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty", got.stderr)
	}
	checkGolden(t, "epic_plain.golden.txt", []byte(got.stdout))

	// ADR-0003: the epic path is one batched fetch and never touches Detail.
	if n := p.ResolveCalls(); n != 1 {
		t.Errorf("ResolveCalls() = %d, want exactly 1 batched fetch", n)
	}
	if n := p.DetailCalls(); n != 0 {
		t.Errorf("DetailCalls() = %d, want 0: rendering a list must not fetch detail", n)
	}
}

func TestPlainRefListReport(t *testing.T) {
	p := fake.New()
	got := run([]string{
		"acme/widgets#112",
		"--plain",
		"acme/widgets#115",
		"acme/widgets#118",
		"acme/widgets#121",
	}, p)

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	checkGolden(t, "ref_list_plain.golden.txt", []byte(got.stdout))
	if !strings.HasPrefix(got.stdout, "\nWatchlist  4 tickets\n") {
		t.Errorf("plain output starts %q, want Ref-list Header", got.stdout[:min(len(got.stdout), 40)])
	}
	if p.ResolveCalls() != 1 || p.DetailCalls() != 0 {
		t.Errorf("calls = Resolve %d, Detail %d; want 1 and 0", p.ResolveCalls(), p.DetailCalls())
	}
}

// The acceptance criterion is "no alt-screen, safe for dumb terminals and
// pipes": escape sequences are exactly what a dumb terminal over SSH and a log
// file cannot handle, so --plain emits none at all — not even when stdout is a
// terminal, because the renderer never looks.
func TestPlainHasNoEscapeSequences(t *testing.T) {
	got := run([]string{"111", "--plain"}, fake.New())

	if strings.ContainsRune(got.stdout, '\x1b') {
		t.Errorf("the report contains an escape byte: %q", got.stdout)
	}
	if strings.Contains(got.stdout, "\x1b[?1049h") {
		t.Error("the report switches to the alternate screen")
	}
}

// An undeclared Capability is silently absent, never an error: with pull
// requests off no Ticket carries pull request text and nothing complains.
func TestPlainOmitsUndeclaredCapabilities(t *testing.T) {
	p := fake.New(fake.WithCapabilities(model.Capabilities{Hierarchy: true}))

	got := run([]string{"111", "--plain"}, p)

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty: an undeclared Capability is not a problem", got.stderr)
	}
	checkGolden(t, "epic_plain_no_pr_capability.golden.txt", []byte(got.stdout))

	for _, absent := range []string{"ci ok", "ci FAIL", "ci ...", "approved", "changes req", "review pending"} {
		if strings.Contains(got.stdout, absent) {
			t.Errorf("the report mentions %q without the PullRequests capability", absent)
		}
	}
}

// An epic with no Tickets renders its header without dividing by zero, and says
// so rather than trailing off into an empty void.
//
// It is asserted against the renderer rather than through the CLI because a Ref
// that answers with no Tickets is decoded as a Ticket now (decodesToTicket). The
// body is still reachable — a Watchlist whose Tickets have all gone renders it,
// and the monitor's empty state is its twin — so it is still worth a golden.
func TestPlainEmptyEpic(t *testing.T) {
	empty := model.WatchlistSnapshot{
		Header: model.WatchlistHeader{
			Key:   "#900",
			Title: "Widget sync v3: nothing planned yet",
			URL:   "https://tracker.example.test/acme/widgets/900",
		},
		Epic: model.Epic{
			ID:           "acme/widgets#900",
			Key:          "#900",
			Title:        "Widget sync v3: nothing planned yet",
			URL:          "https://tracker.example.test/acme/widgets/900",
			Status:       model.StatusTodo,
			NativeStatus: "open",
		},
	}

	var buf bytes.Buffer
	if err := plain.RenderWatchlist(&buf, empty); err != nil {
		t.Fatalf("RenderWatchlist: %v", err)
	}
	checkGolden(t, "epic_plain_empty.golden.txt", buf.Bytes())
}

// A real recorded GitHub payload survives all the way to a text report,
// including the pull request data and the cross-repo Ticket.
func TestPlainGitHubEpicReport(t *testing.T) {
	got := runWith(
		[]string{"https://github.com/niekcandaele/sitrep/issues/2", "--plain"},
		cli.Deps{Provider: githubProvider(t)},
	)

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty", got.stderr)
	}
	checkGolden(t, "epic_plain_github.golden.txt", []byte(got.stdout))
}

// A half-written report followed by an error would leave a human reading a
// truncated epic, so a failed fetch leaves stdout untouched.
func TestPlainProviderFailure(t *testing.T) {
	p := fake.New(fake.WithResolveError(errors.New("boom")))

	got := run([]string{"111", "--plain"}, p)

	if got.code != 1 {
		t.Errorf("exit code = %d, want 1", got.code)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", got.stdout)
	}
	if !strings.Contains(got.stderr, "boom") {
		t.Errorf("stderr = %q, want it to contain the provider's error", got.stderr)
	}
	if !strings.HasPrefix(got.stderr, "sitrep: ") {
		t.Errorf("stderr = %q, want it prefixed with the program name", got.stderr)
	}
}

// The flag may come before or after the ref; both orders are how people type.
func TestPlainFlagOrderDoesNotMatter(t *testing.T) {
	before := run([]string{"--plain", "111"}, fake.New())
	after := run([]string{"111", "--plain"}, fake.New())

	if before.stdout != after.stdout || before.code != after.code {
		t.Errorf("flag order changed the run: %d/%d bytes, codes %d and %d",
			len(before.stdout), len(after.stdout), before.code, after.code)
	}
}

func TestPlainInterspersedBareRefListResolvesEachMemberOnce(t *testing.T) {
	source := model.WatchlistSnapshot{Tickets: []model.Ticket{
		{ID: "acme/widgets#38", Key: "#38", Title: "Thirty eight"},
		{ID: "acme/widgets#39", Key: "#39", Title: "Thirty nine"},
	}}
	p := fake.New(fake.WithSnapshot(source))
	lookups := 0
	got := runWith([]string{"38", "--plain", "#39"}, cli.Deps{
		Provider: p,
		RemoteLookup: func(context.Context, string, string) (string, error) {
			lookups++
			return "git@github.com:acme/widgets.git", nil
		},
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if lookups != 2 {
		t.Errorf("origin lookups = %d, want one per Ref at startup", lookups)
	}
	selector := p.LastSelector().(provider.RefListSelector)
	if len(selector.Refs) != 2 || selector.Refs[0].Number != 38 || selector.Refs[1].Number != 39 {
		t.Errorf("selector = %+v, want ordered 38, 39", selector)
	}
}

// Two one-shot renderers cannot both own stdout, so asking for both is a
// scripting mistake worth surfacing rather than silently resolving.
func TestPlainAndJSONAreMutuallyExclusive(t *testing.T) {
	got := run([]string{"111", "--json", "--plain"}, fake.New())

	if got.code != 2 {
		t.Errorf("exit code = %d, want 2", got.code)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want empty", got.stdout)
	}
	for _, want := range []string{"--json", "--plain", "mutually exclusive"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
}

// A bare number is resolved through the clone's origin remote before any
// Provider sees it — through the injected lookup, never the ambient remote of
// whatever checkout the tests run in.
func TestPlainBareNumberResolvesThroughTheOriginRemote(t *testing.T) {
	p := fake.New()

	got := runWith([]string{"111", "--plain"}, cli.Deps{
		Provider: p,
		RemoteLookup: func(context.Context, string, string) (string, error) {
			return "git@github.com:acme/widgets.git", nil
		},
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	want := ref.Ref{
		Tracker: ref.TrackerGitHub, Host: "github.com",
		Owner: "acme", Repo: "widgets", Number: 111, Raw: "111",
	}
	if p.LastSelector().(provider.EpicSelector).Ref != want {
		t.Errorf("the Provider was given %+v, want %+v", p.LastSelector().(provider.EpicSelector).Ref, want)
	}
}
