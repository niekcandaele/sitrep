package tui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/niekcandaele/sitrep/internal/detailfanout"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/jira"
)

type phase2Calls struct {
	source atomic.Int32
	detail atomic.Int32
	fanout atomic.Int32
}

func phase2AdmissionModel(t *testing.T, clock *policyClock, calls *phase2Calls) Model {
	t.Helper()
	initial := ListInput{
		Tickets: []model.Ticket{
			{ID: "A", Key: "A", Title: "first", Status: model.StatusTodo},
			{ID: "B", Key: "B", Title: "second", Status: model.StatusTodo},
		},
		Capabilities: model.Capabilities{BlockingLinks: true},
		FetchedAt:    clock.now(),
	}
	return New(t.Context(), Options{
		Initial:   &initial,
		Interval:  time.Minute,
		Now:       clock.now,
		Heartbeat: func() tea.Msg { return nil },
		Source: func(context.Context) (ListInput, error) {
			calls.source.Add(1)
			return initial, nil
		},
		DetailSource: func(_ context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
			calls.detail.Add(1)
			return model.Detail{TicketID: id}, initial.Capabilities, nil
		},
		DetailFanout: func(_ context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
			calls.fanout.Add(1)
			details := make(map[model.TicketID]model.Detail, len(ids))
			for _, id := range ids {
				details[id] = model.Detail{TicketID: id}
			}
			return details, initial.Capabilities, nil
		},
	})
}

func phase2KnownHold(clock *policyClock) rateHold {
	return rateHold{
		err:     provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: clock.now().Add(time.Minute)}, "rate limited"),
		resetAt: clock.now().Add(time.Minute),
	}
}

func TestPhase2KnownHoldBlocksEveryLaunchLaneUntilReset(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}

	t.Run("before reset", func(t *testing.T) {
		calls := &phase2Calls{}
		m := phase2AdmissionModel(t, clock, calls)
		m.rateHold = phase2KnownHold(clock)
		generation, attempt := m.generation, m.lastAttempt

		next, cmd := m.onKey(tea.KeyPressMsg{Code: 'r'})
		m = next.(Model)
		if cmd != nil || m.generation != generation || !m.lastAttempt.Equal(attempt) {
			t.Fatal("known hold admitted List refresh")
		}

		next, cmd = m.openDetail()
		m = next.(Model)
		if cmd != nil || m.detail.loading || m.detail.ticket.ID != "A" {
			t.Fatalf("known hold started Detail replacement: loading=%t ticket=%q cmd=%v", m.detail.loading, m.detail.ticket.ID, cmd)
		}

		next, cmd = m.enterFrontier()
		m = next.(Model)
		if m.mode != modeFrontier || m.frontierContext != nil || m.detailFanoutInflight != 0 {
			t.Fatalf("known hold started Frontier work: mode=%v context=%v inflight=%d", m.mode, m.frontierContext, m.detailFanoutInflight)
		}
		phase2RunCommand(cmd)
		if calls.source.Load() != 0 || calls.detail.Load() != 0 || calls.fanout.Load() != 0 {
			t.Fatalf("known hold made calls: source=%d detail=%d fanout=%d", calls.source.Load(), calls.detail.Load(), calls.fanout.Load())
		}
	})

	t.Run("at reset", func(t *testing.T) {
		for _, lane := range []struct {
			name  string
			start func(Model) (tea.Model, tea.Cmd)
			calls func(*phase2Calls) int32
		}{
			{"list", func(m Model) (tea.Model, tea.Cmd) { return m.onKey(tea.KeyPressMsg{Code: 'r'}) }, func(calls *phase2Calls) int32 { return calls.source.Load() }},
			{"detail", func(m Model) (tea.Model, tea.Cmd) { return m.openDetail() }, func(calls *phase2Calls) int32 { return calls.detail.Load() }},
			{"frontier", func(m Model) (tea.Model, tea.Cmd) { return m.enterFrontier() }, func(calls *phase2Calls) int32 { return calls.fanout.Load() }},
		} {
			t.Run(lane.name, func(t *testing.T) {
				calls := &phase2Calls{}
				m := phase2AdmissionModel(t, clock, calls)
				m.rateHold = phase2KnownHold(clock)
				clock.advance(time.Minute)
				next, cmd := lane.start(m)
				if cmd == nil || next.(Model).rateHold != (rateHold{}) {
					t.Fatalf("reset did not admit %s: hold=%+v cmd=%v", lane.name, next.(Model).rateHold, cmd)
				}
				phase2RunCommand(cmd)
				if got := lane.calls(calls); got != 1 {
					t.Fatalf("%s calls at reset = %d, want one", lane.name, got)
				}
				clock.advance(-time.Minute)
			})
		}
	})
}

func phase2RunCommand(cmd tea.Cmd) {
	for range phase2CommandMessages(cmd) {
	}
}

func phase2CommandMessages(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var messages []tea.Msg
		for _, child := range batch {
			messages = append(messages, phase2CommandMessages(child)...)
		}
		return messages
	}
	return []tea.Msg{msg}
}

func TestPhase2RealScreenRefusalBlocksEveryOtherLaunchLane(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	reset := clock.now().Add(time.Hour)
	refusal := provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: reset}, "rate limited")

	t.Run("Detail refusal blocks List and Frontier", func(t *testing.T) {
		calls := &phase2Calls{}
		m := phase2AdmissionModel(t, clock, calls)
		m.fetchDetail = func(ctx context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
			calls.detail.Add(1)
			return model.Detail{TicketID: id}, m.input.Capabilities, refusal
		}
		next, cmd := m.openDetail()
		m = next.(Model)
		if cmd == nil {
			t.Fatal("fixture did not issue Detail read")
		}
		m = m.onDetailFetched(cmd().(detailFetchedMsg))
		if calls.detail.Load() != 1 || m.rateHold.manual || !m.rateHold.resetAt.Equal(reset) {
			t.Fatalf("Detail refusal did not install known hold: calls=%d hold=%+v", calls.detail.Load(), m.rateHold)
		}
		listNext, allowed, _ := m.automaticRefreshAdmission()
		if allowed || listNext.refreshing {
			t.Fatal("Detail refusal admitted List request")
		}
		next, frontierCmd := m.enterFrontier()
		m = next.(Model)
		phase2RunCommand(frontierCmd)
		if m.frontierContext != nil || calls.fanout.Load() != 0 {
			t.Fatalf("Detail refusal admitted Frontier: context=%v calls=%d", m.frontierContext, calls.fanout.Load())
		}
	})

	t.Run("Frontier refusal blocks Detail and List", func(t *testing.T) {
		calls := &phase2Calls{}
		m := phase2AdmissionModel(t, clock, calls)
		m.fetchDetails = func(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
			calls.fanout.Add(1)
			return nil, m.input.Capabilities, refusal
		}
		next, cmd := m.enterFrontier()
		m = next.(Model)
		var response *frontierDetailsMsg
		for _, msg := range phase2CommandMessages(cmd) {
			if details, ok := msg.(frontierDetailsMsg); ok {
				response = &details
			}
		}
		if response == nil {
			t.Fatal("fixture did not issue Frontier plural read")
		}
		next, _ = m.onFrontierDetails(*response)
		m = next.(Model)
		if calls.fanout.Load() != 1 || m.rateHold.manual || !m.rateHold.resetAt.Equal(reset) {
			t.Fatalf("Frontier refusal did not install known hold: calls=%d hold=%+v", calls.fanout.Load(), m.rateHold)
		}
		if next, detailCmd := m.openDetail(); detailCmd != nil || next.(Model).detail.loading {
			t.Fatal("Frontier refusal admitted Detail request")
		}
		listNext, allowed, _ := m.automaticRefreshAdmission()
		if allowed || listNext.refreshing {
			t.Fatal("Frontier refusal admitted List request")
		}
	})
}

