package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/niekcandaele/sitrep/internal/detailfanout"
	"github.com/niekcandaele/sitrep/internal/model"
)

func TestLegendDefaultsOnAndSearchOwnsL(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	in := ListInput{Tickets: []model.Ticket{{ID: "one", Key: "ONE", Title: "One"}}, FetchedAt: now}
	m := New(context.Background(), Options{Initial: &in, Now: func() time.Time { return now }})
	if !m.legendVisible {
		t.Fatal("legend is not default-on")
	}
	updated, _ := m.Update(keyPress("L"))
	m = updated.(Model)
	if m.legendVisible {
		t.Fatal("L did not hide the legend")
	}
	updated, _ = m.Update(keyPress("/"))
	m = updated.(Model)
	updated, _ = m.Update(keyPress("L"))
	m = updated.(Model)
	if !m.searching || m.search.Value() != "L" || m.legendVisible {
		t.Fatalf("find box did not own L: searching=%t query=%q visible=%t", m.searching, m.search.Value(), m.legendVisible)
	}
}

func TestLegendCatalogUsesFinalEvidenceVocabulary(t *testing.T) {
	facts := []legendFact{legendOutsideWatchlist, legendLinksFailed, legendUnknownBlocker, legendPending}
	lines := strings.Join(legendLines(facts, nil, nil, 120), "\n")
	for _, want := range []string{"NOT IN WATCHLIST", "LINKS FAILED", "BLOCKER UNKNOWN", "PENDING"} {
		if !strings.Contains(lines, want) {
			t.Errorf("catalog omitted %q: %q", want, lines)
		}
	}
	for _, forbidden := range []string{"GHOST", "UNVERIFIED"} {
		if strings.Contains(lines, forbidden) {
			t.Errorf("catalog restored obsolete %q: %q", forbidden, lines)
		}
	}
}

func TestFrontierCycleHeaderCountsSetsNotMembers(t *testing.T) {
	tickets := []model.Ticket{
		{ID: "one", Key: "ONE", Status: model.StatusTodo},
		{ID: "two", Key: "TWO", Status: model.StatusTodo},
		{ID: "three", Key: "THREE", Status: model.StatusTodo},
	}
	links := map[model.TicketID][]model.Link{
		"one":   {{Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "two"}}},
		"two":   {{Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "one"}}},
		"three": nil,
	}
	graph := model.BuildBlockingGraph(tickets, links, model.Capabilities{BlockingLinks: true})
	m := Model{width: 120, now: time.Now, frontier: frontierState{
		graph: graph,
		input: FrontierInput{Tickets: tickets, Links: links, Capabilities: model.Capabilities{BlockingLinks: true}},
	}}
	got := m.frontierCycleHeaderCountsWithCycles(3, 0, 1, 3, m.frontier.graph.Cycles())
	if !strings.Contains(got, "1 blocking cycle") || strings.Contains(got, "2 blocking cycles") || strings.Contains(got, "nothing in them can start") {
		t.Fatalf("cycle header = %q", got)
	}
}

func TestLegendToggleDoesNotIssueProviderCommand(t *testing.T) {
	m := Model{
		legendVisible: true,
		keys:          DefaultKeyMap(),
		details: map[model.TicketID]detailEntry{
			"one": {fetchedAt: time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)},
		},
		selected: 2,
		offset:   1,
	}
	updated, cmd := m.onKey(tea.KeyPressMsg{Code: 'L', Text: "L"})
	got := updated.(Model)
	if got.legendVisible || cmd == nil {
		t.Fatalf("toggle result visible=%t command=%v", got.legendVisible, cmd != nil)
	}
	if _, cached := got.details["one"]; !cached || got.selected != m.selected || got.offset != m.offset {
		t.Fatalf("toggle mutated non-display state: cache=%#v selected=%d offset=%d", got.details, got.selected, got.offset)
	}
	if cmd() == nil {
		t.Fatal("toggle repaint command returned no message")
	}
}

func TestListLegendUsesOneSharedActionableFactWithoutMovingListState(t *testing.T) {
	at := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	tickets := []model.Ticket{
		{ID: "one", Key: "ONE", Title: "One", Status: model.StatusTodo},
		{ID: "two", Key: "TWO", Title: "Two", Status: model.StatusTodo},
	}
	m := mouseListModel(t, Options{Now: func() time.Time { return at }}, tickets, 100, 20)
	m.input.FetchedAt = at
	m.input.Capabilities.BlockingLinks = true
	m.help.SetWidth(m.width)
	definition := legendCatalog[legendActionable]
	fact := definition.name + " — " + definition.description

	cold := string(frame(m.View().Content))
	if strings.Count(cold, fact) != 0 {
		t.Fatalf("cold List selected the shared Actionable fact:\n%s", cold)
	}
	coldY := ticketLineY(t, m, "two", 0)
	coldClicked, _, applied := dispatchMouse(t, m, tea.MouseClickMsg{X: 1, Y: coldY, Button: tea.MouseLeft})
	if !applied || coldClicked.selectedID != "two" {
		t.Fatalf("cold marker geometry selected %q, applied=%t, want two", coldClicked.selectedID, applied)
	}

	for _, ticket := range tickets {
		m.details[ticket.ID] = detailEntry{
			detail:            model.Detail{TicketID: ticket.ID},
			fetchedAt:         at,
			frontierDetail:    model.Detail{TicketID: ticket.ID},
			frontierFetchedAt: at,
			frontierEvidence:  true,
		}
	}
	warm := string(frame(m.View().Content))
	if got := strings.Count(warm, fact); got != 1 {
		t.Fatalf("warm List selected shared Actionable fact %d times, want exactly once:\n%s", got, warm)
	}
	warmY := ticketLineY(t, m, "two", 0)
	if warmY != coldY {
		t.Fatalf("marker transition moved Ticket hit row from %d to %d", coldY, warmY)
	}
	warmClicked, _, applied := dispatchMouse(t, m, tea.MouseClickMsg{X: 1, Y: warmY, Button: tea.MouseLeft})
	if !applied || warmClicked.selectedID != "two" {
		t.Fatalf("warm marker geometry selected %q, applied=%t, want two", warmClicked.selectedID, applied)
	}
	m = warmClicked

	wantSelected, wantID := m.selected, m.selectedID
	wantOffset, wantEpoch := m.offset, m.mouseEpoch
	wantBodyHeight, wantFooterLines := m.bodyHeight(), len(m.footerLines())
	oneStamp := m.details["one"].frontierFetchedAt
	twoStamp := m.details["two"].frontierFetchedAt
	updated, cmd := m.Update(keyPress("L"))
	got := updated.(Model)
	if cmd == nil || got.legendVisible {
		t.Fatalf("L toggle visible=%t command=%v, want hidden repaint", got.legendVisible, cmd != nil)
	}
	if strings.Count(string(frame(got.View().Content)), fact) != 0 {
		t.Fatal("hidden List legend still rendered the Actionable fact")
	}
	if got.selected != wantSelected || got.selectedID != wantID || got.offset != wantOffset || got.mouseEpoch != wantEpoch {
		t.Fatalf("L moved list state: selected=%d/%q offset=%d epoch=%d, want %d/%q %d %d",
			got.selected, got.selectedID, got.offset, got.mouseEpoch, wantSelected, wantID, wantOffset, wantEpoch)
	}
	if got.bodyHeight() != wantBodyHeight || len(got.footerLines()) != wantFooterLines ||
		ticketLineY(t, got, "two", 0) != warmY {
		t.Fatalf("L moved primary layout: body/footer/y=%d/%d/%d, want %d/%d/%d",
			got.bodyHeight(), len(got.footerLines()), ticketLineY(t, got, "two", 0),
			wantBodyHeight, wantFooterLines, warmY)
	}
	if len(got.details) != 2 || !got.details["one"].frontierFetchedAt.Equal(oneStamp) ||
		!got.details["two"].frontierFetchedAt.Equal(twoStamp) {
		t.Fatalf("L mutated cache/evidence: %#v", got.details)
	}
	if msg := cmd(); msg == nil {
		t.Fatal("L repaint command returned no message")
	}
}

func TestLegendLinesAreAtomic(t *testing.T) {
	if got := legendLines([]legendFact{legendActionable}, nil, nil, 20); got != nil {
		t.Fatalf("partial legend = %q, want omission", got)
	}
}

func TestLegendOmitsCycleWithMissingDisplayKey(t *testing.T) {
	id := model.TicketID("hostile\x1b[31m-id")
	cycles := [][]model.TicketID{{id}}
	got := legendLines([]legendFact{legendCycle}, cycles, map[model.TicketID]string{id: ""}, 120)
	if got != nil {
		t.Fatalf("cycle legend leaked fallback = %q, want complete block omitted", got)
	}
}

