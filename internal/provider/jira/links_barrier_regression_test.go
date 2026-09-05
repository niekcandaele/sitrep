package jira

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
)

type linkTypesWaitSignalContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *linkTypesWaitSignalContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestLinkTypesPreBarrierWaitersShareRefusingAttempt(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name       string
		knownReset bool
		elapsed    bool
	}{
		{name: "unknown reset"},
		{name: "known reset", knownReset: true},
		{name: "elapsed reset", knownReset: true, elapsed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var catalogueCalls atomic.Int64
			firstStarted := make(chan struct{})
			releaseFirst := make(chan struct{})
			var firstOnce sync.Once
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != apiBase+linkTypesPath {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				call := catalogueCalls.Add(1)
				if call == 1 {
					firstOnce.Do(func() { close(firstStarted) })
					<-releaseFirst
					if tt.knownReset {
						reset := now.Add(time.Minute)
						if tt.elapsed {
							reset = now.Add(-time.Minute)
						}
						w.Header().Set("X-RateLimit-Reset", reset.Format(time.RFC3339))
					}
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"issueLinkTypes":[]}`))
			}))
			t.Cleanup(s.Close)
			p := New("example.test",
				WithBaseURL(s.URL),
				WithCredentials(Credentials{Email: "test@example.test", Token: "token"}),
				WithNow(func() time.Time { return now }),
			)
			policyContext := func(parent context.Context, epoch uint64) context.Context {
				return provider.WithRequestPolicy(parent, provider.RequestPolicy{Epoch: epoch})
			}

			loaderResult := make(chan error, 1)
			go func() {
				_, err := p.linkTypes(policyContext(t.Context(), 4))
				loaderResult <- err
			}()
			select {
			case <-firstStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("initial catalogue attempt did not start")
			}

			waiterWaiting := make(chan struct{})
			waiterContext := &linkTypesWaitSignalContext{Context: t.Context(), waiting: waiterWaiting}
			waiterResult := make(chan error, 1)
			go func() {
				_, err := p.linkTypes(policyContext(waiterContext, 5))
				waiterResult <- err
			}()
			select {
			case <-waiterWaiting:
			case <-time.After(5 * time.Second):
				t.Fatal("newer-epoch caller did not wait on the refusing attempt")
			}

			cancelBase, cancel := context.WithCancel(t.Context())
			cancelWaiting := make(chan struct{})
			cancelContext := &linkTypesWaitSignalContext{Context: cancelBase, waiting: cancelWaiting}
			cancelResult := make(chan error, 1)
			go func() {
				_, err := p.linkTypes(policyContext(cancelContext, 6))
				cancelResult <- err
			}()
			select {
			case <-cancelWaiting:
			case <-time.After(5 * time.Second):
				t.Fatal("cancellable caller did not wait on the attempt")
			}
			cancel()
			close(releaseFirst)
			if err := <-cancelResult; !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled waiter = %v, want context.Canceled", err)
			}

			loaderErr := <-loaderResult
			waiterErr := <-waiterResult
			for name, err := range map[string]error{"loader": loaderErr, "pre-barrier waiter": waiterErr} {
				refusal, ok := provider.InspectRateLimitRefusal(err, now)
				wantKnown := tt.knownReset && !tt.elapsed
				if !ok || refusal.KnownReset != wantKnown || refusal.ExpiredOnly != tt.elapsed {
					t.Errorf("%s refusal = %+v/%t from %v", name, refusal, ok, err)
				}
			}
			if catalogueCalls.Load() != 1 {
				t.Fatalf("catalogue calls after waiter wake = %d, want one refusing attempt", catalogueCalls.Load())
			}

			if tt.knownReset && !tt.elapsed {
				if _, err := p.linkTypes(policyContext(t.Context(), 6)); err == nil {
					t.Fatal("fresh newer epoch bypassed an unexpired known barrier")
				}
				if catalogueCalls.Load() != 1 {
					t.Fatalf("catalogue calls before known reset = %d, want one", catalogueCalls.Load())
				}
				now = now.Add(time.Minute + time.Second)
			}
			if _, err := p.linkTypes(policyContext(t.Context(), 5)); err != nil {
				t.Fatalf("fresh newer epoch replacement: %v", err)
			}
			if catalogueCalls.Load() != 2 {
				t.Fatalf("catalogue calls after fresh replacement = %d, want exactly two", catalogueCalls.Load())
			}
		})
	}
}

func TestLinkTypesRetryAfterBarrierKeepsItsObservedDeadline(t *testing.T) {
	var catalogueCalls, issueCalls, commentCalls atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiBase + linkTypesPath:
			if catalogueCalls.Add(1) == 1 {
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"issueLinkTypes":[]}`))
		case apiBase + "/issue/ABC-12":
			issueCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		case apiBase + "/issue/ABC-12/comment":
			commentCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)

	t0 := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	now := t0
	p := New("example.test",
		WithBaseURL(s.URL),
		WithCredentials(Credentials{Email: "test@example.test", Token: "token"}),
		WithNow(func() time.Time { return now }),
	)
	policyContext := func(epoch uint64) context.Context {
		return provider.WithRequestPolicy(t.Context(), provider.RequestPolicy{Epoch: epoch})
	}

	_, initialErr := p.linkTypes(policyContext(7))
	initial, ok := provider.InspectRateLimitRefusal(initialErr, t0)
	deadline := t0.Add(time.Minute)
	if !ok || !initial.KnownReset || !initial.ResetAt.Equal(deadline) {
		t.Fatalf("initial refusal policy = %+v/%t, want deadline %s", initial, ok, deadline)
	}
	initialMetadata, ok := provider.RateLimitMetadataOf(initialErr)
	if !ok || initialMetadata.RetryAfter != time.Minute || !initialMetadata.ResetAt.IsZero() {
		t.Fatalf("initial metadata = %+v/%t, want raw Retry-After", initialMetadata, ok)
	}

	now = deadline.Add(time.Second)
	if _, err := p.linkTypes(policyContext(8)); err != nil {
		t.Fatalf("newer epoch after deadline: %v", err)
	}

	for _, epoch := range []uint64{7, 0} {
		_, retainedErr := p.linkTypes(policyContext(epoch))
		retained, ok := provider.InspectRateLimitRefusal(retainedErr, now)
		if !ok || !retained.ExpiredOnly || retained.KnownReset {
			t.Errorf("epoch %d retained policy = %+v/%t, want expired-only", epoch, retained, ok)
		}
		metadata, metadataOK := provider.RateLimitMetadataOf(retainedErr)
		if !metadataOK || !metadata.ResetAt.Equal(deadline.UTC()) || metadata.RetryAfter != 0 {
			t.Errorf("epoch %d retained metadata = %+v/%t, want absolute deadline %s", epoch, metadata, metadataOK, deadline)
		}
	}
	if got := catalogueCalls.Load(); got != 2 {
		t.Errorf("catalogue calls = %d, want refusal plus newer-epoch recovery", got)
	}
	if got := issueCalls.Load(); got != 0 {
		t.Errorf("issue calls = %d, want 0", got)
	}
	if got := commentCalls.Load(); got != 0 {
		t.Errorf("comment calls = %d, want 0", got)
	}
}

