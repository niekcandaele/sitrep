package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/cli"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

// fixedClock is the clock every golden in this package is generated against.
var fixedClock = func() time.Time {
	return time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)
}

type result struct {
	code   int
	stdout string
	stderr string
}

func run(args []string, p *fake.Provider) result {
	var stdout, stderr bytes.Buffer
	code := cli.RunWith(args, &stdout, &stderr, cli.Deps{Provider: p, Now: fixedClock})
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func runStdin(args []string, input string, p *fake.Provider) result {
	var stdout, stderr bytes.Buffer
	code := cli.RunWith(args, &stdout, &stderr, cli.Deps{
		Provider: p,
		Now:      fixedClock,
		Stdin:    strings.NewReader(input),
		OpenTTY:  panicTTY,
	})
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// The headline test: the whole program, run against the fake Provider, emits a
// document that is byte-for-byte what the golden says.
func TestJSONEpicDocument(t *testing.T) {
	p := fake.New()

	got := run([]string{"111", "--json"}, p)

	if got.code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty", got.stderr)
	}
	checkGolden(t, "epic.golden.json", []byte(got.stdout))

	// ADR-0003: the epic path is one batched fetch and never touches Detail.
	if n := p.ResolveCalls(); n != 1 {
		t.Errorf("ResolveCalls() = %d, want exactly 1 batched fetch", n)
	}
	if n := p.DetailCalls(); n != 0 {
		t.Errorf("DetailCalls() = %d, want 0: rendering a list must not fetch detail", n)
	}
}

// The flag may come before or after the ref; both orders are how people type.
func TestJSONFlagOrderDoesNotMatter(t *testing.T) {
	before := run([]string{"--json", "111"}, fake.New())
	after := run([]string{"111", "--json"}, fake.New())

	if before.stdout != after.stdout || before.code != after.code {
		t.Errorf("flag order changed the run: %d/%d bytes, codes %d and %d",
			len(before.stdout), len(after.stdout), before.code, after.code)
	}
}

// An undeclared Capability is silently absent, never an error: with pull
// requests off no ticket carries the key at all.
func TestJSONOmitsUndeclaredCapabilities(t *testing.T) {
	p := fake.New(fake.WithCapabilities(model.Capabilities{
		Hierarchy: true,
		Selectors: model.SelectorCapabilities{Epic: true},
	}))

	got := run([]string{"111", "--json"}, p)

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	// Absence is silent: no section, and no complaint about its absence either.
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty: an undeclared Capability is not a problem", got.stderr)
	}
	checkGolden(t, "epic_no_pr_capability.golden.json", []byte(got.stdout))

	var doc struct {
		Tickets []map[string]json.RawMessage `json:"tickets"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil {
		t.Fatalf("unmarshalling the epic document: %v", err)
	}
	if len(doc.Tickets) == 0 {
		t.Fatal("the document has no tickets")
	}
	for _, ticket := range doc.Tickets {
		if _, ok := ticket["pull_requests"]; ok {
			t.Errorf("ticket %s carries pull_requests without the capability", ticket["key"])
		}
	}
}

// A half-written document followed by an error would poison a consuming
// script, so a failed fetch leaves stdout untouched.
func TestJSONProviderFailure(t *testing.T) {
	p := fake.New(fake.WithResolveError(errors.New("boom")))

	got := run([]string{"111", "--json"}, p)

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

func TestJSONRefListDocumentAndSelector(t *testing.T) {
	p := fake.New(fake.WithMaxTickets(1))

	got := run([]string{
		"acme/widgets#112",
		"--json",
		"acme/widgets#115",
		"acme/widgets#118",
		"acme/widgets#121",
	}, p)
	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	checkGolden(t, "ref_list.golden.json", []byte(got.stdout))

	selector, ok := p.LastSelector().(provider.RefListSelector)
	if !ok {
		t.Fatalf("selector = %T, want provider.RefListSelector", p.LastSelector())
	}
	wantNumbers := []int{112, 115, 118, 121}
	gotNumbers := make([]int, len(selector.Refs))
	for i, r := range selector.Refs {
		gotNumbers[i] = r.Number
	}
	if !reflect.DeepEqual(gotNumbers, wantNumbers) {
		t.Errorf("selector numbers = %v, want %v", gotNumbers, wantNumbers)
	}
	if p.ResolveCalls() != 1 || p.DetailCalls() != 0 {
		t.Errorf("calls = Resolve %d, Detail %d; want 1 and 0", p.ResolveCalls(), p.DetailCalls())
	}
}

func TestJSONQueryDocumentAndSelector(t *testing.T) {
	p := fake.New()
	query := "  label:bug assignee:@me  "

	got := run([]string{"--json", "--query", query}, p)
	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty", got.stderr)
	}
	checkGolden(t, "query.golden.json", []byte(got.stdout))

	selector, ok := p.LastSelector().(provider.QuerySelector)
	if !ok {
		t.Fatalf("selector = %T, want provider.QuerySelector", p.LastSelector())
	}
	if selector.Query != query {
		t.Errorf("selector Query = %q, want %q", selector.Query, query)
	}
	if p.ResolveCalls() != 1 || p.DetailCalls() != 0 {
		t.Errorf("calls = Resolve %d, Detail %d; want 1 and 0", p.ResolveCalls(), p.DetailCalls())
	}
}

func TestJSONQueryLimitReached(t *testing.T) {
	p := fake.New(fake.WithMaxTickets(2))
	got := run([]string{"--json", "--query", "state=opened&labels=ready"}, p)
	if got.code != 0 || got.stderr != "" {
		t.Fatalf("result = code %d stderr %q", got.code, got.stderr)
	}

	var doc struct {
		Watchlist map[string]json.RawMessage `json:"watchlist"`
		Progress  struct {
			Total int `json:"total"`
		} `json:"progress"`
		Tickets []struct {
			Key string `json:"key"`
		} `json:"tickets"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := string(doc.Watchlist["limit_reached"]); got != "true" {
		t.Errorf("watchlist.limit_reached = %s, want true", got)
	}
	wantKeys := []string{"#112", "#113"}
	keys := make([]string, len(doc.Tickets))
	for i := range doc.Tickets {
		keys[i] = doc.Tickets[i].Key
	}
	if !reflect.DeepEqual(keys, wantKeys) || doc.Progress.Total != 2 {
		t.Errorf("tickets/progress.total = %v/%d, want %v/2", keys, doc.Progress.Total, wantKeys)
	}
	for _, forbidden := range []string{"max_tickets", "total_matches", "Limit reached"} {
		if strings.Contains(got.stdout, forbidden) {
			t.Errorf("limited Query JSON contains unsupported %q", forbidden)
		}
	}
}

func TestJSONExplicitEmptyQueryKeepsMandatoryQueryKey(t *testing.T) {
	p := fake.New(fake.WithSnapshots(model.WatchlistSnapshot{Tickets: []model.Ticket{}}))

	got := run([]string{"--json", "--query="}, p)
	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}

	var doc struct {
		Watchlist map[string]json.RawMessage `json:"watchlist"`
		Tickets   []json.RawMessage          `json:"tickets"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil {
		t.Fatalf("unmarshal document: %v", err)
	}
	if _, ok := doc.Watchlist["epic"]; ok {
		t.Error("Query Watchlist unexpectedly carries an outer epic")
	}
	var selector map[string]json.RawMessage
	if err := json.Unmarshal(doc.Watchlist["selector"], &selector); err != nil {
		t.Fatalf("unmarshal selector: %v", err)
	}
	if got := string(selector["kind"]); got != `"query"` {
		t.Errorf("selector kind = %s, want %s", got, `"query"`)
	}
	query, ok := selector["query"]
	if !ok {
		t.Fatal("explicit empty Query omitted selector.query")
	}
	if got := string(query); got != `""` {
		t.Errorf("selector query = %s, want empty string", got)
	}
	if doc.Tickets == nil || len(doc.Tickets) != 0 {
		t.Errorf("tickets = %#v, want non-null empty array", doc.Tickets)
	}
}

func TestStdinSentinelAfterFlagSeparator(t *testing.T) {
	got := runStdin([]string{"--json", "--", "-"}, "acme/widgets#112", fake.New())
	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
}

func TestStdinJSONRefListMatchesPositionalSelection(t *testing.T) {
	p := fake.New(fake.WithMaxTickets(1))
	input := "  acme/widgets#112\tacme/widgets#115\r\n\nacme/widgets#118 acme/widgets#121  "
	got := runStdin([]string{"-", "--json"}, input, p)
	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	checkGolden(t, "ref_list.golden.json", []byte(got.stdout))

	positional := run([]string{
		"--json",
		"acme/widgets#112",
		"acme/widgets#115",
		"acme/widgets#118",
		"acme/widgets#121",
	}, fake.New(fake.WithMaxTickets(1)))
	if got.stdout != positional.stdout {
		t.Error("stdin and positional Refs produced different JSON Watchlists")
	}

	selector, ok := p.LastSelector().(provider.RefListSelector)
	if !ok {
		t.Fatalf("selector = %T, want provider.RefListSelector", p.LastSelector())
	}
	wantRaw := []string{
		"acme/widgets#112",
		"acme/widgets#115",
		"acme/widgets#118",
		"acme/widgets#121",
	}
	gotRaw := make([]string, len(selector.Refs))
	for i, r := range selector.Refs {
		gotRaw[i] = r.Raw
	}
	if !reflect.DeepEqual(gotRaw, wantRaw) {
		t.Errorf("selector Refs = %v, want %v", gotRaw, wantRaw)
	}
	if p.ResolveCalls() != 1 || p.DetailCalls() != 0 {
		t.Errorf("calls = Resolve %d Detail %d; want 1 and 0", p.ResolveCalls(), p.DetailCalls())
	}
}

func TestSingleStdinRefRemainsARefList(t *testing.T) {
	p := fake.New(fake.WithMaxTickets(1))
	got := runStdin([]string{"--json", "-"}, "acme/widgets#112\n", p)
	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	selector, ok := p.LastSelector().(provider.RefListSelector)
	if !ok {
		t.Fatalf("selector = %T, want provider.RefListSelector", p.LastSelector())
	}
	if len(selector.Refs) != 1 || selector.Refs[0].Raw != "acme/widgets#112" {
		t.Fatalf("selector Refs = %+v, want one stdin Ref", selector.Refs)
	}
	if p.DetailCalls() != 0 {
		t.Errorf("DetailCalls = %d, want 0: a one-Ref Watchlist must not decode", p.DetailCalls())
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil {
		t.Fatalf("unmarshal document: %v", err)
	}
	var watchlist struct {
		Epic     json.RawMessage `json:"epic"`
		Selector struct {
			Kind string   `json:"kind"`
			Refs []string `json:"refs"`
		} `json:"selector"`
	}
	if err := json.Unmarshal(doc["watchlist"], &watchlist); err != nil {
		t.Fatalf("unmarshal watchlist: %v", err)
	}
	if len(watchlist.Epic) != 0 {
		t.Error("one-Ref stdin Watchlist unexpectedly carries an outer epic")
	}
	if watchlist.Selector.Kind != "ref_list" ||
		!reflect.DeepEqual(watchlist.Selector.Refs, []string{"acme/widgets#112"}) {
		t.Errorf("watchlist = %+v, want one-ticket Ref-list", watchlist)
	}
	var tickets []json.RawMessage
	if err := json.Unmarshal(doc["tickets"], &tickets); err != nil {
		t.Fatalf("unmarshal tickets: %v", err)
	}
	if len(tickets) != 1 {
		t.Errorf("tickets = %d, want 1", len(tickets))
	}
}

func TestStdinDuplicateRefsKeepFirstSpelling(t *testing.T) {
	p := fake.New()
	got := runStdin([]string{"--json", "-"},
		"acme/widgets#112 https://github.com/ACME/WIDGETS/issues/112", p)
	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	selector := p.LastSelector().(provider.RefListSelector)
	if len(selector.Refs) != 1 || selector.Refs[0].Raw != "acme/widgets#112" {
		t.Fatalf("selector Refs = %+v, want first spelling only", selector.Refs)
	}
}

func TestStdinMixedTrackerRefListFailsBeforeProvider(t *testing.T) {
	p := fake.New()
	got := runStdin([]string{"--json", "-"},
		"https://github.com/acme/widgets/issues/112\nhttps://gitlab.com/acme/widgets/-/issues/39", p)
	if got.code != 1 || got.stdout != "" {
		t.Fatalf("result = code %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stderr, "must use one Tracker") {
		t.Errorf("stderr = %q, want mixed-Tracker error", got.stderr)
	}
	if p.ResolveCalls() != 0 {
		t.Errorf("ResolveCalls = %d, want 0", p.ResolveCalls())
	}
}

func TestStdinSameHostCrossRepositoryRefListSucceeds(t *testing.T) {
	p := fake.New()
	got := runStdin([]string{"--json", "-"}, "acme/widgets#112 acme/gadgets#7", p)
	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	selector := p.LastSelector().(provider.RefListSelector)
	if len(selector.Refs) != 2 || selector.Refs[0].Repo != "widgets" || selector.Refs[1].Repo != "gadgets" {
		t.Errorf("selector Refs = %+v, want ordered cross-repository Refs", selector.Refs)
	}
}

func TestMalformedStdinMemberProducesNoPartialDocument(t *testing.T) {
	p := fake.New()
	got := runStdin([]string{"--json", "-"}, "acme/widgets#112 not-a-ref", p)
	if got.code != 1 || got.stdout != "" {
		t.Fatalf("result = code %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if p.ResolveCalls() != 0 {
		t.Errorf("ResolveCalls = %d, want 0", p.ResolveCalls())
	}
}

func TestJSONRefListFlagsBeforeAndBetweenRefs(t *testing.T) {
	before := run([]string{"--json", "acme/widgets#112", "acme/widgets#115"}, fake.New())
	between := run([]string{"acme/widgets#112", "--json", "acme/widgets#115"}, fake.New())
	if before.code != 0 || between.code != 0 {
		t.Fatalf("runs failed: before=%q between=%q", before.stderr, between.stderr)
	}
	if before.stdout != between.stdout {
		t.Error("interspersed flag changed Ref-list order or output")
	}
}

func TestDuplicateRefsKeepFirstSpellingAndRemainARefList(t *testing.T) {
	p := fake.New()
	got := run([]string{"--json", "acme/widgets#112", "https://github.com/ACME/WIDGETS/issues/112"}, p)
	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	selector := p.LastSelector().(provider.RefListSelector)
	if len(selector.Refs) != 1 || selector.Refs[0].Raw != "acme/widgets#112" {
		t.Fatalf("selector refs = %+v, want first spelling only", selector.Refs)
	}
	var doc struct {
		Watchlist struct {
			Selector struct {
				Kind string   `json:"kind"`
				Refs []string `json:"refs"`
			} `json:"selector"`
		} `json:"watchlist"`
		Tickets []json.RawMessage `json:"tickets"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Watchlist.Selector.Kind != "ref_list" || !reflect.DeepEqual(doc.Watchlist.Selector.Refs, []string{"acme/widgets#112"}) {
		t.Errorf("selector document = %+v", doc.Watchlist.Selector)
	}
	if len(doc.Tickets) != 1 {
		t.Errorf("tickets = %d, want 1", len(doc.Tickets))
	}
}

func TestMixedTrackerRefListFailsBeforeProvider(t *testing.T) {
	p := fake.New()
	got := run([]string{
		"--json",
		"https://github.com/acme/widgets/issues/112",
		"https://gitlab.com/acme/widgets/-/issues/39",
	}, p)
	if got.code != 1 || got.stdout != "" {
		t.Fatalf("result = code %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	want := "sitrep: Refs in one Watchlist must use one Tracker; \"https://github.com/acme/widgets/issues/112\" resolves to GitHub (github.com), while \"https://gitlab.com/acme/widgets/-/issues/39\" resolves to GitLab (gitlab.com)\n"
	if got.stderr != want {
		t.Errorf("stderr = %q, want %q", got.stderr, want)
	}
	if p.ResolveCalls() != 0 {
		t.Errorf("ResolveCalls = %d, want 0", p.ResolveCalls())
	}
}

func TestSameTrackerDifferentHostsFailsBeforeProvider(t *testing.T) {
	p := fake.New()
	got := run([]string{
		"--json",
		"https://github.com/acme/widgets/issues/112",
		"https://ghe.acme.test/acme/widgets/issues/115",
	}, p)
	if got.code != 1 || got.stdout != "" {
		t.Fatalf("result = code %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	want := "sitrep: Refs in one Watchlist must use one Tracker connection and host; \"https://github.com/acme/widgets/issues/112\" resolves to the GitHub Provider at github.com, while \"https://ghe.acme.test/acme/widgets/issues/115\" resolves to the GitHub Provider at ghe.acme.test\n"
	if got.stderr != want {
		t.Errorf("stderr = %q, want %q", got.stderr, want)
	}
	if p.ResolveCalls() != 0 {
		t.Errorf("ResolveCalls = %d, want 0", p.ResolveCalls())
	}
}

// Run's production path resolves the Provider from --provider, which is how
// sitrep walks end to end today.
func TestRunResolvesTheFakeProvider(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := cli.Run([]string{"--provider", "fake", "--json", "111"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshalling the epic document: %v", err)
	}
	if got := doc["schema_version"]; got != float64(2) {
		t.Errorf("schema_version = %v, want 2", got)
	}
}

func TestRunResolvesTheFakeProviderForQueryWithoutProfileOrOrigin(t *testing.T) {
	var stdout, stderr bytes.Buffer
	query := " label:agent "

	code := cli.RunWith([]string{"--provider", "fake", "--json", "--query", query}, &stdout, &stderr, cli.Deps{
		Now: fixedClock,
		RemoteLookup: func(context.Context, string, string) (string, error) {
			panic("origin was read for explicit fake Query")
		},
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr.String())
	}

	var doc struct {
		Watchlist struct {
			Selector struct {
				Kind  string `json:"kind"`
				Query string `json:"query"`
			} `json:"selector"`
		} `json:"watchlist"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal Query document: %v", err)
	}
	if doc.Watchlist.Selector.Kind != "query" || doc.Watchlist.Selector.Query != query {
		t.Errorf("selector = %+v, want exact fake Query", doc.Watchlist.Selector)
	}
}