func TestUnresolvedLegendDoesNotClaimFinalFacts(t *testing.T) {
	f := frontierState{input: FrontierInput{Tickets: []model.Ticket{{ID: "one"}}, Capabilities: model.Capabilities{BlockingLinks: true}}}
	f.graph = model.BuildBlockingGraph(f.input.Tickets, nil, f.input.Capabilities)
	facts := frontierLegendFacts(f, "one")
	if len(facts) != 1 || facts[0] != legendPending {
		t.Fatalf("unresolved facts = %v, want PENDING only", facts)
	}
}

func TestListShortHelpDoesNotAdvertiseLegend(t *testing.T) {
	for _, binding := range DefaultKeyMap().ShortHelp() {
		if binding.Help().Key == "L" {
			t.Fatal("List short Help advertises legend")
		}
	}
}

func TestLegendRemainsLiveWithoutFrontierCanvas(t *testing.T) {
	m := Model{legendVisible: true, frontierKeys: DefaultFrontierKeyMap()}
	if !m.effectiveFrontierKeys().Legend.Enabled() {
		t.Fatal("no-capability Frontier disabled the global legend key")
	}

	updated, _ := m.onFrontierKey(keyPress("L"))
	m = updated.(Model)
	if m.legendVisible {
		t.Fatal("L did not hide the session-global legend without a Frontier canvas")
	}
	updated, _ = m.onFrontierKey(keyPress("L"))
	m = updated.(Model)
	if !m.legendVisible {
		t.Fatal("L did not restore the session-global legend without a Frontier canvas")
	}
}

func resolvedLegendFrontier() frontierState {
	capabilities := model.Capabilities{BlockingLinks: true}
	tickets := []model.Ticket{{ID: "one", Key: "ONE", Status: model.StatusTodo}}
	links := map[model.TicketID][]model.Link{"one": nil}
	return frontierState{
		graph: model.BuildBlockingGraph(tickets, links, capabilities),
		input: FrontierInput{Tickets: tickets, Links: links, Capabilities: capabilities},
	}
}

func TestLegendDispatchAndDetailSlackEligibility(t *testing.T) {
	frontier := resolvedLegendFrontier()
	m := Model{
		width:         120,
		height:        30,
		legendVisible: true,
		detailKeys:    DefaultDetailKeyMap(),
		detailReturn:  modeFrontier,
		detail:        detailState{ticket: model.Ticket{ID: "one", Key: "ONE"}, provenance: detailFrontierMember, input: DetailInput{Detail: model.Detail{TicketID: "one"}}},
		frontier:      frontier,
	}
	updated, cmd := m.onDetailKey(keyPress("L"))
	m = updated.(Model)
	if m.legendVisible || cmd == nil || cmd() == nil {
		t.Fatal("Detail L did not issue only the repaint toggle")
	}
	m.legendVisible = true
	if got := strings.Join(m.detailLegendLines(detailDocument{Lines: []string{"complete document"}}), "\n"); !strings.Contains(got, "actionable") {
		t.Fatalf("Frontier Detail legend = %q", got)
	}
	m.detail.offset = 1
	if got := m.detailLegendLines(detailDocument{}); got != nil {
		t.Fatalf("scrolled Detail legend = %q, want omitted", got)
	}
	m.detail.offset = 0
	m.detailReturn = modeList
	if got := m.detailLegendLines(detailDocument{}); got != nil {
		t.Fatalf("List-origin Detail legend = %q, want omitted", got)
	}
}

func TestUnresolvedFrontierDetailOmitsAllLegendFacts(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	tickets := []model.Ticket{{ID: "one", Key: "ONE", Status: model.StatusTodo}}
	m := Model{
		width:         120,
		height:        30,
		legendVisible: true,
		detailReturn:  modeFrontier,
		frontier: frontierState{
			graph: model.BuildBlockingGraph(tickets, nil, capabilities),
			input: FrontierInput{Tickets: tickets, Capabilities: capabilities},
		},
	}
	for _, id := range []model.TicketID{"one", "outside"} {
		m.detail = detailState{input: DetailInput{Detail: model.Detail{TicketID: id}}}
		if got := m.detailLegendLines(detailDocument{}); got != nil {
			t.Fatalf("unresolved Frontier Detail %q legend = %q, want omitted", id, got)
		}
	}

	m.frontier = resolvedLegendFrontier()
	m.detail = detailState{ticket: model.Ticket{ID: "one"}, provenance: detailFrontierMember, input: DetailInput{Detail: model.Detail{TicketID: "one"}}}
	if got := m.detailLegendLines(detailDocument{}); len(got) == 0 {
		t.Fatal("resolved Frontier Detail omitted its applicable legend")
	}
}

func TestLoadingFrontierDetailUsesStableSeatIdentity(t *testing.T) {
	m := Model{
		width:         120,
		height:        30,
		legendVisible: true,
		detailReturn:  modeFrontier,
		detail: detailState{
			ticket: model.Ticket{ID: "one", Key: "ONE"}, provenance: detailFrontierMember,
			input: DetailInput{}, loading: true,
		},
		frontier: resolvedLegendFrontier(),
	}
	got := strings.Join(m.detailLegendLines(detailDocument{}), "\n")
	if !strings.Contains(got, "actionable") || strings.Contains(got, "NOT IN WATCHLIST") {
		t.Fatalf("loading Frontier Detail legend = %q", got)
	}
}

func TestFrontierDetailLegendRefreshesResolvedGraphWithoutLayout(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	ticket := model.Ticket{ID: "one", Key: "ONE", Status: model.StatusTodo}
	m := Model{
		mode:                 modeDetail,
		width:                120,
		height:               30,
		legendVisible:        true,
		detailReturn:         modeFrontier,
		frontierGeneration:   1,
		detailFanoutInflight: 1,
		details:              make(map[model.TicketID]detailEntry),
		now:                  time.Now,
		detail:               detailState{ticket: ticket, provenance: detailFrontierMember},
		frontier: frontierState{
			graph:   model.BuildBlockingGraph([]model.Ticket{ticket}, nil, capabilities),
			input:   FrontierInput{Tickets: []model.Ticket{ticket}, Capabilities: capabilities},
			planned: 1,
		},
	}
	var layouts int
	m.layoutFrontierFn = func(g model.BlockingGraph, nodes []frontierNode, opts frontierLayoutOptions) frontierLayout {
		layouts++
		return layoutFrontier(g, nodes, opts)
	}
	updated, cmd := m.Update(frontierResultMsg(
		1, 0, ticket.ID, model.Detail{TicketID: ticket.ID}, capabilities, nil))
	m = updated.(Model)
	if cmd == nil || layouts != 0 || !m.frontier.isResolved() {
		t.Fatalf("hidden Frontier update cmd=%v layouts=%d resolved=%t, want repaint/no layout/resolved", cmd, layouts, m.frontier.isResolved())
	}
	got := strings.Join(m.detailLegendLines(detailDocument{}), "\n")
	if !strings.Contains(strings.ToUpper(got), "ACTIONABLE") || strings.Contains(got, "LINKS FAILED") {
		t.Fatalf("resolved Frontier Detail legend = %q", got)
	}
}

func TestFrontierDetailLegendRefreshesNewCycleEvidenceWithoutLayout(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	tickets := []model.Ticket{
		{ID: "one", Key: "ONE", Status: model.StatusTodo},
		{ID: "two", Key: "TWO", Status: model.StatusTodo},
	}
	m := Model{
		mode:                 modeDetail,
		width:                120,
		height:               30,
		legendVisible:        true,
		detailReturn:         modeFrontier,
		frontierGeneration:   1,
		detailFanoutInflight: 1,
		details:              make(map[model.TicketID]detailEntry),
		now:                  time.Now,
		detail:               detailState{ticket: tickets[0], provenance: detailFrontierMember},
		frontier: frontierState{
			graph:   model.BuildBlockingGraph(tickets, nil, capabilities),
			input:   FrontierInput{Tickets: tickets, Capabilities: capabilities},
			planned: 2,
		},
	}
	var layouts int
	m.layoutFrontierFn = func(g model.BlockingGraph, nodes []frontierNode, opts frontierLayoutOptions) frontierLayout {
		layouts++
		return layoutFrontier(g, nodes, opts)
	}
	updated, cmd := m.Update(frontierDetailsMsg{
		generation: 1,
		outcomes: []detailfanout.Outcome{
			{ID: "one", Detail: model.Detail{TicketID: "one", Links: []model.Link{{
				Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "two", Key: "TWO"},
			}}}, Caps: capabilities},
			{ID: "two", Detail: model.Detail{TicketID: "two", Links: []model.Link{{
				Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "one", Key: "ONE"},
			}}}, Caps: capabilities},
		},
	})
	m = updated.(Model)
	if cmd == nil || layouts != 0 || !m.frontier.isResolved() {
		t.Fatalf("resolved hidden Frontier update cmd=%v layouts=%d resolved=%t, want repaint/no layout/resolved", cmd, layouts, m.frontier.isResolved())
	}
	got := strings.Join(m.detailLegendLines(detailDocument{}), "\n")
	if !strings.Contains(got, "CYCLE") || !strings.Contains(got, "cycle 1:") {
		t.Fatalf("resolved Frontier Detail cycle legend = %q", got)
	}
}

