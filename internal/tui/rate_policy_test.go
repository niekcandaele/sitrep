package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
)

type policyClock struct{ value time.Time }

func (c *policyClock) now() time.Time          { return c.value }
func (c *policyClock) advance(d time.Duration) { c.value = c.value.Add(d) }

func policyModel(t *testing.T, c *policyClock, interval time.Duration) Model {
	t.Helper()
	return New(t.Context(), Options{
		Interval: interval,
		Now:      c.now,
		Heartbeat: func() tea.Msg {
			return nil
		},
	})
}

func policyReading(reset time.Time, remaining int) ListInput {
	return ListInput{
		Capabilities:    model.Capabilities{RateLimitBudget: true},
		RateLimitBudget: model.RateLimitBudget{Remaining: remaining, ResetsAt: reset},
	}
}

func TestInitialBudgetInstallsRatePolicy(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	reset := c.now().Add(10 * time.Minute)
	exhausted := policyReading(reset, 0)
	exhausted.FetchedAt = c.now()
	m := New(t.Context(), Options{Initial: &exhausted, Interval: time.Minute, Now: c.now, Heartbeat: func() tea.Msg { return nil }})
	if !m.rateHold.exhausted || m.rateHold.resetAt != reset {
		t.Fatalf("initial exhausted budget policy = %+v", m.rateHold)
	}
	if next, allowed, _ := m.automaticRefreshAdmission(); allowed || next.generation != 0 {
		t.Fatal("initial exhausted budget admitted automatic work")
	}
	if next, allowed := m.manualRefreshAdmission(); allowed || next.generation != 0 {
		t.Fatal("initial exhausted budget admitted manual work")
	}

	low := policyReading(reset, 10)
	low.FetchedAt = c.now()
	m = New(t.Context(), Options{Initial: &low, Interval: time.Minute, Now: c.now, Heartbeat: func() tea.Msg { return nil }})
	if !m.lowBudget.active(c.now()) || !m.lowBudget.nextAt.Equal(c.now().Add(2*time.Minute)) {
		t.Fatalf("initial low budget policy = %+v", m.lowBudget)
	}

	invalid := policyReading(reset, -1)
	invalid.FetchedAt = c.now()
	m = New(t.Context(), Options{Initial: &invalid, Interval: time.Minute, Now: c.now, Heartbeat: func() tea.Msg { return nil }})
	if m.rateHold != (rateHold{}) || m.lowBudget != (lowBudgetSchedule{}) {
		t.Fatalf("invalid initial budget changed policy: hold=%+v low=%+v", m.rateHold, m.lowBudget)
	}
}

func TestElapsedKnownRateLimitDeadlineDoesNotCreateManualHold(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	for _, reset := range []time.Time{c.now(), c.now().Add(-time.Second)} {
		m := policyModel(t, c, time.Minute)
		m = m.onRefreshed(refreshedMsg{generation: m.generation, err: provider.RateLimitErrorf(
			provider.RateLimitMetadata{ResetAt: reset}, "github: rate limited")})
		if m.rateHold != (rateHold{}) {
			t.Fatalf("elapsed deadline %s created hold %+v", reset, m.rateHold)
		}
		if got := m.ratePolicyFooter(); strings.Contains(got, "refresh held") || strings.Contains(got, "retrying at") {
			t.Fatalf("elapsed deadline footer = %q, want no false hold", got)
		}
		c.advance(time.Minute)
		next, allowed, _ := m.automaticRefreshAdmission()
		if !allowed || next.rateHold != (rateHold{}) {
			t.Fatalf("elapsed deadline %s did not return to normal automatic admission", reset)
		}
		c.advance(-time.Minute)
	}
}

