package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

func TestDecodedTicketFollowsAndPopsWithoutResolvingWatchlist(t *testing.T) {
	p := fake.New()
	s := startDecoded(t, newClock(), p, decodedTicket(), selectorSource(p, newClock()))
	s.waitFor(t, "DESCRIPTION")

	s.tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	s.tm.Send(enterKey)
	s.waitFor(t, "wo shards can hand out the same widget ID")
	if p.ResolveCalls() != 0 {
		t.Fatalf("Link follow resolved the Watchlist %d times", p.ResolveCalls())
	}

	s.tm.Send(escKey)
	m, _ := s.finishWith(t, escKey)
	if !m.quitting || m.detail.ticket.ID != "acme/widgets#112" || len(m.trail) != 0 {
		t.Errorf("child Esc/root Esc = quitting:%v ticket:%q depth:%d",
			m.quitting, m.detail.ticket.ID, len(m.trail))
	}
	if got := p.DetailCallsFor("acme/widgets#112"); got != 1 {
		t.Errorf("root Detail calls = %d, want 1", got)
	}
	if got := p.DetailCallsFor("acme/widgets#115"); got != 1 {
		t.Errorf("child Detail calls = %d, want 1", got)
	}
	if p.ResolveCalls() != 0 {
		t.Errorf("Esc ladder resolved the Watchlist %d times", p.ResolveCalls())
	}
}

func TestDecodedImmediateQuitAtRoot(t *testing.T) {
	for _, quit := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{name: "q", key: keyPress("q")},
		{name: "ctrl-c", key: ctrlCKey},
	} {
		t.Run(quit.name, func(t *testing.T) {
			p := fake.New()
			c := newClock()
			s := startDecoded(t, c, p, decodedTicket(), selectorSource(p, c))
			s.waitFor(t, "DESCRIPTION")
			m, _ := s.finishWith(t, quit.key)
			if !m.quitting || m.detail.ticket.ID != "acme/widgets#112" || len(m.trail) != 0 {
				t.Errorf("quit at root = quitting:%v ticket:%q depth:%d",
					m.quitting, m.detail.ticket.ID, len(m.trail))
			}
			if got := p.DetailCallsFor("acme/widgets#112"); got != 1 {
				t.Errorf("root Detail calls = %d, want 1", got)
			}
			if p.ResolveCalls() != 0 {
				t.Errorf("root quit resolved %d times", p.ResolveCalls())
			}
		})
	}
}

func TestDecodedCachedRefollowAndImmediateQuitAtTrailDepth(t *testing.T) {
	for _, quit := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{name: "q", key: keyPress("q")},
		{name: "ctrl-c", key: ctrlCKey},
	} {
		t.Run(quit.name, func(t *testing.T) {
			p := fake.New()
			c := newClock()
			s := startDecoded(t, c, p, decodedTicket(), selectorSource(p, c))
			s.waitFor(t, "DESCRIPTION")
			s.tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
			s.tm.Send(enterKey)
			s.waitFor(t, "wo shards can hand out the same widget ID")
			s.tm.Send(escKey)
			// Esc restores the archived focus, so Enter follows #115 from cache.
			s.tm.Send(enterKey)
			m, _ := s.finishWith(t, quit.key)
			if !m.quitting || m.detail.ticket.ID != "acme/widgets#115" || len(m.trail) != 1 {
				t.Errorf("quit at depth = quitting:%v ticket:%q depth:%d",
					m.quitting, m.detail.ticket.ID, len(m.trail))
			}
			if got := p.DetailCallsFor("acme/widgets#115"); got != 1 {
				t.Errorf("cached refollow Detail calls = %d, want 1", got)
			}
			if p.ResolveCalls() != 0 {
				t.Errorf("cached refollow/quit resolved %d times", p.ResolveCalls())
			}
		})
	}
}

