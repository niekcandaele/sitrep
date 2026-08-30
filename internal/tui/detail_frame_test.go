package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

var (
	upKey     = tea.KeyPressMsg{Code: tea.KeyUp}
	pageUpKey = tea.KeyPressMsg{Code: tea.KeyPgUp}
)

// startBoth runs the monitor over the fake Provider through both seams — the
// polled list and the lazy Detail — which is the wiring production uses.
func startBoth(t *testing.T, p *fake.Provider, c *clock, interval time.Duration) *session {
	t.Helper()

	return startWith(t, c, Options{
		Source:       selectorSource(p, c),
		DetailSource: TicketDetailSource(p),
		Interval:     interval,
		Now:          c.now,
	})
}

// startFixtureDetail runs the fixture Epic with the first reading on screen,
// where every drill-in session begins.
func startFixtureDetail(t *testing.T) (*fake.Provider, *session) {
	t.Helper()

	p := fake.New()
	s := startBoth(t, p, newClock(), time.Minute)
	s.waitFor(t, "Widget sync v2")
	return p, s
}

// longDetails is the fixture Ticket #112 with a description taller than the
// terminal, which is what gives scrolling something to scroll. The fixture Epic
// itself is deliberately left alone: a Detail long enough to need a window is a
// property this one test needs, not a change every other golden should carry.
func longDetails() map[model.TicketID]model.Detail {
	details := fake.FixtureDetails()
	d := details["acme/widgets#112"]

	var paragraphs []string
	for i := 1; i <= 40; i++ {
		paragraphs = append(paragraphs, fmt.Sprintf("Step %d of the handshake, spelled out at length.", i))
	}
	d.Description += "\n\n" + strings.Join(paragraphs, "\n")
	details["acme/widgets#112"] = d
	return details
}

// suppressedStatusDetails gives fixture Ticket #115 — Todo with the degenerate
// Native Status "open", no assignee and no pull request — two Links: one whose
// target's status is equally degenerate and one that is informative. It is
// test-local rather than a change to the shared fixture, which would move the
// --json goldens.
func suppressedStatusDetails() map[model.TicketID]model.Detail {
	details := fake.FixtureDetails()
	details["acme/widgets#115"] = model.Detail{
		TicketID:    "acme/widgets#115",
		Description: "Shard IDs drifted apart during the first migration.",
		Links: []model.Link{
			{
				Kind:        model.LinkBlocks,
				NativeLabel: "blocks",
				Target: model.LinkTarget{
					ID:           "acme/widgets#120",
					Key:          "#120",
					Title:        "Write the sync v2 runbook",
					URL:          "https://tracker.example.test/acme/widgets/120",
					Status:       model.StatusTodo,
					NativeStatus: "open",
				},
			},
			{
				Kind:        model.LinkBlockedBy,
				NativeLabel: "is blocked by",
				Target: model.LinkTarget{
					ID:           "acme/widgets#112",
					Key:          "#112",
					Title:        "Draft the shard sync protocol",
					URL:          "https://tracker.example.test/acme/widgets/112",
					Status:       model.StatusInProgress,
					NativeStatus: "In Review",
				},
			},
		},
	}
	return details
}

// The Detail header meta line draws plain.StatusField: no Status Category
// heading stands above it, so a status the list row suppresses as redundant is
// still the only status signal here, and a [Todo] on this line is the rule
// working rather than the suppression failing. The LINKS table keeps every tag
// for the same reason.
func TestDetailFrameSuppressedNativeStatus(t *testing.T) {
	p := fake.New(fake.WithDetails(suppressedStatusDetails()))
	s := startBoth(t, p, newClock(), time.Minute)
	s.waitFor(t, "Widget sync v2")

	s.down(3)
	s.tm.Send(enterKey)
	s.waitFor(t, "LINKS")

	_, got := s.finish(t)

	checkGolden(t, "detail_suppressed_status.golden.txt", got)
	frame := string(got)
	if !strings.Contains(frame, "Reconcile widget IDs across shards") {
		t.Fatalf("the frame is not #115's Detail:\n%s", frame)
	}
	// Exactly one [open]: the link row's. The header's was suppressed, and so
	// was the #115 list row's behind the Detail screen.
	if n := strings.Count(frame, "[open]"); n != 1 {
		t.Errorf("the frame carries %d [open] tags, want only the LINKS row's:\n%s", n, frame)
	}
	for _, want := range []string{"#120", "[open]", "[In Review]"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the LINKS table lost %q; it is exempt from the suppression rule:\n%s", want, frame)
		}
	}
}