func TestRetainedLinkTypesBarrierTimingControls(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)

	t.Run("unknown reset remains unknown", func(t *testing.T) {
		raw := provider.RateLimitErrorf(provider.RateLimitMetadata{}, "jira: unknown reset")
		refusal, ok := provider.InspectRateLimitRefusal(raw, now)
		if !ok {
			t.Fatal("unknown refusal was not inspectable")
		}
		retained := retainedLinkTypesBarrier(refusal)
		if !errors.Is(retained, raw) {
			t.Fatal("unknown refusal was unnecessarily replaced")
		}
		decision, ok := provider.InspectRateLimitRefusal(retained, now.Add(time.Hour))
		if !ok || decision.KnownReset || decision.ExpiredOnly {
			t.Fatalf("retained unknown policy = %+v/%t", decision, ok)
		}
	})

	t.Run("wrapped relative refusal is unwrapped before retention", func(t *testing.T) {
		raw := provider.RateLimitErrorf(
			provider.RateLimitMetadata{RetryAfter: time.Minute},
			"jira: wrapped relative reset",
		)
		refusal, ok := provider.InspectRateLimitRefusal(fmt.Errorf("transport wrapper: %w", raw), now)
		if !ok || !refusal.KnownReset {
			t.Fatalf("wrapped refusal policy = %+v/%t", refusal, ok)
		}
		classified, direct := refusal.Err.(*provider.Error) //nolint:errorlint // InspectRateLimitRefusal returns its direct classified representative.
		if !direct || classified.Kind != provider.KindRateLimit {
			t.Fatalf("wrapped refusal representative = %T, want direct *provider.Error", refusal.Err)
		}
		retained := retainedLinkTypesBarrier(refusal)
		metadata, ok := provider.RateLimitMetadataOf(retained)
		deadline := now.Add(time.Minute).UTC()
		if !ok || !metadata.ResetAt.Equal(deadline) || metadata.RetryAfter != 0 {
			t.Fatalf("retained wrapped metadata = %+v/%t, want pinned deadline %s", metadata, ok, deadline)
		}
	})

	t.Run("absolute reset keeps its deadline and cause", func(t *testing.T) {
		cause := errors.New("absolute refusal cause")
		deadline := now.Add(time.Minute).In(time.FixedZone("offset", 2*60*60))
		raw := provider.RateLimitErrorf(
			provider.RateLimitMetadata{ResetAt: deadline},
			"jira: absolute reset: %w",
			cause,
		)
		refusal, ok := provider.InspectRateLimitRefusal(raw, now)
		if !ok || !refusal.KnownReset {
			t.Fatalf("absolute refusal policy = %+v/%t", refusal, ok)
		}
		retained := retainedLinkTypesBarrier(refusal)
		metadata, ok := provider.RateLimitMetadataOf(retained)
		if !ok || !metadata.ResetAt.Equal(deadline.UTC()) || metadata.RetryAfter != 0 {
			t.Fatalf("retained absolute metadata = %+v/%t", metadata, ok)
		}
		if provider.KindOf(retained) != provider.KindRateLimit || retained.Error() != raw.Error() || !errors.Is(retained, cause) {
			t.Fatalf("retained absolute error = %v, want original classification, message, and cause", retained)
		}
		decision, ok := provider.InspectRateLimitRefusal(retained, deadline.Add(time.Second))
		if !ok || !decision.ExpiredOnly || decision.KnownReset {
			t.Fatalf("elapsed absolute policy = %+v/%t, want expired-only", decision, ok)
		}
	})
}