func TestDecodedNoParentCanFollowAndPopButCannotWalkUp(t *testing.T) {
	open := decodedTicket()
	open.Parent = Header{}
	p := fake.New()
	s := startDecoded(t, newClock(), p, open, nil)
	s.waitFor(t, "DESCRIPTION")
	s.tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	s.tm.Send(enterKey)
	s.waitFor(t, "wo shards can hand out the same widget ID")
	s.tm.Send(escKey)
	s.tm.Send(keyPress("u"))
	m, _ := s.finish(t)
	if m.detail.ticket.ID != open.Ticket.ID || m.detailKeys.Parent.Enabled() {
		t.Errorf("no-Parent root = ticket:%q parent enabled:%v", m.detail.ticket.ID, m.detailKeys.Parent.Enabled())
	}
	if p.ResolveCalls() != 0 {
		t.Errorf("no-Parent u resolved %d times", p.ResolveCalls())
	}
}

func TestDecodedTrailRootJumpResolvesWatchlistOnce(t *testing.T) {
	p := fake.New()
	c := newClock()
	s := startDecoded(t, c, p, decodedTicket(), selectorSource(p, c))
	s.waitFor(t, "DESCRIPTION")
	s.tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	s.tm.Send(enterKey)
	s.waitFor(t, "wo shards can hand out the same widget ID")
	s.tm.Send(keyPress("u"))
	s.waitFor(t, "IN PROGRESS (3)")
	m, _ := s.finish(t)
	if m.mode != modeList || len(m.trail) != 0 {
		t.Errorf("u at depth = mode:%v depth:%d", m.mode, len(m.trail))
	}
	if p.ResolveCalls() != 1 {
		t.Errorf("decoder u Resolve calls = %d, want 1", p.ResolveCalls())
	}
}

func TestBackgroundListRefreshDoesNotTouchTrailSeat(t *testing.T) {
	m, _ := navigableDetailModel(t)
	m.hasSource = true
	m.listArmed = true
	m = focusLinkAt(m, 0)
	child, _ := followFocused(t, m)
	before := child.detailTrailSnapshot()
	beforeGeneration := child.detailGeneration

	refreshed := refreshedMsg{
		generation: child.generation,
		input: ListInput{
			Header:  Header{Key: "refreshed-root"},
			Tickets: []model.Ticket{{ID: "new-list-ticket", Key: "NEW-1"}},
		},
	}
	got := child.onRefreshed(refreshed)
	if !reflect.DeepEqual(got.detailTrailSnapshot(), before) ||
		got.detailGeneration != beforeGeneration || len(got.trail) != 1 {
		t.Errorf("background refresh mutated Detail/Trail seat")
	}
	if got.input.Header.Key != "refreshed-root" {
		t.Error("background refresh did not update the hidden list")
	}
}

func TestDecodedEmptyIdentityFailsLocallyWithoutDetailCall(t *testing.T) {
	calls := 0
	open := OpenTicket{Ticket: model.Ticket{Key: "BROKEN", Title: "Malformed decoded Ticket"}}
	c := newClock()
	s := startWith(t, c, Options{
		Open: &open,
		DetailSource: func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
			calls++
			return model.Detail{}, model.Capabilities{}, nil
		},
		Interval: time.Minute,
		Now:      c.now,
	})
	s.waitFor(t, "has no Ticket identity")
	s.tm.Send(keyPress("r"))
	m, _ := s.finish(t)
	if calls != 0 || m.detail.loading || !errors.Is(m.detail.lastErr, errEmptyLinkTargetID) || m.detailGeneration != 2 {
		t.Errorf("decoded empty identity = calls:%d loading:%v err:%v generation:%d, want repeated local failure at generation 2",
			calls, m.detail.loading, m.detail.lastErr, m.detailGeneration)
	}
}

type controlledDetailReply struct {
	detail model.Detail
	caps   model.Capabilities
	err    error
}

type controlledDetailCall struct {
	id    model.TicketID
	reply chan controlledDetailReply
}

type controlledDetailReads struct {
	calls chan controlledDetailCall
}

