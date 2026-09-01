package tui

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/niekcandaele/sitrep/internal/detailfanout"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/termtext/termtexttest"
)

func frontierResultMsg(generation, detailEvidenceVersion int, id model.TicketID,
	detail model.Detail, caps model.Capabilities, err error,
) frontierDetailsMsg {
	if err == nil && detail.TicketID == "" {
		detail.TicketID = id
	}
	return frontierDetailsMsg{
		generation:            generation,
		detailEvidenceVersion: detailEvidenceVersion,
		outcomes: []detailfanout.Outcome{{
			ID: id, Detail: detail, Caps: caps, Err: err,
		}},
	}
}

func TestFrontierDetailSourceFallbackIsOneSequentialPluralCall(t *testing.T) {
	caps := model.Capabilities{BlockingLinks: true}
	ids := []model.TicketID{"A", "B", "C"}

	t.Run("partial failure preserves siblings", func(t *testing.T) {
		partialErr := errors.New("C unavailable")
		var order []model.TicketID
		live, peak := 0, 0
		m := New(t.Context(), Options{DetailSource: func(_ context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
			live++
			peak = max(peak, live)
			defer func() { live-- }()
			order = append(order, id)
			if id == "C" {
				return model.Detail{}, model.Capabilities{}, partialErr
			}
			return model.Detail{TicketID: id}, caps, nil
		}})
		fallback := m.fetchDetails
		pluralCalls := 0
		m.fetchDetails = func(ctx context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
			pluralCalls++
			return fallback(ctx, ids)
		}
		m.frontierContext = t.Context()

		msg := m.frontierFetchCmd(1, 0, ids)().(frontierDetailsMsg)

		if pluralCalls != 1 {
			t.Fatalf("logical plural calls = %d, want one", pluralCalls)
		}
		if !reflect.DeepEqual(order, ids) {
			t.Fatalf("singular fallback order = %v, want canonical %v", order, ids)
		}
		if peak != 1 {
			t.Errorf("peak concurrent singular fallback calls = %d, want sequential execution", peak)
		}
		if msg.err != nil {
			t.Errorf("semantic Run error = %v, want per-Ticket outcomes", msg.err)
		}
		var failures *provider.DetailFailures
		if !errors.As(msg.rawErr, &failures) || !errors.Is(failures.Failures["C"], partialErr) {
			t.Fatalf("raw fallback error = %#v, want C's inspectable DetailFailures cause", msg.rawErr)
		}
		if got := []model.TicketID{msg.outcomes[0].ID, msg.outcomes[1].ID, msg.outcomes[2].ID}; !reflect.DeepEqual(got, ids) {
			t.Fatalf("outcome order = %v, want %v", got, ids)
		}
		if msg.outcomes[0].Detail.TicketID != "A" || msg.outcomes[1].Detail.TicketID != "B" {
			t.Errorf("successful siblings = %#v, want matching A and B Details", msg.outcomes)
		}
		if msg.outcomes[0].Caps != caps || msg.outcomes[1].Caps != caps {
			t.Errorf("successful fallback Capabilities = %#v, want last successful value %#v", msg.outcomes, caps)
		}
		if !errors.Is(msg.outcomes[2].Err, partialErr) {
			t.Errorf("C outcome error = %v, want %v", msg.outcomes[2].Err, partialErr)
		}
	})

	t.Run("cancellation stops unissued singular calls", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		var order []model.TicketID
		m := New(t.Context(), Options{DetailSource: func(_ context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
			order = append(order, id)
			if id == "A" {
				cancel()
			}
			return model.Detail{TicketID: id}, caps, nil
		}})
		fallback := m.fetchDetails
		pluralCalls := 0
		m.fetchDetails = func(ctx context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
			pluralCalls++
			return fallback(ctx, ids)
		}
		m.frontierContext = ctx

		msg := m.frontierFetchCmd(1, 0, ids)().(frontierDetailsMsg)

		if pluralCalls != 1 {
			t.Fatalf("logical plural calls = %d, want one", pluralCalls)
		}
		if want := []model.TicketID{"A"}; !reflect.DeepEqual(order, want) {
			t.Fatalf("singular fallback order = %v, want only completed %v", order, want)
		}
		if len(msg.outcomes) != 1 || msg.outcomes[0].ID != "A" || msg.outcomes[0].Detail.TicketID != "A" {
			t.Fatalf("cancellation outcomes = %#v, want only completed A", msg.outcomes)
		}
		if !errors.Is(msg.rawErr, context.Canceled) || !errors.Is(msg.err, context.Canceled) {
			t.Errorf("cancellation errors = raw:%v semantic:%v, want context.Canceled", msg.rawErr, msg.err)
		}
	})
}