// down steps the list selection n times. The fixture's first row is #112, so
// counting down from it is how a session names the Ticket it opens.
func (s *session) down(n int) {
	for range n {
		s.tm.Send(downKey)
	}
}

// The headline Detail frame: the breadcrumb to the Epic, the Ticket's identity
// and the same meta line the list row showed, the description as markdown
// source, three comments, and all three Link kinds with the Tracker's own native
// labels — "is duplicated by" included.
func TestDetailFrame(t *testing.T) {
	p, s := startFixtureDetail(t)

	s.tm.Send(enterKey)
	s.waitFor(t, "DESCRIPTION")

	m, got := s.finish(t)

	checkGolden(t, "detail.golden.txt", got)
	if m.mode != modeDetail {
		t.Error("enter did not open the Detail screen")
	}
	for _, want := range []string{"is blocked by", "blocks", "is duplicated by"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("the frame does not carry the native label %q:\n%s", want, got)
		}
	}
	// ADR-0003: the Detail is read at the moment it is opened, and once.
	if n := p.DetailCallsFor("acme/widgets#112"); n != 1 {
		t.Errorf("DetailCallsFor(#112) = %d, want exactly 1", n)
	}
	if n := p.ResolveCalls(); n != 1 {
		t.Errorf("ResolveCalls() = %d, want 1: opening a Ticket is not a list refresh", n)
	}
}

// Scrolling is a different window of the same document: the header stays pinned,
// the position indicator moves, and the frame is still exactly the screen.
func TestDetailFrameScrolled(t *testing.T) {
	p := fake.New(fake.WithDetails(longDetails()))
	s := startBoth(t, p, newClock(), time.Minute)
	s.waitFor(t, "Widget sync v2")

	s.tm.Send(enterKey)
	s.waitFor(t, "DESCRIPTION")
	s.tm.Send(endKey)

	m, got := s.finish(t)

	checkGolden(t, "detail_scrolled.golden.txt", got)
	if m.detail.offset == 0 {
		t.Error("end did not scroll the Detail")
	}
	// The header is pinned: it is above the window, not part of it.
	if !strings.Contains(string(got), "Draft the shard sync protocol") {
		t.Errorf("the header scrolled away with the body:\n%s", got)
	}
	if !strings.Contains(string(got), "100%") {
		t.Errorf("the scroll indicator does not report the bottom:\n%s", got)
	}
	// The bottom of the document is on screen and the top is not.
	if !strings.Contains(string(got), "LINKS (3)") {
		t.Errorf("the last section is not on screen:\n%s", got)
	}
	if strings.Contains(string(got), "DESCRIPTION") {
		t.Errorf("the window did not move off the first section:\n%s", got)
	}
}

// The loading state names the Ticket it is loading: a screen that cannot say
// what it is waiting for is worse than the list it replaced.
func TestDetailFrameWhileLoading(t *testing.T) {
	started := make(chan context.Context, 1)
	p := fake.New()
	c := newClock()
	s := startWith(t, c, Options{
		Source: selectorSource(p, c),
		DetailSource: func(ctx context.Context, _ model.TicketID) (model.Detail, model.Capabilities, error) {
			started <- ctx
			<-ctx.Done()
			return model.Detail{}, model.Capabilities{}, ctx.Err()
		},
		Interval: time.Minute,
		Now:      c.now,
	})
	s.waitFor(t, "Widget sync v2")

	s.tm.Send(enterKey)
	s.waitFor(t, "Reading Ticket detail…")
	var detailCtx context.Context
	select {
	case detailCtx = <-started:
	case <-time.After(waitTimeout):
		t.Fatal("Detail source did not receive its generation context")
	}

	m, got := s.finish(t)
	select {
	case <-detailCtx.Done():
		if !errors.Is(detailCtx.Err(), context.Canceled) {
			t.Errorf("Detail context ended with %v, want context.Canceled", detailCtx.Err())
		}
	default:
		t.Error("quitting did not cancel the in-flight Detail context")
	}

	checkGolden(t, "detail_loading.golden.txt", got)
	if !m.detail.loading {
		t.Error("the model does not report a read in flight")
	}
	for _, want := range []string{"#112", "Draft the shard sync protocol", "reading…"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("the loading frame does not say which Ticket it is reading (%q):\n%s", want, got)
		}
	}
}