func TestFrontierDetailRefreshReplacesEvidenceWithoutLayout(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	tickets := []model.Ticket{
		{ID: "one", Key: "ONE", Status: model.StatusTodo},
		{ID: "two", Key: "TWO", Status: model.StatusTodo},
	}
	links := map[model.TicketID][]model.Link{"one": nil, "two": nil}
	m := Model{
		mode:             modeDetail,
		width:            120,
		height:           30,
		listArmed:        true,
		legendVisible:    true,
		detailKeys:       DefaultDetailKeyMap(),
		detailReturn:     modeFrontier,
		detailGeneration: 3,
		details:          make(map[model.TicketID]detailEntry),
		now:              time.Now,
		detail: detailState{
			ticket:     tickets[0],
			provenance: detailFrontierMember,
			loading:    true,
		},
		frontier: frontierState{
			graph:   model.BuildBlockingGraph(tickets, links, capabilities),
			input:   FrontierInput{Tickets: tickets, Links: links, Capabilities: capabilities},
			failed:  map[model.TicketID]struct{}{"one": {}},
			lastErr: errors.New("stale fan-out failure"),
		},
	}
	var layouts, fetches int
	m.layoutFrontierFn = func(g model.BlockingGraph, nodes []frontierNode, opts frontierLayoutOptions) frontierLayout {
		layouts++
		return layoutFrontier(g, nodes, opts)
	}
	m.fetchDetail = func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
		fetches++
		return model.Detail{}, model.Capabilities{}, nil
	}

	updated, cmd := m.Update(detailFetchedMsg{
		generation: 3,
		id:         "one",
		detail: model.Detail{
			TicketID: "provider-spoofed-id",
			Links: []model.Link{{
				Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "two", Key: "TWO"},
			}},
		},
		caps: capabilities,
	})
	m = updated.(Model)
	if cmd == nil || layouts != 0 || fetches != 0 {
		t.Fatalf("Frontier Detail reread cmd=%v layouts=%d fetches=%d, want repaint/no layout/no fetch", cmd, layouts, fetches)
	}
	if _, stale := m.frontier.input.Links["provider-spoofed-id"]; stale {
		t.Fatal("Detail payload ID redirected Frontier evidence away from the stable seat")
	}
	if got := m.frontier.input.Links["one"]; len(got) != 1 || got[0].Target.ID != "two" {
		t.Fatalf("Frontier Links for stable seat = %#v, want refreshed blocker", got)
	}
	if len(m.frontier.failed) != 0 || m.frontier.lastErr != nil {
		t.Fatalf("successful Detail reread retained stale Frontier failure: failed=%v err=%v", m.frontier.failed, m.frontier.lastErr)
	}
	legend := strings.Join(m.detailLegendLines(detailDocument{}), "\n")
	if !strings.Contains(legend, "blocked by N") || strings.Contains(strings.ToUpper(legend), "ACTIONABLE") {
		t.Fatalf("refreshed Detail legend = %q, want blocked evidence", legend)
	}

	updated, cmd = m.onDetailKey(keyPress("esc"))
	m = updated.(Model)
	if cmd == nil || m.mode != modeFrontier {
		t.Fatalf("Esc after Detail reread mode=%v cmd=%v, want Frontier repaint", m.mode, cmd)
	}
	assessment, member := m.frontier.graph.For("one")
	if !member || assessment.Actionable || knownUnmetBlockers(assessment) != 1 {
		t.Fatalf("Frontier graph after Esc = %#v member=%t, want one unmet blocker", assessment, member)
	}
}

func TestNativeFrontierOpenRefreshesFollowedAliasCache(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	alias := model.Ticket{ID: "two", Key: "TWO", Status: model.StatusTodo}
	followed := model.Detail{Links: []model.Link{{
		Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "outside", Key: "OUTSIDE"},
	}}}
	m := Model{
		mode: modeFrontier, detailReturn: modeFrontier, now: time.Now,
		newReadContext: func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		},
		fetchDetail: func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
			return model.Detail{}, capabilities, nil
		},
		details: map[model.TicketID]detailEntry{
			alias.ID: {detail: followed, frontierIneligible: true, directDetailVersion: 1},
		},
		frontier: frontierState{
			input: FrontierInput{
				Tickets: []model.Ticket{alias}, Links: make(map[model.TicketID][]model.Link), Capabilities: capabilities,
			},
			focusID: alias.ID, hasFocus: true,
		},
	}
	opened, cmd := m.openFrontierNode()
	m = opened.(Model)
	if cmd == nil || !m.detail.loaded || !m.detail.loading || !m.detail.provenance.fromFrontier() {
		t.Fatalf("native open did not refresh followed cache: detail=%#v cmd=%v", m.detail, cmd)
	}
	native := model.Detail{}
	updated, _ := m.Update(detailFetchedMsg{
		generation: m.detailGeneration, id: alias.ID, caps: capabilities, detail: native,
	})
	m = updated.(Model)
	entry := m.details[alias.ID]
	evidence, eligible := entry.frontierLinks()
	if !eligible || entry.frontierIneligible || !slices.Equal(evidence.Links, native.Links) {
		t.Fatalf("native reread did not become Frontier evidence: %#v", entry)
	}
	if links, seated := m.frontier.input.Links[alias.ID]; !seated || !slices.Equal(links, native.Links) {
		t.Fatalf("native reread did not seat hidden Frontier links: %#v", m.frontier.input.Links)
	}

	m.mode = modeFrontier
	m.frontier.hasFocus = true
	opened, cmd = m.openFrontierNode()
	m = opened.(Model)
	if cmd != nil || !m.detail.loaded || m.detail.loading {
		t.Fatalf("eligible native cache caused redundant read: detail=%#v cmd=%v", m.detail, cmd)
	}
}

func TestDetailLegendRequiresNativeFrontierMemberOrigin(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	tickets := []model.Ticket{
		{ID: "one", Key: "ONE", Status: model.StatusTodo},
		{ID: "two", Key: "TWO", Status: model.StatusTodo},
	}
	links := map[model.TicketID][]model.Link{
		"one": {{Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "two", Key: "TWO"}}},
		"two": {{Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "one", Key: "ONE"}}},
	}
	frontier := frontierState{
		input: FrontierInput{Tickets: tickets, Links: links, Capabilities: capabilities},
		graph: model.BuildBlockingGraph(tickets, links, capabilities),
	}
	aliasFacts := frontierLegendFactsForID(frontier, "one", false)
	if len(aliasFacts) != 1 || aliasFacts[0] != legendOutsideWatchlist {
		t.Fatalf("followed alias facts = %v, want NOT IN WATCHLIST only", aliasFacts)
	}
	if cycles := detailLegendCycles(frontier.graph.Cycles(), "one", false); cycles != nil {
		t.Fatalf("followed alias cycles = %v, want none", cycles)
	}
	nativeFacts := frontierLegendFactsForID(frontier, "one", true)
	if !slices.Contains(nativeFacts, legendCycle) {
		t.Fatalf("native member facts = %v, want CYCLE", nativeFacts)
	}
	if cycles := detailLegendCycles(frontier.graph.Cycles(), "one", true); len(cycles) != 1 {
		t.Fatalf("native member cycles = %v, want one SCC", cycles)
	}
}

