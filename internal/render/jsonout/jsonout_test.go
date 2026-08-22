package jsonout_test

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
	"github.com/niekcandaele/sitrep/internal/ref"
	"github.com/niekcandaele/sitrep/internal/render/jsonout"
)

// generatedAt is the fixed clock every golden in this package is rendered
// against.
var generatedAt = time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)

const (
	richTicket    = model.TicketID("acme/widgets#112")
	minimalTicket = model.TicketID("acme/widgets#115")
	emptyDescTick = model.TicketID("acme/widgets#119")
)

func renderDetail(t *testing.T, p *fake.Provider, id model.TicketID) []byte {
	t.Helper()

	detail, err := p.FetchDetail(context.Background(), id)
	if err != nil {
		t.Fatalf("FetchDetail(%q): %v", id, err)
	}

	var buf bytes.Buffer
	if err := jsonout.RenderDetail(&buf, detail, p.Capabilities(), p.Name(), generatedAt); err != nil {
		t.Fatalf("RenderDetail: %v", err)
	}
	return buf.Bytes()
}

// hasKey reports whether a rendered document has key at its top level. Looking
// at the parsed document rather than the raw bytes keeps a capability flag of
// the same name from answering for the section it gates.
func hasKey(t *testing.T, raw []byte, key string) bool {
	t.Helper()

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshalling the document: %v", err)
	}
	_, ok := doc[key]
	return ok
}

func TestRenderDetailRich(t *testing.T) {
	got := renderDetail(t, fake.New(), richTicket)

	checkGolden(t, "detail.golden.json", got)

	for _, want := range []string{`"blocked_by"`, `"blocks"`, `"relates"`, `"is duplicated by"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("detail document is missing %s", want)
		}
	}
}

func TestRenderDetailMinimal(t *testing.T) {
	got := renderDetail(t, fake.New(), minimalTicket)

	checkGolden(t, "detail_minimal.golden.json", got)

	for _, key := range []string{"comments", "links"} {
		if hasKey(t, got, key) {
			t.Errorf("a ticket with nothing to show still emitted %q", key)
		}
	}
}

// An empty description is a real state, not a rendering failure: the key stays
// so consumers do not have to special-case it.
func TestRenderDetailEmptyDescription(t *testing.T) {
	got := renderDetail(t, fake.New(), emptyDescTick)

	checkGolden(t, "detail_empty_description.golden.json", got)

	var doc struct {
		Description *string `json:"description"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("unmarshalling the detail document: %v", err)
	}
	if doc.Description == nil || *doc.Description != "" {
		t.Errorf("description = %v, want an empty string", doc.Description)
	}
}

func TestRenderDetailWithoutCommentsCapability(t *testing.T) {
	p := fake.New(fake.WithCapabilities(model.Capabilities{
		Hierarchy:     true,
		BlockingLinks: true,
		PullRequests:  true,
	}))

	got := renderDetail(t, p, richTicket)

	if hasKey(t, got, "comments") {
		t.Errorf("comments key present without the capability:\n%s", got)
	}
	if !hasKey(t, got, "links") {
		t.Errorf("links disappeared along with comments:\n%s", got)
	}
}

func TestRenderWatchlistAlwaysEmitsATicketArray(t *testing.T) {
	var buf bytes.Buffer
	empty := model.WatchlistSnapshot{Epic: model.Epic{Key: "#1"}, FetchedAt: generatedAt}
	selector := provider.EpicSelector{Ref: ref.Ref{Raw: "1"}}
	if err := jsonout.RenderWatchlist(&buf, empty, selector, "fake"); err != nil {
		t.Fatalf("RenderWatchlist: %v", err)
	}

	if !strings.Contains(buf.String(), `"tickets": []`) {
		t.Errorf("an epic with no tickets must emit an empty array, got:\n%s", buf.String())
	}
}

