package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"syscall"
	"testing"

	"github.com/niekcandaele/sitrep/internal/cli"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

// linksWire decodes the blocking half of the Watchlist document with pointers
// throughout, so an absent key never reads as a computed false.
type linksWire struct {
	SchemaVersion int `json:"schema_version"`
	Blocking      *struct {
		Cycles [][]string `json:"cycles"`
	} `json:"blocking"`
	Tickets []linksTicket `json:"tickets"`
}

type linksTicket struct {
	Key           string        `json:"key"`
	Actionable    *bool         `json:"actionable"`
	LinksKnown    *bool         `json:"links_known"`
	InCycle       *bool         `json:"in_cycle"`
	UnmetBlockers []linksBloker `json:"unmet_blockers"`
}

type linksBloker struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Status      string `json:"status"`
	Member      bool   `json:"member"`
	StatusKnown bool   `json:"status_known"`
}

func decodeLinks(t *testing.T, raw string) map[string]linksTicket {
	t.Helper()

	var doc linksWire
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	byKey := make(map[string]linksTicket, len(doc.Tickets))
	for _, ticket := range doc.Tickets {
		byKey[ticket.Key] = ticket
	}
	return byKey
}

func blockingRun(t *testing.T, args ...string) (result, *fake.Provider) {
	t.Helper()

	p := fake.New(fake.WithBlockingFixture())
	got := run(args, p)
	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty: a failed Detail fetch is said in the document, not on stderr", got.stderr)
	}
	return got, p
}