func TestPhase2DecodedWalkUpArmsListAcrossKnownDetailHold(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	reset := clock.now().Add(time.Minute)
	calls := &phase2Calls{}
	input := ListInput{
		Tickets:   []model.Ticket{{ID: "A", Key: "A", Title: "first", Status: model.StatusTodo}},
		FetchedAt: clock.now(),
	}
	open := OpenTicket{
		Ticket: model.Ticket{ID: "A", Key: "A", Title: "first", Status: model.StatusTodo},
		Parent: Header{Key: "WATCH", Title: "Watchlist"},
	}
	refusal := provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: reset}, "detail rate limited")
	m := New(t.Context(), Options{
		Source: func(context.Context) (ListInput, error) {
			calls.source.Add(1)
			return input, nil
		},
		DetailSource: func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
			calls.detail.Add(1)
			return model.Detail{}, model.Capabilities{}, refusal
		},
		Open:      &open,
		Interval:  time.Minute,
		Now:       clock.now,
		Heartbeat: func() tea.Msg { return nil },
	})
	if m.listArmed || !m.detail.loading {
		t.Fatalf("decoded startup = armed:%t loading:%t, want unarmed Detail read", m.listArmed, m.detail.loading)
	}

	m = m.onDetailFetched(m.detailFetchCmd(m.detailGeneration, m.detail.ticket.ID)().(detailFetchedMsg))
	if calls.detail.Load() != 1 || !m.rateHold.resetAt.Equal(reset) || m.rateHold.manual {
		t.Fatalf("Detail refusal = calls:%d hold:%+v, want one read and known hold", calls.detail.Load(), m.rateHold)
	}

	next, cmd := m.walkUp()
	m = next.(Model)
	if cmd != nil || calls.source.Load() != 0 {
		t.Fatalf("walk-up during hold issued Source: cmd=%v calls=%d", cmd, calls.source.Load())
	}
	if !m.listArmed {
		t.Fatal("denied walk-up did not arm automatic List monitoring")
	}

	next, cmd = m.onHeartbeat()
	m = next.(Model)
	phase2RunCommand(cmd)
	if calls.source.Load() != 0 || m.refreshing {
		t.Fatalf("heartbeat before reset = calls:%d refreshing:%t, want no request", calls.source.Load(), m.refreshing)
	}

	clock.advance(time.Minute)
	next, cmd = m.onHeartbeat()
	m = next.(Model)
	if cmd == nil || !m.refreshing || m.rateHold != (rateHold{}) {
		t.Fatalf("heartbeat at reset = cmd:%v refreshing:%t hold:%+v, want admitted refresh", cmd, m.refreshing, m.rateHold)
	}
	phase2RunCommand(cmd)
	if calls.source.Load() != 1 {
		t.Fatalf("Source calls at reset = %d, want one automatic request", calls.source.Load())
	}
}

func TestPhase2UnknownProbeIsGlobalAndHasExplicitSettlementRules(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	unknown := provider.Errorf(provider.KindRateLimit, "tracker did not provide reset")

	t.Run("one probe across List and Detail, then success clears", func(t *testing.T) {
		calls := &phase2Calls{}
		m := phase2AdmissionModel(t, clock, calls)
		m.rateHold = rateHold{err: unknown, manual: true}

		next, cmd := m.onKey(tea.KeyPressMsg{Code: 'r'})
		m = next.(Model)
		if cmd == nil || m.unknownProbeToken == 0 {
			t.Fatal("List did not reserve the unknown-reset probe")
		}
		if next, denied := m.openDetail(); denied != nil || next.(Model).detail.loading {
			t.Fatal("Detail overlapped the List unknown-reset probe")
		}

		m = m.onRefreshed(cmd().(refreshedMsg))
		if m.rateHold != (rateHold{}) || m.unknownProbeToken != 0 {
			t.Fatalf("successful probe did not clear unknown hold: hold=%+v token=%d", m.rateHold, m.unknownProbeToken)
		}
		next, detailCmd := m.openDetail()
		if detailCmd == nil || !next.(Model).detail.loading {
			t.Fatal("Detail remained denied after the successful global probe")
		}
	})

	t.Run("ordinary failure releases reservation and rate refusal converts hold", func(t *testing.T) {
		calls := &phase2Calls{}
		m := phase2AdmissionModel(t, clock, calls)
		m.rateHold = rateHold{err: unknown, manual: true}
		next, cmd := m.onKey(tea.KeyPressMsg{Code: 'r'})
		m = next.(Model)
		probe := cmd().(refreshedMsg)
		probe.err = errors.New("network")
		m = m.onRefreshed(probe)
		if !m.rateHold.manual || m.unknownProbeToken != 0 {
			t.Fatalf("ordinary failed probe changed unknown hold: hold=%+v token=%d", m.rateHold, m.unknownProbeToken)
		}
		if next, detailCmd := m.openDetail(); detailCmd == nil || !next.(Model).detail.loading {
			t.Fatal("ordinary failed probe did not release Detail's global reservation")
		}

		m = phase2AdmissionModel(t, clock, calls)
		m.rateHold = rateHold{err: unknown, manual: true}
		next, cmd = m.onKey(tea.KeyPressMsg{Code: 'r'})
		m = next.(Model)
		probe = cmd().(refreshedMsg)
		reset := clock.now().Add(time.Hour)
		probe.err = provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: reset}, "rate limited")
		m = m.onRefreshed(probe)
		if m.rateHold.manual || !m.rateHold.resetAt.Equal(reset) || m.unknownProbeToken != 0 {
			t.Fatalf("refusing probe did not convert to known hold: hold=%+v token=%d", m.rateHold, m.unknownProbeToken)
		}
	})
}

func TestPhase2FrontierOwnedUnknownProbeSettlementRules(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	unknown := provider.Errorf(provider.KindRateLimit, "reset unknown")
	reset := clock.now().Add(time.Hour)

	tests := []struct {
		name       string
		rawErr     error
		runErr     error
		wantManual bool
		wantReset  time.Time
	}{
		{name: "success"},
		{name: "ordinary failure", runErr: errors.New("transport failed"), wantManual: true},
		{name: "known refusal", rawErr: provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: reset}, "known reset"), wantReset: reset},
		{name: "unknown refusal", rawErr: provider.Errorf(provider.KindRateLimit, "still unknown"), wantManual: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := phase2AdmissionModel(t, clock, &phase2Calls{})
			m.requestPolicyEpoch = 12
			m.rateHold = rateHold{err: unknown, manual: true}
			next, cmd := m.enterFrontier()
			m = next.(Model)
			policy := m.frontierRequestPolicy
			if cmd == nil || policy.ProbeToken == 0 || policy.ProbeToken != m.unknownProbeToken || m.detailFanoutInflight != 1 {
				t.Fatalf("fixture did not dispatch Frontier probe: cmd=%v policy=%+v token=%d slots=%d", cmd, policy, m.unknownProbeToken, m.detailFanoutInflight)
			}
			next, _ = m.onFrontierDetails(frontierDetailsMsg{
				generation:    m.frontierGeneration,
				rawErr:        test.rawErr,
				err:           test.runErr,
				requestPolicy: policy,
			})
			m = next.(Model)
			if m.unknownProbeToken != 0 {
				t.Fatalf("%s did not release matching Frontier probe: token=%d", test.name, m.unknownProbeToken)
			}
			switch {
			case !test.wantReset.IsZero():
				if m.rateHold.manual || !m.rateHold.resetAt.Equal(test.wantReset) || m.rateHold.probeToken != policy.ProbeToken {
					t.Fatalf("known Frontier probe did not convert hold authoritatively: %+v", m.rateHold)
				}
			case test.wantManual:
				if !m.rateHold.manual {
					t.Fatalf("%s cleared unknown hold: %+v", test.name, m.rateHold)
				}
			default:
				if m.rateHold != (rateHold{}) {
					t.Fatalf("successful Frontier probe did not clear unknown hold: %+v", m.rateHold)
				}
			}
		})
	}
}