func TestRenderRefListSelectorAndOmitsEpic(t *testing.T) {
	snap := fake.FixtureRefListSnapshot()
	snap.FetchedAt = generatedAt
	selector := provider.RefListSelector{Refs: []ref.Ref{
		{Raw: "  acme/widgets#121  "},
		{Owner: "acme", Repo: "widgets", Number: 112},
	}}

	var buf bytes.Buffer
	if err := jsonout.RenderWatchlist(&buf, snap, selector, "fake"); err != nil {
		t.Fatalf("RenderWatchlist: %v", err)
	}

	var doc struct {
		SchemaVersion int `json:"schema_version"`
		Watchlist     struct {
			Selector struct {
				Kind string   `json:"kind"`
				Refs []string `json:"refs"`
			} `json:"selector"`
			Epic *json.RawMessage `json:"epic"`
		} `json:"watchlist"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.SchemaVersion != 2 {
		t.Errorf("schema_version = %d, want 2", doc.SchemaVersion)
	}
	if doc.Watchlist.Selector.Kind != "ref_list" {
		t.Errorf("selector.kind = %q, want ref_list", doc.Watchlist.Selector.Kind)
	}
	wantRefs := []string{"acme/widgets#121", "acme/widgets#112"}
	if !reflect.DeepEqual(doc.Watchlist.Selector.Refs, wantRefs) {
		t.Errorf("selector.refs = %q, want %q", doc.Watchlist.Selector.Refs, wantRefs)
	}
	if doc.Watchlist.Epic != nil {
		t.Error("Ref-list Watchlist emitted an epic")
	}
}

func TestRenderQueryLimitReachedIsOptionalAndStructured(t *testing.T) {
	const query = "state=opened&labels=ready"
	p := fake.New(fake.WithMaxTickets(2))
	snap, err := p.Resolve(context.Background(), provider.QuerySelector{Query: query})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	snap.FetchedAt = generatedAt

	render := func(t *testing.T, snap model.WatchlistSnapshot, selector provider.Selector) []byte {
		t.Helper()
		var buf bytes.Buffer
		if err := jsonout.RenderWatchlist(&buf, snap, selector, p.Name()); err != nil {
			t.Fatalf("RenderWatchlist: %v", err)
		}
		return buf.Bytes()
	}

	raw := render(t, snap, provider.QuerySelector{Query: query})
	var doc struct {
		SchemaVersion int `json:"schema_version"`
		Watchlist     struct {
			Selector struct {
				Kind  string `json:"kind"`
				Query string `json:"query"`
			} `json:"selector"`
			LimitReached *bool `json:"limit_reached"`
		} `json:"watchlist"`
		Progress struct {
			Total int `json:"total"`
		} `json:"progress"`
		Tickets []json.RawMessage `json:"tickets"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.SchemaVersion != 2 || doc.Watchlist.Selector.Kind != "query" || doc.Watchlist.Selector.Query != query {
		t.Errorf("schema/selector = %d/%+v, want schema 2 and exact Query", doc.SchemaVersion, doc.Watchlist.Selector)
	}
	if doc.Watchlist.LimitReached == nil || !*doc.Watchlist.LimitReached {
		t.Errorf("limit_reached = %v, want present true", doc.Watchlist.LimitReached)
	}
	if len(doc.Tickets) != 2 || doc.Progress.Total != 2 {
		t.Errorf("tickets/progress.total = %d/%d, want 2/2", len(doc.Tickets), doc.Progress.Total)
	}
	for _, forbidden := range []string{"max_tickets", `"total_matches"`, "Limit reached"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("limited Query JSON contains unsupported %q: %s", forbidden, raw)
		}
	}

	snap.LimitReached = false
	if raw := render(t, snap, provider.QuerySelector{Query: query}); strings.Contains(string(raw), `"limit_reached"`) {
		t.Errorf("exhausted Query emitted limit_reached: %s", raw)
	}
	snap.LimitReached = true
	if raw := render(t, snap, provider.RefListSelector{Refs: []ref.Ref{{Raw: "acme/widgets#112"}}}); strings.Contains(string(raw), `"limit_reached"`) {
		t.Errorf("Ref-list emitted Query-only limit_reached: %s", raw)
	}
}

func TestRenderWatchlistRejectsUnsupportedSelector(t *testing.T) {
	var buf bytes.Buffer
	if err := jsonout.RenderWatchlist(&buf, model.WatchlistSnapshot{}, nil, "fake"); err == nil {
		t.Fatal("RenderWatchlist accepted a nil Selector")
	}
}

// Whatever else changes, every document sitrep emits is valid JSON carrying the
// schema version a consumer keys off.
func TestDocumentsCarryTheSchemaVersion(t *testing.T) {
	p := fake.New()

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: ref.Ref{Raw: "111"}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	snap.FetchedAt = generatedAt

	var watchlistBuf bytes.Buffer
	selector := provider.EpicSelector{Ref: ref.Ref{Raw: "111"}}
	if err := jsonout.RenderWatchlist(&watchlistBuf, snap, selector, p.Name()); err != nil {
		t.Fatalf("RenderWatchlist: %v", err)
	}

	documents := map[string]struct {
		raw     []byte
		version float64
	}{
		"watchlist": {raw: watchlistBuf.Bytes(), version: 2},
		"detail":    {raw: renderDetail(t, p, richTicket), version: 1},
	}
	for name, document := range documents {
		var doc map[string]any
		if err := json.Unmarshal(document.raw, &doc); err != nil {
			t.Errorf("the %s document is not valid JSON: %v", name, err)
			continue
		}
		if got := doc["schema_version"]; got != document.version {
			t.Errorf("%s schema_version = %v, want %v", name, got, document.version)
		}
		if got := doc["generated_at"]; got != "2026-01-15T12:00:00Z" {
			t.Errorf("%s generated_at = %v, want an RFC 3339 UTC timestamp", name, got)
		}
	}
}