// The headline document: --json --links over the blocking fixture, byte for
// byte, plus the specific rows that pin each case of the computation.
func TestJSONLinksDocument(t *testing.T) {
	got, p := blockingRun(t, "200", "--json", "--links")
	checkGolden(t, "blocking.golden.json", []byte(got.stdout))

	// The fan-out is exactly one Detail read per member, on top of the one
	// batched Resolve every mode makes.
	if n := p.ResolveCalls(); n != 1 {
		t.Errorf("ResolveCalls() = %d, want exactly 1 batched fetch", n)
	}
	members := len(fake.FixtureBlockingSnapshot().Tickets)
	if n := p.DetailCalls(); n != members {
		t.Errorf("DetailCalls() = %d, want %d: one per Watchlist member", n, members)
	}

	tickets := decodeLinks(t, got.stdout)
	if len(tickets) != members {
		t.Fatalf("tickets = %d, want %d", len(tickets), members)
	}

	is := func(t *testing.T, key string, field string, got *bool, want bool) {
		t.Helper()
		if got == nil {
			t.Errorf("%s: %s absent, want a computed %v", key, field, want)
			return
		}
		if *got != want {
			t.Errorf("%s: %s = %v, want %v", key, field, *got, want)
		}
	}

	// #201 is blocked by #205 (done) and #206 (cancelled): both blockers have
	// come to rest, so it is actionable and lists nothing.
	clean := tickets["#201"]
	is(t, "#201", "actionable", clean.Actionable, true)
	is(t, "#201", "links_known", clean.LinksKnown, true)
	is(t, "#201", "in_cycle", clean.InCycle, false)
	if len(clean.UnmetBlockers) != 0 {
		t.Errorf("#201 unmet_blockers = %+v, want none: satisfied blockers are not listed", clean.UnmetBlockers)
	}

	// #203 is blocked by Ghost #401, which is todo: unmet, non-member, and its
	// status was read.
	ghost := tickets["#203"]
	is(t, "#203", "actionable", ghost.Actionable, false)
	if len(ghost.UnmetBlockers) != 1 {
		t.Fatalf("#203 unmet_blockers = %+v, want the Ghost", ghost.UnmetBlockers)
	}
	if b := ghost.UnmetBlockers[0]; b.ID != "acme/widgets#401" || b.Member || !b.StatusKnown || b.Status != "todo" {
		t.Errorf("#203 blocker = %+v, want the todo Ghost #401, member false, status known", b)
	}

	// #204's Ghost #402 has a status the Provider could not map: unmet and
	// visibly unverified, which is what separates it from a cleanly blocked row.
	unverified := tickets["#204"]
	is(t, "#204", "actionable", unverified.Actionable, false)
	if len(unverified.UnmetBlockers) != 1 {
		t.Fatalf("#204 unmet_blockers = %+v, want the unverified Ghost", unverified.UnmetBlockers)
	}
	if b := unverified.UnmetBlockers[0]; b.Status != "unknown" || b.StatusKnown {
		t.Errorf("#204 blocker = %+v, want status unknown and status_known false", b)
	}

	// #213's Ghost #403 is done, so it satisfies and is not listed at all.
	satisfied := tickets["#213"]
	is(t, "#213", "actionable", satisfied.Actionable, true)
	if len(satisfied.UnmetBlockers) != 0 {
		t.Errorf("#213 unmet_blockers = %+v, want none: a finished Ghost satisfies", satisfied.UnmetBlockers)
	}

	// #211 has no fixture Detail, so its FetchDetail fails. Its Links are
	// unknown, it is not actionable, and it must claim no blockers at all.
	unreadable := tickets["#211"]
	is(t, "#211", "links_known", unreadable.LinksKnown, false)
	is(t, "#211", "actionable", unreadable.Actionable, false)
	if unreadable.UnmetBlockers != nil {
		t.Errorf("#211 unmet_blockers = %+v, want the key absent: sitrep never read them",
			unreadable.UnmetBlockers)
	}

	// Relates carries no ordering, so #212's only Link is not an edge.
	relates := tickets["#212"]
	is(t, "#212", "actionable", relates.Actionable, true)
	if len(relates.UnmetBlockers) != 0 {
		t.Errorf("#212 unmet_blockers = %+v, want none: Relates is not an edge", relates.UnmetBlockers)
	}

	is(t, "#208", "in_cycle", tickets["#208"].InCycle, true)
	is(t, "#209", "in_cycle", tickets["#209"].InCycle, true)
	is(t, "#210", "in_cycle", tickets["#210"].InCycle, true)

	var doc linksWire
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.SchemaVersion != 3 {
		t.Errorf("schema_version = %d, want 3", doc.SchemaVersion)
	}
	want := [][]string{
		{"acme/widgets#208", "acme/widgets#209"},
		{"acme/widgets#210"},
	}
	if doc.Blocking == nil {
		t.Fatal("blocking absent from a --links document")
	}
	if len(doc.Blocking.Cycles) != len(want) {
		t.Fatalf("cycles = %v, want %v", doc.Blocking.Cycles, want)
	}
	for i, cycle := range want {
		if len(doc.Blocking.Cycles[i]) != len(cycle) {
			t.Errorf("cycles[%d] = %v, want %v", i, doc.Blocking.Cycles[i], cycle)
			continue
		}
		for j, id := range cycle {
			if doc.Blocking.Cycles[i][j] != id {
				t.Errorf("cycles[%d][%d] = %q, want %q", i, j, doc.Blocking.Cycles[i][j], id)
			}
		}
	}
}