// A Detail that cannot be read says so and says what to press. The program is
// still alive underneath it.
func TestDetailFrameWhenTheFetchFails(t *testing.T) {
	_, s := startFixtureDetail(t)

	// #118 has no fixture Detail, which is what makes FetchDetail fail.
	s.down(6)
	s.tm.Send(enterKey)
	s.waitFor(t, "Could not read this Ticket's detail")

	m, got := s.finish(t)

	checkGolden(t, "detail_error.golden.txt", got)
	if m.detail.lastErr == nil {
		t.Error("the failed read was swallowed")
	}
	if m.detail.ticket.Key != "#118" {
		t.Errorf("the Detail screen is on %q, want #118", m.detail.ticket.Key)
	}
	for _, want := range []string{"#118", "Cache shard lookups", "Press r to try again, esc to go back."} {
		if !strings.Contains(string(got), want) {
			t.Errorf("the failure frame does not contain %q:\n%s", want, got)
		}
	}
}

// Silent absence, the whole rule in one frame: a Provider declaring neither
// Comments nor BlockingLinks shows a description and the Relates link, with no
// heading, no placeholder and no error to say what is missing.
func TestDetailFrameWithoutTheDetailCapabilities(t *testing.T) {
	p := fake.New(fake.WithCapabilities(model.Capabilities{
		Hierarchy: true, PullRequests: true,
		Selectors: model.SelectorCapabilities{Epic: true},
	}))
	s := startBoth(t, p, newClock(), time.Minute)
	s.waitFor(t, "Widget sync v2")

	s.tm.Send(enterKey)
	s.waitFor(t, "DESCRIPTION")

	_, got := s.finish(t)

	checkGolden(t, "detail_no_capabilities.golden.txt", got)
	for _, absent := range []string{"COMMENT", "is blocked by", "No comments", "#115", "#116"} {
		if strings.Contains(string(got), absent) {
			t.Errorf("the frame mentions %q without the Capability behind it:\n%s", absent, got)
		}
	}
	// A Relates link survives without BlockingLinks — that rule is the fake's,
	// jsonout's and the renderer's, and all three have to agree.
	if !strings.Contains(string(got), "is duplicated by") {
		t.Errorf("the Relates link went with the blocking ones:\n%s", got)
	}
}

// An empty description reads as "No description." and takes nothing with it.
func TestDetailFrameWithAnEmptyDescription(t *testing.T) {
	_, s := startFixtureDetail(t)

	// #119 is comments-only.
	s.down(7)
	s.tm.Send(enterKey)
	s.waitFor(t, "No description.")

	m, got := s.finish(t)

	checkGolden(t, "detail_empty_description.golden.txt", got)
	if m.detail.ticket.Key != "#119" {
		t.Errorf("the Detail screen is on %q, want #119", m.detail.ticket.Key)
	}
	if !strings.Contains(string(got), "Dashboards are live.") {
		t.Errorf("the comments went with the empty description:\n%s", got)
	}
}

// The acceptance criterion in its strongest form: esc puts the list back
// byte-for-byte, because opening Detail never touched it.
func TestEscapeFromDetailRestoresTheListExactly(t *testing.T) {
	_, s := startFixtureDetail(t)

	s.tm.Send(enterKey)
	s.waitFor(t, "DESCRIPTION")
	s.tm.Send(escKey)

	m, got := s.finish(t)

	checkSameFrameAs(t, "initial.golden.txt", got)
	if m.mode != modeList {
		t.Error("esc did not return to the list")
	}
	if !m.quitting {
		t.Error("the session did not end with q")
	}
}

// The two survive together: a filter narrows the list, a match's Detail opens
// and closes, and the narrowed list is exactly where it was.
func TestDetailRoundTripKeepsTheFilter(t *testing.T) {
	_, s := startFixtureDetail(t)

	s.tm.Send(keyPress("/"))
	s.typeText("shard")
	s.tm.Send(enterKey)
	s.tm.Send(enterKey)
	s.waitFor(t, "DESCRIPTION")
	s.tm.Send(escKey)

	m, got := s.finish(t)

	checkSameFrameAs(t, "search_committed.golden.txt", got)
	if m.filter.Query != "shard" {
		t.Errorf("the filter is %q, want it untouched by the drill-in", m.filter.Query)
	}
}

