package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

func navigableDetailModel(t *testing.T) (Model, *[]model.TicketID) {
	t.Helper()
	m := detailModel(t)
	calls := make([]model.TicketID, 0)
	m.fetchDetail = func(_ context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
		calls = append(calls, id)
		d, ok := fake.FixtureDetails()[id]
		if !ok {
			return model.Detail{}, allCaps, errors.New("fixture detail not found")
		}
		return d, allCaps, nil
	}
	m.width, m.height = 80, 18
	return m.reconcileDetail(true), &calls
}

func focusLinkAt(m Model, index int) Model {
	doc := m.detailDocument()
	m.detail.linkFocus = doc.LinkRows[index].Identity
	m.detail.hasLinkFocus = true
	return m.reconcileDetail(true)
}

func followFocused(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.onDetailKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	return next.(Model), cmd
}

func acceptDetailCommand(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("Detail transition issued no fetch command")
	}
	raw := cmd()
	msg, ok := raw.(detailFetchedMsg)
	if !ok {
		t.Fatalf("Detail command produced %T", raw)
	}
	return m.onDetailFetched(msg)
}

func TestWindowResizeReflowsFocusedDetailLinkIntoView(t *testing.T) {
	m, _ := navigableDetailModel(t)
	m.detail.input.Detail.Description = strings.Repeat("wide body content that reflows when the terminal narrows\n", 30)
	m.width, m.height = 120, 18
	m.help.SetWidth(m.width)
	m = m.reconcileDetail(true)
	m = focusLinkAt(m, len(m.detailDocument().LinkRows)-1)
	identity := m.detail.linkFocus
	m.detail.offset = 0

	next, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	m = next.(Model)
	if m.detail.linkFocus != identity || !m.detail.hasLinkFocus {
		t.Fatalf("resize changed focused Link from %+v to %+v", identity, m.detail.linkFocus)
	}
	row, ok := detailLinkRowByIdentity(m.detailDocument(), identity)
	if !ok || row.Line < m.detail.offset || row.Line >= m.detail.offset+m.detailBodyHeight() {
		t.Errorf("resized focus is offscreen: row=%d offset=%d body=%d", row.Line, m.detail.offset, m.detailBodyHeight())
	}
}

func TestFollowRequiresCurrentVisibleFocus(t *testing.T) {
	m, calls := navigableDetailModel(t)
	if m.detail.hasLinkFocus || m.detailKeys.Follow.Enabled() {
		t.Fatal("new Detail seat starts with a Link focus")
	}
	next, cmd := m.onDetailKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := next.(Model)
	if cmd != nil || len(got.trail) != 0 || got.detail.ticket.ID != m.detail.ticket.ID || len(*calls) != 0 {
		t.Error("Enter without focus followed a Link")
	}

	m.detail.input.Detail.Links = nil
	m = m.reconcileDetail(true)
	for _, keyMsg := range []tea.KeyPressMsg{
		{Code: tea.KeyTab},
		{Code: tea.KeyTab, Mod: tea.ModShift},
		{Code: tea.KeyEnter},
	} {
		next, cmd = m.onDetailKey(keyMsg)
		got = next.(Model)
		if cmd != nil || got.detail.hasLinkFocus || len(got.trail) != 0 {
			t.Errorf("%s changed a Detail with no visible Links", keyMsg.String())
		}
	}
}