func TestFrontierPluralResultsAtTUIBoundary(t *testing.T) {
	caps := model.Capabilities{BlockingLinks: true}
	tickets := []model.Ticket{
		{ID: "A", Key: "A", Status: model.StatusTodo},
		{ID: "B", Key: "B", Status: model.StatusTodo},
	}
	responseErr := errors.New("response unavailable")
	rateErr := provider.RateLimitErrorf(provider.RateLimitMetadata{}, "response rate limited")
	aliasErr := errors.New("B alias unavailable")
	var typedNil *provider.DetailFailures
	valid := func(id model.TicketID) model.Detail { return model.Detail{TicketID: id} }

	tests := []struct {
		name            string
		details         map[model.TicketID]model.Detail
		err             error
		cancel          bool
		stale           bool
		wantCache       []model.TicketID
		wantSeated      []model.TicketID
		wantFailed      []model.TicketID
		wantDone        int
		wantLastErr     error
		wantLastText    string
		wantWarningText string
		wantRawErr      error
		wantRawText     string
		wantRunErr      error
		wantRunText     string
		wantResolved    bool
		wantRetryPlan   []model.TicketID
	}{
		{
			name: "response-wide error", err: responseErr,
			wantFailed: []model.TicketID{"A", "B"}, wantDone: 2,
			wantLastErr: responseErr, wantRawErr: responseErr, wantResolved: true,
		},
		{
			name:    "complete Details plus ordinary response warning",
			details: map[model.TicketID]model.Detail{"A": valid("A"), "B": valid("B")}, err: responseErr,
			wantCache: []model.TicketID{"A", "B"}, wantSeated: []model.TicketID{"A", "B"}, wantDone: 2,
			wantWarningText: responseErr.Error(), wantRawErr: responseErr, wantResolved: true,
		},
		{
			name:    "complete Details plus rate policy evidence",
			details: map[model.TicketID]model.Detail{"A": valid("A"), "B": valid("B")}, err: rateErr,
			wantCache: []model.TicketID{"A", "B"}, wantSeated: []model.TicketID{"A", "B"}, wantDone: 2,
			wantRawErr: rateErr, wantResolved: true,
		},
		{
			name: "typed-nil DetailFailures", err: typedNil,
			wantFailed: []model.TicketID{"A", "B"}, wantDone: 2,
			wantLastText: "typed-nil DetailFailures", wantRawText: "typed-nil DetailFailures",
			wantResolved: true,
		},
		{
			name: "malformed result", details: map[model.TicketID]model.Detail{
				"A": {TicketID: "WRONG"}, "B": valid("B"),
			},
			wantCache: []model.TicketID{"B"}, wantSeated: []model.TicketID{"B"},
			wantFailed: []model.TicketID{"A"}, wantDone: 2,
			wantLastText: "returned TicketID", wantResolved: true,
		},
		{
			name: "unrequested result", details: map[model.TicketID]model.Detail{
				"A": valid("A"), "B": valid("B"), "EXTRA": valid("EXTRA"),
			},
			wantCache: []model.TicketID{"A", "B"}, wantSeated: []model.TicketID{"A", "B"},
			wantDone: 2, wantWarningText: "unrequested TicketID",
			wantRunText: "unrequested TicketID", wantResolved: true,
		},
		{
			name: "mixed protocol warning and member failure", details: map[model.TicketID]model.Detail{
				"A": valid("A"), "EXTRA": valid("EXTRA"),
			},
			err:          &provider.DetailFailures{Failures: map[model.TicketID]error{"B": aliasErr}},
			wantCache:    []model.TicketID{"A"},
			wantSeated:   []model.TicketID{"A"},
			wantFailed:   []model.TicketID{"B"},
			wantDone:     2,
			wantLastText: "unrequested TicketID", wantRawErr: aliasErr,
			wantRunText: "unrequested TicketID", wantResolved: true,
		},
		{
			name: "omitted result", details: map[model.TicketID]model.Detail{"A": valid("A")},
			wantCache: []model.TicketID{"A"}, wantSeated: []model.TicketID{"A"},
			wantFailed: []model.TicketID{"B"}, wantDone: 2,
			wantLastText: "omitted detail", wantResolved: true,
		},
		{
			name: "alias-local partial", details: map[model.TicketID]model.Detail{"A": valid("A")},
			err:       &provider.DetailFailures{Failures: map[model.TicketID]error{"B": aliasErr}},
			wantCache: []model.TicketID{"A"}, wantSeated: []model.TicketID{"A"},
			wantFailed: []model.TicketID{"B"}, wantDone: 2,
			wantLastErr: aliasErr, wantRawErr: aliasErr, wantResolved: true,
		},
		{
			name: "current cancellation", details: map[model.TicketID]model.Detail{"A": valid("A")},
			err: context.Canceled, cancel: true,
			wantCache: []model.TicketID{"A"}, wantSeated: []model.TicketID{"A"},
			wantDone: 1, wantRawErr: context.Canceled, wantRunErr: context.Canceled,
			wantRetryPlan: []model.TicketID{"B"},
		},
		{
			name: "stale cancellation", details: map[model.TicketID]model.Detail{"A": valid("A")},
			err: context.Canceled, cancel: true, stale: true,
			wantCache: []model.TicketID{"A"}, wantDone: 0,
			wantRawErr: context.Canceled, wantRunErr: context.Canceled,
			wantRetryPlan: []model.TicketID{"B"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cancelDuringFetch context.CancelFunc
			fanoutCalls := 0
			in := ListInput{
				Header: Header{Key: "W", Title: "plural boundary"}, Tickets: tickets,
				Capabilities: caps, FetchedAt: time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC),
			}
			m := New(t.Context(), Options{
				Initial: &in,
				DetailFanout: func(_ context.Context, _ []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
					fanoutCalls++
					if tt.cancel {
						cancelDuringFetch()
					}
					return tt.details, caps, tt.err
				},
				Now: func() time.Time { return in.FetchedAt },
			})
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
			m = updated.(Model)
			updated, _ = m.Update(keyPress("v"))
			m = updated.(Model)
			cancelDuringFetch = m.cancelFrontier
			commandContext := m.frontierContext
			generation := m.frontierGeneration
			msg := m.frontierFetchCmd(generation, m.detailEvidenceVersion,
				[]model.TicketID{"A", "B"})().(frontierDetailsMsg)

			var currentContext context.Context
			if tt.stale {
				currentContext, m.cancelFrontier = context.WithCancel(t.Context())
				t.Cleanup(m.cancelFrontier)
				m.frontierContext = currentContext
				m.frontierGeneration++
			}
			updated, _ = m.onFrontierDetails(msg)
			m = updated.(Model)

			switch {
			case tt.wantRawErr != nil && !errors.Is(msg.rawErr, tt.wantRawErr):
				t.Errorf("raw Provider error = %v, want identity %v", msg.rawErr, tt.wantRawErr)
			case tt.wantRawText != "" && (msg.rawErr == nil || !strings.Contains(msg.rawErr.Error(), tt.wantRawText)):
				t.Errorf("raw Provider error = %v, want text %q", msg.rawErr, tt.wantRawText)
			case tt.wantRawErr == nil && tt.wantRawText == "" && msg.rawErr != nil:
				t.Errorf("raw Provider error = %v, want nil", msg.rawErr)
			}
			switch {
			case tt.wantRunErr != nil && !errors.Is(msg.err, tt.wantRunErr):
				t.Errorf("Run error = %v, want identity %v", msg.err, tt.wantRunErr)
			case tt.wantRunText != "" && (msg.err == nil || !strings.Contains(msg.err.Error(), tt.wantRunText)):
				t.Errorf("Run error = %v, want text %q", msg.err, tt.wantRunText)
			case tt.wantRunErr == nil && tt.wantRunText == "" && msg.err != nil:
				t.Errorf("Run error = %v, want nil", msg.err)
			}
			for _, id := range []model.TicketID{"A", "B"} {
				if got, want := m.haveDetail(id), slices.Contains(tt.wantCache, id); got != want {
					t.Errorf("cache contains %s = %t, want %t", id, got, want)
				}
				_, seated := m.frontier.input.Links[id]
				if want := slices.Contains(tt.wantSeated, id); seated != want {
					t.Errorf("seat contains %s = %t, want %t", id, seated, want)
				}
				_, failed := m.frontier.failed[id]
				if want := slices.Contains(tt.wantFailed, id); failed != want {
					t.Errorf("failed contains %s = %t, want %t", id, failed, want)
				}
			}
			if m.frontier.done != tt.wantDone {
				t.Errorf("done = %d, want %d canonical outcomes only", m.frontier.done, tt.wantDone)
			}
			if m.frontier.isResolved() != tt.wantResolved {
				t.Errorf("isResolved = %t, want %t", m.frontier.isResolved(), tt.wantResolved)
			}
			if tt.wantLastErr != nil && !errors.Is(m.frontier.lastErr, tt.wantLastErr) {
				t.Errorf("lastErr = %v, want identity %v", m.frontier.lastErr, tt.wantLastErr)
			}
			if tt.wantLastText != "" && !strings.Contains(m.frontier.lastErr.Error(), tt.wantLastText) {
				t.Errorf("lastErr = %v, want text %q", m.frontier.lastErr, tt.wantLastText)
			}
			if tt.wantLastErr == nil && tt.wantLastText == "" && m.frontier.lastErr != nil {
				t.Errorf("lastErr = %v, want cancellation control flow or protocol-only warning", m.frontier.lastErr)
			}
			if tt.wantWarningText != "" {
				if m.frontier.protocolWarning == nil || !strings.Contains(m.frontier.protocolWarning.Error(), tt.wantWarningText) {
					t.Errorf("protocol warning = %v, want text %q", m.frontier.protocolWarning, tt.wantWarningText)
				}
			} else if m.frontier.protocolWarning != nil {
				t.Errorf("protocol warning = %v, want nil", m.frontier.protocolWarning)
			}
			if got := detailfanout.Plan(tickets, m.haveDetail); !reflect.DeepEqual(got, tt.wantRetryPlan) && tt.wantRetryPlan != nil {
				t.Errorf("retry plan = %v, want %v", got, tt.wantRetryPlan)
			}
			if m.detailFanoutInflight != 0 {
				t.Errorf("in-flight plural commands = %d, want released slot", m.detailFanoutInflight)
			}
			if tt.name == "current cancellation" {
				if m.frontierContext != nil {
					t.Errorf("completed partial command retained an active context: %v", m.frontierContext)
				}
				header := m.frontierHeader()
				if strings.Contains(header, "reading Detail") || !strings.Contains(header, "Details pending") {
					t.Errorf("partial cancellation header = %q, want pending retry state", header)
				}
				retriedModel, retryCmd := m.refreshFrontier()
				retried := retriedModel.(Model)
				if retryCmd == nil || retried.frontier.planned != 1 || retried.detailFanoutInflight != 1 {
					t.Errorf("retry after partial cancellation = cmd:%v planned:%d in-flight:%d, want one missing plural read",
						retryCmd, retried.frontier.planned, retried.detailFanoutInflight)
				}
				if retried.frontierContext == nil || retried.frontierContext == commandContext || retried.frontierContext.Err() != nil {
					t.Errorf("retry context = %v, want a distinct live generation", retried.frontierContext)
				}
				if retried.cancelFrontier != nil {
					retried.cancelFrontier()
				}
			}
			if tt.wantWarningText != "" {
				warningFrame := string(frame(m.View().Content))
				if !strings.Contains(warningFrame, "provider response warning:") ||
					!strings.Contains(warningFrame, "press r to dismiss warning") ||
					strings.Contains(warningFrame, "read failed:") || strings.Contains(warningFrame, "retry the Tickets") {
					t.Errorf("protocol-only warning frame is misleading:\n%s", warningFrame)
				}
				if got := m.effectiveFrontierKeys().Refresh.Help().Desc; got != "dismiss warning" {
					t.Errorf("protocol-only Refresh help = %q, want dismiss warning", got)
				}
				footerHeight := m.frontierFooterHeight()
				bodyHeight := m.frontierBodyHeight()
				calls := fanoutCalls
				refreshedModel, refreshCmd := m.refreshFrontier()
				refreshed := refreshedModel.(Model)
				if refreshCmd == nil || refreshed.frontier.protocolWarning != nil || refreshed.frontier.lastErr != nil ||
					refreshed.detailFanoutInflight != 0 || fanoutCalls != calls {
					t.Errorf("dismiss completed malformed batch = cmd:%v warning:%v err:%v in-flight:%d calls:%d→%d, want warning dismissed without I/O",
						refreshCmd, refreshed.frontier.protocolWarning, refreshed.frontier.lastErr,
						refreshed.detailFanoutInflight, calls, fanoutCalls)
				}
				if got := refreshed.effectiveFrontierKeys().Refresh.Help().Desc; got != "re-read Details" {
					t.Errorf("Refresh help after dismissal = %q, want re-read Details", got)
				}
				if got := string(frame(refreshed.View().Content)); strings.Contains(got, "provider response warning") {
					t.Errorf("dismissed warning remains in frame:\n%s", got)
				}
				if refreshed.frontierFooterHeight() != footerHeight-1 || refreshed.frontierBodyHeight() != bodyHeight+1 {
					t.Errorf("dismissal geometry = footer %d→%d body %d→%d, want warning line reclaimed",
						footerHeight, refreshed.frontierFooterHeight(), bodyHeight, refreshed.frontierBodyHeight())
				}
			}
			if tt.name == "mixed protocol warning and member failure" {
				mixedFrame := string(frame(m.View().Content))
				if !strings.Contains(mixedFrame, "read failed for B:") || !strings.Contains(mixedFrame, "press r to retry the Tickets that failed") ||
					strings.Contains(mixedFrame, "dismiss warning") {
					t.Errorf("mixed protocol/member failure lost retry semantics:\n%s", mixedFrame)
				}
				if got := m.effectiveFrontierKeys().Refresh.Help().Desc; got != "re-read Details" {
					t.Errorf("mixed failure Refresh help = %q, want re-read Details", got)
				}
				if got := detailfanout.Plan(tickets, m.haveDetail); !reflect.DeepEqual(got, []model.TicketID{"B"}) {
					t.Errorf("mixed failure retry plan = %v, want only B", got)
				}
			}
			if tt.stale && (m.frontierContext != currentContext || currentContext.Err() != nil) {
				t.Errorf("stale cancellation changed current context: current=%v model=%v", currentContext.Err(), m.frontierContext)
			}
		})
	}
}

