package fake_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

// blockingID is the fixture's TicketID for one of its 200-series members.
func blockingID(number int) model.TicketID {
	return model.TicketID(fmt.Sprintf("acme/widgets#%d", number))
}

// unreadableTicket is the member deliberately absent from the blocking Details.
const unreadableTicket = model.TicketID("acme/widgets#211")

func TestBlockingFixtureIsDeterministicAndFresh(t *testing.T) {
	if !reflect.DeepEqual(fake.FixtureBlockingSnapshot(), fake.FixtureBlockingSnapshot()) {
		t.Fatal("FixtureBlockingSnapshot is not deterministic")
	}
	if !reflect.DeepEqual(fake.FixtureBlockingDetails(), fake.FixtureBlockingDetails()) {
		t.Fatal("FixtureBlockingDetails is not deterministic")
	}

	snap := fake.FixtureBlockingSnapshot()
	snap.Tickets[0].Title = "mutated"
	if fake.FixtureBlockingSnapshot().Tickets[0].Title == "mutated" {
		t.Error("FixtureBlockingSnapshot must build a fresh copy per call")
	}

	details := fake.FixtureBlockingDetails()
	d := details[blockingID(201)]
	d.Links[0].NativeLabel = "mutated"
	delete(details, blockingID(202))
	fresh := fake.FixtureBlockingDetails()
	if fresh[blockingID(201)].Links[0].NativeLabel == "mutated" {
		t.Error("FixtureBlockingDetails must build a fresh copy per call")
	}
	if _, ok := fresh[blockingID(202)]; !ok {
		t.Error("FixtureBlockingDetails must build a fresh map per call")
	}
}

func TestWithBlockingFixtureServesBothHalves(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: fixtureRef(200)})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.Epic.Key != "#200" || len(snap.Tickets) != 13 {
		t.Fatalf("Resolve served %q with %d Tickets, want #200 with 13",
			snap.Epic.Key, len(snap.Tickets))
	}

	d, err := p.FetchDetail(context.Background(), blockingID(201))
	if err != nil {
		t.Fatalf("FetchDetail(#201): %v", err)
	}
	if len(d.Links) != 2 {
		t.Errorf("FetchDetail(#201) links = %+v, want two blockers", d.Links)
	}

	if _, err := p.FetchDetail(context.Background(), unreadableTicket); err == nil {
		t.Error("FetchDetail(#211) must fail: its Links are the unreadable case")
	}
}

func TestBlockingFixtureWithoutBlockingLinksCapability(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture(), fake.WithCapabilities(model.Capabilities{
		Selectors: model.SelectorCapabilities{Epic: true},
	}))
	d, err := p.FetchDetail(context.Background(), blockingID(201))
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}
	if d.Links != nil {
		t.Errorf("Links = %+v, want none: a Provider without BlockingLinks strips them", d.Links)
	}
	d, err = p.FetchDetail(context.Background(), blockingID(212))
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}
	if len(d.Links) != 1 || d.Links[0].Kind != model.LinkRelates {
		t.Errorf("Links = %+v, want the Relates link to survive", d.Links)
	}
}

// TestBlockingFixtureThroughTheGraph ties the two halves together: what the
// fake serves, run through the computation the fixture exists for.
func TestBlockingFixtureThroughTheGraph(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	ctx := context.Background()
	snap, err := p.Resolve(ctx, provider.EpicSelector{Ref: fixtureRef(200)})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	links := make(map[model.TicketID][]model.Link, len(snap.Tickets))
	for _, ticket := range snap.Tickets {
		d, err := p.FetchDetail(ctx, ticket.ID)
		if err != nil {
			continue // exactly the fail-closed case: the key stays absent.
		}
		links[ticket.ID] = d.Links
	}
	if _, ok := links[unreadableTicket]; ok {
		t.Fatalf("%s must have no readable Links", unreadableTicket)
	}

	g := model.BuildBlockingGraph(snap.Tickets, links, snap.Capabilities)

	actionable := map[model.TicketID]bool{
		blockingID(201): true,
		blockingID(212): true,
		blockingID(213): true,
	}
	for _, m := range g.Members() {
		if m.Actionable != actionable[m.TicketID] {
			t.Errorf("%s Actionable = %v, want %v", m.TicketID, m.Actionable, actionable[m.TicketID])
		}
	}

	inProgress, _ := g.For(blockingID(207))
	if inProgress.Status != model.StatusInProgress || len(inProgress.Unmet()) != 1 {
		t.Errorf("#207 = %+v, want InProgress and blocked", inProgress)
	}

	unreadable, _ := g.For(unreadableTicket)
	if unreadable.LinksKnown || !unreadable.HasUnknown() {
		t.Errorf("#211 = %+v, want unreadable Links", unreadable)
	}

	unverified, _ := g.For(blockingID(204))
	if !unverified.HasUnknown() || len(unverified.Unmet()) != 1 || unverified.Unmet()[0].StatusKnown {
		t.Errorf("#204 = %+v, want one unverified blocker", unverified)
	}

	knownBlocked, _ := g.For(blockingID(203))
	if knownBlocked.HasUnknown() || len(knownBlocked.Unmet()) != 1 {
		t.Errorf("#203 = %+v, want one known Ghost blocker", knownBlocked)
	}

	reciprocal, _ := g.For(blockingID(201))
	if len(reciprocal.Blockers) != 2 {
		t.Errorf("#201 blockers = %+v, want the reciprocal pair deduplicated to two",
			reciprocal.Blockers)
	}

	var ghosts []model.TicketID
	for _, gh := range g.Ghosts() {
		ghosts = append(ghosts, gh.Target.ID)
	}
	wantGhosts := []model.TicketID{"acme/widgets#401", "acme/widgets#402", "acme/widgets#403"}
	if !reflect.DeepEqual(ghosts, wantGhosts) {
		t.Errorf("Ghosts() = %v, want %v", ghosts, wantGhosts)
	}

	wantCycles := [][]model.TicketID{
		{blockingID(208), blockingID(209)},
		{blockingID(210)},
	}
	if !reflect.DeepEqual(g.Cycles(), wantCycles) {
		t.Errorf("Cycles() = %v, want %v", g.Cycles(), wantCycles)
	}
}

func TestExistingFixturesAreUntouchedByTheBlockingOne(t *testing.T) {
	p := fake.New()
	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: fixtureRef(111)})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !reflect.DeepEqual(snap.Tickets, fake.FixtureSnapshot().Tickets) {
		t.Error("the default fixture must still be the #111 Watchlist")
	}
}