func TestPhase2StaleUnknownRefusalRetainsButInvalidatesActiveProbe(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	unknown := provider.Errorf(provider.KindRateLimit, "reset unknown")
	m := phase2AdmissionModel(t, clock, &phase2Calls{})
	m.requestPolicyEpoch = 8
	m.rateHold = rateHold{err: unknown, manual: true}
	next, cmd := m.onKey(tea.KeyPressMsg{Code: 'r'})
	m = next.(Model)
	if cmd == nil || m.unknownProbeToken == 0 {
		t.Fatal("fixture did not reserve an unknown probe")
	}
	reserved := m.unknownProbeToken
	m, refused := m.observeRateRefusal(unknown, provider.RequestPolicy{Epoch: m.sourceRequestPolicy.Epoch - 1})
	if !refused || m.unknownProbeToken != reserved {
		t.Fatalf("stale refusal released active reservation: refused=%t token=%d want=%d", refused, m.unknownProbeToken, reserved)
	}
	if next, denied := m.openDetail(); denied != nil || next.(Model).detail.loading {
		t.Fatal("stale refusal admitted a second explicit request")
	}
	m, refused = m.settleTrackerRequest(m.sourceRequestPolicy, nil, true)
	if refused || !m.rateHold.manual || m.unknownProbeToken != 0 {
		t.Fatalf("matching probe success cleared later stale unknown hold: refused=%t hold=%+v token=%d", refused, m.rateHold, m.unknownProbeToken)
	}
}

func TestPhase2ElapsedRefusalAdvancesEpochWithoutInstallingHold(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	m := phase2AdmissionModel(t, clock, &phase2Calls{})
	m.requestPolicyEpoch = 12
	policy := provider.RequestPolicy{Epoch: m.requestPolicyEpoch}
	elapsed := provider.RateLimitErrorf(
		provider.RateLimitMetadata{ResetAt: clock.now().Add(-time.Second)},
		"elapsed refusal",
	)

	m, refused := m.observeRateRefusal(elapsed, policy)
	if !refused || m.requestPolicyEpoch != 13 {
		t.Fatalf("elapsed settlement = refused:%t epoch:%d, want true/13", refused, m.requestPolicyEpoch)
	}
	if m.rateHold != (rateHold{}) || m.unknownProbeToken != 0 {
		t.Fatalf("elapsed refusal installed a hold or probe: hold=%+v token=%d", m.rateHold, m.unknownProbeToken)
	}
	m, next, allowed := m.trackerRequestAdmission(trackerRequestExplicit)
	if !allowed || next != (provider.RequestPolicy{Epoch: 13}) || m.rateHold != (rateHold{}) {
		t.Fatalf("post-elapsed admission = %+v/%t hold=%+v, want normal epoch-13 admission", next, allowed, m.rateHold)
	}
}

func TestPhase2ElapsedListRefusalRemainsVisibleWithoutHold(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	m := phase2AdmissionModel(t, clock, &phase2Calls{})
	m.width, m.height = 120, 24
	m.requestPolicyEpoch = 12
	policy := provider.RequestPolicy{Epoch: m.requestPolicyEpoch}
	elapsed := provider.RateLimitErrorf(
		provider.RateLimitMetadata{ResetAt: clock.now().Add(-time.Second)},
		"elapsed refusal",
	)

	m = m.onRefreshed(refreshedMsg{
		generation:    m.generation,
		err:           elapsed,
		requestPolicy: policy,
	})
	if m.rateHold != (rateHold{}) {
		t.Fatalf("elapsed refusal installed hold %+v", m.rateHold)
	}
	footer := strings.Join(m.footerLines(), "\n")
	if !strings.Contains(footer, "refresh failed: elapsed refusal") ||
		!strings.Contains(footer, "retrying in") {
		t.Fatalf("elapsed refusal footer lost failure or retry guidance:\n%s", footer)
	}
}

func TestPhase2ActiveListRateHoldSuppressesDuplicateLocalFailure(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	m := phase2AdmissionModel(t, clock, &phase2Calls{})
	m.width, m.height = 120, 24
	m.requestPolicyEpoch = 12
	refusal := provider.RateLimitErrorf(
		provider.RateLimitMetadata{ResetAt: clock.now().Add(time.Minute)},
		"active refusal",
	)

	m = m.onRefreshed(refreshedMsg{
		generation:    m.generation,
		err:           refusal,
		requestPolicy: provider.RequestPolicy{Epoch: m.requestPolicyEpoch},
	})
	footer := strings.Join(m.footerLines(), "\n")
	if !strings.Contains(footer, "Tracker requests are held") {
		t.Fatalf("active hold footer lost Tracker-wide policy notice:\n%s", footer)
	}
	if strings.Contains(footer, "refresh failed:") {
		t.Fatalf("active hold footer duplicated local rate failure:\n%s", footer)
	}
}

func TestPhase2ElapsedRefusalSettlesProbeWithoutClearingManualHold(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	unknown := provider.Errorf(provider.KindRateLimit, "reset unknown")
	elapsed := provider.RateLimitErrorf(
		provider.RateLimitMetadata{ResetAt: clock.now()},
		"elapsed refusal",
	)

	t.Run("matching probe", func(t *testing.T) {
		m := phase2AdmissionModel(t, clock, &phase2Calls{})
		m.requestPolicyEpoch = 9
		m.rateHold = rateHold{err: unknown, manual: true}
		m, probe, allowed := m.trackerRequestAdmission(trackerRequestExplicit)
		if !allowed || probe.ProbeToken == 0 {
			t.Fatal("fixture did not reserve an unknown probe")
		}
		m, refused := m.settleTrackerRequest(probe, elapsed, false)
		if !refused || m.requestPolicyEpoch != 10 || m.unknownProbeToken != 0 || !m.rateHold.manual {
			t.Fatalf("probe settlement = refused:%t epoch:%d token:%d hold:%+v", refused, m.requestPolicyEpoch, m.unknownProbeToken, m.rateHold)
		}
	})

	t.Run("stale tokenless response during probe", func(t *testing.T) {
		m := phase2AdmissionModel(t, clock, &phase2Calls{})
		m.requestPolicyEpoch = 11
		m.rateHold = rateHold{err: unknown, manual: true}
		m, probe, allowed := m.trackerRequestAdmission(trackerRequestExplicit)
		if !allowed || probe.ProbeToken == 0 {
			t.Fatal("fixture did not reserve an unknown probe")
		}
		reserved := m.unknownProbeToken
		stale := provider.RequestPolicy{Epoch: probe.Epoch}
		m, refused := m.settleTrackerRequest(stale, elapsed, false)
		if !refused || m.requestPolicyEpoch != 12 || m.unknownProbeToken != reserved || !m.rateHold.manual {
			t.Fatalf("stale elapsed settlement = refused:%t epoch:%d token:%d hold:%+v", refused, m.requestPolicyEpoch, m.unknownProbeToken, m.rateHold)
		}
		if _, _, secondAllowed := m.trackerRequestAdmission(trackerRequestExplicit); secondAllowed {
			t.Fatal("stale elapsed refusal admitted an overlapping explicit probe")
		}
	})
}