func TestLinkTypesNewerEpochWaitersShareUnknownBarrierReplacement(t *testing.T) {
	const callers = 8
	var catalogueCalls, active, maxActive atomic.Int64
	replacementStarted := make(chan struct{})
	releaseReplacement := make(chan struct{})
	var startedOnce sync.Once
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apiBase+linkTypesPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		call := catalogueCalls.Add(1)
		if call == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		current := active.Add(1)
		for {
			maximum := maxActive.Load()
			if current <= maximum || maxActive.CompareAndSwap(maximum, current) {
				break
			}
		}
		startedOnce.Do(func() { close(replacementStarted) })
		<-releaseReplacement
		active.Add(-1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issueLinkTypes":[]}`))
	}))
	t.Cleanup(s.Close)
	p := New("example.test",
		WithBaseURL(s.URL),
		WithCredentials(Credentials{Email: "test@example.test", Token: "token"}),
	)
	policyContext := func(epoch uint64) context.Context {
		return provider.WithRequestPolicy(t.Context(), provider.RequestPolicy{Epoch: epoch})
	}
	if _, err := p.linkTypes(policyContext(4)); err == nil {
		t.Fatal("initial unknown-reset refusal = nil")
	}

	var ready, done sync.WaitGroup
	ready.Add(callers)
	done.Add(callers)
	errs := make(chan error, callers)
	for range callers {
		go func() {
			defer done.Done()
			ready.Done()
			_, err := p.linkTypes(policyContext(5))
			errs <- err
		}()
	}
	ready.Wait()
	select {
	case <-replacementStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("newer-epoch replacement did not start")
	}

	cancelled, cancel := context.WithCancel(policyContext(5))
	cancelledResult := make(chan error, 1)
	go func() {
		_, err := p.linkTypes(cancelled)
		cancelledResult <- err
	}()
	cancel()
	select {
	case err := <-cancelledResult:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled waiter error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled waiter remained blocked behind replacement")
	}

	close(releaseReplacement)
	done.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("live newer-epoch waiter: %v", err)
		}
	}
	if catalogueCalls.Load() != 2 || maxActive.Load() != 1 {
		t.Errorf("catalogue calls/max active = %d/%d, want initial refusal plus one replacement/1",
			catalogueCalls.Load(), maxActive.Load())
	}
}

type linksBarrierRoundTripper func(*http.Request) (*http.Response, error)