func TestFrontierDetailRefreshNamesOnlyRemainingFailure(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	tickets := []model.Ticket{
		{ID: "one", Key: "ONE", Status: model.StatusTodo},
		{ID: "two", Key: "TWO", Status: model.StatusTodo},
	}
	links := map[model.TicketID][]model.Link{"one": nil, "two": nil}
	m := Model{
		mode:             modeDetail,
		width:            120,
		height:           30,
		legendVisible:    true,
		detailReturn:     modeFrontier,
		detailGeneration: 2,
		details:          make(map[model.TicketID]detailEntry),
		now:              time.Now,
		detail:           detailState{ticket: tickets[0], provenance: detailFrontierMember, loading: true},
		frontier: frontierState{
			graph:  model.BuildBlockingGraph(tickets, links, capabilities),
			input:  FrontierInput{Tickets: tickets, Links: links, Capabilities: capabilities},
			failed: map[model.TicketID]struct{}{"one": {}, "two": {}},
			failureErrors: map[model.TicketID]error{
				"one": errors.New("recovered failure"),
				"two": errors.New("remaining failure"),
			},
			lastErr: errors.New("recovered failure"),
		},
	}
	var layouts, fetches int
	m.layoutFrontierFn = func(g model.BlockingGraph, nodes []frontierNode, opts frontierLayoutOptions) frontierLayout {
		layouts++
		return layoutFrontier(g, nodes, opts)
	}
	m.fetchDetail = func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
		fetches++
		return model.Detail{}, model.Capabilities{}, nil
	}

	updated, cmd := m.Update(detailFetchedMsg{
		generation: 2,
		id:         "one",
		detail:     model.Detail{},
		caps:       capabilities,
	})
	m = updated.(Model)
	if cmd == nil || layouts != 0 || fetches != 0 {
		t.Fatalf("recovery cmd=%v layouts=%d fetches=%d, want repaint/no layout/no fetch", cmd, layouts, fetches)
	}
	if _, recovered := m.frontier.failed["one"]; recovered {
		t.Fatal("successful Detail reread left its own failure seated")
	}
	if _, remaining := m.frontier.failed["two"]; !remaining || m.frontier.lastErr == nil || m.frontier.lastErr.Error() != "remaining failure" {
		t.Fatalf("remaining failure notice = failed:%v err:%v", m.frontier.failed, m.frontier.lastErr)
	}
	footer := strings.Join(m.frontierFooterLines(), "\n")
	if strings.Contains(footer, "recovered failure") || !strings.Contains(footer, "remaining failure") || !strings.Contains(footer, "TWO") {
		t.Fatalf("Frontier footer after member recovery = %q", footer)
	}
}

func TestFrontierDetailRefreshAddsCycleWithoutLayout(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	tickets := []model.Ticket{
		{ID: "one", Key: "ONE", Status: model.StatusTodo},
		{ID: "two", Key: "TWO", Status: model.StatusTodo},
	}
	links := map[model.TicketID][]model.Link{
		"one": nil,
		"two": {{Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "one", Key: "ONE"}}},
	}
	m := Model{
		mode:             modeDetail,
		width:            120,
		height:           30,
		legendVisible:    true,
		detailReturn:     modeFrontier,
		detailGeneration: 5,
		details:          make(map[model.TicketID]detailEntry),
		now:              time.Now,
		detail:           detailState{ticket: tickets[0], provenance: detailFrontierMember, loading: true},
		frontier: frontierState{
			graph: model.BuildBlockingGraph(tickets, links, capabilities),
			input: FrontierInput{Tickets: tickets, Links: links, Capabilities: capabilities},
		},
	}
	var layouts, fetches int
	m.layoutFrontierFn = func(g model.BlockingGraph, nodes []frontierNode, opts frontierLayoutOptions) frontierLayout {
		layouts++
		return layoutFrontier(g, nodes, opts)
	}
	m.fetchDetail = func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
		fetches++
		return model.Detail{}, model.Capabilities{}, nil
	}

	updated, cmd := m.Update(detailFetchedMsg{
		generation: 5,
		id:         "one",
		detail: model.Detail{Links: []model.Link{{
			Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "two", Key: "TWO"},
		}}},
		caps: capabilities,
	})
	m = updated.(Model)
	if cmd == nil || layouts != 0 || fetches != 0 {
		t.Fatalf("cycle Detail reread cmd=%v layouts=%d fetches=%d, want repaint/no layout/no fetch", cmd, layouts, fetches)
	}
	legend := strings.Join(m.detailLegendLines(detailDocument{}), "\n")
	if !strings.Contains(legend, "CYCLE") || !strings.Contains(legend, "cycle 1:") {
		t.Fatalf("Detail legend after refreshed cycle evidence = %q", legend)
	}
}

func TestFrontierFanoutDoesNotOverwriteNewerDetailEvidence(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	tickets := []model.Ticket{
		{ID: "one", Key: "ONE", Status: model.StatusTodo},
		{ID: "two", Key: "TWO", Status: model.StatusTodo},
	}
	for _, tc := range []struct {
		name       string
		generation int
		detail     model.Detail
		err        error
	}{
		{name: "delayed success", generation: 4, detail: model.Detail{Description: "stale fan-out success"}},
		{name: "delayed failure", generation: 4, err: errors.New("stale fan-out failure")},
		{name: "stale generation success", generation: 3, detail: model.Detail{Description: "stale F3 success"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			links := map[model.TicketID][]model.Link{"one": nil, "two": nil}
			m := Model{
				mode:                 modeDetail,
				width:                120,
				height:               30,
				listArmed:            true,
				legendVisible:        true,
				detailKeys:           DefaultDetailKeyMap(),
				detailReturn:         modeFrontier,
				detailGeneration:     7,
				frontierGeneration:   4,
				detailFanoutInflight: 1,
				details:              make(map[model.TicketID]detailEntry),
				now:                  time.Now,
				detail:               detailState{ticket: tickets[0], provenance: detailFrontierMember, loading: true},
				frontier: frontierState{
					graph:   model.BuildBlockingGraph(tickets, links, capabilities),
					input:   FrontierInput{Tickets: tickets, Links: links, Capabilities: capabilities},
					planned: 1,
				},
			}
			var layouts, fetches int
			m.layoutFrontierFn = func(g model.BlockingGraph, nodes []frontierNode, opts frontierLayoutOptions) frontierLayout {
				layouts++
				return layoutFrontier(g, nodes, opts)
			}
			m.fetchDetail = func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
				fetches++
				return model.Detail{}, model.Capabilities{}, nil
			}

			updated, cmd := m.Update(detailFetchedMsg{
				generation: 7,
				id:         "one",
				detail: model.Detail{
					Description: "new direct Detail",
					Links: []model.Link{{
						Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "two", Key: "TWO"},
					}},
				},
				caps: capabilities,
			})
			m = updated.(Model)
			if cmd == nil || layouts != 0 || fetches != 0 {
				t.Fatalf("direct Detail cmd=%v layouts=%d fetches=%d, want repaint/no layout/no fetch", cmd, layouts, fetches)
			}

			updated, cmd = m.Update(frontierResultMsg(
				tc.generation, 0, "one", tc.detail, capabilities, tc.err))
			m = updated.(Model)
			if layouts != 0 || fetches != 0 || (tc.generation == 4 && cmd == nil) || (tc.generation != 4 && cmd != nil) {
				t.Fatalf("late fan-out cmd=%v layouts=%d fetches=%d, want current repaint or stale no-op with no layout/fetch", cmd, layouts, fetches)
			}
			if got := m.details["one"].detail.Description; got != "new direct Detail" {
				t.Fatalf("late fan-out replaced direct Detail cache with %q", got)
			}
			if got := m.frontier.input.Links["one"]; len(got) != 1 || got[0].Target.ID != "two" {
				t.Fatalf("late fan-out replaced direct Links with %#v", got)
			}
			if len(m.frontier.failed) != 0 || m.frontier.lastErr != nil {
				t.Fatalf("late fan-out retained a false failure: failed=%v err=%v", m.frontier.failed, m.frontier.lastErr)
			}
			if footer := strings.Join(m.frontierFooterLines(), "\n"); strings.Contains(footer, "stale fan-out failure") {
				t.Fatalf("late fan-out failure leaked into Frontier footer: %q", footer)
			}
			legend := strings.Join(m.detailLegendLines(detailDocument{}), "\n")
			if !strings.Contains(legend, "blocked by N") || strings.Contains(strings.ToUpper(legend), "ACTIONABLE") {
				t.Fatalf("Detail legend after delayed fan-out = %q", legend)
			}

			updated, cmd = m.onDetailKey(keyPress("esc"))
			m = updated.(Model)
			assessment, member := m.frontier.graph.For("one")
			if cmd == nil || m.mode != modeFrontier || !member || assessment.Actionable || knownUnmetBlockers(assessment) != 1 {
				t.Fatalf("Esc after delayed fan-out mode=%v cmd=%v assessment=%#v member=%t", m.mode, cmd, assessment, member)
			}
		})
	}
}