func TestPhase2JiraBarrierRecoversWhenResetExpiresBeforeSettlement(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	reset := clock.now().Add(time.Second)
	var catalogueCalls, issueCalls, commentCalls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/2/issueLinkType":
			if catalogueCalls.Add(1) == 1 {
				w.Header().Set("X-RateLimit-Reset", reset.Format(time.RFC3339))
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"issueLinkTypes":[]}`))
		case "/rest/api/2/issue/ABC-12":
			issueCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"1","key":"ABC-12","fields":{}}`))
		case "/rest/api/2/issue/ABC-12/comment":
			commentCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	tracker := jira.New("example.test",
		jira.WithBaseURL(s.URL),
		jira.WithCredentials(jira.Credentials{Email: "test@example.test", Token: "token"}),
		jira.WithNow(clock.now),
	)
	m := phase2AdmissionModel(t, clock, &phase2Calls{})
	m.requestPolicyEpoch = 4
	m, firstPolicy, allowed := m.trackerRequestAdmission(trackerRequestExplicit)
	if !allowed {
		t.Fatal("initial Jira Detail request was not admitted")
	}
	firstContext := provider.WithRequestPolicy(t.Context(), firstPolicy)
	_, firstErr := tracker.FetchDetails(firstContext, []model.TicketID{"ABC-12"})
	if refusal, ok := provider.InspectRateLimitRefusal(firstErr, clock.now()); !ok || !refusal.KnownReset || !refusal.ResetAt.Equal(reset) {
		t.Fatalf("initial Jira refusal = %+v/%t from %v", refusal, ok, firstErr)
	}
	if catalogueCalls.Load() != 1 || issueCalls.Load() != 0 || commentCalls.Load() != 0 {
		t.Fatalf("calls before settlement = %d/%d/%d, want 1/0/0", catalogueCalls.Load(), issueCalls.Load(), commentCalls.Load())
	}

	clock.value = reset.Add(time.Second)
	m, refused := m.settleTrackerRequest(firstPolicy, firstErr, false)
	if !refused || m.requestPolicyEpoch != 5 || m.rateHold != (rateHold{}) {
		t.Fatalf("delayed settlement = refused:%t epoch:%d hold:%+v, want true/5/no hold", refused, m.requestPolicyEpoch, m.rateHold)
	}
	m, retryPolicy, allowed := m.trackerRequestAdmission(trackerRequestExplicit)
	if !allowed || retryPolicy != (provider.RequestPolicy{Epoch: 5}) {
		t.Fatalf("retry admission = %+v/%t, want epoch 5", retryPolicy, allowed)
	}
	retryContext := provider.WithRequestPolicy(t.Context(), retryPolicy)
	details, retryErr := tracker.FetchDetails(retryContext, []model.TicketID{"ABC-12"})
	if retryErr != nil || details["ABC-12"].TicketID != "ABC-12" {
		t.Fatalf("Jira retry = details:%+v error:%v, want one successful Detail", details, retryErr)
	}
	if catalogueCalls.Load() != 2 || issueCalls.Load() != 1 || commentCalls.Load() != 1 {
		t.Fatalf("calls after retry = %d/%d/%d, want exactly 2/1/1", catalogueCalls.Load(), issueCalls.Load(), commentCalls.Load())
	}
}

func TestPhase2TokenlessRefusalSettlesPolicyBeforeActiveProbe(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	unknown := provider.Errorf(provider.KindRateLimit, "reset unknown")
	for _, metadata := range []struct {
		name      string
		resetAt   time.Time
		wantKnown bool
		unknown   bool
	}{
		{name: "future reset", resetAt: clock.now().Add(time.Hour), wantKnown: true},
		{name: "unknown reset", unknown: true},
		{name: "current reset", resetAt: clock.now()},
		{name: "expired reset", resetAt: clock.now().Add(-time.Second)},
	} {
		for _, staleFirst := range []bool{true, false} {
			name := metadata.name + "/probe success first"
			if staleFirst {
				name = metadata.name + "/tokenless refusal first"
			}
			t.Run(name, func(t *testing.T) {
				m := phase2AdmissionModel(t, clock, &phase2Calls{})
				m.rateHold = rateHold{err: unknown, manual: true}
				m, probe, allowed := m.trackerRequestAdmission(trackerRequestExplicit)
				if !allowed || probe.ProbeToken == 0 {
					t.Fatal("fixture did not reserve the active unknown probe")
				}
				stale := provider.RequestPolicy{Epoch: probe.Epoch}
				refusal := provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: metadata.resetAt}, "rate limited")
				if metadata.unknown {
					refusal = unknown
				}
				if staleFirst {
					m, _ = m.settleTrackerRequest(stale, refusal, false)
					m, _ = m.settleTrackerRequest(probe, nil, true)
				} else {
					m, _ = m.settleTrackerRequest(probe, nil, true)
					m, _ = m.settleTrackerRequest(stale, refusal, false)
				}
				if metadata.wantKnown && !staleFirst {
					if m.rateHold.manual || !m.rateHold.resetAt.Equal(metadata.resetAt) || m.unknownProbeToken != 0 {
						t.Fatalf("future tokenless refusal after probe settlement did not install known policy: hold=%+v token=%d", m.rateHold, m.unknownProbeToken)
					}
				} else if metadata.wantKnown {
					if m.rateHold != (rateHold{}) || m.unknownProbeToken != 0 {
						t.Fatalf("pre-settlement tokenless known refusal overrode the unknown hold: hold=%+v token=%d", m.rateHold, m.unknownProbeToken)
					}
				} else if metadata.unknown {
					if !m.rateHold.manual || m.unknownProbeToken != 0 {
						t.Fatalf("newer tokenless unknown refusal was cleared by active probe: hold=%+v token=%d", m.rateHold, m.unknownProbeToken)
					}
				} else if m.rateHold != (rateHold{}) || m.unknownProbeToken != 0 {
					t.Fatalf("elapsed tokenless refusal installed policy: hold=%+v token=%d", m.rateHold, m.unknownProbeToken)
				}
			})
		}
	}

}

func TestPhase2ConcurrentUnknownRefusalDominatesKnownInBothResponseOrders(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	unknown := provider.Errorf(provider.KindRateLimit, "reset unknown")
	reset := clock.now().Add(time.Hour)
	known := provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: reset}, "known reset")

	for _, unknownFirst := range []bool{false, true} {
		name := "known then unknown"
		if unknownFirst {
			name = "unknown then known"
		}
		t.Run(name, func(t *testing.T) {
			m := phase2AdmissionModel(t, clock, &phase2Calls{})
			m, knownPolicy, allowed := m.trackerRequestAdmission(trackerRequestExplicit)
			if !allowed || knownPolicy.ProbeToken != 0 {
				t.Fatal("fixture did not admit pre-hold known request")
			}
			m, unknownPolicy, allowed := m.trackerRequestAdmission(trackerRequestExplicit)
			if !allowed || unknownPolicy != knownPolicy {
				t.Fatal("fixture did not admit concurrent pre-hold unknown request")
			}
			if unknownFirst {
				m, _ = m.settleTrackerRequest(unknownPolicy, unknown, false)
				m, _ = m.settleTrackerRequest(knownPolicy, known, false)
			} else {
				m, _ = m.settleTrackerRequest(knownPolicy, known, false)
				m, _ = m.settleTrackerRequest(unknownPolicy, unknown, false)
			}
			if !m.rateHold.manual || m.rateHold.resetAt != (time.Time{}) {
				t.Fatalf("%s did not retain conservative unknown hold: %+v", name, m.rateHold)
			}
		})
	}
}