func TestFollowUsesThinSeatTrailAndSessionCache(t *testing.T) {
	root, calls := navigableDetailModel(t)
	root.detail.offset = 2
	root = focusLinkAt(root, 0)
	rootSnapshot := root.detailTrailSnapshot()

	child, cmd := followFocused(t, root)
	if len(child.trail) != 1 {
		t.Fatalf("Trail depth = %d, want 1", len(child.trail))
	}
	if child.detail.ticket.ID != "acme/widgets#115" {
		t.Fatalf("follow seated %q, want #115", child.detail.ticket.ID)
	}
	if child.detail.ticket.Repository != "" || child.detail.ticket.Assignees != nil ||
		child.detail.ticket.PullRequests != nil || child.detail.ticket.ParentID != "" {
		t.Errorf("thin Link target borrowed list-only fields: %+v", child.detail.ticket)
	}
	if child.detail.hasLinkFocus || child.detail.offset != 0 {
		t.Errorf("new seat kept source focus/offset: focus=%v offset=%d", child.detail.hasLinkFocus, child.detail.offset)
	}
	if cmd == nil {
		t.Fatal("uncached target issued no FetchDetail command")
	}
	child = acceptDetailCommand(t, child, cmd)
	if !reflect.DeepEqual(*calls, []model.TicketID{"acme/widgets#115"}) {
		t.Errorf("FetchDetail calls = %v, want one #115", *calls)
	}

	backModel, backCmd := child.onDetailKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if backCmd != nil {
		t.Fatalf("Trail pop command = %v, want nil", backCmd)
	}
	back := backModel.(Model)
	if len(back.trail) != 0 || back.detail.loading {
		t.Errorf("pop restored Trail/loading: depth=%d loading=%v", len(back.trail), back.detail.loading)
	}
	if got := back.detailTrailSnapshot(); !reflect.DeepEqual(got, rootSnapshot) {
		t.Errorf("pop did not restore the root snapshot:\n got: %#v\nwant: %#v", got, rootSnapshot)
	}

	cachedChild, cachedCmd := followFocused(t, back)
	if cachedCmd != nil {
		t.Fatal("cached target issued another FetchDetail command")
	}
	if !cachedChild.detail.loaded || len(cachedChild.trail) != 1 {
		t.Errorf("cached follow did not open loaded child with history: loaded=%v depth=%d",
			cachedChild.detail.loaded, len(cachedChild.trail))
	}
	if len(*calls) != 1 {
		t.Errorf("cached follow changed FetchDetail calls to %v", *calls)
	}
}

func TestPopRestoresOffscreenFocusOffsetUnlessGeometryChanged(t *testing.T) {
	root, _ := navigableDetailModel(t)
	root = focusLinkAt(root, 2)
	root = root.scrollDetailTo(0)
	focused := root.detailDocument().LinkRows[2]
	if focused.Line < root.detail.offset+root.detailBodyHeight() {
		t.Fatal("test focus is still visible")
	}

	child, _ := followFocused(t, root)
	popped := child.popDetailTrail()
	if popped.detail.offset != root.detail.offset ||
		popped.detail.linkFocus != root.detail.linkFocus || !popped.detail.hasLinkFocus {
		t.Errorf("unchanged pop = offset:%d focus:%+v, want offset:%d focus:%+v",
			popped.detail.offset, popped.detail.linkFocus, root.detail.offset, root.detail.linkFocus)
	}

	child, _ = followFocused(t, root)
	child.width = 40
	popped = child.popDetailTrail()
	doc := popped.detailDocument()
	row, ok := detailLinkRowByIdentity(doc, root.detail.linkFocus)
	if !ok || row.Line < popped.detail.offset ||
		row.Line >= popped.detail.offset+popped.detailBodyHeight() {
		t.Errorf("reflowed pop did not make retained focus visible: row=%d offset=%d body=%d",
			row.Line, popped.detail.offset, popped.detailBodyHeight())
	}
}

func TestChildThenRootEscapeReturnsExactList(t *testing.T) {
	root, _ := navigableDetailModel(t)
	root = focusLinkAt(root, 0)
	wantRows := append([]Row(nil), root.rows...)
	wantSelected, wantOffset, wantFilter := root.selected, root.offset, root.filter
	child, _ := followFocused(t, root)

	next, cmd := child.onDetailKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil || next.(Model).detail.ticket.ID != root.detail.ticket.ID {
		t.Fatal("first Esc did not pop to root Detail")
	}
	next, cmd = next.(Model).onDetailKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	list := next.(Model)
	if cmd != nil || list.mode != modeList {
		t.Fatalf("second Esc = mode:%v cmd:%v", list.mode, cmd)
	}
	if !reflect.DeepEqual(list.rows, wantRows) || list.selected != wantSelected ||
		list.offset != wantOffset || !reflect.DeepEqual(list.filter, wantFilter) {
		t.Error("Detail Trail round trip changed list state")
	}
}

