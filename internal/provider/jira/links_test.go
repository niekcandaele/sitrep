package jira_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

// linksFor drills into ABC-12 with the given link type catalogue and returns
// the Links it produced.
func linksFor(t *testing.T, catalogue response) []model.Link {
	t.Helper()

	s := newReplayServer(t, map[string][]response{
		linkTypePath: {catalogue},
		ticketPath:   {{file: "detail_full.json"}},
		commentsPath: {{file: "comments_page.json"}},
	})
	detail, err := newProvider(s).FetchDetail(context.Background(), "ABC-12")
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}
	return detail.Links
}

// want is one expected Link, flattened to what a reader of the screen sees.
type wantLink struct {
	kind   model.LinkKind
	label  string
	target string
}

func checkLinks(t *testing.T, got []model.Link, want []wantLink) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("got %d links, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Kind != w.kind || got[i].NativeLabel != w.label || got[i].Target.Key != w.target {
			t.Errorf("Links[%d] = {%v %q %s}, want {%v %q %s}",
				i, got[i].Kind, got[i].NativeLabel, got[i].Target.Key, w.kind, w.label, w.target)
		}
	}
}

// The direction rule, which getting backwards would invert every dependency on
// screen: an inwardIssue entry means the target points at this Ticket, and an
// outwardIssue entry means this Ticket points at the target.
//
// The order is Jira's own order within issuelinks — the driver sorts nothing.
func TestLinksWithTheStockCatalogue(t *testing.T) {
	checkLinks(t, linksFor(t, response{file: "issue_link_types.json"}), []wantLink{
		{model.LinkBlockedBy, "is blocked by", "ABC-3"},
		{model.LinkBlocks, "blocks", "ABC-9"},
		{model.LinkRelates, "relates to", "ABC-6"},
		// A type id the catalogue does not carry falls back to the type object
		// Jira inlines on the entry itself.
		{model.LinkRelates, "causes", "DEF-4"},
		// The fifth entry names neither side and is skipped: it points at
		// nothing that can be displayed or navigated to.
	})
}

// The acceptance criterion: an instance that renamed its link types still gets
// its dependencies read as dependencies, and an invented type is Relates
// carrying the instance's own label rather than being dropped.
func TestLinksWithARenamedCatalogue(t *testing.T) {
	checkLinks(t, linksFor(t, response{file: "issue_link_types_renamed.json"}), []wantLink{
		{model.LinkBlockedBy, "is blocked by", "ABC-3"},
		{model.LinkBlocks, "blocks", "ABC-9"},
		{model.LinkRelates, "relates to", "ABC-6"},
		{model.LinkRelates, "causes", "DEF-4"},
	})
}

// An administrator may swap the two directions of the blocking type. Reading
// the pair as a unit rather than each phrase in its own voice would show a
// blocked ticket as blocking — the opposite of the truth, on the one field a
// human uses to decide what to work on next.
func TestLinksWithAnInvertedCatalogue(t *testing.T) {
	checkLinks(t, linksFor(t, response{file: "issue_link_types_inverted.json"}), []wantLink{
		// The entry Jira reports through inwardIssue now carries the active
		// phrase, so this Ticket blocks ABC-3.
		{model.LinkBlocks, "blocks", "ABC-3"},
		{model.LinkBlockedBy, "is blocked by", "ABC-9"},
		// Neither remaining type is in this catalogue, so both fall back to the
		// type object Jira inlines on the entry.
		{model.LinkRelates, "relates to", "ABC-6"},
		{model.LinkRelates, "causes", "DEF-4"},
	})
}

// The catalogue's job is consistency, not availability: a Detail that renders
// its links through their inline labels is strictly better than one that
// refuses to open.
func TestLinkTypeDiscoveryFailureStillOpensTheDetail(t *testing.T) {
	links := linksFor(t, response{status: http.StatusInternalServerError, body: `{}`})
	checkLinks(t, links, []wantLink{
		{model.LinkBlockedBy, "is blocked by", "ABC-3"},
		{model.LinkBlocks, "blocks", "ABC-9"},
		{model.LinkRelates, "relates to", "ABC-6"},
		{model.LinkRelates, "causes", "DEF-4"},
	})
}

// A link target carries enough to display and navigate: its own key, title,
// browse URL, Status Category and Native Status.
func TestLinkTargetsCarryTheirOwnStatus(t *testing.T) {
	links := linksFor(t, response{file: "issue_link_types.json"})

	blockedBy := links[0].Target
	want := model.LinkTarget{
		ID:           "ABC-3",
		Key:          "ABC-3",
		Title:        "Walking skeleton",
		URL:          "https://acme.atlassian.net/browse/ABC-3",
		Status:       model.StatusInProgress,
		NativeStatus: "In Review",
	}
	if blockedBy != want {
		t.Errorf("Links[0].Target = %+v, want %+v", blockedBy, want)
	}

	// A won't-do target reads in the links table exactly as it does in the list.
	cancelled := links[3].Target
	if cancelled.Status != model.StatusCancelled || cancelled.NativeStatus != "Won't Do" {
		t.Errorf("Links[3].Target = {%v %q}, want {cancelled \"Won't Do\"}",
			cancelled.Status, cancelled.NativeStatus)
	}
}