func TestPhase2ReservedProbeKnownRefusalDominatesPreHoldUnknownInBothResponseOrders(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	unknown := provider.Errorf(provider.KindRateLimit, "reset unknown")
	reset := clock.now().Add(time.Hour)
	known := provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: reset}, "known reset")

	for _, unknownFirst := range []bool{false, true} {
		name := "probe known then pre-hold unknown"
		if unknownFirst {
			name = "pre-hold unknown then probe known"
		}
		t.Run(name, func(t *testing.T) {
			m := phase2AdmissionModel(t, clock, &phase2Calls{})
			m.requestPolicyEpoch = 9
			m.rateHold = rateHold{err: unknown, manual: true}
			m, probe, allowed := m.trackerRequestAdmission(trackerRequestExplicit)
			if !allowed || probe.ProbeToken == 0 {
				t.Fatal("fixture did not reserve explicit probe")
			}
			preHold := provider.RequestPolicy{Epoch: probe.Epoch}
			if unknownFirst {
				m, _ = m.settleTrackerRequest(preHold, unknown, false)
				m, _ = m.settleTrackerRequest(probe, known, false)
			} else {
				m, _ = m.settleTrackerRequest(probe, known, false)
				m, _ = m.settleTrackerRequest(preHold, unknown, false)
			}
			if m.rateHold.manual || !m.rateHold.resetAt.Equal(reset) ||
				m.rateHold.probeToken != probe.ProbeToken || m.rateHold.probeEpoch != probe.Epoch ||
				m.unknownProbeToken != 0 {
				t.Fatalf("%s lost authoritative probe reset: hold=%+v token=%d", name, m.rateHold, m.unknownProbeToken)
			}
		})
	}
}

func TestPhase2AdmittedPolicyReachesEveryTrackerCallback(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	unknown := provider.Errorf(provider.KindRateLimit, "reset unknown")

	assertProbe := func(t *testing.T, got provider.RequestPolicy, want provider.RequestPolicy) {
		t.Helper()
		if got != want || got.Epoch == 0 || got.ProbeToken == 0 {
			t.Fatalf("callback policy = %+v, want admitted unknown probe %+v", got, want)
		}
	}

	t.Run("Source", func(t *testing.T) {
		m := phase2AdmissionModel(t, clock, &phase2Calls{})
		m.requestPolicyEpoch = 7
		m.rateHold = rateHold{err: unknown, manual: true}
		var got provider.RequestPolicy
		m.fetch = func(ctx context.Context) (ListInput, error) {
			got = provider.RequestPolicyFromContext(ctx)
			return m.input, nil
		}

		next, cmd := m.onKey(tea.KeyPressMsg{Code: 'r'})
		m = next.(Model)
		if cmd == nil {
			t.Fatal("unknown-hold Source probe was not admitted")
		}
		_ = cmd()
		assertProbe(t, got, m.sourceRequestPolicy)
	})

	t.Run("singular Detail", func(t *testing.T) {
		m := phase2AdmissionModel(t, clock, &phase2Calls{})
		m.requestPolicyEpoch = 11
		m.rateHold = rateHold{err: unknown, manual: true}
		var got provider.RequestPolicy
		m.fetchDetail = func(ctx context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
			got = provider.RequestPolicyFromContext(ctx)
			return model.Detail{TicketID: id}, m.input.Capabilities, nil
		}

		next, cmd := m.openDetail()
		m = next.(Model)
		if cmd == nil {
			t.Fatal("unknown-hold Detail probe was not admitted")
		}
		_ = cmd()
		assertProbe(t, got, m.detailRequestPolicy)
	})

	t.Run("plural Frontier", func(t *testing.T) {
		m := phase2AdmissionModel(t, clock, &phase2Calls{})
		m.requestPolicyEpoch = 13
		m.rateHold = rateHold{err: unknown, manual: true}
		var got provider.RequestPolicy
		m.fetchDetails = func(ctx context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
			got = provider.RequestPolicyFromContext(ctx)
			details := make(map[model.TicketID]model.Detail, len(ids))
			for _, id := range ids {
				details[id] = model.Detail{TicketID: id}
			}
			return details, m.input.Capabilities, nil
		}

		next, cmd := m.enterFrontier()
		m = next.(Model)
		if cmd == nil {
			t.Fatal("unknown-hold Frontier probe was not admitted")
		}
		phase2RunCommand(cmd)
		assertProbe(t, got, m.frontierRequestPolicy)
	})
}

func TestPhase2StaleRefusalChangesPolicyWithoutChangingReplacementSeat(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	calls := &phase2Calls{}
	m := phase2AdmissionModel(t, clock, calls)

	first, firstCmd := m.openDetail()
	m = first.(Model)
	if firstCmd == nil || !m.detail.loading {
		t.Fatal("fixture did not start first Detail read")
	}
	oldGeneration, oldPolicy := m.detailGeneration, m.detailRequestPolicy
	second, secondCmd := m.seatDetail(model.Ticket{ID: "B", Key: "B", Title: "second", Status: model.StatusTodo}, Header{}, m.input.Capabilities, true)
	m = second.(Model)
	if secondCmd == nil || m.detail.ticket.ID != "B" || !m.detail.loading {
		t.Fatal("fixture did not replace Detail seat")
	}

	reset := clock.now().Add(time.Hour)
	m = m.onDetailFetched(detailFetchedMsg{
		generation:    oldGeneration,
		id:            "A",
		err:           provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: reset}, "rate limited"),
		requestPolicy: oldPolicy,
	})
	if !m.rateHold.resetAt.Equal(reset) || m.rateHold.manual {
		t.Fatalf("stale refusal did not install known hold: %+v", m.rateHold)
	}
	if m.detail.ticket.ID != "B" || !m.detail.loading {
		t.Fatalf("stale refusal changed replacement seat: ticket=%q loading=%t", m.detail.ticket.ID, m.detail.loading)
	}
	if _, cached := m.details["A"]; cached {
		t.Fatal("stale refusal cached the abandoned Detail response")
	}
}

func TestPhase2PreHoldSuccessCannotClearKnownHold(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	calls := &phase2Calls{}
	m := phase2AdmissionModel(t, clock, calls)
	next, cmd := m.openDetail()
	m = next.(Model)
	if cmd == nil {
		t.Fatal("fixture did not start pre-hold Detail read")
	}
	policy := m.detailRequestPolicy
	reset := clock.now().Add(time.Hour)
	m, refused := m.observeRateRefusal(provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: reset}, "later refusal"), provider.RequestPolicy{})
	if !refused {
		t.Fatal("fixture did not install known hold")
	}

	m = m.onDetailFetched(detailFetchedMsg{
		generation:    m.detailGeneration,
		id:            m.detail.ticket.ID,
		detail:        model.Detail{TicketID: m.detail.ticket.ID},
		caps:          m.input.Capabilities,
		requestPolicy: policy,
	})
	if !m.rateHold.resetAt.Equal(reset) || m.rateHold.manual {
		t.Fatalf("pre-hold success cleared or weakened known hold: %+v", m.rateHold)
	}
}