func TestKnownRateLimitHoldBlocksAutomaticAndManualUntilDeadline(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	m := policyModel(t, c, time.Minute)
	reset := c.now().Add(5 * time.Minute)
	m = m.onRefreshed(refreshedMsg{generation: m.generation, err: provider.RateLimitErrorf(
		provider.RateLimitMetadata{ResetAt: reset}, "github: API rate limit exceeded")})
	generation, attempt := m.generation, m.lastAttempt
	c.advance(2 * time.Minute)

	if next, allowed, _ := m.automaticRefreshAdmission(); allowed || next.generation != generation {
		t.Fatal("automatic refresh was admitted before the known reset")
	}
	if next, allowed := m.manualRefreshAdmission(); allowed || next.generation != generation || !next.lastAttempt.Equal(attempt) {
		t.Fatal("manual refresh bypassed a known reset")
	}
	if got := m.ratePolicyFooter(); got == "" || !strings.Contains(got, "r held") {
		t.Errorf("held footer = %q, want known hold", got)
	}

	c.advance(3 * time.Minute)
	if got := m.ratePolicyFooter(); strings.Contains(got, "refresh held") || strings.Contains(got, "retrying at") {
		t.Errorf("expired hold footer = %q, want no hold wording", got)
	}
	next, allowed, _ := m.automaticRefreshAdmission()
	if !allowed || next.rateHold.active(c.now()) {
		t.Fatal("automatic refresh was not admitted exactly at reset")
	}
	if got := next.ratePolicyFooter(); strings.Contains(got, "refresh held") || strings.Contains(got, "retrying at") {
		t.Errorf("expired hold footer = %q, want no hold wording", got)
	}
	next, _ = next.startRefresh()
	if next.generation != generation+1 {
		t.Fatalf("generation = %d, want exactly one new refresh", next.generation)
	}
}

func TestKnownRateLimitDeadlineBeatsLongerConfiguredInterval(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	m := policyModel(t, c, time.Minute)
	reset := c.now().Add(10 * time.Second)
	m = m.onRefreshed(refreshedMsg{generation: m.generation, err: provider.RateLimitErrorf(
		provider.RateLimitMetadata{ResetAt: reset}, "github: rate limited")})
	c.advance(10 * time.Second)
	next, allowed, due := m.automaticRefreshAdmission()
	if !allowed || !due.Equal(c.now()) || next.rateHold != (rateHold{}) {
		t.Fatalf("automatic admission at known reset = allowed:%t due:%s hold:%+v", allowed, due, next.rateHold)
	}
}

func TestUnknownRateLimitRequiresManualSuccess(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	m := policyModel(t, c, time.Minute)
	m = m.onRefreshed(refreshedMsg{generation: m.generation, err: provider.Errorf(provider.KindRateLimit, "gitlab: rate limited")})

	for range 3 {
		c.advance(time.Hour)
		if next, allowed, _ := m.automaticRefreshAdmission(); allowed || !next.rateHold.manual {
			t.Fatal("unknown rate-limit hold admitted automatic work")
		}
	}
	next, allowed := m.manualRefreshAdmission()
	if !allowed {
		t.Fatal("manual refresh was not admitted for unknown rate limit")
	}
	next, _ = next.startRefresh()
	next = next.onRefreshed(refreshedMsg{generation: next.generation, err: provider.Errorf(provider.KindUnavailable, "network")})
	if !next.rateHold.manual {
		t.Fatal("ordinary manual failure cleared unknown rate-limit hold")
	}
	if got := next.ratePolicyFooter(); got == "" || !strings.Contains(got, "press r to try again") {
		t.Errorf("manual hold footer = %q", got)
	}
}

func TestLowBudgetWidensAutomaticCadenceButNotManual(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	m := policyModel(t, c, time.Minute)
	m = m.onRefreshed(refreshedMsg{generation: m.generation, input: policyReading(c.now().Add(10*time.Minute), 3)})
	want := c.now().Add(3*time.Minute + 20*time.Second)
	if !m.lowBudget.nextAt.Equal(want) {
		t.Fatalf("low-budget due = %s, want spread %s", m.lowBudget.nextAt, want)
	}
	floor := policyModel(t, c, time.Minute)
	floor = floor.onRefreshed(refreshedMsg{generation: floor.generation, input: policyReading(c.now().Add(10*time.Minute), 10)})
	if wantFloor := c.now().Add(2 * time.Minute); !floor.lowBudget.nextAt.Equal(wantFloor) {
		t.Fatalf("low-budget floor due = %s, want %s", floor.lowBudget.nextAt, wantFloor)
	}
	c.advance(time.Minute)
	if _, allowed, due := m.automaticRefreshAdmission(); allowed || !due.Equal(want) {
		t.Fatalf("automatic admission = %t at %s, want blocked until %s", allowed, due, want)
	}
	if _, allowed := m.manualRefreshAdmission(); !allowed {
		t.Fatal("manual r did not bypass positive low-budget widening")
	}
	if got := m.ratePolicyFooter(); got == "" || !strings.Contains(got, "refresh slowed") {
		t.Errorf("low-budget footer = %q", got)
	}
	c.advance(9 * time.Minute)
	next, allowed, _ := m.automaticRefreshAdmission()
	if !allowed || !next.lowBudget.resetAt.IsZero() {
		t.Fatal("low-budget state remained active past reset")
	}
}

