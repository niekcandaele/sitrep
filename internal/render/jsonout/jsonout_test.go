package jsonout_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
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

func TestRenderEpicAlwaysEmitsATicketArray(t *testing.T) {
	var buf bytes.Buffer
	empty := model.EpicSnapshot{Epic: model.Epic{Key: "#1"}, FetchedAt: generatedAt}
	if err := jsonout.RenderEpic(&buf, empty, "fake"); err != nil {
		t.Fatalf("RenderEpic: %v", err)
	}

	if !strings.Contains(buf.String(), `"tickets": []`) {
		t.Errorf("an epic with no tickets must emit an empty array, got:\n%s", buf.String())
	}
}

// Whatever else changes, every document sitrep emits is valid JSON carrying the
// schema version a consumer keys off.
func TestDocumentsCarryTheSchemaVersion(t *testing.T) {
	p := fake.New()

	snap, err := p.FetchEpic(context.Background(), ref.Ref{Raw: "111"})
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	snap.FetchedAt = generatedAt

	var epicBuf bytes.Buffer
	if err := jsonout.RenderEpic(&epicBuf, snap, p.Name()); err != nil {
		t.Fatalf("RenderEpic: %v", err)
	}

	documents := map[string][]byte{
		"epic":   epicBuf.Bytes(),
		"detail": renderDetail(t, p, richTicket),
	}
	for name, raw := range documents {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Errorf("the %s document is not valid JSON: %v", name, err)
			continue
		}
		if got := doc["schema_version"]; got != float64(1) {
			t.Errorf("%s schema_version = %v, want 1", name, got)
		}
		if got := doc["generated_at"]; got != "2026-01-15T12:00:00Z" {
			t.Errorf("%s generated_at = %v, want an RFC 3339 UTC timestamp", name, got)
		}
	}
}