func TestPhase2ExhaustedBlocksEveryLaneWhileLowBudgetBlocksOnlyAutomaticList(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	reset := clock.now().Add(time.Hour)

	t.Run("exhausted", func(t *testing.T) {
		calls := &phase2Calls{}
		m := phase2AdmissionModel(t, clock, calls)
		m = m.applySuccessfulBudgetPolicy(policyReading(reset, 0), provider.RequestPolicy{Epoch: m.requestPolicyEpoch})
		if !m.rateHold.exhausted {
			t.Fatal("fixture did not install exhausted hold")
		}
		if next, cmd := m.onKey(tea.KeyPressMsg{Code: 'r'}); cmd != nil || next.(Model).refreshing {
			t.Fatal("exhausted budget admitted List work")
		}
		if next, cmd := m.openDetail(); cmd != nil || next.(Model).detail.loading {
			t.Fatal("exhausted budget admitted Detail work")
		}
		if next, cmd := m.enterFrontier(); next.(Model).frontierContext != nil || next.(Model).detailFanoutInflight != 0 {
			t.Fatal("exhausted budget admitted Frontier work")
		} else {
			phase2RunCommand(cmd)
		}
		if calls.fanout.Load() != 0 {
			t.Fatalf("exhausted budget made %d Frontier calls", calls.fanout.Load())
		}
	})

	t.Run("low budget", func(t *testing.T) {
		calls := &phase2Calls{}
		m := phase2AdmissionModel(t, clock, calls)
		m = m.applySuccessfulBudgetPolicy(policyReading(reset, 3), provider.RequestPolicy{Epoch: m.requestPolicyEpoch})
		if !m.lowBudget.active(clock.now()) {
			t.Fatal("fixture did not install low-budget cadence")
		}
		if _, allowed, _ := m.automaticRefreshAdmission(); allowed {
			t.Fatal("low budget admitted automatic List work before widened cadence")
		}
		if next, cmd := m.openDetail(); cmd == nil || !next.(Model).detail.loading {
			t.Fatal("low positive budget denied explicit Detail work")
		}
		m = phase2AdmissionModel(t, clock, calls)
		m = m.applySuccessfulBudgetPolicy(policyReading(reset, 3), provider.RequestPolicy{Epoch: m.requestPolicyEpoch})
		if next, cmd := m.enterFrontier(); cmd == nil || next.(Model).frontierContext == nil {
			t.Fatal("low positive budget denied explicit Frontier work")
		}
	})
}

func TestPhase2FrontierRawOutcomeAndRunRefusalsBeatStaleGuard(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	refusal := provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: clock.now().Add(time.Hour)}, "rate limited")
	for _, tc := range []struct {
		name string
		msg  frontierDetailsMsg
	}{
		{name: "raw Provider error", msg: frontierDetailsMsg{rawErr: refusal}},
		{name: "outcome-only error", msg: frontierDetailsMsg{outcomes: []detailfanout.Outcome{{ID: "A", Err: refusal}}}},
		{name: "Run control-flow error", msg: frontierDetailsMsg{err: refusal}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := &phase2Calls{}
			m := phase2AdmissionModel(t, clock, calls)
			m.mode = modeFrontier
			m.frontierGeneration = 9
			m.detailFanoutInflight = 1
			msg := tc.msg
			msg.generation = 8 // a cancelled/replaced Frontier seat
			next, _ := m.onFrontierDetails(msg)
			m = next.(Model)
			if m.rateHold.manual || !m.rateHold.resetAt.Equal(clock.now().Add(time.Hour)) {
				t.Fatalf("stale %s did not install known tracker hold: %+v", tc.name, m.rateHold)
			}
			if m.frontierGeneration != 9 {
				t.Fatal("stale Frontier refusal mutated replacement generation")
			}
		})
	}
}

func TestPhase2StaleFrontierRefusalPreservesSuccessfulSibling(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	m := phase2AdmissionModel(t, clock, &phase2Calls{})
	m.mode = modeFrontier
	m.frontierGeneration = 9
	m.detailFanoutInflight = 1
	before := m.frontier
	reset := clock.now().Add(time.Hour)
	refusal := provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: reset}, "rate limited")

	next, _ := m.onFrontierDetails(frontierDetailsMsg{
		generation: 8,
		outcomes: []detailfanout.Outcome{{
			ID: "A", Detail: model.Detail{TicketID: "A"}, Caps: m.input.Capabilities,
		}},
		rawErr:        refusal,
		requestPolicy: provider.RequestPolicy{Epoch: m.requestPolicyEpoch},
	})
	m = next.(Model)

	if m.rateHold.manual || !m.rateHold.resetAt.Equal(reset) {
		t.Fatalf("stale refusal did not install known hold: %+v", m.rateHold)
	}
	if !m.haveDetail("A") {
		t.Fatal("stale refused batch discarded its successful sibling")
	}
	if m.frontierGeneration != 9 || m.frontier.done != before.done ||
		len(m.frontier.failed) != len(before.failed) || m.frontier.lastErr != nil {
		t.Fatalf("stale batch mutated current seat: generation=%d done=%d failed=%v last=%v",
			m.frontierGeneration, m.frontier.done, m.frontier.failed, m.frontier.lastErr)
	}
	if m.detailFanoutInflight != 0 {
		t.Fatalf("stale completion leaked shared slot: %d", m.detailFanoutInflight)
	}
}

func TestPhase2CancellationRacingFrontierRefusalPreservesCompletedSiblings(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	m := phase2AdmissionModel(t, clock, &phase2Calls{})
	next, cmd := m.enterFrontier()
	m = next.(Model)
	if cmd == nil || m.detailFanoutInflight != 1 || m.frontier.planned != 2 {
		t.Fatalf("fixture did not dispatch two-ID Frontier plan: cmd=%v slots=%d planned=%d", cmd, m.detailFanoutInflight, m.frontier.planned)
	}
	policy := m.frontierRequestPolicy
	reset := clock.now().Add(time.Hour)
	refusal := provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: reset}, "rate limited while cancelling")
	next, _ = m.onFrontierDetails(frontierDetailsMsg{
		generation: m.frontierGeneration,
		outcomes: []detailfanout.Outcome{{
			ID: "A", Detail: model.Detail{TicketID: "A"}, Caps: m.input.Capabilities,
		}},
		rawErr:        errors.Join(context.Canceled, refusal),
		err:           context.Canceled,
		requestPolicy: policy,
	})
	m = next.(Model)
	if m.rateHold.manual || !m.rateHold.resetAt.Equal(reset) {
		t.Fatalf("cancellation-racing raw refusal did not install known hold: %+v", m.rateHold)
	}
	if _, seated := m.frontier.input.Links["A"]; !seated {
		t.Fatal("completed sibling A was not preserved")
	}
	if _, cached := m.details["A"]; !cached {
		t.Fatal("completed sibling A was not cached")
	}
	if _, seated := m.frontier.input.Links["B"]; seated || len(m.frontier.failed) != 0 ||
		len(m.frontier.failureErrors) != 0 || m.frontier.lastErr != nil {
		t.Fatalf("cancellation fabricated failure evidence: links=%v failed=%v errors=%v last=%v",
			m.frontier.input.Links, m.frontier.failed, m.frontier.failureErrors, m.frontier.lastErr)
	}
	if m.detailFanoutInflight != 0 {
		t.Fatalf("cancellation-racing completion leaked shared slot: %d", m.detailFanoutInflight)
	}
}

func TestPhase2QueuedPluralInvalidationDoesNotMutateSlots(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	calls := &phase2Calls{}
	m := phase2AdmissionModel(t, clock, calls)
	m.mode = modeFrontier
	m.frontier.queued = []model.TicketID{"A"}
	m.frontier.planned = 1
	m.frontierAdmissionReady = true
	m.frontierRequestPolicy = provider.RequestPolicy{Epoch: m.requestPolicyEpoch}
	m.detailFanoutInflight = 1 // a retired command still owns this shared slot.

	refusal := provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: clock.now().Add(time.Hour)}, "rate limited")
	m, refused := m.settleTrackerRequest(provider.RequestPolicy{Epoch: m.requestPolicyEpoch}, refusal, false)
	if !refused {
		t.Fatal("fixture did not advance policy epoch")
	}
	beforeSlots := m.detailFanoutInflight
	next, _ := m.issueFrontierFetches()
	m = next.(Model)
	if m.detailFanoutInflight != beforeSlots || len(m.frontier.queued) != 0 || m.frontierAdmissionReady || m.frontierContext != nil {
		t.Fatalf("policy-invalidated queue changed slot or retained work: slots=%d queued=%v ready=%t context=%v",
			m.detailFanoutInflight, m.frontier.queued, m.frontierAdmissionReady, m.frontierContext)
	}
}