// Detail is fetched on drill-in and never by the heartbeat: DetailCalls is
// unmoved by any number of refreshes (ADR-0003).
func TestRefreshesNeverFetchDetail(t *testing.T) {
	p, s := startFixtureDetail(t)

	for range 3 {
		s.clock.advance(61 * time.Second)
		s.beat()
	}
	waitUntil(t, "the auto-refreshes to land", func() bool { return p.ResolveCalls() >= 3 })

	if n := p.DetailCalls(); n != 0 {
		t.Fatalf("DetailCalls() = %d after %d refreshes, want 0", n, p.ResolveCalls())
	}

	s.tm.Send(enterKey)
	s.waitFor(t, "DESCRIPTION")
	s.finish(t)

	if n := p.DetailCalls(); n != 1 {
		t.Errorf("DetailCalls() = %d, want exactly 1: one drill-in is one read", n)
	}
}

// The list keeps refreshing behind an open Detail, and neither disturbs the
// other: the reading advances, the Detail screen stays put, and no Detail is
// fetched by the beat.
func TestRefreshWhileReadingDetail(t *testing.T) {
	before := fake.FixtureSnapshot()
	after := fake.FixtureSnapshot()
	after.Tickets[1].Status = model.StatusDone
	after.Tickets[1].NativeStatus = "closed"

	p := fake.New(fake.WithSnapshots(before, after), fake.WithDetails(longDetails()))
	s := startBoth(t, p, newClock(), time.Minute)
	s.waitFor(t, "Widget sync v2")

	s.tm.Send(enterKey)
	s.waitFor(t, "DESCRIPTION")
	s.tm.Send(endKey)

	s.clock.advance(61 * time.Second)
	s.beat()
	waitUntil(t, "the auto-refresh to land", func() bool { return p.ResolveCalls() >= 2 })

	m, _ := s.finish(t)

	if m.mode != modeDetail {
		t.Error("a refresh closed the Detail screen")
	}
	if m.detail.offset == 0 {
		t.Error("a refresh moved the Detail scroll offset back to the top")
	}
	if n := p.DetailCalls(); n != 1 {
		t.Errorf("DetailCalls() = %d, want 1: a refresh never reads Detail", n)
	}
	// The list really did advance underneath.
	if got := model.ComputeProgress(m.input.Tickets).Done; got != 4 {
		t.Errorf("the list holds %d done Tickets, want the refreshed 4", got)
	}
}

// A Ticket read once this session opens instantly and costs the Tracker nothing.
// r in Detail is how a reader asks for it again — and asks only for that one.
func TestDetailIsCachedForTheSession(t *testing.T) {
	p, s := startFixtureDetail(t)
	const opened = model.TicketID("acme/widgets#112")

	s.tm.Send(enterKey)
	s.waitFor(t, "DESCRIPTION")
	s.tm.Send(escKey)
	s.tm.Send(enterKey)

	waitUntil(t, "the first read", func() bool { return p.DetailCallsFor(opened) >= 1 })
	if n := p.DetailCallsFor(opened); n != 1 {
		t.Fatalf("DetailCallsFor(%s) = %d after two opens, want 1: the second is cached", opened, n)
	}

	s.tm.Send(keyPress("r"))
	waitUntil(t, "the forced re-read", func() bool { return p.DetailCallsFor(opened) >= 2 })

	m, _ := s.finish(t)

	if n := p.DetailCallsFor(opened); n != 2 {
		t.Errorf("DetailCallsFor(%s) = %d after r, want 2", opened, n)
	}
	if n := p.ResolveCalls(); n != 1 {
		t.Errorf("ResolveCalls() = %d, want 1: r in Detail refreshes one Ticket, not the list", n)
	}
	if !m.detail.loaded {
		t.Error("the re-read left the screen without a Detail")
	}
}

// q and ctrl+c quit from Detail; esc does not. A key that sometimes quits and
// sometimes navigates is the bug people file.
func TestQuitKeysFromDetail(t *testing.T) {
	for _, quit := range []tea.KeyPressMsg{keyPress("q"), ctrlCKey} {
		_, s := startFixtureDetail(t)

		s.tm.Send(enterKey)
		s.waitFor(t, "DESCRIPTION")

		m, _ := s.finishWith(t, quit)

		if !m.quitting {
			t.Errorf("%v did not quit from the Detail screen", quit)
		}
	}
}