func TestFollowFailureAndEmptyIdentityRemainPoppableAndRetryable(t *testing.T) {
	root, calls := navigableDetailModel(t)
	// #116 is an explicit fixture Link whose target has no Detail fixture.
	root = focusLinkAt(root, 1)
	failed, cmd := followFocused(t, root)
	failed = acceptDetailCommand(t, failed, cmd)
	if failed.detail.lastErr == nil || failed.detail.loading || failed.detail.loaded {
		t.Fatalf("failed target state = loaded:%v loading:%v err:%v", failed.detail.loaded, failed.detail.loading, failed.detail.lastErr)
	}
	if _, cached := failed.details[failed.detail.ticket.ID]; cached {
		t.Error("failed target was cached")
	}
	retry, retryCmd := failed.startDetailFetch()
	if retryCmd == nil {
		t.Fatal("failed target is not retryable")
	}
	if retry.detailGeneration == failed.detailGeneration {
		t.Error("retry did not advance the Detail generation")
	}
	back, _ := failed.onDetailKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if back.(Model).detail.ticket.ID != root.detail.ticket.ID {
		t.Error("failed target did not pop to its source")
	}
	if len(*calls) != 1 {
		t.Errorf("failed follow calls = %v, want one", *calls)
	}

	emptyLink := model.Link{
		Kind:   model.LinkRelates,
		Target: model.LinkTarget{Key: "BROKEN-1", Title: "Malformed target"},
	}
	root.detail.input.Detail.Links = []model.Link{emptyLink}
	root.detail.input.Capabilities.BlockingLinks = true
	root = focusLinkAt(root, 0)
	empty, emptyCmd := followFocused(t, root)
	if emptyCmd != nil || empty.detail.loading || empty.detail.lastErr == nil {
		t.Fatalf("empty-ID follow = cmd:%v loading:%v err:%v", emptyCmd, empty.detail.loading, empty.detail.lastErr)
	}
	if empty.detail.ticket.Key != "BROKEN-1" || empty.detail.ticket.ID != "" {
		t.Errorf("empty-ID target did not keep its thin visible header: %+v", empty.detail.ticket)
	}
	retried, cmd := empty.startDetailFetch()
	if cmd != nil || retried.detail.loading || !errors.Is(retried.detail.lastErr, errEmptyLinkTargetID) {
		t.Errorf("empty-ID retry called Provider or changed failure: cmd=%v loading=%v err=%v", cmd, retried.detail.loading, retried.detail.lastErr)
	}
	popped, _ := retried.onDetailKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if popped.(Model).detail.ticket.ID != root.detail.ticket.ID {
		t.Error("empty-ID target did not pop")
	}
}

func TestCrossRepositoryFailureKeepsThinTargetAndCachesNoError(t *testing.T) {
	root, calls := navigableDetailModel(t)
	root = focusLinkAt(root, 2)
	child, cmd := followFocused(t, root)
	if child.detail.ticket.ID != "acme/gadgets#7" {
		t.Fatalf("cross-repository Link seated %q", child.detail.ticket.ID)
	}
	if child.detail.ticket.Repository != "" || child.detail.ticket.Assignees != nil ||
		child.detail.ticket.PullRequests != nil || child.detail.ticket.ParentID != "" {
		t.Errorf("cross-repository target borrowed rich list fields: %+v", child.detail.ticket)
	}
	child = acceptDetailCommand(t, child, cmd)
	if child.detail.lastErr == nil || child.detail.loaded {
		t.Errorf("cross-repository missing Detail state = loaded:%v err:%v", child.detail.loaded, child.detail.lastErr)
	}
	if _, cached := child.details[child.detail.ticket.ID]; cached {
		t.Error("cross-repository failure was cached")
	}
	if !reflect.DeepEqual(*calls, []model.TicketID{"acme/gadgets#7"}) {
		t.Errorf("cross-repository FetchDetail calls = %v", *calls)
	}
}