func TestExhaustedBudgetBlocksBothAdmissions(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	m := policyModel(t, c, time.Minute)
	m = m.onRefreshed(refreshedMsg{generation: m.generation, input: policyReading(c.now().Add(time.Minute), 0)})
	if _, allowed, _ := m.automaticRefreshAdmission(); allowed {
		t.Fatal("automatic refresh bypassed exhausted budget")
	}
	generation, attempt := m.generation, m.lastAttempt
	if next, allowed := m.manualRefreshAdmission(); allowed || next.generation != generation || !next.lastAttempt.Equal(attempt) {
		t.Fatal("manual refresh bypassed exhausted budget")
	}
	if got := m.ratePolicyFooter(); got == "" || !strings.Contains(got, "budget exhausted") {
		t.Errorf("exhausted footer = %q", got)
	}
}

func TestInitialKnownRateLimitHoldAvoidsDuplicateStartupFetch(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	var calls atomic.Int32
	m := New(t.Context(), Options{
		Interval: time.Minute,
		Now:      c.now,
		Heartbeat: func() tea.Msg {
			return nil
		},
		Source: func(context.Context) (ListInput, error) {
			calls.Add(1)
			return ListInput{}, nil
		},
		InitialError: provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: c.now().Add(time.Hour)}, "github: rate limited"),
	})
	if m.refreshing || m.generation != 0 || !m.rateHold.active(c.now()) {
		t.Fatalf("initial rate-limit state = refreshing:%t generation:%d hold:%+v", m.refreshing, m.generation, m.rateHold)
	}
	m.Init()()
	if calls.Load() != 0 {
		t.Fatalf("initial known hold started %d source calls", calls.Load())
	}
	next, _ := m.onHeartbeat()
	if next.(Model).refreshing || calls.Load() != 0 {
		t.Fatal("known startup hold admitted a duplicate fetch before reset")
	}
}

func TestElapsedInitialRateLimitDeadlineStartsTheNormalInitialFetch(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	var calls atomic.Int32
	m := New(t.Context(), Options{
		Interval:  time.Minute,
		Now:       c.now,
		Heartbeat: func() tea.Msg { return nil },
		Source: func(context.Context) (ListInput, error) {
			calls.Add(1)
			return ListInput{}, nil
		},
		InitialError: provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: c.now()}, "github: rate limited"),
	})
	if !m.refreshing || m.generation != 1 || m.rateHold != (rateHold{}) {
		t.Fatalf("elapsed initial rate-limit state = refreshing:%t generation:%d hold:%+v", m.refreshing, m.generation, m.rateHold)
	}
	batch, ok := m.Init()().(tea.BatchMsg)
	if !ok {
		t.Fatal("initial refresh command was not batched")
	}
	for _, cmd := range batch {
		if _, ok := cmd().(refreshedMsg); ok {
			break
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("elapsed initial deadline started %d source calls, want one", calls.Load())
	}
}

func TestUnseededRateLimitBodyExplainsManualAvailability(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	for _, test := range []struct {
		name     string
		hold     rateHold
		contains string
		excludes string
	}{
		{"known", rateHold{resetAt: c.now().Add(time.Hour)}, "r is held", "Press r to try again"},
		{"exhausted", rateHold{resetAt: c.now().Add(time.Hour), exhausted: true}, "r is held", "Press r to try again"},
		{"unknown", rateHold{manual: true}, "Press r to try again", "r is held"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := policyModel(t, c, time.Minute)
			m.width, m.height = 120, 20
			m.refreshing = false
			m.lastErr = provider.Errorf(provider.KindRateLimit, "github: rate limited")
			m.rateHold = test.hold
			m.rateHold.err = m.lastErr
			body := m.renderBody(listMarkers{})
			if !strings.Contains(body, test.contains) || strings.Contains(body, test.excludes) {
				t.Fatalf("%s unseeded rate-limit body = %q", test.name, body)
			}
		})
	}
}