func TestFrontierFanoutPreservesNewerDirectDetailAcrossModes(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	tickets := []model.Ticket{
		{ID: "one", Key: "ONE", Status: model.StatusTodo},
		{ID: "two", Key: "TWO", Status: model.StatusTodo},
	}
	for _, tc := range []struct {
		name         string
		detailReturn mode
	}{
		{name: "Frontier Detail after Frontier re-entry", detailReturn: modeFrontier},
		{name: "List Detail after Frontier re-entry", detailReturn: modeList},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{
				mode:                 modeDetail,
				detailReturn:         tc.detailReturn,
				detailGeneration:     7,
				frontierGeneration:   4,
				detailFanoutInflight: 2,
				details:              make(map[model.TicketID]detailEntry),
				now:                  time.Now,
				detail: detailState{
					ticket: tickets[0], provenance: frontierSeatProvenance(tc.detailReturn == modeFrontier), loading: true,
				},
				frontier: frontierState{
					input: FrontierInput{Tickets: tickets, Links: map[model.TicketID][]model.Link{"one": nil, "two": nil}, Capabilities: capabilities},
				},
			}

			updated, _ := m.Update(detailFetchedMsg{
				generation: 7, id: "one", caps: capabilities,
				detail: model.Detail{Description: "new direct Detail", Links: []model.Link{{
					Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "two", Key: "TWO"},
				}}},
			})
			m = updated.(Model)

			// The stale F4 command was issued before this direct read. Re-entering
			// Frontier replaces its state, but not cache provenance.
			m.mode = modeFrontier
			m.frontierGeneration = 5
			m.frontier = frontierState{
				input: FrontierInput{Tickets: tickets, Links: map[model.TicketID][]model.Link{"two": nil}, Capabilities: capabilities},
			}
			updated, cmd := m.Update(frontierResultMsg(
				4, 0, "one", model.Detail{Description: "stale F4 Detail"}, capabilities, nil))
			m = updated.(Model)
			if m.details["one"].detail.Description != "new direct Detail" {
				t.Fatalf("stale F4 replaced newer direct cache: cmd=%v entry=%#v", cmd, m.details["one"])
			}

			// A F5 command issued after the direct read carries its version and may
			// replace it normally.
			updated, cmd = m.Update(frontierResultMsg(
				5, m.detailEvidenceVersion, "one",
				model.Detail{Description: "new F5 Detail"}, capabilities, nil))
			m = updated.(Model)
			if cmd == nil || m.details["one"].detail.Description != "new F5 Detail" {
				t.Fatalf("F5 did not replace direct cache: cmd=%v entry=%#v", cmd, m.details["one"])
			}
		})
	}
}

func TestCurrentFrontierFanoutOverridesStaleGenerationCache(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	tickets := []model.Ticket{
		{ID: "one", Key: "ONE", Status: model.StatusTodo},
		{ID: "two", Key: "TWO", Status: model.StatusTodo},
	}
	m := Model{
		mode:                 modeDetail,
		width:                120,
		height:               30,
		detailReturn:         modeFrontier,
		frontierGeneration:   7,
		detailFanoutInflight: 2,
		details:              make(map[model.TicketID]detailEntry),
		now:                  time.Now,
		frontier: frontierState{
			graph:   model.BuildBlockingGraph(tickets, map[model.TicketID][]model.Link{"two": nil}, capabilities),
			input:   FrontierInput{Tickets: tickets, Links: map[model.TicketID][]model.Link{"two": nil}, Capabilities: capabilities},
			planned: 1,
		},
	}
	var layouts, fetches int
	m.layoutFrontierFn = func(g model.BlockingGraph, nodes []frontierNode, opts frontierLayoutOptions) frontierLayout {
		layouts++
		return layoutFrontier(g, nodes, opts)
	}
	m.fetchDetail = func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
		fetches++
		return model.Detail{}, model.Capabilities{}, nil
	}

	updated, cmd := m.Update(frontierResultMsg(
		6, 0, "one", model.Detail{Description: "old F6", Links: nil}, capabilities, nil))
	m = updated.(Model)
	if cmd != nil || m.details["one"].detail.Description != "old F6" {
		t.Fatalf("stale F6 did not preserve normal cache warming: cmd=%v entry=%#v", cmd, m.details["one"])
	}

	updated, cmd = m.Update(frontierResultMsg(
		7, 0, "one", model.Detail{Description: "new F7", Links: []model.Link{{
			Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "two", Key: "TWO"},
		}}}, capabilities, nil))
	m = updated.(Model)
	if cmd == nil || layouts != 0 || fetches != 0 {
		t.Fatalf("current F7 cmd=%v layouts=%d fetches=%d, want repaint/no layout/no fetch", cmd, layouts, fetches)
	}
	if got := m.details["one"].detail.Description; got != "new F7" {
		t.Fatalf("current F7 failed to replace stale cache with %q", got)
	}
	if got := m.frontier.input.Links["one"]; len(got) != 1 || got[0].Target.ID != "two" {
		t.Fatalf("current F7 failed to seat its Links: %#v", got)
	}
}

func TestNewerSuccessfulFanoutRejectsOlderSuccessEverywhere(t *testing.T) {
	caps := model.Capabilities{BlockingLinks: true}
	tickets := []model.Ticket{
		{ID: "A", Key: "A", Status: model.StatusTodo},
		{ID: "B", Key: "B", Status: model.StatusTodo},
	}
	hAt := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	oldArrivesAt := hAt.Add(time.Hour)
	now := hAt
	m := Model{
		mode:                 modeFrontier,
		width:                120,
		height:               30,
		frontierGeneration:   8,
		detailFanoutInflight: 2,
		details: map[model.TicketID]detailEntry{
			"B": {
				detail:            model.Detail{TicketID: "B"},
				caps:              caps,
				fetchedAt:         hAt,
				frontierDetail:    model.Detail{TicketID: "B"},
				frontierFetchedAt: hAt,
				frontierEvidence:  true,
			},
		},
		now:   func() time.Time { return now },
		input: ListInput{Tickets: tickets, Capabilities: caps, FetchedAt: hAt},
		frontier: frontierState{
			input: FrontierInput{
				Tickets: tickets, Links: map[model.TicketID][]model.Link{"B": nil}, Capabilities: caps,
			},
			planned: 1,
		},
	}
	hDetail := model.Detail{TicketID: "A", Description: "accepted H", Links: []model.Link{{
		Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "B", Key: "B"},
	}}}
	updated, _ := m.onFrontierDetails(frontierResultMsg(8, 0, "A", hDetail, caps, nil))
	m = updated.(Model)
	hMarkers := m.listMarkers()
	if !hMarkers.active || hMarkers.has("A") || !hMarkers.has("B") || hMarkers.count != 1 {
		t.Fatalf("List markers after H = %+v, want only B Actionable", hMarkers)
	}

	now = oldArrivesAt
	gDetail := model.Detail{TicketID: "A", Description: "late G"}
	updated, _ = m.onFrontierDetails(frontierResultMsg(7, 0, "A", gDetail, caps, nil))
	m = updated.(Model)

	entry := m.details["A"]
	if entry.detail.Description != hDetail.Description || entry.lastFanoutGeneration != 8 {
		t.Fatalf("late G regressed accepted H cache/provenance: %#v", entry)
	}
	if !entry.fetchedAt.Equal(hAt) || !entry.frontierFetchedAt.Equal(hAt) {
		t.Errorf("late G restamped H evidence: fetched=%v frontier=%v want %v",
			entry.fetchedAt, entry.frontierFetchedAt, hAt)
	}
	if got := m.frontier.input.Links["A"]; len(got) != 1 || got[0].Target.ID != "B" {
		t.Fatalf("late G regressed current H seat: %#v", got)
	}
	markers := m.listMarkers()
	if markers.active != hMarkers.active || markers.count != hMarkers.count || markers.has("A") != hMarkers.has("A") || markers.has("B") != hMarkers.has("B") {
		t.Errorf("late G regressed List markers from %+v to %+v", hMarkers, markers)
	}

	adopted := m
	adopted.frontier.input.Links = map[model.TicketID][]model.Link{"B": nil}
	adopted = adopted.adoptCachedLinks()
	if got := adopted.frontier.input.Links["A"]; len(got) != 1 || got[0].Target.ID != "B" {
		t.Errorf("later cache adoption used G instead of accepted H: %#v", got)
	}
}

