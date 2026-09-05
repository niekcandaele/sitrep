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
	if got := m.ratePolicyFooter(); got == "" || !strings.Contains(got, "Tracker requests are held") || !strings.Contains(got, "r is held") || strings.Contains(got, "API rate limit exceeded") {
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
	footer := next.ratePolicyFooter()
	if footer == "" || !strings.Contains(footer, "one explicit List, Detail, or Frontier request") ||
		!strings.Contains(footer, "Frontier may read the whole Watchlist") || strings.Contains(footer, "gitlab: rate limited") {
		t.Errorf("manual hold footer = %q", footer)
	}
	next.width, next.height = 120, 20
	body := next.renderBody(listMarkers{})
	if !strings.Contains(body, "Last Tracker request failed: network") || strings.Contains(body, "retrying in") {
		t.Errorf("manual probe failure body = %q", body)
	}
	loaded := next
	loaded.hasData = true
	loadedFooter := strings.Join(loaded.footerLines(), "\n")
	if !strings.Contains(loadedFooter, "refresh failed: network") ||
		!strings.Contains(loadedFooter, "another explicit action is required") ||
		strings.Contains(loadedFooter, "retrying in") {
		t.Errorf("manual probe failure footer = %q", loadedFooter)
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
	var gotPolicy provider.RequestPolicy
	m := New(t.Context(), Options{
		Interval:  time.Minute,
		Now:       c.now,
		Heartbeat: func() tea.Msg { return nil },
		Source: func(ctx context.Context) (ListInput, error) {
			calls.Add(1)
			gotPolicy = provider.RequestPolicyFromContext(ctx)
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
	if gotPolicy != (provider.RequestPolicy{Epoch: 1}) {
		t.Fatalf("initial fetch policy = %+v, want acknowledged epoch 1", gotPolicy)
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
		{"unknown", rateHold{manual: true}, "one explicit List, Detail, or Frontier request", "r is held"},
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

func TestInitialRateLimitErrorBodyUsesInstalledPolicy(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	reset := c.now().Add(time.Hour)
	for _, test := range []struct {
		name       string
		err        error
		want       string
		forbid     string
		heldManual bool
	}{
		{
			name:   "known reset",
			err:    provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: reset}, "github: rate limited"),
			want:   "Automatic refresh resumes at " + reset.Local().Format(time.Kitchen) + "; r is held",
			forbid: "Press r to try again",
		},
		{
			name:       "unknown reset",
			err:        provider.Errorf(provider.KindRateLimit, "gitlab: rate limited"),
			want:       "one explicit List, Detail, or Frontier request",
			forbid:     "r is held",
			heldManual: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := New(t.Context(), Options{
				Source:       func(context.Context) (ListInput, error) { return ListInput{}, nil },
				InitialError: test.err,
				Interval:     time.Minute,
				Now:          c.now,
				Heartbeat:    func() tea.Msg { return nil },
			})
			m.width, m.height, m.ready = 120, 20, true
			if m.refreshing || m.rateHold.manual != test.heldManual {
				t.Fatalf("InitialError policy = refreshing:%t hold:%+v", m.refreshing, m.rateHold)
			}
			body := m.View().Content
			if !strings.Contains(body, test.want) || strings.Contains(body, test.forbid) {
				t.Fatalf("InitialError body = %q", body)
			}

			m, cmd := pressListKey(t, m, 'p')
			if cmd != nil || !m.refreshHeld {
				t.Fatalf("holding InitialError model = held:%t cmd:%v", m.refreshHeld, cmd)
			}
			held := m.View().Content
			if !strings.Contains(held, "Monitor automatic refresh is held") || strings.Contains(held, "Automatic refresh resumes at") {
				t.Fatalf("held InitialError body = %q", held)
			}
			if test.heldManual && (!strings.Contains(held, "press r to retry this List") ||
				!strings.Contains(held, "one explicit List, Detail, or Frontier request")) {
				t.Fatalf("held manual-only InitialError lost r remedy: %q", held)
			}
			if !test.heldManual && (!strings.Contains(held, "r is held") || strings.Contains(held, "Press r to try again")) {
				t.Fatalf("held known InitialError weakened r restriction: %q", held)
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
	if !strings.Contains(view.Content, "Tracker requests are held") {
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
	if !strings.Contains(lines[1], "Tracker requests") {
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

func TestElapsedSuccessfulExhaustedBudgetDoesNotScheduleLowBudget(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	for _, resetAt := range []time.Time{c.now(), c.now().Add(-time.Minute)} {
		m := policyModel(t, c, time.Minute)
		m.lowBudget = lowBudgetSchedule{
			resetAt: c.now().Add(time.Hour), nextAt: c.now().Add(time.Minute), cadence: time.Minute,
		}
		m = m.applySuccessfulBudgetPolicy(policyReading(resetAt, 0), provider.RequestPolicy{})
		if m.rateHold != (rateHold{}) || m.lowBudget != (lowBudgetSchedule{}) {
			t.Errorf("elapsed exhausted budget at %s left policy hold=%+v low=%+v",
				resetAt, m.rateHold, m.lowBudget)
		}
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

func TestTerminalFocusDefaultsToFocused(t *testing.T) {
	m := policyModel(t, &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}, time.Minute)
	if !m.terminalFocused {
		t.Fatal("new monitor is not optimistically focused")
	}
}

func TestTerminalFocusExtendsOnlyAutomaticAdmission(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	var calls atomic.Int32
	m := New(t.Context(), Options{
		Interval: time.Minute, Now: c.now, Heartbeat: func() tea.Msg { return nil },
		Source: func(context.Context) (ListInput, error) {
			calls.Add(1)
			return ListInput{}, nil
		},
	})
	m = m.onRefreshed(refreshedMsg{generation: m.generation, input: ListInput{FetchedAt: c.now()}})
	c.advance(time.Minute)

	blurred, cmd := m.Update(tea.BlurMsg{})
	m = blurred.(Model)
	if cmd != nil || m.terminalFocused {
		t.Fatal("blur did not only record unfocused state")
	}
	generation := m.generation
	if next, cmd := m.onHeartbeat(); cmd == nil || next.(Model).refreshing || next.(Model).generation != generation || calls.Load() != 0 {
		t.Fatal("blurred heartbeat started automatic work")
	}

	manual, cmd := m.Update(tea.KeyPressMsg{Code: 'r'})
	m = manual.(Model)
	if cmd == nil || !m.refreshing || m.terminalFocused {
		t.Fatal("blurred List r was not admitted by the existing manual policy")
	}
	if got := cmd(); got == nil || calls.Load() != 1 {
		t.Fatal("blurred manual refresh did not call Source exactly once")
	}
	m = m.onRefreshed(refreshedMsg{generation: m.generation, input: ListInput{FetchedAt: c.now()}})
	c.advance(time.Minute)

	focused, cmd := m.Update(tea.FocusMsg{})
	m = focused.(Model)
	if cmd == nil || !m.refreshing || !m.terminalFocused || m.generation != generation+2 {
		t.Fatalf("due focus regain = focused:%t refreshing:%t generation:%d", m.terminalFocused, m.refreshing, m.generation)
	}
	if duplicate, duplicateCmd := m.Update(tea.FocusMsg{}); duplicateCmd != nil || duplicate.(Model).generation != m.generation {
		t.Fatal("duplicate focus started additional work")
	}
	m.refreshing = false
	m.lastAttempt = c.now().Add(-time.Minute)
	if duplicate, duplicateCmd := m.Update(tea.FocusMsg{}); duplicateCmd != nil || duplicate.(Model).generation != m.generation {
		t.Fatal("duplicate focused report rechecked automatic admission")
	}
}

func TestMonitorPauseAndFocusGateOnlyAutomaticListRequests(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	for _, test := range []struct {
		name        string
		refreshHeld bool
		focused     bool
	}{
		{name: "user-held automatic refresh", refreshHeld: true, focused: true},
		{name: "unfocused terminal", focused: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			newModel := func() Model {
				m := phase2AdmissionModel(t, clock, &phase2Calls{})
				m.refreshHeld = test.refreshHeld
				m.terminalFocused = test.focused
				m.lastAttempt = clock.now().Add(-time.Hour)
				return m
			}

			m := newModel()
			if next, allowed, _ := m.automaticRefreshAdmission(); allowed || next.refreshing {
				t.Fatal("automatic List request bypassed Monitor-only gate")
			}
			m = newModel()
			if next, cmd := m.openDetail(); cmd == nil || !next.(Model).detail.loading {
				t.Fatal("Monitor-only gate denied explicit Detail request")
			}
			m = newModel()
			if next, cmd := m.enterFrontier(); cmd == nil || next.(Model).frontierContext == nil {
				t.Fatal("Monitor-only gate denied explicit Frontier request")
			}
		})
	}
}

func TestFocusRegainRespectsDueAndRatePolicy(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	for _, test := range []struct {
		name    string
		advance time.Duration
		hold    rateHold
		low     lowBudgetSchedule
	}{
		{name: "before due", advance: 30 * time.Second},
		{name: "known hold", advance: time.Minute, hold: rateHold{resetAt: c.now().Add(time.Hour)}},
		{name: "unknown hold", advance: time.Minute, hold: rateHold{manual: true}},
		{name: "low budget", advance: time.Minute, low: lowBudgetSchedule{resetAt: c.now().Add(time.Hour), nextAt: c.now().Add(2 * time.Hour)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := policyModel(t, c, time.Minute)
			m = m.onRefreshed(refreshedMsg{generation: m.generation, input: ListInput{FetchedAt: c.now()}})
			m.rateHold, m.lowBudget = test.hold, test.low
			blurred, _ := m.Update(tea.BlurMsg{})
			m = blurred.(Model)
			c.advance(test.advance)
			focused, cmd := m.Update(tea.FocusMsg{})
			if cmd != nil || focused.(Model).refreshing {
				t.Fatalf("focus regain bypassed %s policy", test.name)
			}
			c.advance(-test.advance)
		})
	}
}

func TestBlurredStatusIsTruthfulAndStalenessAdvances(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	m := policyModel(t, c, time.Minute)
	m = m.onRefreshed(refreshedMsg{generation: m.generation, input: ListInput{FetchedAt: c.now()}})
	m.hasData = true
	m.width, m.height, m.ready = 120, 24, true
	blurred, _ := m.Update(tea.BlurMsg{})
	m = blurred.(Model)
	m.lastErr = provider.Errorf(provider.KindUnavailable, "network")
	footer := strings.Join(m.footerLines(), "\n")
	if !strings.Contains(footer, "automatic refresh paused: terminal unfocused") || strings.Contains(footer, "retrying in") {
		t.Fatalf("blurred generic failure = %q", footer)
	}
	m.refreshing = true
	if footer = strings.Join(m.footerLines(), "\n"); strings.Contains(footer, "refresh failed:") || strings.Contains(footer, "paused: terminal unfocused") || m.staleness() != "refreshing…" {
		t.Fatalf("blurred in-flight generic status did not defer to refreshing: %q", footer)
	}
	m.refreshing = false
	c.advance(2 * time.Minute)
	if got := m.focusPauseFooter(); !strings.Contains(got, "updated 2m ago") {
		t.Fatalf("blurred pause status does not advance staleness: %q", got)
	}
	noData := policyModel(t, c, time.Minute)
	noData.refreshing = false
	noData.lastErr = provider.Errorf(provider.KindUnavailable, "network")
	blurredNoData, _ := noData.Update(tea.BlurMsg{})
	if got := blurredNoData.(Model).retryBodyHint(); !strings.Contains(got, "paused while terminal is unfocused") || strings.Contains(got, "resumes at") {
		t.Fatalf("blurred no-data hint = %q", got)
	}
	for _, test := range []struct {
		name string
		hold rateHold
		low  lowBudgetSchedule
		want string
	}{
		{"known", rateHold{err: provider.Errorf(provider.KindRateLimit, "limited"), resetAt: c.now().Add(time.Hour)}, lowBudgetSchedule{}, "r is held"},
		{"unknown", rateHold{err: provider.Errorf(provider.KindRateLimit, "limited"), manual: true}, lowBudgetSchedule{}, "one explicit List, Detail, or Frontier request"},
		{"exhausted", rateHold{exhausted: true, resetAt: c.now().Add(time.Hour)}, lowBudgetSchedule{}, "budget exhausted"},
		{"low", rateHold{}, lowBudgetSchedule{resetAt: c.now().Add(time.Hour), nextAt: c.now().Add(30 * time.Minute)}, "refresh slowed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m.rateHold, m.lowBudget = test.hold, test.low
			got := m.ratePolicyFooter()
			hardHold := test.hold != (rateHold{})
			if hardHold {
				if !strings.Contains(got, "Tracker requests are held") || !strings.Contains(got, test.want) ||
					strings.Contains(got, "limited") || strings.Contains(got, "paused: terminal unfocused") {
					t.Fatalf("blurred %s policy = %q", test.name, got)
				}
			} else if !strings.Contains(got, "paused: terminal unfocused") || !strings.Contains(got, test.want) ||
				strings.Contains(got, "next automatic refresh") {
				t.Fatalf("blurred %s policy = %q", test.name, got)
			}
			m.refreshing = true
			got = m.ratePolicyFooter()
			if hardHold {
				if !strings.Contains(got, "Tracker requests are held") {
					t.Fatalf("blurred in-flight %s hid global Tracker policy: %q", test.name, got)
				}
			} else if got != "" {
				t.Fatalf("blurred in-flight %s policy rendered beside refreshing state: %q", test.name, got)
			}
			m.refreshing = false
		})
	}
}

func TestBlurredUnknownManualRefreshKeepsGlobalPolicyVisible(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	m := policyModel(t, c, time.Minute)
	m = m.onRefreshed(refreshedMsg{generation: m.generation, input: ListInput{FetchedAt: c.now()}})
	m.rateHold = rateHold{err: provider.Errorf(provider.KindRateLimit, "limited"), manual: true}
	blurred, _ := m.Update(tea.BlurMsg{})
	m = blurred.(Model)
	manual, cmd := m.onKey(tea.KeyPressMsg{Code: 'r'})
	m = manual.(Model)
	if cmd == nil || !m.refreshing || !strings.Contains(m.ratePolicyFooter(), "explicit retry is in progress") ||
		m.focusPauseFooter() != "" || m.staleness() != "refreshing…" {
		t.Fatal("blurred unknown-hold manual refresh hid global Tracker policy")
	}
	m = m.onRefreshed(refreshedMsg{generation: m.generation, err: provider.Errorf(provider.KindUnavailable, "network")})
	if got := m.ratePolicyFooter(); !strings.Contains(got, "Tracker requests are held") ||
		!strings.Contains(got, "one explicit List, Detail, or Frontier request") || strings.Contains(got, "limited") {
		t.Fatalf("settled unknown-hold manual refresh lost paused policy: %q", got)
	}
}

func TestFocusMessagesPreserveScreenState(t *testing.T) {
	m := policyModel(t, &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}, time.Minute)
	m.mode, m.searching, m.mouseEpoch = modeFrontier, true, 9
	m.frontierGeneration, m.detailGeneration = 7, 11
	m.refreshing, m.generation = true, 13
	m.legendVisible = false
	blurred, _ := m.Update(tea.BlurMsg{})
	next := blurred.(Model)
	if !next.refreshing || next.generation != 13 {
		t.Fatal("blur cancelled or resequenced in-flight Watchlist work")
	}
	if next.mode != m.mode || next.searching != m.searching || next.mouseEpoch != m.mouseEpoch || next.frontierGeneration != m.frontierGeneration || next.detailGeneration != m.detailGeneration || next.legendVisible != m.legendVisible {
		t.Fatal("blur routed into screen navigation, cancellation, or legend state")
	}
	focused, _ := next.Update(tea.FocusMsg{})
	next = focused.(Model)
	if next.mode != m.mode || next.searching != m.searching || next.mouseEpoch != m.mouseEpoch || next.frontierGeneration != m.frontierGeneration || next.detailGeneration != m.detailGeneration || next.legendVisible != m.legendVisible {
		t.Fatal("focus routed into screen navigation, cancellation, or legend state")
	}
}

func TestViewAlwaysReportsFocus(t *testing.T) {
	for _, test := range []struct {
		name  string
		ready bool
		mode  mode
	}{
		{name: "not ready"},
		{name: "list", ready: true, mode: modeList},
		{name: "detail", ready: true, mode: modeDetail},
		{name: "frontier", ready: true, mode: modeFrontier},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := policyModel(t, &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}, time.Minute)
			m.ready, m.mode, m.width, m.height = test.ready, test.mode, 80, 24
			if !m.View().ReportFocus {
				t.Fatal("View did not request terminal focus reports")
			}
		})
	}
}

func refreshHoldModel(t *testing.T, c *policyClock, interval time.Duration, calls *atomic.Int32) Model {
	t.Helper()
	m := New(t.Context(), Options{
		Interval: interval,
		Now:      c.now,
		Heartbeat: func() tea.Msg {
			return heartbeatMsg(c.now())
		},
		Source: func(context.Context) (ListInput, error) {
			calls.Add(1)
			return ListInput{FetchedAt: c.now()}, nil
		},
	})
	return m.onRefreshed(refreshedMsg{
		generation: m.generation,
		input:      ListInput{FetchedAt: c.now()},
	})
}

func pressListKey(t *testing.T, m Model, code rune) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.onKey(tea.KeyPressMsg{Code: code, Text: string(code)})
	return next.(Model), cmd
}

func TestRefreshHoldBlocksHeartbeatsAndResumesExactlyOnce(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	var calls atomic.Int32
	m := refreshHoldModel(t, c, time.Minute, &calls)
	generation, attempt := m.generation, m.lastAttempt
	c.advance(10 * time.Second)

	m, cmd := pressListKey(t, m, 'p')
	if cmd != nil || !m.refreshHeld || m.keys.ToggleRefreshHold.Help().Desc != "resume refresh" ||
		m.generation != generation || !m.lastAttempt.Equal(attempt) {
		t.Fatalf("setting hold changed scheduler state: held=%t generation=%d attempt=%s help=%q cmd=%v",
			m.refreshHeld, m.generation, m.lastAttempt, m.keys.ToggleRefreshHold.Help().Desc, cmd)
	}

	c.advance(2 * time.Minute)
	for range 3 {
		next, heartbeatCmd := m.onHeartbeat()
		m = next.(Model)
		if heartbeatCmd == nil || m.refreshing || m.generation != generation {
			t.Fatal("held heartbeat did not only re-arm")
		}
		if _, ok := heartbeatCmd().(heartbeatMsg); !ok {
			t.Fatal("held heartbeat did not schedule the next heartbeat")
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("held heartbeats made %d Source calls", calls.Load())
	}
	if got := m.listStaleness(); got != "monitor held · updated 2m ago" {
		t.Fatalf("held List staleness = %q", got)
	}

	m, cmd = pressListKey(t, m, 'p')
	if cmd == nil || m.refreshHeld || !m.refreshing || m.generation != generation+1 || !m.lastAttempt.Equal(c.now()) {
		t.Fatalf("overdue release = held:%t refreshing:%t generation:%d attempt:%s cmd:%v",
			m.refreshHeld, m.refreshing, m.generation, m.lastAttempt, cmd)
	}
	result, ok := cmd().(refreshedMsg)
	if !ok || calls.Load() != 1 {
		t.Fatalf("overdue release Source calls = %d, result=%T", calls.Load(), result)
	}
	if next, heartbeatCmd := m.onHeartbeat(); heartbeatCmd == nil || next.(Model).generation != m.generation || calls.Load() != 1 {
		t.Fatal("queued heartbeat overlapped the release refresh")
	}
	m = m.onRefreshed(result)
	if next, heartbeatCmd := m.onHeartbeat(); heartbeatCmd == nil || next.(Model).refreshing || calls.Load() != 1 {
		t.Fatal("settled release scheduled a second refresh before cadence")
	}
}

func TestRefreshHoldDoesNotCancelOrFollowUpInFlightWork(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	var calls atomic.Int32
	m := refreshHoldModel(t, c, time.Minute, &calls)
	m.refreshing, m.generation = true, 13
	m.frontierGeneration, m.detailGeneration = 7, 11
	m.detailFanoutInflight = 3
	detailContext, cancelDetail := context.WithCancel(t.Context())
	frontierContext, cancelFrontier := context.WithCancel(t.Context())
	defer cancelDetail()
	defer cancelFrontier()
	m.detailContext, m.cancelDetail = detailContext, cancelDetail
	m.frontierContext, m.cancelFrontier = frontierContext, cancelFrontier
	attempt := m.lastAttempt

	m, cmd := pressListKey(t, m, 'p')
	if cmd != nil || !m.refreshHeld || !m.refreshing || m.generation != 13 ||
		m.frontierGeneration != 7 || m.detailGeneration != 11 || m.detailFanoutInflight != 3 ||
		m.detailContext != detailContext || m.frontierContext != frontierContext || !m.lastAttempt.Equal(attempt) {
		t.Fatalf("setting hold resequenced in-flight work: %+v", m)
	}
	m = m.onRefreshed(refreshedMsg{generation: 13, input: ListInput{FetchedAt: c.now()}})
	if !m.refreshHeld || m.refreshing {
		t.Fatal("in-flight completion did not settle under the existing hold")
	}
	c.advance(time.Minute)
	if next, heartbeatCmd := m.onHeartbeat(); heartbeatCmd == nil || next.(Model).refreshing || calls.Load() != 0 {
		t.Fatal("in-flight completion scheduled an automatic follow-up while held")
	}
}

func TestRefreshHoldReleasePreservesAutomaticBlockers(t *testing.T) {
	start := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	for _, test := range []struct {
		name       string
		prepare    func(Model, *policyClock) Model
		advance    time.Duration
		wantLaunch bool
	}{
		{name: "before due", advance: 30 * time.Second},
		{name: "terminal unfocused", advance: 2 * time.Minute, prepare: func(m Model, _ *policyClock) Model {
			m.terminalFocused = false
			return m
		}},
		{name: "positive low budget", advance: 2 * time.Minute, prepare: func(m Model, c *policyClock) Model {
			m.lowBudget = lowBudgetSchedule{resetAt: c.now().Add(time.Hour), nextAt: c.now().Add(10 * time.Minute)}
			return m
		}},
		{name: "unknown manual-only rate hold", advance: 2 * time.Minute, prepare: func(m Model, _ *policyClock) Model {
			m.rateHold = rateHold{err: provider.Errorf(provider.KindRateLimit, "limited"), manual: true}
			return m
		}},
		{name: "known rate hold", advance: 2 * time.Minute, prepare: func(m Model, c *policyClock) Model {
			m.rateHold = rateHold{err: provider.Errorf(provider.KindRateLimit, "limited"), resetAt: c.now().Add(10 * time.Minute)}
			return m
		}},
		{name: "expired known hold", advance: 2 * time.Minute, wantLaunch: true, prepare: func(m Model, c *policyClock) Model {
			m.rateHold = rateHold{err: provider.Errorf(provider.KindRateLimit, "limited"), resetAt: c.now().Add(time.Minute)}
			return m
		}},
		{name: "expired exhausted budget", advance: 2 * time.Minute, wantLaunch: true, prepare: func(m Model, c *policyClock) Model {
			m.rateHold = rateHold{resetAt: c.now().Add(time.Minute), exhausted: true}
			return m
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := &policyClock{value: start}
			var calls atomic.Int32
			m := refreshHoldModel(t, c, time.Hour, &calls)
			if test.prepare != nil {
				m = test.prepare(m, c)
			}
			attempt := m.lastAttempt
			m, _ = pressListKey(t, m, 'p')
			c.advance(test.advance)
			// A beat after an elapsed reset must not consume the immediate-due edge.
			mNext, heartbeatCmd := m.onHeartbeat()
			m = mNext.(Model)
			if heartbeatCmd == nil || calls.Load() != 0 || !m.lastAttempt.Equal(attempt) {
				t.Fatal("held heartbeat changed Source or cadence")
			}
			m, releaseCmd := pressListKey(t, m, 'p')
			if got := releaseCmd != nil; got != test.wantLaunch {
				t.Fatalf("release launched=%t, want %t; state=%+v", got, test.wantLaunch, m)
			}
			if test.wantLaunch {
				if !m.refreshing || m.rateHold != (rateHold{}) {
					t.Fatalf("expired hold release state = refreshing:%t rate:%+v", m.refreshing, m.rateHold)
				}
			} else if m.refreshing || !m.lastAttempt.Equal(attempt) {
				t.Fatal("blocked release changed refresh or cadence")
			}
		})
	}
}

func TestExpiredRateDeadlineSurvivesHoldReleaseWhileBlurred(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		name := "known refusal"
		if exhausted {
			name = "exhausted budget"
		}
		t.Run(name, func(t *testing.T) {
			c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
			var calls atomic.Int32
			m := refreshHoldModel(t, c, time.Hour, &calls)
			m.rateHold = rateHold{
				err:       provider.Errorf(provider.KindRateLimit, "limited"),
				resetAt:   c.now().Add(time.Minute),
				exhausted: exhausted,
			}
			m, _ = pressListKey(t, m, 'p')
			blurred, _ := m.Update(tea.BlurMsg{})
			m = blurred.(Model)
			c.advance(2 * time.Minute)
			m, releaseCmd := pressListKey(t, m, 'p')
			if releaseCmd != nil || m.refreshing || m.rateHold == (rateHold{}) {
				t.Fatalf("blurred release consumed expired deadline: refreshing=%t hold=%+v cmd=%v", m.refreshing, m.rateHold, releaseCmd)
			}
			focused, cmd := m.Update(tea.FocusMsg{})
			m = focused.(Model)
			if cmd == nil || !m.refreshing || m.rateHold != (rateHold{}) || m.generation != 2 {
				t.Fatalf("focus did not consume expired deadline exactly once: refreshing=%t generation=%d hold=%+v cmd=%v",
					m.refreshing, m.generation, m.rateHold, cmd)
			}
			if result := cmd(); calls.Load() != 1 {
				t.Fatalf("expired-deadline focus made %d Source calls, want one (result %T)", calls.Load(), result)
			}
			if duplicate, duplicateCmd := m.Update(tea.FocusMsg{}); duplicateCmd != nil || duplicate.(Model).generation != m.generation || calls.Load() != 1 {
				t.Fatal("duplicate focus launched a second expired-deadline refresh")
			}
		})
	}
}

func TestManualRefreshWhileHeldPreservesHoldAndRateSemantics(t *testing.T) {
	start := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	for _, test := range []struct {
		name    string
		policy  func(Model, *policyClock) Model
		allowed bool
	}{
		{name: "ordinary", allowed: true},
		{name: "blurred", allowed: true, policy: func(m Model, _ *policyClock) Model {
			m.terminalFocused = false
			return m
		}},
		{name: "positive low budget", allowed: true, policy: func(m Model, c *policyClock) Model {
			m.lowBudget = lowBudgetSchedule{resetAt: c.now().Add(time.Hour), nextAt: c.now().Add(time.Hour)}
			return m
		}},
		{name: "unknown manual-only hold", allowed: true, policy: func(m Model, _ *policyClock) Model {
			m.rateHold = rateHold{err: provider.Errorf(provider.KindRateLimit, "limited"), manual: true}
			return m
		}},
		{name: "known hold", policy: func(m Model, c *policyClock) Model {
			m.rateHold = rateHold{err: provider.Errorf(provider.KindRateLimit, "limited"), resetAt: c.now().Add(time.Hour)}
			return m
		}},
		{name: "exhausted budget", policy: func(m Model, c *policyClock) Model {
			m.rateHold = rateHold{resetAt: c.now().Add(time.Hour), exhausted: true}
			return m
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := &policyClock{value: start}
			var calls atomic.Int32
			m := refreshHoldModel(t, c, time.Minute, &calls)
			m, _ = pressListKey(t, m, 'p')
			if test.policy != nil {
				m = test.policy(m, c)
			}
			generation, attempt := m.generation, m.lastAttempt
			m, cmd := pressListKey(t, m, 'r')
			if got := cmd != nil; got != test.allowed {
				t.Fatalf("manual r allowed=%t, want %t", got, test.allowed)
			}
			if !m.refreshHeld {
				t.Fatal("manual r released user hold")
			}
			if !test.allowed {
				if m.generation != generation || !m.lastAttempt.Equal(attempt) {
					t.Fatal("blocked manual r changed scheduler state")
				}
				return
			}
			if !m.refreshing || m.generation != generation+1 || !m.lastAttempt.Equal(c.now()) {
				t.Fatal("allowed manual r did not start exactly one refresh")
			}
			result := cmd().(refreshedMsg)
			if calls.Load() != 1 {
				t.Fatalf("manual r Source calls = %d", calls.Load())
			}
			m = m.onRefreshed(result)
			c.advance(time.Minute)
			if next, heartbeatCmd := m.onHeartbeat(); heartbeatCmd == nil || next.(Model).refreshing || calls.Load() != 1 {
				t.Fatal("held monitor auto-followed a manual refresh")
			}
		})
	}
}

func TestRefreshHoldStatusSuppressesAutomaticPromises(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	var calls atomic.Int32
	m := refreshHoldModel(t, c, time.Minute, &calls)
	m.hasData = true
	m.width, m.height, m.ready = 120, 24, true
	m, _ = pressListKey(t, m, 'p')
	m.lastErr = provider.Errorf(provider.KindUnavailable, "network")
	footer := strings.Join(m.footerLines(), "\n")
	if !strings.Contains(footer, "refresh failed: network · monitor held") || strings.Contains(footer, "retrying in") {
		t.Fatalf("held generic failure footer = %q", footer)
	}
	for _, width := range []int{42, 60, 80, 120} {
		m.width = width
		headerLine := strings.Split(m.View().Content, "\n")[1]
		if !strings.Contains(headerLine, "monitor held") {
			t.Errorf("%d-column List header lost user hold: %q", width, headerLine)
		}
	}
	m.width = 120

	m.refreshing = true
	if got := m.listStaleness(); got != "refreshing…" {
		t.Fatalf("in-flight hold status = %q", got)
	}
	m.refreshing = false
	m.terminalFocused = false
	c.advance(2 * time.Minute)
	if got := m.listStaleness(); got != "monitor held · terminal unfocused · updated 2m ago" {
		t.Fatalf("blurred held List status = %q", got)
	}
	if got := m.focusPauseFooter(); got != "monitor held · terminal unfocused · updated 2m ago" {
		t.Fatalf("blurred held footer status = %q", got)
	}

	m.terminalFocused = true
	m.lastErr = provider.Errorf(provider.KindRateLimit, "limited")
	for _, test := range []struct {
		name   string
		hold   rateHold
		low    lowBudgetSchedule
		want   string
		forbid string
	}{
		{name: "known", hold: rateHold{err: m.lastErr, resetAt: c.now().Add(time.Hour)}, want: "r is held", forbid: "limited"},
		{name: "exhausted", hold: rateHold{resetAt: c.now().Add(time.Hour), exhausted: true}, want: "budget exhausted", forbid: "retrying at"},
		{name: "unknown", hold: rateHold{err: m.lastErr, manual: true}, want: "one explicit List, Detail, or Frontier request", forbid: "limited"},
		{name: "low", low: lowBudgetSchedule{resetAt: c.now().Add(time.Hour), nextAt: c.now().Add(30 * time.Minute)}, want: "refresh slowed", forbid: "next automatic refresh"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m.rateHold, m.lowBudget = test.hold, test.low
			got := m.ratePolicyFooter()
			if !strings.Contains(got, test.want) || strings.Contains(got, test.forbid) {
				t.Fatalf("held policy footer = %q", got)
			}
		})
	}

	m.hasData = false
	m.rateHold = rateHold{err: m.lastErr, resetAt: c.now().Add(time.Hour)}
	if got := m.retryBodyHint(); !strings.Contains(got, "Monitor automatic refresh is held") || !strings.Contains(got, "r is held") || strings.Contains(got, "resumes at") {
		t.Fatalf("first-fetch hard-hold hint = %q", got)
	}
	m.rateHold = rateHold{err: m.lastErr, manual: true}
	if got := m.retryBodyHint(); !strings.Contains(got, "r to retry this List") || !strings.Contains(got, "p to resume it") || strings.Contains(got, "release hold") {
		t.Fatalf("first-fetch manual-hold hint = %q", got)
	}
	m.refreshHeld = false
	if got := m.retryBodyHint(); !strings.Contains(got, "r to retry this List") || strings.Contains(got, " p ") || strings.Contains(got, "release") || strings.Contains(got, "resume") {
		t.Fatalf("unheld Monitor advertised p as Tracker release: %q", got)
	}
}

func TestRefreshHeldTrackerNoticeMatchesActiveScreen(t *testing.T) {
	clock := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	var calls atomic.Int32
	base := refreshHoldModel(t, clock, time.Minute, &calls)
	base.refreshHeld = true

	holds := []struct {
		name       string
		hold       rateHold
		probeToken uint64
		wantR      string
		forbidR    string
	}{
		{
			name:    "known reset",
			hold:    rateHold{resetAt: clock.now().Add(time.Hour)},
			wantR:   "r is held",
			forbidR: "Press r to try",
		},
		{
			name:    "exhausted budget",
			hold:    rateHold{resetAt: clock.now().Add(time.Hour), exhausted: true},
			wantR:   "r is held",
			forbidR: "Press r to try",
		},
		{
			name:    "unknown reset",
			hold:    rateHold{manual: true},
			wantR:   "one explicit List, Detail, or Frontier request",
			forbidR: "r is held",
		},
		{
			name:       "unknown probe in progress",
			hold:       rateHold{manual: true},
			probeToken: 1,
			wantR:      "r is held",
			forbidR:    "Press r to try",
		},
	}
	modes := []struct {
		name string
		mode mode
	}{
		{name: "List", mode: modeList},
		{name: "Detail", mode: modeDetail},
		{name: "Frontier", mode: modeFrontier},
	}

	for _, screen := range modes {
		for _, policy := range holds {
			t.Run(screen.name+"/"+policy.name, func(t *testing.T) {
				m := base
				m.mode = screen.mode
				m.rateHold = policy.hold
				m.unknownProbeToken = policy.probeToken
				notice := m.trackerHoldNotice()
				if !strings.Contains(notice, policy.wantR) || strings.Contains(notice, policy.forbidR) {
					t.Fatalf("r guidance = %q", notice)
				}
				if screen.mode == modeList {
					if !strings.Contains(notice, "press p to resume it") {
						t.Fatalf("List lost p guidance: %q", notice)
					}
					return
				}
				if !strings.Contains(notice, "Automatic List monitoring is also held") ||
					!strings.Contains(notice, "resume it from the List") ||
					strings.Contains(strings.ToLower(notice), "press p") {
					t.Fatalf("%s advertised an unavailable p action: %q", screen.name, notice)
				}
			})
		}
	}
}

func TestRefreshHoldKeyIsListOnlyAndPreservesScreenWork(t *testing.T) {
	c := &policyClock{value: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	var calls atomic.Int32
	m := refreshHoldModel(t, c, time.Minute, &calls)
	m.frontierGeneration, m.detailGeneration = 7, 11
	m.detailFanoutInflight = 3

	next, cmd := pressListKey(t, m, 'P')
	if cmd != nil || next.refreshHeld || next.generation != m.generation {
		t.Fatal("uppercase P changed List hold state")
	}

	m.searching = true
	m.search.Focus()
	searched, _ := m.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	m = searched.(Model)
	if m.refreshHeld || m.search.Value() != "p" {
		t.Fatalf("search p = value:%q held:%t", m.search.Value(), m.refreshHeld)
	}
	m.searching = false
	m.search.Blur()
	m.search.SetValue("")

	m, _ = pressListKey(t, m, 'p')
	before := m
	for _, screen := range []mode{modeDetail, modeFrontier} {
		m.mode = screen
		for _, code := range []rune{'p', 'P'} {
			updated, screenCmd := m.Update(tea.KeyPressMsg{Code: code, Text: string(code)})
			m = updated.(Model)
			if screenCmd != nil || !m.refreshHeld || m.frontierGeneration != before.frontierGeneration ||
				m.detailGeneration != before.detailGeneration || m.detailFanoutInflight != before.detailFanoutInflight {
				t.Fatalf("%c changed %v work while hold was set", code, screen)
			}
		}
		heartbeat, heartbeatCmd := m.onHeartbeat()
		m = heartbeat.(Model)
		if heartbeatCmd == nil || m.refreshing || calls.Load() != 0 ||
			m.frontierGeneration != before.frontierGeneration || m.detailGeneration != before.detailGeneration {
			t.Fatalf("held heartbeat changed %v work", screen)
		}
	}
	m.mode = modeList
	blurred, _ := m.Update(tea.BlurMsg{})
	m = blurred.(Model)
	c.advance(2 * time.Minute)
	focused, focusCmd := m.Update(tea.FocusMsg{})
	m = focused.(Model)
	if focusCmd != nil || !m.refreshHeld || m.refreshing || calls.Load() != 0 {
		t.Fatal("focus regain overrode the user hold")
	}
	if next, heartbeatCmd := m.onHeartbeat(); heartbeatCmd == nil || next.(Model).refreshing || calls.Load() != 0 {
		t.Fatal("hold did not survive screen transitions")
	}
}