func newControlledDetailReads() *controlledDetailReads {
	return &controlledDetailReads{calls: make(chan controlledDetailCall, 8)}
}

func (r *controlledDetailReads) source(ctx context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
	call := controlledDetailCall{id: id, reply: make(chan controlledDetailReply, 1)}
	select {
	case r.calls <- call:
	case <-ctx.Done():
		return model.Detail{}, model.Capabilities{}, ctx.Err()
	}
	select {
	case response := <-call.reply:
		return response.detail, response.caps, response.err
	case <-ctx.Done():
		return model.Detail{}, model.Capabilities{}, ctx.Err()
	}
}

func (r *controlledDetailReads) next(t *testing.T, want model.TicketID) controlledDetailCall {
	t.Helper()
	select {
	case call := <-r.calls:
		if call.id != want {
			t.Fatalf("Detail read = %q, want %q", call.id, want)
		}
		return call
	case <-time.After(waitTimeout):
		t.Fatalf("timed out waiting for Detail read %q", want)
		return controlledDetailCall{}
	}
}

func testNavigationDetail(id model.TicketID, body string, links ...model.Link) model.Detail {
	return model.Detail{TicketID: id, Description: body, Links: links}
}

func TestOpenSessionDropsReorderedRootRereadAfterChildPop(t *testing.T) {
	const (
		rootID  model.TicketID = "A"
		childID model.TicketID = "B"
	)
	rootTicket := model.Ticket{ID: rootID, Key: "A", Title: "Root A"}
	child := model.LinkTarget{ID: childID, Key: "B", Title: "Child B"}
	reads := newControlledDetailReads()
	c := newClock()
	s := startWith(t, c, Options{
		Open:         &OpenTicket{Ticket: rootTicket},
		DetailSource: reads.source,
		Interval:     time.Minute,
		Now:          c.now,
	})

	initial := reads.next(t, rootID)
	initial.reply <- controlledDetailReply{
		detail: testNavigationDetail(rootID, "ROOT BODY ORIGINAL", model.Link{Kind: model.LinkRelates, Target: child}),
	}
	s.waitFor(t, "ROOT BODY ORIGINAL")

	s.tm.Send(keyPress("r"))
	delayedRoot := reads.next(t, rootID)
	s.tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	s.tm.Send(enterKey)
	childRead := reads.next(t, childID)
	childRead.reply <- controlledDetailReply{detail: testNavigationDetail(childID, "CHILD BODY WINS")}
	s.waitFor(t, "CHILD BODY WINS")
	s.tm.Send(escKey)
	s.waitFor(t, "ROOT BODY ORIGINAL")

	delayedRoot.reply <- controlledDetailReply{detail: testNavigationDetail(rootID, "STALE ROOT REPLACEMENT")}
	c.advance(time.Second)
	s.beat()
	s.waitFor(t, "current · read 1s ago")
	m, _ := s.finish(t)

	if m.detail.ticket.ID != rootID || len(m.trail) != 0 {
		t.Fatalf("final seat = %q depth %d, want root with empty Trail", m.detail.ticket.ID, len(m.trail))
	}
	if m.detail.input.Detail.Description != "ROOT BODY ORIGINAL" {
		t.Errorf("stale same-ID reread replaced visible root: %q", m.detail.input.Detail.Description)
	}
	if cached := m.details[rootID].detail.Description; cached != "ROOT BODY ORIGINAL" {
		t.Errorf("stale same-ID reread replaced root cache: %q", cached)
	}
	if cached := m.details[childID].detail.Description; cached != "CHILD BODY WINS" {
		t.Errorf("child cache = %q, want successful reordered read", cached)
	}
}