func TestPhase2QueuedUndispatchedFrontierProbeReleasesOnlyWhenAbandoned(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	unknown := provider.Errorf(provider.KindRateLimit, "reset unknown")
	queued := func(t *testing.T, slots int) (Model, provider.RequestPolicy, tea.Cmd) {
		t.Helper()
		calls := &phase2Calls{}
		m := phase2AdmissionModel(t, clock, calls)
		m.requestPolicyEpoch = 7
		m.rateHold = rateHold{err: unknown, manual: true}
		m.detailFanoutInflight = slots
		next, cmd := m.enterFrontier()
		m = next.(Model)
		policy := m.frontierRequestPolicy
		if policy.ProbeToken == 0 || policy.ProbeToken != m.unknownProbeToken {
			t.Fatalf("Frontier did not reserve unknown probe: policy=%+v token=%d", policy, m.unknownProbeToken)
		}
		if calls.fanout.Load() != 0 {
			t.Fatalf("setup synchronously called fanout %d times", calls.fanout.Load())
		}
		return m, policy, cmd
	}

	t.Run("retirement releases queued reservation", func(t *testing.T) {
		m, policy, cmd := queued(t, detailfanout.Parallelism)
		phase2RunCommand(cmd)
		if !m.frontierAdmissionReady || len(m.frontier.queued) == 0 || m.detailFanoutInflight != detailfanout.Parallelism {
			t.Fatalf("probe was not held in queue: ready=%t queued=%v slots=%d", m.frontierAdmissionReady, m.frontier.queued, m.detailFanoutInflight)
		}
		if _, _, allowed := m.trackerRequestAdmission(trackerRequestExplicit); allowed {
			t.Fatal("queued probe admitted a second explicit request")
		}
		m = m.retireFrontierFanout()
		if !m.rateHold.manual || m.unknownProbeToken != 0 || m.detailFanoutInflight != detailfanout.Parallelism {
			t.Fatalf("retirement leaked probe or changed slots: hold=%+v token=%d slots=%d", m.rateHold, m.unknownProbeToken, m.detailFanoutInflight)
		}
		m, replacement, allowed := m.trackerRequestAdmission(trackerRequestExplicit)
		if !allowed || replacement.ProbeToken == 0 || replacement.ProbeToken == policy.ProbeToken {
			t.Fatalf("retired queue did not permit exactly one replacement probe: allowed=%t policy=%+v", allowed, replacement)
		}
	})

	t.Run("policy invalidation releases queued reservation", func(t *testing.T) {
		m, policy, _ := queued(t, detailfanout.Parallelism)
		reset := clock.now().Add(time.Hour)
		refusal := provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: reset}, "known reset")
		m, _ = m.settleTrackerRequest(provider.RequestPolicy{Epoch: policy.Epoch}, refusal, false)
		if m.unknownProbeToken != policy.ProbeToken {
			t.Fatal("tokenless refusal released queued probe before its queue was abandoned")
		}
		// A retired command releases one shared slot; the queued generation can now
		// observe that its admission epoch was invalidated before dispatch.
		m.detailFanoutInflight--
		beforeSlots := m.detailFanoutInflight
		next, _ := m.issueFrontierFetches()
		m = next.(Model)
		if m.unknownProbeToken != 0 || m.frontierAdmissionReady || len(m.frontier.queued) != 0 ||
			m.frontierContext != nil || m.detailFanoutInflight != beforeSlots {
			t.Fatalf("invalidated queue leaked reservation or slot: token=%d ready=%t queued=%v context=%v slots=%d/%d",
				m.unknownProbeToken, m.frontierAdmissionReady, m.frontier.queued, m.frontierContext, m.detailFanoutInflight, beforeSlots)
		}
	})

	t.Run("retirement retains dispatched reservation until completion", func(t *testing.T) {
		m, policy, cmd := queued(t, detailfanout.Parallelism-1)
		if cmd == nil || m.frontierAdmissionReady || len(m.frontier.queued) != 0 || m.detailFanoutInflight != detailfanout.Parallelism {
			t.Fatalf("probe did not dispatch: cmd=%v ready=%t queued=%v slots=%d", cmd, m.frontierAdmissionReady, m.frontier.queued, m.detailFanoutInflight)
		}
		m = m.retireFrontierFanout()
		if m.unknownProbeToken != policy.ProbeToken {
			t.Fatal("retirement released a dispatched probe before its completion")
		}
		m, _ = m.settleTrackerRequest(policy, errors.New("transport failed"), false)
		if m.unknownProbeToken != 0 || !m.rateHold.manual {
			t.Fatalf("matching dispatched completion did not release reservation: hold=%+v token=%d", m.rateHold, m.unknownProbeToken)
		}
	})
}

func TestPhase2DenialLeavesSameSeatButRetiresReplacement(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	calls := &phase2Calls{}
	m := phase2AdmissionModel(t, clock, calls)
	m.rateHold = phase2KnownHold(clock)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m.mode = modeDetail
	m.detail = detailState{ticket: model.Ticket{ID: "A", Key: "A"}}
	m.detailGeneration = 7
	m.detailContext, m.cancelDetail = ctx, cancel
	m.detailRequestPolicy = provider.RequestPolicy{Epoch: 3}
	unchanged, cmd := m.startDetailFetch()
	if cmd != nil || unchanged.detailGeneration != 7 || unchanged.detailContext != ctx || unchanged.detail.loading {
		t.Fatalf("denied same-seat retry changed state: generation=%d context=%v loading=%t cmd=%v",
			unchanged.detailGeneration, unchanged.detailContext, unchanged.detail.loading, cmd)
	}
	select {
	case <-ctx.Done():
		t.Fatal("denied same-seat retry cancelled live Detail context")
	default:
	}

	nextSeat, cmd := m.seatDetail(model.Ticket{ID: "B", Key: "B"}, Header{}, m.input.Capabilities, true)
	replacement := nextSeat.(Model)
	if cmd != nil || replacement.detailGeneration != 8 || replacement.detailContext != nil || replacement.detail.loading || replacement.detail.ticket.ID != "B" {
		t.Fatalf("denied replacement did not retire and reseat: generation=%d context=%v loading=%t ticket=%q cmd=%v",
			replacement.detailGeneration, replacement.detailContext, replacement.detail.loading, replacement.detail.ticket.ID, cmd)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("denied replacement left old Detail context live")
	}
}