func TestRatePolicyFrameUsesOneClockSnapshot(t *testing.T) {
	before := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	after := before.Add(2 * time.Minute)
	m := policyModel(t, &policyClock{value: before}, time.Minute)
	m.ready = true
	m.hasData = true
	m.width, m.height = 80, 24
	m.searching = true
	m.search.Focus()
	m.rateHold = rateHold{
		err:     provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: before.Add(time.Minute)}, "github: rate limited"),
		resetAt: before.Add(time.Minute),
	}
	calls := 0
	m.now = func() time.Time {
		calls++
		if calls == 1 {
			return before
		}
		return after
	}
	view := m.View()
	if calls != 1 {
		t.Fatalf("View read clock %d times, want one frame snapshot", calls)
	}
	if !strings.Contains(view.Content, "refresh held") {
		t.Fatalf("clock-boundary frame lost active hold: %q", view.Content)
	}
	if view.Cursor == nil {
		t.Fatal("clock-boundary frame lost filter cursor")
	}
	frame := m
	frame.now = func() time.Time { return before }
	wantCursor := frame.cursor()
	if wantCursor == nil || view.Cursor.Y != wantCursor.Y || view.Cursor.X != wantCursor.X {
		t.Fatalf("clock-boundary cursor = %+v, want %+v", view.Cursor, wantCursor)
	}
}

func TestFailedAutomaticRefreshRetainsLowBudgetCadence(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	m := policyModel(t, c, time.Minute)
	m = m.onRefreshed(refreshedMsg{generation: m.generation, input: policyReading(c.now().Add(30*time.Minute), 3)})
	if got, want := m.lowBudget.cadence, 10*time.Minute; got != want {
		t.Fatalf("low-budget cadence = %s, want %s", got, want)
	}
	c.advance(10 * time.Minute)
	m, allowed, _ := m.automaticRefreshAdmission()
	if !allowed {
		t.Fatal("first low-budget automatic refresh was not admitted at its due time")
	}
	m, _ = m.startRefresh()
	m = m.onRefreshed(refreshedMsg{generation: m.generation, err: provider.Errorf(provider.KindUnavailable, "network")})
	if got, want := m.lowBudget.nextAt, c.now().Add(10*time.Minute); !got.Equal(want) {
		t.Fatalf("failed low-budget refresh next attempt = %s, want %s", got, want)
	}
	c.advance(9 * time.Minute)
	if _, allowed, _ := m.automaticRefreshAdmission(); allowed {
		t.Fatal("failed low-budget refresh retried before the preserved cadence")
	}
	c.advance(time.Minute)
	if _, allowed, _ := m.automaticRefreshAdmission(); !allowed {
		t.Fatal("failed low-budget refresh was not admitted at the preserved cadence")
	}

	manual := policyModel(t, c, time.Minute)
	manual = manual.onRefreshed(refreshedMsg{generation: manual.generation, input: policyReading(c.now().Add(30*time.Minute), 3)})
	originalDue := manual.lowBudget.nextAt
	manual, _ = manual.startRefresh()
	manual = manual.onRefreshed(refreshedMsg{generation: manual.generation, err: provider.Errorf(provider.KindUnavailable, "network")})
	if !manual.lowBudget.nextAt.Equal(originalDue) {
		t.Fatalf("failed manual low-budget retry moved automatic due from %s to %s", originalDue, manual.lowBudget.nextAt)
	}
}

func TestManualRefreshKeyHonorsHardAndManualOnlyHolds(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	var calls atomic.Int32
	m := New(t.Context(), Options{
		Interval:  time.Minute,
		Now:       c.now,
		Heartbeat: func() tea.Msg { return nil },
		Source: func(context.Context) (ListInput, error) {
			calls.Add(1)
			return ListInput{}, nil
		},
	})
	m = m.onRefreshed(refreshedMsg{generation: m.generation})
	m = m.onRefreshed(refreshedMsg{generation: m.generation, err: provider.RateLimitErrorf(
		provider.RateLimitMetadata{ResetAt: c.now().Add(time.Hour)}, "github: rate limited")})
	generation, attempt := m.generation, m.lastAttempt
	next, cmd := m.onKey(tea.KeyPressMsg{Code: 'r'})
	if cmd != nil || next.(Model).generation != generation || !next.(Model).lastAttempt.Equal(attempt) || calls.Load() != 0 {
		t.Fatal("r bypassed a known rate-limit hold")
	}

	m.rateHold = rateHold{err: provider.Errorf(provider.KindRateLimit, "gitlab: rate limited"), manual: true}
	next, cmd = m.onKey(tea.KeyPressMsg{Code: 'r'})
	if cmd == nil || next.(Model).generation != generation+1 {
		t.Fatal("r did not admit exactly one manual-only retry")
	}
	cmd()
	if calls.Load() != 1 {
		t.Fatalf("manual r source calls = %d, want 1", calls.Load())
	}
}

