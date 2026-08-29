package jsonout_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/ref"
	"github.com/niekcandaele/sitrep/internal/render/jsonout"
)

// --json is an agent's copy of the same Tickets a human approves on screen, so
// it is covered by the same rule: it reads what provider.Sanitized returned.
// The document is asserted on the literal runes because encoding/json emits
// these code points as themselves rather than escaping them.
func TestJSONTitleArrivesBalanced(t *testing.T) {
	const title = "Ship the widget \u202eNORMAL"

	p := provider.Sanitized(&bidiProvider{title: title})
	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: ref.Ref{}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	var buf bytes.Buffer
	if err := jsonout.RenderWatchlist(&buf, jsonout.WatchlistDocument{
		Snapshot:     snap,
		Selector:     provider.EpicSelector{Ref: ref.Ref{}},
		ProviderName: p.Name(),
	}); err != nil {
		t.Fatalf("RenderWatchlist: %v", err)
	}
	document := buf.String()

	if !strings.Contains(document, title+"\u202c") {
		t.Errorf("the JSON document does not carry the title terminated by U+202C:\n%+q", document)
	}
	if strings.Count(document, "\u202e") != strings.Count(document, "\u202c") {
		t.Errorf("the JSON document has %d U+202E against %d U+202C:\n%+q",
			strings.Count(document, "\u202e"), strings.Count(document, "\u202c"), document)
	}
}

// bidiProvider answers with one Ticket whose title opens a bidirectional
// override and never closes it.
type bidiProvider struct{ title string }

func (*bidiProvider) Name() string                     { return "bidi" }
func (*bidiProvider) Capabilities() model.Capabilities { return model.Capabilities{} }
func (*bidiProvider) FetchDetail(context.Context, model.TicketID) (model.Detail, error) {
	return model.Detail{}, nil
}

func (p *bidiProvider) Resolve(context.Context, provider.Selector) (model.WatchlistSnapshot, error) {
	return model.WatchlistSnapshot{
		Header:  model.WatchlistHeader{Key: "EPIC-1", Title: "Watchlist"},
		Tickets: []model.Ticket{{ID: "t1", Key: "#1", Title: p.title, Status: model.StatusTodo}},
	}, nil
}
