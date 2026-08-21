package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

// decodedTicket is the fixture Ticket #112 as a decoder hands it over: the
// Ticket itself, and the collection it was reached through.
func decodedTicket() OpenTicket {
	snap := fake.FixtureSnapshot()
	return OpenTicket{
		Ticket:       snap.Tickets[0],
		Parent:       Header{Key: snap.Epic.Key, Title: snap.Epic.Title, URL: snap.Epic.URL},
		Capabilities: allCaps,
	}
}

// startDecoded runs the program the way the decoder starts it: on one Ticket's
// Detail, with a Source for the collection behind it that nothing has read yet.
func startDecoded(t *testing.T, c *clock, p *fake.Provider, open OpenTicket, source Source) *session {
	t.Helper()

	return startWith(t, c, Options{
		Source:       source,
		DetailSource: TicketDetailSource(p),
		Open:         &open,
		Interval:     time.Minute,
		Now:          c.now,
	})
}

// The headline decoder frame: the breadcrumb to the Epic, the Ticket's own
// identity and meta line, its Detail, and the u affordance in the footer.
func TestDecodedFrame(t *testing.T) {
	p := fake.New()
	c := newClock()
	s := startDecoded(t, c, p, decodedTicket(), epicSource(p, c))
	s.waitFor(t, "DESCRIPTION")

	// Several beats past the interval: a decoder session that never walks up
	// must not fetch a collection nobody asked to see.
	for range 3 {
		c.advance(61 * time.Second)
		s.beat()
	}

	m, got := s.finish(t)

	checkGolden(t, "decoded.golden.txt", got)
	if m.mode != modeDetail {
		t.Error("the program did not start on the Ticket's Detail")
	}
	if n := p.DetailCalls(); n != 1 {
		t.Errorf("DetailCalls() = %d, want exactly 1", n)
	}
	if n := p.EpicCalls(); n != 0 {
		t.Errorf("EpicCalls() = %d, want 0 until the user walks up", n)
	}
	// The footer line is the whole affordance for the walk-up: no second help
	// line, no box.
	if !m.detailKeys.Parent.Enabled() {
		t.Error("the walk-up binding is disabled with a parent and a Source")
	}
	if !strings.Contains(string(got), "u epic") {
		t.Errorf("the footer does not make the walk-up discoverable:\n%s", got)
	}
}

// A Ticket with no parent opens like any other: no breadcrumb, no walk-up key
// offered, no error and no placeholder.
func TestDecodedFrameWithNoParent(t *testing.T) {
	open := decodedTicket()
	open.Parent = Header{}

	p := fake.New()
	s := startDecoded(t, newClock(), p, open, nil)
	s.waitFor(t, "DESCRIPTION")

	// u with nothing to walk up into does nothing at all.
	s.tm.Send(keyPress("u"))

	m, got := s.finish(t)

	checkGolden(t, "decoded_no_parent.golden.txt", got)
	if m.mode != modeDetail {
		t.Error("u left the Detail screen with no collection to go to")
	}
	if n := p.EpicCalls(); n != 0 {
		t.Errorf("EpicCalls() = %d, want 0", n)
	}
	if strings.Contains(string(got), "u epic") {
		t.Errorf("the footer offers a walk-up this Ticket has no parent for:\n%s", got)
	}
	// The breadcrumb line is the zero-Parent one: staleness and nothing else.
	if strings.Contains(string(got), "Widget sync v2") {
		t.Errorf("a Ticket with no parent drew a breadcrumb:\n%s", got)
	}
}

// The walk-up lands in the full monitor: the grouped list, the progress bar and
// the staleness indicator, all of #8's behaviour with nothing special about it.
func TestWalkUpOpensTheCollection(t *testing.T) {
	p := fake.New()
	c := newClock()
	s := startDecoded(t, c, p, decodedTicket(), epicSource(p, c))
	s.waitFor(t, "DESCRIPTION")

	s.tm.Send(keyPress("u"))
	s.waitFor(t, "IN PROGRESS (3)")

	if n := p.EpicCalls(); n != 1 {
		t.Errorf("EpicCalls() = %d, want exactly 1: the walk-up is the first list read", n)
	}

	m, got := s.finish(t)

	checkGolden(t, "decoded_up.golden.txt", got)
	if m.mode != modeList {
		t.Error("u did not open the collection")
	}
}