func TestPhase2ExhaustedListBudgetCannotClearCrossScreenUnknownProbe(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	unknown := provider.Errorf(provider.KindRateLimit, "reset unknown")
	reset := clock.now().Add(time.Hour)

	lanes := []struct {
		name  string
		start func(Model) (Model, provider.RequestPolicy)
	}{
		{
			name: "Detail",
			start: func(m Model) (Model, provider.RequestPolicy) {
				next, cmd := m.openDetail()
				m = next.(Model)
				if cmd == nil || !m.detail.loading {
					t.Fatal("fixture did not start Detail unknown probe")
				}
				return m, m.detailRequestPolicy
			},
		},
		{
			name: "Frontier",
			start: func(m Model) (Model, provider.RequestPolicy) {
				next, cmd := m.enterFrontier()
				m = next.(Model)
				if cmd == nil || m.frontierContext == nil {
					t.Fatal("fixture did not start Frontier unknown probe")
				}
				return m, m.frontierRequestPolicy
			},
		},
	}

	for _, lane := range lanes {
		for _, stale := range []bool{false, true} {
			for _, budgetFirst := range []bool{false, true} {
				name := lane.name + "/current List/probe first"
				if stale {
					name = lane.name + "/stale List/probe first"
				}
				if budgetFirst {
					name = name[:len(name)-len("probe first")] + "budget first"
				}
				t.Run(name, func(t *testing.T) {
					m := phase2AdmissionModel(t, clock, &phase2Calls{})
					m.requestPolicyEpoch = 10
					m.rateHold = rateHold{err: unknown, manual: true}
					m, probe := lane.start(m)
					listPolicy := provider.RequestPolicy{Epoch: probe.Epoch}
					generation := m.generation
					if stale {
						generation--
					}
					budget := refreshedMsg{
						generation:    generation,
						input:         policyReading(reset, 0),
						requestPolicy: listPolicy,
					}

					if budgetFirst {
						m = m.onRefreshed(budget)
						if !m.rateHold.exhausted || m.unknownProbeToken != probe.ProbeToken {
							t.Fatalf("List exhausted budget changed active %s probe: hold=%+v token=%d want=%d", lane.name, m.rateHold, m.unknownProbeToken, probe.ProbeToken)
						}
						m, _ = m.settleTrackerRequest(probe, nil, true)
					} else {
						m, _ = m.settleTrackerRequest(probe, nil, true)
						m = m.onRefreshed(budget)
					}
					if !m.rateHold.exhausted || !m.rateHold.resetAt.Equal(reset) || m.unknownProbeToken != 0 {
						t.Fatalf("List exhausted budget was weakened after %s probe settlement: hold=%+v token=%d", lane.name, m.rateHold, m.unknownProbeToken)
					}
				})
			}
		}

		t.Run(lane.name+"/pre-hold List success", func(t *testing.T) {
			m := phase2AdmissionModel(t, clock, &phase2Calls{})
			m.requestPolicyEpoch = 10
			m.rateHold = rateHold{err: unknown, manual: true}
			m, probe := lane.start(m)
			m = m.onRefreshed(refreshedMsg{
				generation:    m.generation,
				input:         policyReading(reset, 0),
				requestPolicy: provider.RequestPolicy{Epoch: probe.Epoch - 1},
			})
			if !m.rateHold.exhausted || m.unknownProbeToken != probe.ProbeToken {
				t.Fatalf("pre-hold List success cleared active %s probe: hold=%+v token=%d want=%d", lane.name, m.rateHold, m.unknownProbeToken, probe.ProbeToken)
			}
			m, _ = m.settleTrackerRequest(probe, nil, true)
			if !m.rateHold.exhausted || m.unknownProbeToken != 0 {
				t.Fatalf("pre-hold List success weakened exhausted policy after %s probe: hold=%+v token=%d", lane.name, m.rateHold, m.unknownProbeToken)
			}
		})
	}
}

func TestPhase2StartupInitialErrorAndBudgetInstallAdmissionPolicy(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}

	t.Run("InitialError prevents duplicate startup fetch", func(t *testing.T) {
		var sourceCalls atomic.Int32
		m := New(t.Context(), Options{
			Interval:  time.Minute,
			Now:       clock.now,
			Heartbeat: func() tea.Msg { return nil },
			Source: func(context.Context) (ListInput, error) {
				sourceCalls.Add(1)
				return ListInput{}, nil
			},
			InitialError: provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: clock.now().Add(time.Hour)}, "startup rate limited"),
		})
		if m.refreshing || !m.rateHold.active(clock.now()) {
			t.Fatalf("InitialError did not install a startup hold: refreshing=%t hold=%+v", m.refreshing, m.rateHold)
		}
		phase2RunCommand(m.Init())
		if sourceCalls.Load() != 0 {
			t.Fatalf("startup hold issued %d duplicate Source calls", sourceCalls.Load())
		}
	})

	t.Run("initial exhausted budget blocks all requests", func(t *testing.T) {
		calls := &phase2Calls{}
		initial := ListInput{
			Tickets:         []model.Ticket{{ID: "A", Key: "A"}},
			Capabilities:    model.Capabilities{BlockingLinks: true, RateLimitBudget: true},
			RateLimitBudget: model.RateLimitBudget{Remaining: 0, ResetsAt: clock.now().Add(time.Hour)},
			FetchedAt:       clock.now(),
		}
		m := New(t.Context(), Options{
			Initial:   &initial,
			Interval:  time.Minute,
			Now:       clock.now,
			Heartbeat: func() tea.Msg { return nil },
			Source:    func(context.Context) (ListInput, error) { calls.source.Add(1); return initial, nil },
			DetailSource: func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
				calls.detail.Add(1)
				return model.Detail{}, initial.Capabilities, nil
			},
			DetailFanout: func(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
				calls.fanout.Add(1)
				return nil, initial.Capabilities, nil
			},
		})
		if !m.rateHold.exhausted {
			t.Fatal("initial exhausted budget did not install hard hold")
		}
		if next, allowed, _ := m.automaticRefreshAdmission(); allowed || !next.rateHold.exhausted {
			t.Fatal("initial exhausted budget admitted automatic Source work")
		}
		if next, cmd := m.openDetail(); cmd != nil || next.(Model).detail.loading {
			t.Fatal("initial exhausted budget admitted Detail work")
		}
		if next, cmd := m.enterFrontier(); next.(Model).frontierContext != nil || next.(Model).detailFanoutInflight != 0 {
			t.Fatal("initial exhausted budget admitted Frontier work")
		} else {
			phase2RunCommand(cmd)
		}
		if calls.source.Load() != 0 || calls.detail.Load() != 0 || calls.fanout.Load() != 0 {
			t.Fatalf("initial exhausted budget called provider: source=%d detail=%d fanout=%d", calls.source.Load(), calls.detail.Load(), calls.fanout.Load())
		}
	})
}

func TestPhase2StaleSourceRefusalAndBothPreHoldOrdersPreservePolicy(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	refusal := provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: clock.now().Add(time.Hour)}, "rate limited")

	t.Run("stale Source refusal", func(t *testing.T) {
		m := phase2AdmissionModel(t, clock, &phase2Calls{})
		m.generation = 9
		m = m.onRefreshed(refreshedMsg{generation: 8, err: refusal, requestPolicy: provider.RequestPolicy{Epoch: m.requestPolicyEpoch}})
		if m.rateHold.manual || !m.rateHold.resetAt.Equal(clock.now().Add(time.Hour)) {
			t.Fatalf("stale Source refusal did not install tracker policy: %+v", m.rateHold)
		}
	})

	for _, refusalFirst := range []bool{true, false} {
		name := "success then refusal"
		if refusalFirst {
			name = "refusal then success"
		}
		t.Run(name, func(t *testing.T) {
			m := phase2AdmissionModel(t, clock, &phase2Calls{})
			m, first, allowed := m.trackerRequestAdmission(trackerRequestExplicit)
			if !allowed {
				t.Fatal("first pre-hold request was not admitted")
			}
			m, second, allowed := m.trackerRequestAdmission(trackerRequestExplicit)
			if !allowed || first.Epoch != second.Epoch {
				t.Fatal("concurrent pre-hold requests did not share an admission epoch")
			}
			if refusalFirst {
				m, _ = m.settleTrackerRequest(first, refusal, false)
				m, _ = m.settleTrackerRequest(second, nil, true)
			} else {
				m, _ = m.settleTrackerRequest(first, nil, true)
				m, _ = m.settleTrackerRequest(second, refusal, false)
			}
			if m.rateHold.manual || !m.rateHold.resetAt.Equal(clock.now().Add(time.Hour)) {
				t.Fatalf("response order %q cleared or weakened known hold: %+v", name, m.rateHold)
			}
		})
	}
}