func TestFrontierProtectedFailureKeepsProtocolWarningDismissible(t *testing.T) {
	caps := model.Capabilities{BlockingLinks: true}
	at := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	in := ListInput{
		Header: Header{Key: "W", Title: "protected protocol warning"},
		Tickets: []model.Ticket{
			{ID: "A", Key: "A", Status: model.StatusTodo},
			{ID: "B", Key: "B", Status: model.StatusTodo},
		},
		Capabilities: caps,
		FetchedAt:    at,
	}
	m := New(t.Context(), Options{
		Initial: &in,
		DetailFanout: func(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
			return map[model.TicketID]model.Detail{
					"A":     {TicketID: "A"},
					"EXTRA": {TicketID: "EXTRA"},
				}, caps, &provider.DetailFailures{Failures: map[model.TicketID]error{
					"B": errors.New("older B failure"),
				}}
		},
		Now: func() time.Time { return at },
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = updated.(Model)
	updated, _ = m.Update(keyPress("v"))
	m = updated.(Model)
	msg := m.frontierFetchCmd(m.frontierGeneration, m.detailEvidenceVersion,
		[]model.TicketID{"A", "B"})().(frontierDetailsMsg)

	m.detailEvidenceVersion++
	m.details["B"] = detailEntry{
		detail: model.Detail{TicketID: "B"}, caps: caps, fetchedAt: at,
		frontierDetail: model.Detail{TicketID: "B"}, frontierFetchedAt: at, frontierEvidence: true,
		directDetailVersion: m.detailEvidenceVersion,
	}
	m = m.seatFanoutLinks("B", nil)
	updated, _ = m.onFrontierDetails(msg)
	m = updated.(Model)

	if len(m.frontier.failed) != 0 || m.frontier.lastErr != nil || m.frontier.protocolWarning == nil {
		t.Fatalf("protected failure classification = failed:%v last:%v warning:%v, want dismissible protocol warning only",
			m.frontier.failed, m.frontier.lastErr, m.frontier.protocolWarning)
	}
	frame := string(frame(m.View().Content))
	if !strings.Contains(frame, "press r to dismiss warning") || strings.Contains(frame, "retry the Tickets") {
		t.Errorf("protected failure protocol frame is misleading:\n%s", frame)
	}
	if got := m.effectiveFrontierKeys().Refresh.Help().Desc; got != "dismiss warning" {
		t.Errorf("protected failure Refresh help = %q, want dismiss warning", got)
	}
}

func TestFrontierPluralBoundarySanitizesDetailAndErrorIdentity(t *testing.T) {
	caps := model.Capabilities{BlockingLinks: true}
	hostileErr := errors.New("alias failed \x1b[31mRED\x1b[0m \u202e")
	if termtexttest.IsClean(hostileErr.Error()) {
		t.Fatal("hostile error fixture carries nothing requiring sanitation")
	}
	in := ListInput{
		Header: Header{Key: "W", Title: "sanitation"},
		Tickets: []model.Ticket{
			{ID: "A", Key: "A", Status: model.StatusTodo},
			{ID: "B", Key: "B", Status: model.StatusTodo},
		},
		Capabilities: caps,
		FetchedAt:    time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC),
	}
	m := New(t.Context(), Options{
		Initial: &in,
		DetailFanout: func(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
			return map[model.TicketID]model.Detail{"A": hostileDetail("A")}, caps,
				&provider.DetailFailures{Failures: map[model.TicketID]error{"B": hostileErr}}
		},
		Now: func() time.Time { return in.FetchedAt },
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = updated.(Model)
	updated, _ = m.Update(keyPress("v"))
	m = updated.(Model)
	msg := m.frontierFetchCmd(m.frontierGeneration, m.detailEvidenceVersion,
		[]model.TicketID{"A", "B"})().(frontierDetailsMsg)

	if len(msg.outcomes) != 2 || msg.outcomes[0].ID != "A" || msg.outcomes[1].ID != "B" {
		t.Fatalf("plural outcomes = %#v, want canonical A success and B failure", msg.outcomes)
	}
	termtexttest.AssertClean(t, "plural success", msg.outcomes[0].Detail, detailValueOptions...)
	termtexttest.AssertClean(t, "plural constituent error", msg.outcomes[1].Err.Error())
	if !errors.Is(msg.outcomes[1].Err, hostileErr) || !errors.Is(msg.rawErr, hostileErr) {
		t.Errorf("sanitation broke error identity: outcome=%v raw=%v", msg.outcomes[1].Err, msg.rawErr)
	}

	updated, _ = m.onFrontierDetails(msg)
	m = updated.(Model)
	termtexttest.AssertClean(t, "cached plural success", m.details["A"].detail, detailValueOptions...)
	termtexttest.AssertClean(t, "seated plural failure", m.frontier.failureErrors["B"].Error())
	if !errors.Is(m.frontier.failureErrors["B"], hostileErr) {
		t.Errorf("seated failure lost original identity: %v", m.frontier.failureErrors["B"])
	}
}

func TestFrontierRetryUsesCapacityAlongsideRetiringGeneration(t *testing.T) {
	caps := model.Capabilities{BlockingLinks: true}
	in := ListInput{
		Header: Header{Key: "W", Title: "retry beside retiring work"},
		Tickets: []model.Ticket{
			{ID: "A", Key: "A", Status: model.StatusTodo},
			{ID: "B", Key: "B", Status: model.StatusTodo},
		},
		Capabilities: caps,
		FetchedAt:    time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC),
	}
	reads := newControlledFrontierReads()
	reads.ignoreCancellation = true
	m := New(t.Context(), Options{Initial: &in, DetailSource: reads.source, DetailFanout: reads.fanout})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = updated.(Model)

	updated, _ = m.enterFrontier()
	m = updated.(Model)
	oldResult := make(chan frontierDetailsMsg, 1)
	oldCmd := m.frontierFetchCmd(m.frontierGeneration, m.detailEvidenceVersion,
		[]model.TicketID{"A", "B"})
	go func() { oldResult <- oldCmd().(frontierDetailsMsg) }()
	oldCall := reads.next(t)
	updated, _ = m.leaveFrontier()
	m = updated.(Model)
	assertFrontierCallsCanceled(t, oldCall)

	updated, _ = m.enterFrontier()
	m = updated.(Model)
	currentResult := make(chan frontierDetailsMsg, 1)
	currentCmd := m.frontierFetchCmd(m.frontierGeneration, m.detailEvidenceVersion,
		[]model.TicketID{"A", "B"})
	go func() { currentResult <- currentCmd().(frontierDetailsMsg) }()
	currentCall := reads.next(t)
	m.cancelFrontier()
	currentCall.reply <- controlledFrontierReply{
		details: map[model.TicketID]model.Detail{"A": {TicketID: "A"}},
		caps:    caps,
		err:     context.Canceled,
	}
	updated, _ = m.onFrontierDetails(<-currentResult)
	m = updated.(Model)
	if got := detailfanout.Plan(in.Tickets, m.haveDetail); !reflect.DeepEqual(got, []model.TicketID{"B"}) {
		t.Fatalf("partial command retry plan = %v, want only B", got)
	}
	if m.detailFanoutInflight != 1 || reads.active.Load() != 1 {
		t.Fatalf("retiring work = model:%d active:%d, want one stale command", m.detailFanoutInflight, reads.active.Load())
	}

	retriedModel, retryCmd := m.refreshFrontier()
	m = retriedModel.(Model)
	if retryCmd == nil || m.frontier.planned != 1 || m.detailFanoutInflight != 2 {
		t.Fatalf("retry beside stale work = cmd:%v planned:%d in-flight:%d, want one dispatched missing-ID command",
			retryCmd, m.frontier.planned, m.detailFanoutInflight)
	}
	retryBatchMsg := retryCmd()
	batch, ok := retryBatchMsg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("retry command = %T, want tea.BatchMsg", retryBatchMsg)
	}
	retryMessages := make(chan tea.Msg, len(batch))
	launched := 0
	for _, sub := range batch {
		if sub == nil {
			continue
		}
		launched++
		go func(cmd tea.Cmd) { retryMessages <- cmd() }(sub)
	}
	retryCall := reads.next(t)
	if !reflect.DeepEqual(retryCall.ids, []model.TicketID{"B"}) {
		t.Fatalf("retry IDs = %v, want only B with no duplicate A", retryCall.ids)
	}
	assertFrontierCallsLive(t, retryCall)
	if got := reads.peak.Load(); got > detailfanout.Parallelism {
		t.Errorf("peak plural commands = %d, want at most %d", got, detailfanout.Parallelism)
	}

	oldCall.reply <- controlledFrontierReply{err: context.Canceled}
	updated, _ = m.onFrontierDetails(<-oldResult)
	m = updated.(Model)
	retryCall.reply <- controlledFrontierReply{
		details: map[model.TicketID]model.Detail{"B": {TicketID: "B"}}, caps: caps,
	}
	for range launched {
		msg := <-retryMessages
		if result, ok := msg.(frontierDetailsMsg); ok {
			updated, _ = m.onFrontierDetails(result)
			m = updated.(Model)
		}
	}
	waitUntil(t, "retiring and retry plural commands to return", func() bool {
		return reads.active.Load() == 0
	})
	if m.detailFanoutInflight != 0 || !m.frontier.isResolved() || !m.haveDetail("A") || !m.haveDetail("B") {
		t.Errorf("settled retry = in-flight:%d resolved:%t cache A/B:%t/%t",
			m.detailFanoutInflight, m.frontier.isResolved(), m.haveDetail("A"), m.haveDetail("B"))
	}
}

func TestFrontierBoundsCancellationIgnoringPluralGenerations(t *testing.T) {
	caps := model.Capabilities{BlockingLinks: true}
	in := ListInput{
		Header:       Header{Key: "W", Title: "bounded plural generations"},
		Tickets:      []model.Ticket{{ID: "A", Key: "A", Status: model.StatusTodo}},
		Capabilities: caps, FetchedAt: time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC),
	}
	reads := newControlledFrontierReads()
	reads.ignoreCancellation = true
	m := New(t.Context(), Options{Initial: &in, DetailSource: reads.source, DetailFanout: reads.fanout})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = updated.(Model)

	oldCalls := make([]controlledFrontierCall, 0, detailfanout.Parallelism)
	oldResults := make([]<-chan frontierDetailsMsg, 0, detailfanout.Parallelism)
	for range detailfanout.Parallelism {
		updated, _ = m.enterFrontier()
		m = updated.(Model)
		result := make(chan frontierDetailsMsg, 1)
		cmd := m.frontierFetchCmd(m.frontierGeneration, m.detailEvidenceVersion, []model.TicketID{"A"})
		go func() { result <- cmd().(frontierDetailsMsg) }()
		oldCalls = append(oldCalls, reads.next(t))
		oldResults = append(oldResults, result)
		updated, _ = m.leaveFrontier()
		m = updated.(Model)
	}
	assertFrontierCallsCanceled(t, oldCalls...)
	if got := reads.active.Load(); got != detailfanout.Parallelism {
		t.Fatalf("active cancellation-ignoring generations = %d, want the full bound %d", got, detailfanout.Parallelism)
	}

	updated, _ = m.enterFrontier()
	m = updated.(Model)
	if got := m.detailFanoutInflight; got != detailfanout.Parallelism {
		t.Fatalf("model in-flight commands = %d, want bound %d", got, detailfanout.Parallelism)
	}
	if got := m.frontier.queued; !reflect.DeepEqual(got, []model.TicketID{"A"}) {
		t.Fatalf("current queued IDs = %v, want A waiting for a shared slot", got)
	}

	oldCalls[0].reply <- controlledFrontierReply{err: context.Canceled}
	firstResult := <-oldResults[0]
	updated, nextCmd := m.onFrontierDetails(firstResult)
	m = updated.(Model)
	if nextCmd == nil {
		t.Fatal("releasing one stale slot did not schedule the queued current generation")
	}
	batch, ok := nextCmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("replacement command = %T, want tea.BatchMsg", nextCmd())
	}
	nextMessages := make(chan tea.Msg, len(batch))
	launched := 0
	for _, sub := range batch {
		if sub == nil {
			continue
		}
		launched++
		go func(cmd tea.Cmd) { nextMessages <- cmd() }(sub)
	}
	currentCall := reads.next(t)
	assertFrontierCallsLive(t, currentCall)
	if got := reads.total.Load(); got != int64(detailfanout.Parallelism+1) {
		t.Errorf("plural commands started = %d, want one replacement after one stale slot released", got)
	}
	if got := m.detailFanoutInflight; got != detailfanout.Parallelism {
		t.Errorf("model in-flight commands = %d after replacement, want bound %d", got, detailfanout.Parallelism)
	}
	if got := reads.peak.Load(); got != detailfanout.Parallelism {
		t.Errorf("peak active plural commands = %d, want exact shared bound %d", got, detailfanout.Parallelism)
	}
	if len(m.frontier.queued) != 0 {
		t.Errorf("queued IDs = %v after replacement dispatch, want none", m.frontier.queued)
	}

	for i := 1; i < len(oldCalls); i++ {
		oldCalls[i].reply <- controlledFrontierReply{err: context.Canceled}
		<-oldResults[i]
	}
	currentCall.reply <- controlledFrontierReply{
		details: map[model.TicketID]model.Detail{"A": {TicketID: "A"}}, caps: caps,
	}
	for range launched {
		<-nextMessages
	}
	waitUntil(t, "all cancellation-ignoring plural commands to return", func() bool {
		return reads.active.Load() == 0
	})
	if m.cancelFrontier != nil {
		m.cancelFrontier()
	}
}