func (f linksBarrierRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestLinkTypesLiveContendersShareReplacementAfterCancelledLoader(t *testing.T) {
	const callers = 8
	var catalogueCalls, active, maxActive atomic.Int64
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseReplacement := make(chan struct{})
	var firstOnce, secondOnce sync.Once
	client := &http.Client{Transport: linksBarrierRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != apiBase+linkTypesPath {
			return nil, errors.New("unexpected Jira path " + req.URL.Path)
		}
		call := catalogueCalls.Add(1)
		current := active.Add(1)
		for {
			maximum := maxActive.Load()
			if current <= maximum || maxActive.CompareAndSwap(maximum, current) {
				break
			}
		}
		if call == 1 {
			firstOnce.Do(func() { close(firstStarted) })
			<-req.Context().Done()
			active.Add(-1)
			return nil, req.Context().Err()
		}
		secondOnce.Do(func() { close(secondStarted) })
		<-releaseReplacement
		active.Add(-1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"issueLinkTypes":[]}`)),
		}, nil
	})}
	p := New("example.test",
		WithBaseURL("https://jira.example.test"),
		WithHTTPClient(client),
		WithCredentials(Credentials{Email: "test@example.test", Token: "token"}),
	)
	policyContext := func(epoch uint64) context.Context {
		return provider.WithRequestPolicy(t.Context(), provider.RequestPolicy{Epoch: epoch})
	}

	loader, cancelLoader := context.WithCancel(policyContext(3))
	loaderResult := make(chan error, 1)
	go func() {
		_, err := p.linkTypes(loader)
		loaderResult <- err
	}()
	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("initial loader did not start")
	}

	var ready, done sync.WaitGroup
	ready.Add(callers)
	done.Add(callers)
	errs := make(chan error, callers)
	for range callers {
		go func() {
			defer done.Done()
			ready.Done()
			_, err := p.linkTypes(policyContext(3))
			errs <- err
		}()
	}
	ready.Wait()
	cancelLoader()
	if err := <-loaderResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled loader error = %v, want context.Canceled", err)
	}
	select {
	case <-secondStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("live contenders did not replace cancelled loader")
	}

	cancelled, cancel := context.WithCancel(policyContext(3))
	cancelledResult := make(chan error, 1)
	go func() {
		_, err := p.linkTypes(cancelled)
		cancelledResult <- err
	}()
	cancel()
	select {
	case err := <-cancelledResult:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled replacement waiter error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled replacement waiter remained blocked")
	}

	close(releaseReplacement)
	done.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("live contender: %v", err)
		}
	}
	if catalogueCalls.Load() != 2 || maxActive.Load() != 1 {
		t.Errorf("catalogue calls/max active = %d/%d, want cancelled loader plus one replacement/1",
			catalogueCalls.Load(), maxActive.Load())
	}
}

func TestFetchDetailConcurrentOrdinaryFallbackWaitersShareCatalogueFailure(t *testing.T) {
	const callers = 8
	var catalogueCalls, issueCalls, commentCalls atomic.Int64
	catalogueStarted := make(chan struct{})
	releaseCatalogue := make(chan struct{})
	var startedOnce sync.Once
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiBase + linkTypesPath:
			catalogueCalls.Add(1)
			startedOnce.Do(func() { close(catalogueStarted) })
			<-releaseCatalogue
			w.WriteHeader(http.StatusInternalServerError)
		case apiBase + "/issue/ABC-12":
			issueCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case apiBase + "/issue/ABC-12/comment":
			commentCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	p := New("example.test",
		WithBaseURL(s.URL),
		WithCredentials(Credentials{Email: "test@example.test", Token: "token"}),
	)

	var ready, done sync.WaitGroup
	ready.Add(callers)
	done.Add(callers)
	errs := make(chan error, callers)
	for range callers {
		go func() {
			defer done.Done()
			ready.Done()
			_, err := p.FetchDetail(t.Context(), model.TicketID("ABC-12"))
			errs <- err
		}()
	}
	ready.Wait()
	select {
	case <-catalogueStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("catalogue loader did not start")
	}
	close(releaseCatalogue)
	done.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("ordinary-fallback Detail: %v", err)
		}
	}
	if catalogueCalls.Load() != 1 || issueCalls.Load() != callers || commentCalls.Load() != callers {
		t.Errorf("catalogue/issue/comment calls = %d/%d/%d, want 1/%d/%d",
			catalogueCalls.Load(), issueCalls.Load(), commentCalls.Load(), callers, callers)
	}
}