func TestTrailCyclesAndCacheKeepEachSeat(t *testing.T) {
	root, calls := navigableDetailModel(t)
	root = focusLinkAt(root, 0)
	b, cmd := followFocused(t, root)
	b = acceptDetailCommand(t, b, cmd)

	b.detail.input.Detail.Links = []model.Link{{
		Kind:        model.LinkRelates,
		NativeLabel: "returns to",
		Target: model.LinkTarget{
			ID: "acme/widgets#112", Key: "#112 thin", Title: "Thin cycle header",
			Status: model.StatusTodo, NativeStatus: "cycle",
		},
	}}
	b.detail.input.Capabilities.BlockingLinks = true
	b = focusLinkAt(b, 0)
	nestedA, cmd := followFocused(t, b)
	if cmd != nil {
		t.Fatal("A → B → A fetched the already cached root Detail")
	}
	if len(nestedA.trail) != 2 {
		t.Fatalf("cycle Trail depth = %d, want 2", len(nestedA.trail))
	}
	if nestedA.detail.ticket.Key != "#112 thin" || nestedA.detail.ticket.Repository != "" {
		t.Errorf("cached cycle enriched the thin seat: %+v", nestedA.detail.ticket)
	}
	if len(*calls) != 1 {
		t.Errorf("cycle FetchDetail calls = %v, want only B", *calls)
	}

	for _, want := range []model.TicketID{"acme/widgets#115", "acme/widgets#112"} {
		next, _ := nestedA.onDetailKey(tea.KeyPressMsg{Code: tea.KeyEscape})
		nestedA = next.(Model)
		if nestedA.detail.ticket.ID != want {
			t.Fatalf("cycle pop reached %q, want %q", nestedA.detail.ticket.ID, want)
		}
	}
}

func TestNavigationGenerationsDropAbandonedResults(t *testing.T) {
	root, _ := navigableDetailModel(t)
	root = focusLinkAt(root, 0)

	rereading, rereadCmd := root.startDetailFetch()
	if rereadCmd == nil {
		t.Fatal("root reread issued no command")
	}
	rereadGeneration := rereading.detailGeneration
	child, childCmd := followFocused(t, rereading)
	childGeneration := child.detailGeneration
	if childGeneration <= rereadGeneration {
		t.Fatalf("child generation %d did not advance past reread %d", childGeneration, rereadGeneration)
	}
	popped := child.popDetailTrail()
	if popped.detail.loading {
		t.Fatal("pop restored the abandoned root reread as loading")
	}
	if popped.detailGeneration <= childGeneration {
		t.Error("pop did not invalidate the child generation")
	}

	oldRoot := rereadCmd().(detailFetchedMsg)
	oldRoot.detail.Description = "stale root reread"
	afterRoot := popped.onDetailFetched(oldRoot)
	if afterRoot.details[oldRoot.id].detail.Description == "stale root reread" {
		t.Error("abandoned root reread populated the cache")
	}
	if afterRoot.detail.loading || !errors.Is(afterRoot.detail.lastErr, popped.detail.lastErr) {
		t.Error("abandoned root reread mutated the restored seat")
	}

	oldChild := childCmd().(detailFetchedMsg)
	oldChild.detail.Description = "stale child"
	afterChild := popped.onDetailFetched(oldChild)
	if _, ok := afterChild.details[oldChild.id]; ok {
		t.Error("child result after pop populated the cache")
	}

	// An old answer for the same ID is still stale after a cycle back to A.
	sameID := detailFetchedMsg{
		generation: rereadGeneration,
		id:         popped.detail.ticket.ID,
		detail:     model.Detail{Description: "same ID, wrong generation"},
	}
	afterSameID := popped.onDetailFetched(sameID)
	if afterSameID.detail.input.Detail.Description == sameID.detail.Description {
		t.Error("same-ID stale result landed after a cycle")
	}
}

func TestSuccessfulRereadReconcilesCompositeFocus(t *testing.T) {
	m, _ := navigableDetailModel(t)
	m = focusLinkAt(m, 0)
	identity := m.detail.linkFocus

	updated := m.detail.input.Detail
	updated.Links[0].Target.Title = "A renamed target"
	updated.Links[0].Target.URL = "https://example.test/renamed"
	updated.Links[0].Target.NativeStatus = "renamed"
	m.detailGeneration++
	kept := m.onDetailFetched(detailFetchedMsg{
		generation: m.detailGeneration,
		id:         m.detail.ticket.ID,
		detail:     updated,
		caps:       m.detail.input.Capabilities,
	})
	if !kept.detail.hasLinkFocus || kept.detail.linkFocus != identity {
		t.Error("mutable Link target fields cleared composite focus")
	}

	updated.Links[0].NativeLabel = "relationship renamed"
	kept.detailGeneration++
	cleared := kept.onDetailFetched(detailFetchedMsg{
		generation: kept.detailGeneration,
		id:         kept.detail.ticket.ID,
		detail:     updated,
		caps:       kept.detail.input.Capabilities,
	})
	if cleared.detail.hasLinkFocus || cleared.detailKeys.Follow.Enabled() {
		t.Error("changed relationship identity retained focus/follow help")
	}

	hidden, _ := navigableDetailModel(t)
	hidden = focusLinkAt(hidden, 0)
	hidden.detailGeneration++
	caps := hidden.detail.input.Capabilities
	caps.BlockingLinks = false
	hidden = hidden.onDetailFetched(detailFetchedMsg{
		generation: hidden.detailGeneration,
		id:         hidden.detail.ticket.ID,
		detail:     hidden.detail.input.Detail,
		caps:       caps,
	})
	if hidden.detail.hasLinkFocus || hidden.detailKeys.Follow.Enabled() {
		t.Error("capability-hidden relationship retained focus/follow help")
	}
}