// From the collection the walk-up landed in, the heartbeat refreshes exactly as
// #8 built it: the list is armed now, and nothing about it is special.
func TestWalkUpArmsTheHeartbeat(t *testing.T) {
	p := fake.New()
	c := newClock()
	s := startDecoded(t, c, p, decodedTicket(), epicSource(p, c))
	s.waitFor(t, "DESCRIPTION")

	s.tm.Send(keyPress("u"))
	waitUntil(t, "the first list read", func() bool { return p.EpicCalls() >= 1 })

	c.advance(61 * time.Second)
	s.beat()
	waitUntil(t, "the auto-refresh after the walk-up", func() bool { return p.EpicCalls() >= 2 })

	s.finish(t)
}

// The whole "decoding one ticket flows into watching the whole batch" story in
// one number: after the walk-up, re-opening the decoded Ticket is served from
// the session cache and costs the Tracker nothing.
func TestWalkUpReusesTheDecodedDetail(t *testing.T) {
	const decoded = model.TicketID("acme/widgets#112")

	p := fake.New()
	c := newClock()
	s := startDecoded(t, c, p, decodedTicket(), epicSource(p, c))
	s.waitFor(t, "DESCRIPTION")

	s.tm.Send(keyPress("u"))
	s.waitFor(t, "IN PROGRESS (3)")
	// The fixture's first Ticket is #112, which is the one that was decoded.
	s.tm.Send(enterKey)

	m, _ := s.finish(t)

	if m.mode != modeDetail {
		t.Error("enter did not re-open the decoded Ticket")
	}
	if n := p.DetailCallsFor(decoded); n != 1 {
		t.Errorf("DetailCallsFor(%s) = %d, want 1: the walk-up serves the session cache", decoded, n)
	}
}

// esc from a decoded Detail quits: it is the esc ladder's last rung, because
// there is no list behind this screen to go back to.
func TestEscapeQuitsADecodedDetail(t *testing.T) {
	p := fake.New()
	s := startDecoded(t, newClock(), p, decodedTicket(), nil)
	s.waitFor(t, "DESCRIPTION")

	m, _ := s.finishWith(t, escKey)

	if !m.quitting {
		t.Error("esc did not quit a Detail with nothing behind it")
	}
}

// A seeded monitor draws the reading its caller already took and does not fetch
// it again — and then refreshes on the caller's reading, one interval after it
// was taken rather than one interval after startup.
func TestSeededMonitorDrawsWithoutRefetching(t *testing.T) {
	snap := fake.FixtureSnapshot()
	c := newClock()
	snap.FetchedAt = c.now()
	initial := ListFromEpicSnapshot(snap)

	p := fake.New()
	s := startWith(t, c, Options{
		Source:   epicSource(p, c),
		Initial:  &initial,
		Interval: time.Minute,
		Now:      c.now,
	})
	s.waitFor(t, "Widget sync v2")

	if n := p.EpicCalls(); n != 0 {
		t.Fatalf("EpicCalls() = %d, want 0: the seeded reading was re-fetched", n)
	}

	m, got := s.finish(t)

	// The strongest form of "the first frame already has data": it is the frame
	// an unseeded monitor draws once its first fetch lands.
	checkSameFrameAs(t, "initial.golden.txt", got)
	if !m.hasData {
		t.Error("the seeded reading never landed")
	}
}

// The seed sets the refresh clock from when the reading was taken, so a monitor
// seeded with a stale reading refreshes at once rather than an interval late.
func TestSeededMonitorRefreshesOnTheReadingsAge(t *testing.T) {
	snap := fake.FixtureSnapshot()
	c := newClock()
	snap.FetchedAt = c.now()
	initial := ListFromEpicSnapshot(snap)

	p := fake.New()
	s := startWith(t, c, Options{
		Source:   epicSource(p, c),
		Initial:  &initial,
		Interval: time.Minute,
		Now:      c.now,
	})
	s.waitFor(t, "Widget sync v2")

	c.advance(30 * time.Second)
	s.beat()
	s.waitFor(t, "updated 30s ago")
	if n := p.EpicCalls(); n != 0 {
		t.Fatalf("EpicCalls() = %d after 30s of a 60s interval, want 0", n)
	}

	c.advance(31 * time.Second)
	s.beat()
	waitUntil(t, "the first auto-refresh", func() bool { return p.EpicCalls() >= 1 })

	s.finish(t)

	if n := p.EpicCalls(); n != 1 {
		t.Errorf("EpicCalls() = %d, want exactly 1", n)
	}
}