// enter with nothing selectable is a no-op at the program level too: no fetch,
// no mode change, no panic.
func TestEnterWithNothingSelectable(t *testing.T) {
	empty := model.WatchlistSnapshot{
		Epic: model.Epic{Key: "#900", Title: "Widget sync v3: nothing planned yet"},
	}
	p := fake.New(fake.WithSnapshot(empty))
	s := startBoth(t, p, newClock(), time.Minute)
	s.waitFor(t, "Widget sync v3")

	s.tm.Send(enterKey)

	m, _ := s.finish(t)

	if m.mode != modeList {
		t.Error("enter opened a Detail screen with no Ticket selected")
	}
	if n := p.DetailCalls(); n != 0 {
		t.Errorf("DetailCalls() = %d, want 0", n)
	}
}

// detailModel returns a Model already reading one fixture Ticket's Detail, so
// the transitions can be exercised without a program.
func detailModel(t *testing.T) Model {
	t.Helper()

	m := listModel(t, fixtureTickets())
	m.input.Capabilities = allCaps
	m.input.Header = Header{Key: "#111", Title: "Widget sync v2"}
	m.details["acme/widgets#112"] = detailEntry{detail: fake.FixtureDetails()["acme/widgets#112"], caps: allCaps}

	next, _ := m.openDetail()
	return next.(Model)
}

// A slow answer for a Ticket the reader has already left must not paint over the
// one they are on.
func TestStaleDetailFetchIsDropped(t *testing.T) {
	m := detailModel(t)
	m.detail.loading = true
	m.detailGeneration = 7

	stale := m.onDetailFetched(detailFetchedMsg{
		generation: 6,
		id:         m.detail.ticket.ID,
		detail:     model.Detail{Description: "an answer to a question nobody is asking"},
	})
	if strings.Contains(stale.detail.input.Detail.Description, "nobody is asking") {
		t.Error("a stale generation landed on the screen")
	}
	if !stale.detail.loading {
		t.Error("a dropped answer cleared the in-flight flag; the real read is still running")
	}

	wrongTicket := m.onDetailFetched(detailFetchedMsg{
		generation: 7,
		id:         "acme/widgets#999",
		detail:     model.Detail{Description: "another Ticket entirely"},
	})
	if strings.Contains(wrongTicket.detail.input.Detail.Description, "another Ticket") {
		t.Error("an answer for a different Ticket landed on the screen")
	}
}

// esc leaves every field the list screen renders from exactly as it was. This is
// the property the byte-identical golden proves, asserted field by field.
func TestEscapeFromDetailTouchesNoListState(t *testing.T) {
	m := listModel(t, fixtureTickets())
	m.input.Capabilities = allCaps
	m = m.setFilter(Filter{Query: "shard"}).move(1)
	m.details["acme/widgets#115"] = detailEntry{caps: allCaps}

	before := m
	opened, _ := m.openDetail()
	back, _ := opened.(Model).onDetailKey(escKey)
	after := back.(Model)

	if after.mode != modeList {
		t.Fatal("esc did not return to the list")
	}
	if after.selected != before.selected || after.selectedID != before.selectedID {
		t.Errorf("the selection moved from row %d (%q) to row %d (%q)",
			before.selected, before.selectedID, after.selected, after.selectedID)
	}
	if after.offset != before.offset {
		t.Errorf("the list scroll offset moved from %d to %d", before.offset, after.offset)
	}
	if after.filter != before.filter {
		t.Errorf("the filter changed from %+v to %+v", before.filter, after.filter)
	}
	if len(after.rows) != len(before.rows) {
		t.Errorf("the rows were rebuilt: %d, want %d", len(after.rows), len(before.rows))
	}
}

// A refresh landing while Detail is open rebuilds the rows underneath and
// touches nothing on the screen in front of them — even when the Ticket being
// read has left the list entirely.
func TestRefreshUnderAnOpenDetail(t *testing.T) {
	m := detailModel(t)
	m.detail.offset = 3

	// #112 is gone from the new reading, and the Detail screen carries on: it is
	// rendering from a DetailInput it already holds, not from the list.
	after := m.onRefreshed(refreshedMsg{
		generation: m.generation,
		input:      ListInput{Tickets: []model.Ticket{ticket("#900", model.StatusTodo)}},
	})

	if after.mode != modeDetail {
		t.Error("a refresh closed the Detail screen")
	}
	if after.detail.offset != 3 {
		t.Errorf("the Detail scroll offset moved to %d", after.detail.offset)
	}
	if after.detail.ticket.Key != "#112" {
		t.Errorf("the Detail screen is on %q, want the Ticket it was opened for", after.detail.ticket.Key)
	}
	if _, ok := rowOf(after.rows, "acme/widgets#112"); ok {
		t.Error("the refreshed list still holds the vanished Ticket")
	}
	// The frame still renders, which is the thing that must not panic.
	if after.detailFrame(after.detailDocument()) == "" {
		t.Error("the Detail screen rendered nothing after the Ticket vanished")
	}
}