func TestRootJumpClearsTrailWithoutResolvingAnArmedList(t *testing.T) {
	root, _ := navigableDetailModel(t)
	root.hasSource = true
	root.listArmed = true
	root = focusLinkAt(root, 0)
	child, _ := followFocused(t, root)
	if len(child.trail) != 1 {
		t.Fatal("test did not create a Trail")
	}

	next, cmd := child.onDetailKey(keyPress("u"))
	up := next.(Model)
	if cmd != nil || up.mode != modeList || len(up.trail) != 0 {
		t.Errorf("u on armed list = cmd:%v mode:%v depth:%d", cmd, up.mode, len(up.trail))
	}
}

func TestTrailSnapshotNeverArchivesLoading(t *testing.T) {
	m, _ := navigableDetailModel(t)
	m = focusLinkAt(m, 0)
	m.detail.loading = true
	m.detail.lastErr = errors.New("last stable reread failure")
	child, _ := followFocused(t, m)
	entry := child.trail[0]
	if !entry.loaded || entry.lastErr == nil {
		t.Errorf("snapshot lost stable loaded/error state: %+v", entry)
	}
	back := child.popDetailTrail()
	if back.detail.loading {
		t.Fatal("snapshot restored loading without an in-flight command")
	}
}

func TestTicketFromLinkTargetCopiesOnlyThinFields(t *testing.T) {
	target := model.LinkTarget{
		ID: "T-1", Key: "T-1", Title: "Thin", URL: "https://example.test/T-1",
		Status: model.StatusInProgress, NativeStatus: "review",
	}
	got := ticketFromLinkTarget(target)
	want := model.Ticket{
		ID: target.ID, Key: target.Key, Title: target.Title, URL: target.URL,
		Status: target.Status, NativeStatus: target.NativeStatus,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ticketFromLinkTarget = %#v, want %#v", got, want)
	}
}

func TestPopPreservesSuccessfulAndFailedParentRereads(t *testing.T) {
	m, _ := navigableDetailModel(t)
	m = focusLinkAt(m, 0)

	m.detailGeneration++
	fresh := m.detail.input.Detail
	fresh.Description = "successful parent reread"
	m = m.onDetailFetched(detailFetchedMsg{
		generation: m.detailGeneration, id: m.detail.ticket.ID,
		detail: fresh, caps: m.detail.input.Capabilities,
	})
	child, _ := followFocused(t, m)
	if got := child.popDetailTrail().detail.input.Detail.Description; got != fresh.Description {
		t.Errorf("pop restored description %q, want successful reread", got)
	}

	m.detailGeneration++
	m.detail.loading = true
	m = m.onDetailFetched(detailFetchedMsg{
		generation: m.detailGeneration, id: m.detail.ticket.ID,
		err: errors.New("failed parent reread"),
	})
	m = focusLinkAt(m, 0)
	child, _ = followFocused(t, m)
	restored := child.popDetailTrail()
	if restored.detail.lastErr == nil || !restored.detail.loaded || restored.detail.loading {
		t.Errorf("failed parent reread snapshot restored incorrectly: %+v", restored.detail)
	}
}

func TestTrailIsSessionLocalAndUnboundedByTime(t *testing.T) {
	m, _ := navigableDetailModel(t)
	m.now = func() time.Time { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) }
	m = focusLinkAt(m, 0)
	child, cmd := followFocused(t, m)
	child = acceptDetailCommand(t, child, cmd)
	if len(child.trail) != 1 {
		t.Errorf("Trail disappeared with clock changes: %d", len(child.trail))
	}
}