// The ADR-0003 acceptance criterion: without --links the default --json run is
// still one batched Resolve and no Detail read at all, and the document carries
// no blocking keys.
func TestJSONWithoutLinksFetchesNoDetailAndEmitsNoBlockingKeys(t *testing.T) {
	got, p := blockingRun(t, "200", "--json")
	checkGolden(t, "blocking_no_links.golden.json", []byte(got.stdout))

	if n := p.ResolveCalls(); n != 1 {
		t.Errorf("ResolveCalls() = %d, want exactly 1 batched fetch", n)
	}
	if n := p.DetailCalls(); n != 0 {
		t.Errorf("DetailCalls() = %d, want 0: a default --json run must not fan out", n)
	}

	var doc struct {
		Blocking any              `json:"blocking"`
		Tickets  []map[string]any `json:"tickets"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Blocking != nil {
		t.Errorf("blocking = %v, want the key absent, not null", doc.Blocking)
	}
	for _, ticket := range doc.Tickets {
		for _, key := range []string{"actionable", "links_known", "in_cycle", "unmet_blockers"} {
			if _, present := ticket[key]; present {
				t.Errorf("ticket %v carries %q without --links", ticket["key"], key)
			}
		}
	}
}

// An undeclared Capability is silent, exactly like pull_requests: no keys, no
// warning, no fetch, exit 0. BuildBlockingGraph claims nothing without it, so
// emitting actionable: false everywhere would read as a computed negative.
func TestJSONLinksIsSilentWithoutTheBlockingLinksCapability(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture(), fake.WithCapabilities(model.Capabilities{
		Hierarchy: true,
		Selectors: model.SelectorCapabilities{Epic: true, RefList: true, Query: true},
	}))

	got := run([]string{"200", "--json", "--links"}, p)

	if got.code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty: a missing optional Capability is silent", got.stderr)
	}
	if n := p.DetailCalls(); n != 0 {
		t.Errorf("DetailCalls() = %d, want 0: nothing to compute means nothing to fetch", n)
	}

	var doc struct {
		Blocking any              `json:"blocking"`
		Tickets  []map[string]any `json:"tickets"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Blocking != nil {
		t.Errorf("blocking = %v, want absent", doc.Blocking)
	}
	for _, ticket := range doc.Tickets {
		if _, present := ticket["actionable"]; present {
			t.Errorf("ticket %v carries actionable without the Capability", ticket["key"])
		}
	}
}

// --links is only ever present because someone typed it, and typing it means
// "give me blocking data". Neither --plain nor the monitor produces any, so
// both are a usage error rather than a silently dropped request.
func TestLinksRequiresJSON(t *testing.T) {
	help, err := os.ReadFile("testdata/help.golden.txt")
	if err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"200", "--links"},
		{"200", "--links", "--plain"},
	} {
		t.Run(args[1], func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := cli.RunWith(args, &stdout, &stderr, cli.Deps{
				Provider: fake.New(fake.WithBlockingFixture()),
				Now:      fixedClock,
			})

			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			want := "sitrep: --links requires --json\n\n" + string(help)
			if got := stderr.String(); got != want {
				t.Errorf("stderr does not append exact help\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

// A single Ref that decodes to a plain Ticket already carries its complete links
// array, and Actionable is a Watchlist-level property that needs the other
// members' statuses. --links is therefore a silent no-op there, so a script
// passing it uniformly over a mixed list of Refs does not fail on the one Ref
// that happens to name a Ticket.
func TestLinksIsANoOpOnADecodedTicket(t *testing.T) {
	withLinks := run([]string{"112", "--json", "--links"}, decoder())
	plain := run([]string{"112", "--json"}, decoder())

	if withLinks.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", withLinks.code, withLinks.stderr)
	}
	if withLinks.stdout != plain.stdout {
		t.Errorf("--links changed a decoded Ticket document\n--- with ---\n%s\n--- without ---\n%s",
			withLinks.stdout, plain.stdout)
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(withLinks.stdout), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := doc["schema_version"]; got != float64(1) {
		t.Errorf("schema_version = %v, want 1: the Ticket/Detail family did not move", got)
	}
}

// interruptingDetailProvider resolves normally and then raises SIGINT on the
// first Detail read, which is the fan-out being interrupted halfway.
type interruptingDetailProvider struct {
	*fake.Provider
}

func (p interruptingDetailProvider) FetchDetail(ctx context.Context, id model.TicketID) (model.Detail, error) {
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		return model.Detail{}, err
	}
	<-ctx.Done()
	return model.Detail{}, provider.Errorf(provider.KindUnavailable, "fake: reading Detail: %w", ctx.Err())
}

// A half-fetched fan-out must never be emitted as if it were complete: a
// Watchlist where most rows say links_known: false because the user pressed
// ctrl+c is a lie by omission. Interruption stays interruption.
func TestLinksInterruptExitsQuietly(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := cli.RunWith([]string{"200", "--json", "--links"}, &stdout, &stderr, cli.Deps{
		Provider: interruptingDetailProvider{Provider: fake.New(fake.WithBlockingFixture())},
		Now:      fixedClock,
	})

	if code != 130 {
		t.Errorf("exit code = %d, want 130 (stderr: %q)", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty: no half-fetched document", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty: the user knows they pressed ctrl+c", stderr.String())
	}
}