// A read that fails with a Detail already on screen keeps the Detail and puts
// the error in the footer. Stale detail beats a blank screen.
func TestFailedReReadKeepsTheCachedDetail(t *testing.T) {
	m := detailModel(t)
	if !m.detail.loaded {
		t.Fatal("the cached Detail did not open")
	}

	next, _ := m.startDetailFetch()
	failed := next.onDetailFetched(detailFetchedMsg{
		generation: next.detailGeneration,
		id:         next.detail.ticket.ID,
		err:        errors.New("dial tcp: lookup tracker.example.test: no such host"),
	})

	if !failed.detail.loaded {
		t.Error("the failed re-read blanked the Detail that was already on screen")
	}
	footer := strings.Join(failed.detailFooterLines(failed.detailDocument()), "\n")
	if !strings.Contains(footer, "could not re-read") {
		t.Errorf("the failed re-read is not in the footer:\n%s", footer)
	}
	body := strings.Join(failed.detailBodyLines(), "\n")
	if !strings.Contains(body, "shard sync protocol") {
		t.Errorf("the body lost the cached Detail:\n%s", body)
	}
}

// A monitor opened without a Detail source says why rather than panicking.
func TestOpeningDetailWithNoSource(t *testing.T) {
	m := listModel(t, fixtureTickets())

	next, cmd := m.openDetail()
	if cmd == nil {
		t.Fatal("enter issued no command")
	}
	msg, ok := cmd().(detailFetchedMsg)
	if !ok {
		t.Fatalf("the command produced %T, want a detailFetchedMsg", cmd())
	}

	failed := next.(Model).onDetailFetched(msg)
	if failed.detail.lastErr == nil {
		t.Fatal("a monitor with no Detail source reported no error")
	}
	if !strings.Contains(strings.Join(failed.detailBodyLines(), "\n"), "Detail source") {
		t.Errorf("the error does not name the missing seam: %v", failed.detail.lastErr)
	}
}

// Scrolling stays inside the document from either end, and a resize re-clamps
// rather than leaving the reader below the last line.
func TestDetailScrolling(t *testing.T) {
	m := detailModel(t)
	m.width, m.height = 120, 40

	if up := m.scrollDetail(-5); up.detail.offset != 0 {
		t.Errorf("scrolling up from the top reached %d, want 0", up.detail.offset)
	}

	bottom := m.scrollDetailTo(len(m.detailBodyLines()))
	want := clampDetailOffset(len(m.detailBodyLines()), len(m.detailBodyLines()), m.detailBodyHeight())
	if bottom.detail.offset != want {
		t.Errorf("end reached %d, want %d", bottom.detail.offset, want)
	}

	// A terminal that just got taller has fewer lines to scroll through.
	taller := bottom
	taller.height = 200
	if got := taller.clampDetail(taller.detail.offset); got != 0 {
		t.Errorf("a resize left the offset at %d, want the whole Detail on screen", got)
	}
}

// Scrolling by page and by line both move, and neither leaves the document.
func TestDetailPagingKeys(t *testing.T) {
	m := detailModel(t)
	m.width, m.height = 120, 12

	stepped := m
	for _, k := range []tea.KeyPressMsg{downKey, pageDnKey} {
		next, _ := stepped.onDetailKey(k)
		stepped = next.(Model)
	}
	if stepped.detail.offset == 0 {
		t.Fatal("down and pgdn did not scroll")
	}

	for _, k := range []tea.KeyPressMsg{upKey, pageUpKey, homeKey} {
		next, _ := stepped.onDetailKey(k)
		stepped = next.(Model)
	}
	if stepped.detail.offset != 0 {
		t.Errorf("home left the offset at %d", stepped.detail.offset)
	}
}