func TestOpenSessionDropsDelayedChildAfterPopAndRestoresOffset(t *testing.T) {
	const (
		rootID  model.TicketID = "A"
		childID model.TicketID = "B"
	)
	child := model.LinkTarget{ID: childID, Key: "B", Title: "Child B"}
	reads := newControlledDetailReads()
	c := newClock()
	s := startWith(t, c, Options{
		Open:         &OpenTicket{Ticket: model.Ticket{ID: rootID, Key: "A", Title: "Root A"}},
		DetailSource: reads.source,
		Interval:     time.Minute,
		Now:          c.now,
	})
	rootBody := strings.Repeat("root body line that wraps across the terminal\n", 60)
	reads.next(t, rootID).reply <- controlledDetailReply{
		detail: testNavigationDetail(rootID, rootBody, model.Link{Kind: model.LinkRelates, Target: child}),
	}
	s.waitFor(t, "root body line")
	s.tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	s.tm.Send(tea.KeyPressMsg{Code: tea.KeyHome})
	for range 5 {
		s.tm.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	s.tm.Send(enterKey)
	childRead := reads.next(t, childID)
	s.tm.Send(escKey)
	s.waitFor(t, "root body line that wraps across the terminal")
	childRead.reply <- controlledDetailReply{detail: testNavigationDetail(childID, "ABANDONED CHILD BODY")}
	c.advance(time.Second)
	s.beat()
	s.waitFor(t, "current · read 1s ago")
	m, _ := s.finish(t)

	if m.detail.ticket.ID != rootID || m.detail.offset != 5 || len(m.trail) != 0 {
		t.Errorf("popped root = ticket %q offset %d depth %d, want A/5/0",
			m.detail.ticket.ID, m.detail.offset, len(m.trail))
	}
	if !m.detail.hasLinkFocus {
		t.Error("popped root lost its archived Link focus")
	}
	if _, cached := m.details[childID]; cached {
		t.Error("abandoned child result entered the Detail cache")
	}
	if strings.Contains(m.detail.input.Detail.Description, "ABANDONED") {
		t.Error("abandoned child result replaced the popped root")
	}
}

func TestOpenSessionRestoresFailedRereadSeatAndRetriesAfterChild(t *testing.T) {
	const (
		rootID  model.TicketID = "A"
		childID model.TicketID = "B"
	)
	child := model.LinkTarget{ID: childID, Key: "B", Title: "Child B"}
	reads := newControlledDetailReads()
	c := newClock()
	s := startWith(t, c, Options{
		Open:         &OpenTicket{Ticket: model.Ticket{ID: rootID, Key: "A", Title: "Root A"}},
		DetailSource: reads.source,
		Interval:     time.Minute,
		Now:          c.now,
	})
	rootBody := strings.Repeat("restorable root line\n", 60)
	reads.next(t, rootID).reply <- controlledDetailReply{
		detail: testNavigationDetail(rootID, rootBody, model.Link{Kind: model.LinkRelates, Target: child}),
	}
	s.waitFor(t, "restorable root line")
	s.tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	s.tm.Send(tea.KeyPressMsg{Code: tea.KeyHome})
	for range 5 {
		s.tm.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	s.tm.Send(keyPress("r"))
	failedReread := reads.next(t, rootID)
	failedReread.reply <- controlledDetailReply{err: errors.New("root reread denied")}
	s.waitFor(t, "root reread denied")

	s.tm.Send(enterKey)
	childRead := reads.next(t, childID)
	childRead.reply <- controlledDetailReply{detail: testNavigationDetail(childID, "CHILD AFTER ROOT ERROR")}
	s.waitFor(t, "CHILD AFTER ROOT ERROR")
	s.tm.Send(escKey)
	s.waitFor(t, "root reread denied")
	s.tm.Send(keyPress("r"))
	retry := reads.next(t, rootID)
	recoveredBody := strings.Repeat("ROOT RECOVERED\n", 60)
	retry.reply <- controlledDetailReply{
		detail: testNavigationDetail(rootID, recoveredBody, model.Link{Kind: model.LinkRelates, Target: child}),
	}
	s.waitFor(t, "ROOT RECOVERED")
	m, _ := s.finish(t)

	if m.detail.ticket.ID != rootID || len(m.trail) != 0 || m.detail.lastErr != nil {
		t.Errorf("recovered root = ticket:%q depth:%d err:%v", m.detail.ticket.ID, len(m.trail), m.detail.lastErr)
	}
	if m.detail.input.Detail.Description != recoveredBody || m.details[rootID].detail.Description != recoveredBody {
		t.Errorf("root retry did not replace visible/cache content")
	}
	if !m.detail.hasLinkFocus {
		t.Error("failed-reread Trail round trip lost Link focus")
	} else {
		row, ok := detailLinkRowByIdentity(m.detailDocument(), m.detail.linkFocus)
		if !ok || row.Line < m.detail.offset || row.Line >= m.detail.offset+m.detailBodyHeight() {
			t.Errorf("recovered root focus is offscreen: row=%d offset=%d body=%d",
				row.Line, m.detail.offset, m.detailBodyHeight())
		}
	}
}

func TestOpenSessionRetriesFailureToSuccess(t *testing.T) {
	const id model.TicketID = "A"
	reads := newControlledDetailReads()
	c := newClock()
	s := startWith(t, c, Options{
		Open:         &OpenTicket{Ticket: model.Ticket{ID: id, Key: "A", Title: "Root A"}},
		DetailSource: reads.source,
		Interval:     time.Minute,
		Now:          c.now,
	})

	reads.next(t, id).reply <- controlledDetailReply{err: errors.New("permission denied while reading private project details")}
	s.waitFor(t, "permission denied while reading private project details")
	s.tm.Send(keyPress("r"))
	retry := reads.next(t, id)
	retry.reply <- controlledDetailReply{detail: testNavigationDetail(id, "RETRY SUCCEEDED")}
	s.waitFor(t, "RETRY SUCCEEDED")
	m, _ := s.finish(t)

	if !m.detail.loaded || m.detail.loading || m.detail.lastErr != nil {
		t.Errorf("retry state = loaded:%v loading:%v err:%v", m.detail.loaded, m.detail.loading, m.detail.lastErr)
	}
	if got := m.details[id].detail.Description; got != "RETRY SUCCEEDED" {
		t.Errorf("successful retry cache = %q", got)
	}
	if m.detailGeneration != 2 {
		t.Errorf("Detail generation = %d, want initial read plus retry", m.detailGeneration)
	}
}

func TestTeatestDetailLinkClickFollowsExactlyOnce(t *testing.T) {
	root := model.Ticket{ID: "ROOT-1", Key: "ROOT-1", Title: "Root"}
	child := model.LinkTarget{ID: "CHILD-2", Key: "CHILD-2", Title: "Child"}
	var mu sync.Mutex
	calls := make(map[model.TicketID]int)
	detailSource := func(_ context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
		mu.Lock()
		calls[id]++
		mu.Unlock()
		if id == root.ID {
			return model.Detail{TicketID: id, Links: []model.Link{{Kind: model.LinkRelates, Target: child}}},
				model.Capabilities{}, nil
		}
		return model.Detail{TicketID: id, Description: "Child body reached once."}, model.Capabilities{}, nil
	}
	c := newClock()
	s := startWith(t, c, Options{
		Open:         &OpenTicket{Ticket: root},
		DetailSource: detailSource,
		Interval:     time.Minute,
		Now:          c.now,
	})
	s.waitFor(t, "LINKS (1)")
	// Empty description produces Link document line 6; Detail begins at row 4.
	s.tm.Send(tea.MouseClickMsg{X: termWidth - 1, Y: detailHeaderHeight + 6, Button: tea.MouseLeft})
	s.waitFor(t, "Child body reached once.")
	m, _ := s.finish(t)

	mu.Lock()
	defer mu.Unlock()
	if calls[root.ID] != 1 || calls[child.ID] != 1 {
		t.Errorf("Detail calls = %v, want one root and one child", calls)
	}
	if m.detail.ticket.ID != child.ID || len(m.trail) != 1 {
		t.Errorf("mouse whole-program transition = ticket:%q depth:%d", m.detail.ticket.ID, len(m.trail))
	}
}