func TestRatePolicyFooterParticipatesInFrameSizingAndFilterIndex(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	m := policyModel(t, c, time.Minute)
	m.hasData = true
	m.searching = true
	m.width = 20
	m.height = 15
	m.rateHold = rateHold{
		err:     provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: c.now().Add(time.Hour)}, "github: rate limited"),
		resetAt: c.now().Add(time.Hour),
	}
	lines := m.footerLines()
	if got, want := m.filterLineIndex(), 2; got != want {
		t.Fatalf("filter line index = %d, want %d", got, want)
	}
	if got, want := m.bodyHeight(), m.height-headerHeight-len(lines); got != want {
		t.Fatalf("body height = %d, want %d with policy footer", got, want)
	}
	if !strings.Contains(lines[1], "refresh held") {
		t.Errorf("narrow policy footer lost held status: %q", lines[1])
	}
}

func TestSuccessfulBudgetPolicyClearsAndExpiresOnlyAtItsReset(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	m := policyModel(t, c, time.Minute)
	reset := c.now().Add(2 * time.Minute)
	m = m.onRefreshed(refreshedMsg{generation: m.generation, input: policyReading(reset, 0)})
	if !m.rateHold.exhausted {
		t.Fatal("zero remaining budget did not create an exhausted hold")
	}
	c.advance(2 * time.Minute)
	next, allowed, _ := m.automaticRefreshAdmission()
	if !allowed || next.rateHold != (rateHold{}) {
		t.Fatal("expired exhausted budget did not return to normal automatic admission")
	}

	low := policyModel(t, c, time.Minute)
	low = low.onRefreshed(refreshedMsg{generation: low.generation, input: policyReading(c.now().Add(10*time.Minute), 1)})
	if !low.lowBudget.active(c.now()) {
		t.Fatal("low budget was not installed")
	}
	low = low.onRefreshed(refreshedMsg{generation: low.generation, err: provider.Errorf(provider.KindUnavailable, "network")})
	if !low.lowBudget.active(c.now()) {
		t.Fatal("ordinary failure cleared valid low budget")
	}
	low = low.onRefreshed(refreshedMsg{generation: low.generation, input: policyReading(c.now().Add(10*time.Minute), 101)})
	if low.lowBudget != (lowBudgetSchedule{}) || low.rateHold != (rateHold{}) {
		t.Fatal("recovered successful budget did not clear policy")
	}

	absent := policyModel(t, c, time.Minute)
	absent = absent.onRefreshed(refreshedMsg{generation: absent.generation, input: ListInput{RateLimitBudget: policyReading(c.now().Add(time.Minute), 0).RateLimitBudget}})
	if absent.rateHold != (rateHold{}) || absent.lowBudget != (lowBudgetSchedule{}) {
		t.Fatal("capability-absent budget changed policy")
	}
}

func TestQuittingDoesNotScheduleAnotherHeartbeat(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	m := policyModel(t, c, time.Minute)
	m.quitting = true
	if _, cmd := m.onHeartbeat(); cmd != nil {
		t.Fatal("quitting model scheduled another heartbeat")
	}
}

func TestRatePolicyIgnoresStaleResultsAndDoesNotCallSourceWhenHeld(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	var calls atomic.Int32
	m := New(t.Context(), Options{
		Interval:  time.Minute,
		Now:       c.now,
		Heartbeat: func() tea.Msg { return nil },
		Source: func(_ context.Context) (ListInput, error) {
			calls.Add(1)
			return ListInput{}, nil
		},
	})
	m = m.onRefreshed(refreshedMsg{generation: m.generation, input: ListInput{}})
	m.frontierGeneration = 7
	m.detailGeneration = 11
	m = m.onRefreshed(refreshedMsg{generation: m.generation, err: provider.RateLimitErrorf(
		provider.RateLimitMetadata{RetryAfter: time.Hour}, "github: limited")})
	if m.frontierGeneration != 7 || m.detailGeneration != 11 {
		t.Fatal("rate-limit policy changed Frontier or Detail cancellation generations")
	}
	before := m.rateHold
	m = m.onRefreshed(refreshedMsg{generation: m.generation - 1, input: policyReading(c.now().Add(time.Hour), 0)})
	if m.rateHold != before {
		t.Fatal("stale result changed rate policy")
	}
	if next, cmd := m.onHeartbeat(); cmd == nil || next.(Model).refreshing || calls.Load() != 0 {
		t.Fatal("held heartbeat launched Source work")
	}
}