func TestNewerFanoutFailureDoesNotBlockOlderSuccessWarming(t *testing.T) {
	caps := model.Capabilities{BlockingLinks: true}
	tickets := []model.Ticket{
		{ID: "A", Key: "A", Status: model.StatusTodo},
		{ID: "B", Key: "B", Status: model.StatusTodo},
	}
	gAt := time.Date(2026, time.September, 1, 13, 0, 0, 0, time.UTC)
	hErr := errors.New("H failed")
	m := Model{
		mode:                 modeFrontier,
		width:                120,
		height:               30,
		frontierGeneration:   8,
		detailFanoutInflight: 2,
		details:              make(map[model.TicketID]detailEntry),
		now:                  func() time.Time { return gAt },
		input:                ListInput{Tickets: tickets, Capabilities: caps, FetchedAt: gAt},
		frontier: frontierState{
			input: FrontierInput{
				Tickets: tickets, Links: map[model.TicketID][]model.Link{"B": nil}, Capabilities: caps,
			},
			planned: 1,
		},
	}

	updated, _ := m.onFrontierDetails(frontierResultMsg(8, 0, "A", model.Detail{}, caps, hErr))
	m = updated.(Model)
	if _, failed := m.frontier.failed["A"]; !failed || !errors.Is(m.frontier.lastErr, hErr) {
		t.Fatalf("H failure was not retained on its current seat: failed=%v err=%v", m.frontier.failed, m.frontier.lastErr)
	}

	gDetail := model.Detail{TicketID: "A", Description: "paid-for G", Links: []model.Link{{
		Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "B", Key: "B"},
	}}}
	updated, _ = m.onFrontierDetails(frontierResultMsg(7, 0, "A", gDetail, caps, nil))
	m = updated.(Model)

	entry := m.details["A"]
	if entry.detail.Description != gDetail.Description || entry.lastFanoutGeneration != 7 || !entry.fetchedAt.Equal(gAt) {
		t.Fatalf("H failure suppressed paid-for G cache warming: %#v", entry)
	}
	if _, seated := m.frontier.input.Links["A"]; seated {
		t.Fatal("stale G mutated H's current failed seat before adoption")
	}
	if _, failed := m.frontier.failed["A"]; !failed || !errors.Is(m.frontier.lastErr, hErr) {
		t.Fatalf("stale G cleared H failure: failed=%v err=%v", m.frontier.failed, m.frontier.lastErr)
	}

	adopted := m.adoptCachedLinks()
	if got := adopted.frontier.input.Links["A"]; len(got) != 1 || got[0].Target.ID != "B" {
		t.Errorf("normal adoption did not seat paid-for G: %#v", got)
	}
	if _, failed := adopted.frontier.failed["A"]; failed {
		t.Error("normal adoption left H's obsolete failure attached")
	}
}

func TestFrontierDetailRefreshLeavesGhostAndEmptyMemberEvidenceAlone(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	member := model.Ticket{ID: "one", Key: "ONE", Status: model.StatusTodo}
	for _, tc := range []struct {
		name       string
		ticket     model.Ticket
		member     bool
		id         model.TicketID
		returnMode mode
	}{
		{name: "followed ghost", ticket: model.Ticket{ID: "ghost", Key: "GHOST"}, id: "ghost", returnMode: modeFrontier},
		{name: "empty member", ticket: model.Ticket{ID: ""}, member: true, id: "", returnMode: modeFrontier},
		{name: "List Detail", ticket: member, member: true, id: "one", returnMode: modeList},
	} {
		t.Run(tc.name, func(t *testing.T) {
			links := map[model.TicketID][]model.Link{"one": nil, "": nil}
			m := Model{
				mode:             modeDetail,
				detailReturn:     tc.returnMode,
				detailGeneration: 1,
				details:          make(map[model.TicketID]detailEntry),
				now:              time.Now,
				detail:           detailState{ticket: tc.ticket, provenance: frontierSeatProvenance(tc.member), loading: true},
				frontier: frontierState{
					graph: model.BuildBlockingGraph([]model.Ticket{member}, links, capabilities),
					input: FrontierInput{Tickets: []model.Ticket{member}, Links: links, Capabilities: capabilities},
				},
			}
			updated, cmd := m.Update(detailFetchedMsg{
				generation: 1,
				id:         tc.id,
				detail: model.Detail{Links: []model.Link{{
					Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "one", Key: "ONE"},
				}}},
				caps: capabilities,
			})
			m = updated.(Model)
			if cmd != nil {
				t.Fatalf("anonymous/empty Detail reread cmd=%v, want no Frontier rebuild", cmd)
			}
			if len(m.frontier.input.Links["one"]) != 0 || len(m.frontier.input.Links[""]) != 0 {
				t.Fatalf("anonymous/empty Detail reread changed canonical evidence: %#v", m.frontier.input.Links)
			}
			if _, ghost := m.frontier.input.Links["ghost"]; ghost {
				t.Fatal("followed ghost Detail created Frontier member evidence")
			}
		})
	}
}

func TestFollowedWatchlistAliasNeverSeedsFrontierEvidence(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	source := model.Ticket{ID: "one", Key: "ONE", Status: model.StatusTodo}
	alias := model.Ticket{ID: "two", Key: "TWO", Status: model.StatusTodo}
	sourceLinks := []model.Link{{
		Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: alias.ID, Key: alias.Key},
	}}
	followedLinks := []model.Link{{
		Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: source.ID, Key: source.Key},
	}}
	m := Model{
		mode:                 modeDetail,
		hasData:              true,
		listArmed:            true,
		detailReturn:         modeFrontier,
		detailGeneration:     1,
		detailKeys:           DefaultDetailKeyMap(),
		detailFanoutInflight: 2,
		details: map[model.TicketID]detailEntry{
			source.ID: {detail: model.Detail{Links: sourceLinks}},
		},
		now: time.Now,
		newReadContext: func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		},
		fetchDetail: func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
			return model.Detail{}, capabilities, nil
		},
		input: ListInput{
			Tickets: []model.Ticket{source, alias}, Capabilities: capabilities,
		},
		detail: detailState{
			ticket: source, loaded: true, provenance: detailFrontierMember,
			input: DetailInput{Detail: model.Detail{Links: sourceLinks}, Capabilities: capabilities},
		},
		frontier: frontierState{
			input: FrontierInput{
				Tickets: []model.Ticket{source, alias},
				Links:   map[model.TicketID][]model.Link{source.ID: sourceLinks}, Capabilities: capabilities,
			},
		},
	}

	followed, cmd := m.followDetailLink(detailLinkIdentity{TargetID: alias.ID, Kind: model.LinkBlockedBy})
	m = followed.(Model)
	if cmd == nil || m.detail.ticket.ID != alias.ID || m.detail.provenance.fromFrontier() {
		t.Fatalf("followed Detail = ticket:%q member:%t cmd:%v, want alias/non-member/fetch", m.detail.ticket.ID, m.detail.provenance.fromFrontier(), cmd)
	}
	updated, cmd := m.Update(detailFetchedMsg{
		generation: m.detailGeneration, id: alias.ID, caps: capabilities,
		detail: model.Detail{Links: followedLinks},
	})
	m = updated.(Model)
	if cmd != nil || !m.details[alias.ID].frontierIneligible {
		t.Fatalf("followed alias cache = %#v cmd=%v, want display-only cache/no Frontier mutation", m.details[alias.ID], cmd)
	}

	older := model.Detail{Links: []model.Link{{
		Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: source.ID, Key: source.Key},
	}}}
	updated, _ = m.onFrontierDetails(frontierResultMsg(
		m.frontierGeneration, 0, alias.ID, older, model.Capabilities{}, nil))
	m = updated.(Model)
	if !m.details[alias.ID].frontierIneligible || !slices.Equal(m.details[alias.ID].detail.Links, followedLinks) {
		t.Fatalf("older fan-out replaced followed alias display cache: %#v", m.details[alias.ID])
	}
	if _, leaked := m.frontier.input.Links[alias.ID]; leaked {
		t.Fatal("older fan-out seeded followed alias as Frontier evidence")
	}
	if _, eligible := m.details[alias.ID].frontierLinks(); eligible || m.frontier.done != 1 || m.detailFanoutInflight != 1 {
		t.Fatalf("older fan-out success changed evidence or accounting: entry=%#v done=%d inflight=%d", m.details[alias.ID], m.frontier.done, m.detailFanoutInflight)
	}
	failure := errors.New("older fan-out failed")
	updated, _ = m.onFrontierDetails(frontierResultMsg(
		m.frontierGeneration, 0, alias.ID, model.Detail{}, model.Capabilities{}, failure))
	m = updated.(Model)
	if _, failed := m.frontier.failed[alias.ID]; failed {
		t.Fatal("older fan-out failure became followed alias Frontier failure")
	}
	if m.frontier.lastErr != nil || m.frontier.done != 2 || m.detailFanoutInflight != 0 {
		t.Fatalf("older fan-out failure changed footer or accounting: lastErr=%v done=%d inflight=%d", m.frontier.lastErr, m.frontier.done, m.detailFanoutInflight)
	}

	m = m.popDetailTrail()
	returned, _ := m.onDetailKey(keyPress("esc"))
	m = returned.(Model)
	if m.mode != modeFrontier {
		t.Fatalf("root Detail return mode = %v, want Frontier", m.mode)
	}
	if _, leaked := m.frontier.input.Links[alias.ID]; leaked {
		t.Fatal("followed alias Detail leaked into hidden Frontier through cached adoption")
	}
	m.mode = modeList
	reentered, _ := m.enterFrontier()
	m = reentered.(Model)
	if _, leaked := m.frontier.input.Links[alias.ID]; leaked || m.haveDetail(alias.ID) {
		t.Fatalf("followed alias leaked across Frontier re-entry: links=%#v haveDetail=%t", m.frontier.input.Links, m.haveDetail(alias.ID))
	}
	assessment, member := m.frontier.graph.For(alias.ID)
	if !member || assessment.LinksKnown {
		t.Fatalf("re-entered alias assessment = %#v member=%t, want unresolved Watchlist member", assessment, member)
	}
}

