package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
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

const (
	singularLinksNotice     = "sitrep: 1 Ticket's Links could not be read; anything it blocks is not Actionable\n"
	pluralLinksNotice       = "sitrep: 2 Tickets' Links could not be read; anything they block is not Actionable\n"
	noLinksCapabilityNotice = "sitrep: --links: blocking keys are absent because this Provider does not declare the blocking_links Capability\n"
)

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

func blockingRun(t *testing.T, wantStderr string, args ...string) (result, *fake.Provider) {
	t.Helper()

	p := fake.New(fake.WithBlockingFixture())
	got := run(args, p)
	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if got.stderr != wantStderr {
		t.Errorf("stderr = %q, want %q", got.stderr, wantStderr)
	}
	return got, p
}

// The headline document: --json --links over the blocking fixture, byte for
// byte, plus the specific rows that pin each case of the computation.
func TestJSONLinksDocument(t *testing.T) {
	got, p := blockingRun(t, singularLinksNotice, "200", "--json", "--links")
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

func TestJSONLinksBlockingFixtureThroughCLIConstruction(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.RunWith([]string{
		"--provider", "fake", "--fake-fixture", "blocking", "--json", "--links", "200",
	}, &stdout, &stderr, cli.Deps{Now: fixedClock})
	if code != 0 || stderr.String() != singularLinksNotice {
		t.Fatalf("result = code %d stderr %q, want 0/%q", code, stderr.String(), singularLinksNotice)
	}
	checkGolden(t, "blocking.golden.json", stdout.Bytes())

	var doc linksWire
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Blocking == nil || len(doc.Blocking.Cycles) == 0 {
		t.Fatalf("blocking cycles = %+v, want non-empty", doc.Blocking)
	}
	var ghost bool
	for _, ticket := range doc.Tickets {
		for _, blocker := range ticket.UnmetBlockers {
			ghost = ghost || !blocker.Member
		}
	}
	if !ghost {
		t.Error("document has no non-member unmet blocker")
	}
}

func TestJSONLinksPipeOmitsProgressAndPreservesDocument(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	var stdout bytes.Buffer
	code := cli.RunWith([]string{"200", "--json", "--links"}, &stdout, writer, cli.Deps{
		Provider: fake.New(fake.WithBlockingFixture()),
		Now:      fixedClock,
	})
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	status, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if bytes.Contains(status, []byte{'\r'}) {
		t.Errorf("stderr = %q, want no carriage-return progress on a pipe", status)
	}
	if got := string(status); got != singularLinksNotice {
		t.Errorf("stderr = %q, want %q", got, singularLinksNotice)
	}
	checkGolden(t, "blocking.golden.json", stdout.Bytes())
}

type rejectingStatusWriter struct{}

func (rejectingStatusWriter) Write([]byte) (int, error) {
	return 0, errors.New("status sink closed")
}

func TestJSONLinksStatusWriteFailureDoesNotReplaceReport(t *testing.T) {
	var stdout bytes.Buffer
	code := cli.RunWith([]string{"200", "--json", "--links"}, &stdout, rejectingStatusWriter{}, cli.Deps{
		Provider: fake.New(fake.WithBlockingFixture()),
		Now:      fixedClock,
	})

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	checkGolden(t, "blocking.golden.json", stdout.Bytes())
}

func TestJSONLinksNoBlockingCapabilityThroughCLIConstruction(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.RunWith([]string{
		"--provider", "fake", "--fake-fixture", "no-blocking-links", "--json", "--links", "200",
	}, &stdout, &stderr, cli.Deps{Now: fixedClock})
	if code != 0 || stderr.String() != noLinksCapabilityNotice {
		t.Fatalf("result = code %d stderr %q, want 0/%q", code, stderr.String(), noLinksCapabilityNotice)
	}
	checkGolden(t, "blocking_no_capability.golden.json", stdout.Bytes())

	var doc struct {
		Provider struct {
			Capabilities map[string]any `json:"capabilities"`
		} `json:"provider"`
		Blocking any              `json:"blocking"`
		Tickets  []map[string]any `json:"tickets"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, present := doc.Provider.Capabilities["blocking_links"]; !present || got != false {
		t.Errorf("blocking_links = %v, present=%t; want false", got, present)
	}
	if doc.Blocking != nil {
		t.Errorf("blocking = %v, want absent", doc.Blocking)
	}
	for _, ticket := range doc.Tickets {
		for _, key := range []string{"actionable", "links_known", "in_cycle", "unmet_blockers"} {
			if _, present := ticket[key]; present {
				t.Errorf("ticket %v carries %q without blocking_links", ticket["key"], key)
			}
		}
	}
}

// The ADR-0003 acceptance criterion: without --links the default --json run is
// still one batched Resolve and no Detail read at all, and the document carries
// no blocking keys.
func TestJSONWithoutLinksFetchesNoDetailAndEmitsNoBlockingKeys(t *testing.T) {
	got, p := blockingRun(t, "", "200", "--json")
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

// An undeclared Capability leaves the blocking keys absent and fetches no
// Details, while stderr explains why the explicit request produced no keys.
func TestJSONLinksExplainsMissingBlockingLinksCapability(t *testing.T) {
	caps := fake.FixtureBlockingSnapshot().Capabilities
	caps.BlockingLinks = false
	p := fake.New(fake.WithBlockingFixture(), fake.WithCapabilities(caps))

	got := run([]string{"200", "--json", "--links"}, p)

	if got.code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if got.stderr != noLinksCapabilityNotice {
		t.Errorf("stderr = %q, want %q", got.stderr, noLinksCapabilityNotice)
	}
	if n := p.DetailCalls(); n != 0 {
		t.Errorf("DetailCalls() = %d, want 0: nothing to compute means nothing to fetch", n)
	}
	checkGolden(t, "blocking_no_capability.golden.json", []byte(got.stdout))

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

type interruptingResolveProvider struct {
	*fake.Provider
}

func (p interruptingResolveProvider) Resolve(ctx context.Context, selector provider.Selector) (model.WatchlistSnapshot, error) {
	snapshot, err := p.Provider.Resolve(ctx, selector)
	if err != nil {
		return model.WatchlistSnapshot{}, err
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		return model.WatchlistSnapshot{}, err
	}
	<-ctx.Done()
	return snapshot, nil
}

func TestJSONLinksCancellationBeforeCapabilityNoticeIsQuiet(t *testing.T) {
	caps := fake.FixtureBlockingSnapshot().Capabilities
	caps.BlockingLinks = false
	p := fake.New(fake.WithBlockingFixture(), fake.WithCapabilities(caps))
	var stdout, stderr bytes.Buffer

	code := cli.RunWith([]string{"200", "--json", "--links"}, &stdout, &stderr, cli.Deps{
		Provider: interruptingResolveProvider{Provider: p},
		Now:      fixedClock,
	})

	if code != 130 {
		t.Errorf("exit code = %d, want 130", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want no document", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want no Capability notice after interruption", stderr.String())
	}
	if p.DetailCalls() != 0 {
		t.Errorf("DetailCalls() = %d, want 0", p.DetailCalls())
	}
}

type failingDetailProvider struct {
	*fake.Provider
	fail model.TicketID
}

func (p failingDetailProvider) FetchDetail(ctx context.Context, id model.TicketID) (model.Detail, error) {
	if id == p.fail {
		return model.Detail{}, provider.Errorf(provider.KindUnavailable, "fake: forced Detail failure")
	}
	return p.Provider.FetchDetail(ctx, id)
}

func TestJSONLinksAggregatesMultipleDetailFailures(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	var stdout, stderr bytes.Buffer
	code := cli.RunWith([]string{"200", "--json", "--links"}, &stdout, &stderr, cli.Deps{
		Provider: failingDetailProvider{Provider: p, fail: "acme/widgets#202"},
		Now:      fixedClock,
	})

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if got := stderr.String(); got != pluralLinksNotice {
		t.Errorf("stderr = %q, want one aggregate line %q", got, pluralLinksNotice)
	}
	tickets := decodeLinks(t, stdout.String())
	for key, ticket := range map[string]linksTicket{
		"#202": tickets["#202"],
		"#211": tickets["#211"],
	} {
		if ticket.LinksKnown == nil || *ticket.LinksKnown {
			t.Errorf("%s links_known = %v, want false", key, ticket.LinksKnown)
		}
		if ticket.Actionable == nil || *ticket.Actionable {
			t.Errorf("%s actionable = %v, want false", key, ticket.Actionable)
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

// A single Ref that decodes to a plain Ticket cannot answer --links: Actionable
// is a Watchlist-level property that needs the other members' statuses, and the
// decoded document has no key to carry it. Emitting that document with an exit
// code of 0 is the silent drop --links already refuses without --json, so the
// run fails and says which Ref it was.
func TestLinksFailsOnADecodedTicket(t *testing.T) {
	got := run([]string{"112", "--json", "--links"}, decoder())

	// Exit 2, the same as --links without --json: it is the same misuse of the
	// same flag, and the Ref itself resolved perfectly well, so it is not a bad
	// Ref either.
	if got.code != 2 {
		t.Errorf("exit code = %d, want 2 (stdout: %q)", got.code, got.stdout)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want no document at all", got.stdout)
	}
	if !strings.HasPrefix(got.stderr, "sitrep: --links needs a Watchlist: #112 names a single Ticket\n") {
		t.Errorf("stderr = %q, want it to name --links and the Ticket", got.stderr)
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