func TestEligibleDetailReadsSeedFrontierCache(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	ticket := model.Ticket{ID: "one", Key: "ONE", Status: model.StatusTodo}
	for _, tc := range []struct {
		name       string
		returnMode mode
		member     bool
	}{
		{name: "List Detail", returnMode: modeList},
		{name: "native Frontier Detail", returnMode: modeFrontier, member: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fetchedAt := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
			m := Model{
				mode: modeDetail, detailReturn: tc.returnMode, detailGeneration: 1,
				details: make(map[model.TicketID]detailEntry), now: func() time.Time { return fetchedAt },
				detail: detailState{ticket: ticket, provenance: watchlistSeatProvenance(tc.member), loading: true},
				frontier: frontierState{input: FrontierInput{
					Tickets: []model.Ticket{ticket}, Links: make(map[model.TicketID][]model.Link), Capabilities: capabilities,
				}},
			}
			updated, _ := m.Update(detailFetchedMsg{
				generation: 1, id: ticket.ID, caps: capabilities, detail: model.Detail{},
			})
			m = updated.(Model)
			entry := m.details[ticket.ID]
			if entry.frontierIneligible {
				t.Fatalf("%s was not eligible for Frontier cache", tc.name)
			}
			if got, ok := entry.frontierEvidenceStamp(); !ok || !got.Equal(fetchedAt) {
				t.Fatalf("%s evidence stamp = (%v, %v), want (%v, true)", tc.name, got, ok, fetchedAt)
			}
			if _, seeded := m.linksFromCache()[ticket.ID]; !seeded || !m.haveDetail(ticket.ID) {
				t.Fatalf("%s did not seed eligible Frontier cache", tc.name)
			}
		})
	}
}

func TestListDetailOriginDoesNotTransferToFollowedAlias(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	source := model.Ticket{ID: "one", Key: "ONE", Status: model.StatusTodo}
	alias := model.Ticket{ID: "two", Key: "TWO", Status: model.StatusTodo}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	in := ListInput{Tickets: []model.Ticket{source, alias}, Capabilities: capabilities, FetchedAt: now}
	m := New(context.Background(), Options{
		Initial: &in, Now: func() time.Time { return now },
		DetailSource: func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
			return model.Detail{}, capabilities, nil
		},
	})
	opened, cmd := m.openDetail()
	m = opened.(Model)
	if cmd == nil || !m.detail.provenance.watchlistMember() {
		t.Fatalf("List root did not establish native origin: detail=%#v cmd=%v", m.detail, cmd)
	}
	sourceLinks := []model.Link{{
		Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: alias.ID, Key: alias.Key},
	}}
	updated, _ := m.Update(detailFetchedMsg{
		generation: m.detailGeneration, id: source.ID, caps: capabilities, detail: model.Detail{Links: sourceLinks},
	})
	m = updated.(Model)
	if !m.haveDetail(source.ID) {
		t.Fatal("List root Detail did not seed eligible cache")
	}

	followed, cmd := m.followDetailLink(detailLinkIdentity{TargetID: alias.ID, Kind: model.LinkBlockedBy})
	m = followed.(Model)
	if cmd == nil || m.detail.provenance.watchlistMember() {
		t.Fatalf("List followed alias inherited native origin: detail=%#v cmd=%v", m.detail, cmd)
	}
	aliasLinks := []model.Link{{
		Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: source.ID, Key: source.Key},
	}}
	updated, _ = m.Update(detailFetchedMsg{
		generation: m.detailGeneration, id: alias.ID, caps: capabilities, detail: model.Detail{Links: aliasLinks},
	})
	m = updated.(Model)
	if !m.details[alias.ID].frontierIneligible || m.haveDetail(alias.ID) {
		t.Fatalf("List followed alias seeded Frontier cache: %#v", m.details[alias.ID])
	}
	m.mode = modeList
	reentered, _ := m.enterFrontier()
	m = reentered.(Model)
	if _, leaked := m.frontier.input.Links[alias.ID]; leaked || m.haveDetail(alias.ID) {
		t.Fatalf("List followed alias leaked at Frontier entry: links=%#v", m.frontier.input.Links)
	}
}

func TestFollowedAliasPreservesPriorEligibleEvidenceOrdering(t *testing.T) {
	alias := model.Ticket{ID: "two", Key: "TWO"}
	priorAt := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	displayAt := priorAt.Add(time.Hour)
	prior := model.Detail{Links: []model.Link{{
		Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "prior", Key: "PRIOR"},
	}}}
	followed := model.Detail{Links: []model.Link{{
		Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "followed", Key: "FOLLOWED"},
	}}}
	m := Model{
		mode: modeDetail, detailReturn: modeFrontier, detailGeneration: 1,
		detailEvidenceVersion: 7, now: func() time.Time { return displayAt },
		detail:               detailState{ticket: alias, loading: true},
		frontierGeneration:   1,
		detailFanoutInflight: 2,
		details: map[model.TicketID]detailEntry{
			alias.ID: {
				detail: prior, fetchedAt: priorAt, frontierDetail: prior,
				frontierFetchedAt: priorAt, frontierEvidence: true, directDetailVersion: 7,
			},
		},
	}
	updated, _ := m.Update(detailFetchedMsg{generation: 1, id: alias.ID, detail: followed})
	m = updated.(Model)
	entry := m.details[alias.ID]
	got, eligible := entry.frontierLinks()
	if !entry.frontierIneligible || !eligible || !slices.Equal(got.Links, prior.Links) || entry.directDetailVersion != 8 {
		t.Fatalf("followed alias replaced prior evidence: entry=%#v evidence=%#v eligible=%t", entry, got, eligible)
	}
	if evidenceAt, ok := entry.frontierEvidenceStamp(); !ok || !evidenceAt.Equal(priorAt) || !entry.fetchedAt.Equal(displayAt) {
		t.Fatalf("followed alias moved evidence time: evidence=(%v, %v) display=%v", evidenceAt, ok, entry.fetchedAt)
	}

	stale := model.Detail{Links: []model.Link{{Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "stale", Key: "STALE"}}}}
	updated, _ = m.onFrontierDetails(frontierResultMsg(
		1, 7, alias.ID, stale, model.Capabilities{}, nil))
	m = updated.(Model)
	got, eligible = m.details[alias.ID].frontierLinks()
	if !eligible || !slices.Equal(got.Links, prior.Links) || !slices.Equal(m.details[alias.ID].detail.Links, followed.Links) || !m.details[alias.ID].frontierIneligible {
		t.Fatalf("stale fan-out displaced newer followed display or prior eligible evidence: %#v", m.details[alias.ID])
	}

	current := model.Detail{Links: []model.Link{{Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "current", Key: "CURRENT"}}}}
	updated, _ = m.onFrontierDetails(frontierResultMsg(
		1, 8, alias.ID, current, model.Capabilities{}, nil))
	m = updated.(Model)
	got, eligible = m.details[alias.ID].frontierLinks()
	if !eligible || !slices.Equal(got.Links, current.Links) {
		t.Fatalf("current fan-out did not replace stale eligible evidence: %#v", got)
	}
	if evidenceAt, ok := m.details[alias.ID].frontierEvidenceStamp(); !ok || !evidenceAt.Equal(displayAt) {
		t.Fatalf("current fan-out evidence stamp = (%v, %v), want (%v, true)", evidenceAt, ok, displayAt)
	}
}

func TestFrontierLegendUsesOnlyTrueRightSlack(t *testing.T) {
	frontier := resolvedLegendFrontier()
	frontier.layout = frontierLayout{direction: frontierRanksVertical, width: 20, height: 3}
	m := Model{width: 120, legendVisible: true, frontier: frontier}
	body := make([]string, 20)
	got := strings.Join(m.frontierLegendBody(body, len(body), m.frontier.graph.Cycles()), "\n")
	if !strings.Contains(got, "Legend · L hide") {
		t.Fatalf("right slack did not render legend: %q", got)
	}
	m.frontier.offsetX = 1
	got = strings.Join(m.frontierLegendBody(make([]string, 20), 20, m.frontier.graph.Cycles()), "\n")
	if strings.Contains(got, "Legend · L hide") {
		t.Fatalf("panned Frontier rendered legend: %q", got)
	}
}

func TestUnresolvedFrontierLegendIncludesRenderedOutsideWatchlistFact(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	tickets := []model.Ticket{{ID: "one", Key: "ONE", Status: model.StatusTodo}}
	links := map[model.TicketID][]model.Link{
		"one": {{Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "outside", Key: "OUT"}}},
	}
	f := frontierState{
		graph: model.BuildBlockingGraph(tickets, links, capabilities),
		input: FrontierInput{Tickets: tickets, Capabilities: capabilities},
	}
	facts := allFrontierLegendFacts(f)
	if !slices.Equal(facts, []legendFact{legendPending, legendOutsideWatchlist}) {
		t.Fatalf("unresolved legend facts = %v", facts)
	}
}

func TestFrontierNoCycleHeaderPreservesEstablishedSegments(t *testing.T) {
	m := Model{frontier: resolvedLegendFrontier()}
	if got, want := m.frontierCycleHeaderCountsWithCycles(4, 2, 0, 0, m.frontier.graph.Cycles()), "frontier · 4 nodes · 2 ghosts · actionable unknown"; got != want {
		t.Fatalf("no-cycle header = %q, want %q", got, want)
	}
}

func TestEmptyIDMemberNeverClaimsOutsideWatchlist(t *testing.T) {
	f := resolvedLegendFrontier()
	facts := frontierLegendFactsForNode(f, frontierNode{
		member:   true,
		emphasis: frontierEmphasis{badge: "ACTIONABLE"},
	})
	if !slices.Equal(facts, []legendFact{legendActionable, legendStatusChannel}) {
		t.Fatalf("empty member legend facts = %v", facts)
	}
}

func TestEmptyIDFrontierDetailNeverClaimsOutsideWatchlist(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	tickets := []model.Ticket{{ID: "", Key: "EMPTY", Status: model.StatusTodo}}
	links := map[model.TicketID][]model.Link{"": nil}
	m := Model{
		width:         120,
		height:        30,
		legendVisible: true,
		detailReturn:  modeFrontier,
		detail:        detailState{ticket: model.Ticket{}, provenance: detailFrontierMember, input: DetailInput{Detail: model.Detail{TicketID: ""}}},
		frontier: frontierState{
			graph: model.BuildBlockingGraph(tickets, links, capabilities),
			input: FrontierInput{Tickets: tickets, Links: links, Capabilities: capabilities},
		},
	}
	got := strings.Join(m.detailLegendLines(detailDocument{}), "\n")
	if got == "" || strings.Contains(got, "NOT IN WATCHLIST") {
		t.Fatalf("empty member Detail legend = %q", got)
	}
}

func TestAnonymousFrontierDetailDoesNotBorrowEmptyMemberFacts(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	tickets := []model.Ticket{
		{ID: "one", Key: "ONE", Status: model.StatusTodo},
		{ID: "", Key: "EMPTY", Status: model.StatusTodo},
		{ID: "two", Key: "TWO", Status: model.StatusTodo},
	}
	links := map[model.TicketID][]model.Link{
		"one": {{Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "", Key: "GHOST"}}},
		"":    {{Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "two", Key: "TWO"}}},
		"two": {{Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "", Key: "EMPTY"}}},
	}
	m := Model{
		width:         120,
		height:        30,
		legendVisible: true,
		detailReturn:  modeFrontier,
		detail:        detailState{ticket: model.Ticket{}},
		frontier: frontierState{
			graph: model.BuildBlockingGraph(tickets, links, capabilities),
			input: FrontierInput{Tickets: tickets, Links: links, Capabilities: capabilities},
		},
	}
	anonymous := strings.Join(m.detailLegendLines(detailDocument{}), "\n")
	if !strings.Contains(anonymous, "NOT IN WATCHLIST") || strings.Contains(anonymous, "PENDING") || strings.Contains(anonymous, "CYCLE") || strings.Contains(anonymous, "cycle 1:") {
		t.Fatalf("anonymous Frontier Detail legend = %q", anonymous)
	}
	m.detail.provenance = detailFrontierMember
	member := strings.Join(m.detailLegendLines(detailDocument{}), "\n")
	if member == "" || strings.Contains(member, "GHOST") || strings.Contains(member, "NOT IN WATCHLIST") {
		t.Fatalf("empty member Frontier Detail legend = %q", member)
	}
}

func TestFrontierMemberOriginSurvivesDetailTrail(t *testing.T) {
	m := New(context.Background(), Options{Now: time.Now})
	m.width = 120
	m.height = 30
	m.detail = detailState{ticket: model.Ticket{ID: ""}, provenance: detailFrontierMember}
	m.trail = []detailTrailEntry{m.detailTrailSnapshot()}
	m.detail = detailState{}
	m = m.popDetailTrail()
	if !m.detail.provenance.fromFrontier() {
		t.Fatal("Detail trail lost Frontier member origin")
	}
}

func TestEmptyDetailCyclesAndKeysRequireMemberOrigin(t *testing.T) {
	cycles := [][]model.TicketID{{"", "two"}}
	if got := detailLegendCycles(cycles, "", false); got != nil {
		t.Fatalf("anonymous empty Detail cycles = %v, want none", got)
	}
	if got := detailLegendCycles(cycles, "", true); len(got) != 1 || !slices.Equal(got[0], cycles[0]) {
		t.Fatalf("member empty Detail cycles = %v, want %v", got, cycles)
	}
	keys := frontierLegendKeysForNodes([]frontierNode{
		{id: "", member: true, key: "EMPTY"},
		{id: "", key: "GHOST"},
	})
	if got := keys[""]; got != "EMPTY" {
		t.Fatalf("duplicate empty-ID cycle key = %q, want member key", got)
	}
}

func TestGhostCycleDetailLegendStaysOutsideWatchlist(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	tickets := []model.Ticket{{ID: "one", Key: "ONE", Status: model.StatusTodo}}
	links := map[model.TicketID][]model.Link{
		"one": {
			{Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "ghost", Key: "GHOST"}},
			{Kind: model.LinkBlocks, Target: model.LinkTarget{ID: "ghost", Key: "GHOST"}},
		},
	}
	m := Model{
		width:         120,
		height:        30,
		now:           time.Now,
		legendVisible: true,
		detailReturn:  modeFrontier,
		detail:        detailState{ticket: model.Ticket{ID: "ghost", Key: "GHOST"}, input: DetailInput{Detail: model.Detail{TicketID: "ghost"}}},
		frontier: frontierState{
			graph: model.BuildBlockingGraph(tickets, links, capabilities),
			input: FrontierInput{Tickets: tickets, Links: links, Capabilities: capabilities},
		},
	}
	if got := strings.Join(m.detailLegendLines(detailDocument{}), "\n"); strings.Contains(got, "CYCLE") || !strings.Contains(got, "NOT IN WATCHLIST") {
		t.Fatalf("ghost cycle Detail legend = %q, want outside-Watchlist only", got)
	}
}

func TestFrontierCycleSnapshotFeedsHeaderAndLegend(t *testing.T) {
	capabilities := model.Capabilities{BlockingLinks: true}
	tickets := []model.Ticket{
		{ID: "one", Key: "ONE", Status: model.StatusTodo},
		{ID: "two", Key: "TWO", Status: model.StatusTodo},
	}
	links := map[model.TicketID][]model.Link{
		"one": {{Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "two", Key: "TWO"}}},
		"two": {{Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: "one", Key: "ONE"}}},
	}
	frontier := frontierState{
		graph: model.BuildBlockingGraph(tickets, links, capabilities),
		input: FrontierInput{Tickets: tickets, Links: links, Capabilities: capabilities},
		layout: frontierLayout{direction: frontierRanksVertical, width: 20, height: 3,
			cells:  [][]frontierCell{make([]frontierCell, 20), make([]frontierCell, 20), make([]frontierCell, 20)},
			styles: []frontierStyle{{}}, links: []string{""}, order: []model.TicketID{"one", "two"}},
	}
	m := New(context.Background(), Options{Now: time.Now})
	m.width = 120
	m.height = 30
	m.legendVisible = true
	m.frontier = frontier
	cycles := m.frontierCyclesForFrame()
	if got := m.frontierHeaderWithCycles(cycles); !strings.Contains(got, "1 blocking cycle") {
		t.Fatalf("shared-cycle header = %q", got)
	}
	body := strings.Join(m.frontierLegendBody(make([]string, 20), 20, cycles), "\n")
	if !strings.Contains(body, "cycle 1: ONE, TWO") {
		t.Fatalf("shared-cycle legend = %q", body)
	}
	frame := m.frontierFrameWithCycles(cycles)
	for _, want := range []string{"1 blocking cycle", "cycle 1: ONE, TWO"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("shared cycle snapshot frame omitted %q: %q", want, frame)
		}
	}
}

// frontierSeatProvenance builds the seat provenance a Frontier-origin test case
// expects: a member opened from the canvas, or a display-only seat.
func frontierSeatProvenance(member bool) detailProvenance {
	if member {
		return detailFrontierMember
	}
	return detailDisplayOnly
}

// watchlistSeatProvenance builds the provenance of a Watchlist member seat,
// opened either from the Frontier canvas or from the List.
func watchlistSeatProvenance(fromFrontier bool) detailProvenance {
	if fromFrontier {
		return detailFrontierMember
	}
	return detailListMember
}
