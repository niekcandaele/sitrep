package jira_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/jira"
	"github.com/niekcandaele/sitrep/internal/provider/providertest"
)

// fullDetail serves the drill-in fixtures: the catalogue, the issue and its
// comments.
func fullDetail(t *testing.T) *replayServer {
	t.Helper()
	return newReplayServer(t, map[string][]response{
		linkTypePath: {{file: "issue_link_types.json"}},
		ticketPath:   {{file: "detail_full.json"}},
		commentsPath: {{file: "comments_page.json"}},
	})
}

// The description Jira returns for ABC-12, byte for byte. It is spelled out
// here rather than read from the fixture because "verbatim" is the claim under
// test: on REST v2 this is wiki markup, and sitrep stores it unrendered.
const wantDescription = "h2. Decoding a Ref\n\n" +
	"A Ref with children is an Epic & one without is a Ticket.\n\n" +
	"{code:go}\nfunc decodesToTicket(snap model.Epic" + "Snapshot) bool { return len(snap.Tickets) == 0 }\n{code}\n\n" +
	"See « éclair » for the naming discussion."

func TestFetchDetailReadsTheDescriptionVerbatim(t *testing.T) {
	p := newProvider(fullDetail(t))

	detail, err := p.FetchDetail(context.Background(), "ABC-12")
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	if detail.TicketID != "ABC-12" {
		t.Errorf("TicketID = %q, want the issue key", detail.TicketID)
	}
	if detail.Description != wantDescription {
		t.Errorf("Description = %q,\nwant %q", detail.Description, wantDescription)
	}
}

// Comments arrive newest-first because the request orders by -created, and
// model.Detail requires oldest-first: the reversal is the whole point.
func TestFetchDetailOrdersCommentsOldestFirst(t *testing.T) {
	p := newProvider(fullDetail(t))

	detail, err := p.FetchDetail(context.Background(), "ABC-12")
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	if len(detail.Comments) != 3 {
		t.Fatalf("got %d comments, want 3", len(detail.Comments))
	}
	for i, want := range []string{"30001", "30002", "30003"} {
		if detail.Comments[i].ID != want {
			t.Errorf("Comments[%d].ID = %q, want %q (oldest first)", i, detail.Comments[i].ID, want)
		}
	}

	// Jira's own timestamp format carries no colon in its offset, so it is not
	// RFC3339 — and the instant has to survive the conversion to UTC.
	first := detail.Comments[0]
	want := time.Date(2026, 1, 12, 8, 14, 33, 123_000_000, time.UTC)
	if !first.CreatedAt.Equal(want) {
		t.Errorf("Comments[0].CreatedAt = %s, want %s", first.CreatedAt, want)
	}
	if first.CreatedAt.Location() != time.UTC {
		t.Errorf("Comments[0].CreatedAt is in %s, want UTC", first.CreatedAt.Location())
	}
	if first.Author.DisplayName != "Ada Lovelace" {
		t.Errorf("Comments[0].Author = %+v, want Ada Lovelace", first.Author)
	}
	if got, want := first.URL, "https://acme.atlassian.net/browse/ABC-12?focusedCommentId=30001"; got != want {
		t.Errorf("Comments[0].URL = %q, want %q", got, want)
	}

	// A comment must never be dropped because its writer left the company.
	if got := detail.Comments[1].Author; got != (model.User{}) {
		t.Errorf("Comments[1].Author = %+v, want the zero User for a null author", got)
	}
	if detail.Comments[1].Body == "" {
		t.Error("the null-author comment lost its body")
	}
}

// An empty description, no comments and no links are the ordinary state of a
// freshly filed Ticket, and must never read as an error.
func TestFetchDetailOnABareTicket(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		linkTypePath:                       {{file: "issue_link_types.json"}},
		"/rest/api/2/issue/ABC-13":         {{file: "detail_bare.json"}},
		"/rest/api/2/issue/ABC-13/comment": {{file: "comments_empty.json"}},
	})
	p := newProvider(s)

	detail, err := p.FetchDetail(context.Background(), "ABC-13")
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}
	if detail.Description != "" {
		t.Errorf("Description = %q, want empty for a null description", detail.Description)
	}
	if len(detail.Comments) != 0 {
		t.Errorf("got %d comments, want none", len(detail.Comments))
	}
	if len(detail.Links) != 0 {
		t.Errorf("got %d links, want none", len(detail.Links))
	}
}

func TestFetchDetailSendsThreeRequestsAndDiscoversLinkTypesOnce(t *testing.T) {
	s := fullDetail(t)
	p := newProvider(s)

	for i := range 2 {
		if _, err := p.FetchDetail(context.Background(), "ABC-12"); err != nil {
			t.Fatalf("FetchDetail %d: %v", i+1, err)
		}
	}

	if n := len(s.requestsTo(ticketPath)); n != 2 {
		t.Errorf("%d issue reads for two drill-ins, want 2", n)
	}
	if n := len(s.requestsTo(commentsPath)); n != 2 {
		t.Errorf("%d comment reads for two drill-ins, want 2", n)
	}
	// The catalogue is discovered once per process, before the first link is
	// mapped, and never again.
	if n := len(s.requestsTo(linkTypePath)); n != 1 {
		t.Errorf("%d link type discoveries, want exactly 1", n)
	}

	issue := s.requestsTo(ticketPath)[0]
	if got := issue.query["fields"][0]; got != "description,issuelinks,summary,status,resolution,project" {
		t.Errorf("fields = %q, want the drill-in field selection", got)
	}
	comments := s.requestsTo(commentsPath)[0]
	if got := comments.query["orderBy"]; len(got) != 1 || got[0] != "-created" {
		t.Errorf("orderBy = %v, want -created", got)
	}
	if got := comments.query["maxResults"]; len(got) != 1 || got[0] != "100" {
		t.Errorf("maxResults = %v, want 100", got)
	}
}

func TestFetchDetailsUsesTheSingularFallback(t *testing.T) {
	s := fullDetail(t)
	details, err := newProvider(s).FetchDetails(t.Context(), []model.TicketID{"", "ABC-12", "ABC-12"})
	if err != nil {
		t.Fatalf("FetchDetails: %v", err)
	}
	if len(details) != 1 || details["ABC-12"].TicketID != "ABC-12" {
		t.Errorf("Details = %+v, want one canonical ABC-12 result", details)
	}
	if got := len(s.requestsTo(ticketPath)); got != 1 {
		t.Errorf("issue reads = %d, want one singular Detail read", got)
	}
	if got := len(s.requestsTo(commentsPath)); got != 1 {
		t.Errorf("comment reads = %d, want one singular Detail read", got)
	}
	if got := len(s.requestsTo(linkTypePath)); got != 1 {
		t.Errorf("link type discoveries = %d, want one", got)
	}
}

func TestFetchDetailKnownCatalogueRateBarrierBlocksNewerEpochUntilExpiry(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		linkTypePath: {
			{status: http.StatusTooManyRequests, body: `{}`, headers: map[string]string{"Retry-After": "60"}},
			{file: "issue_link_types.json"},
		},
		ticketPath:   {{file: "detail_full.json"}},
		commentsPath: {{file: "comments_page.json"}},
	})
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	p := newProvider(s, jira.WithNow(func() time.Time { return now }))
	policyContext := func(epoch uint64) context.Context {
		return provider.WithRequestPolicy(t.Context(), provider.RequestPolicy{Epoch: epoch})
	}

	_, refusal := p.FetchDetail(policyContext(4), "ABC-12")
	decision, ok := provider.InspectRateLimitRefusal(refusal, now)
	deadline := now.Add(time.Minute)
	if !ok || !decision.KnownReset || !decision.ResetAt.Equal(deadline) {
		t.Fatalf("first error = %v, policy %+v/%t", refusal, decision, ok)
	}
	if len(s.requestsTo(linkTypePath)) != 1 || len(s.requestsTo(ticketPath)) != 0 || len(s.requestsTo(commentsPath)) != 0 {
		t.Fatalf("requests after refusal = catalogue/issue/comments %d/%d/%d, want 1/0/0",
			len(s.requestsTo(linkTypePath)), len(s.requestsTo(ticketPath)), len(s.requestsTo(commentsPath)))
	}

	for _, epoch := range []uint64{0, 4, 3, 5} {
		_, err := p.FetchDetail(policyContext(epoch), "ABC-12")
		retained, ok := provider.InspectRateLimitRefusal(err, now)
		if !ok || !retained.KnownReset || !retained.ResetAt.Equal(deadline) {
			t.Errorf("epoch %d error = %v, policy %+v/%t; want retained deadline %s", epoch, err, retained, ok, deadline)
		}
	}
	if len(s.requestsTo(linkTypePath)) != 1 || len(s.requestsTo(ticketPath)) != 0 {
		t.Fatalf("known future barrier sent I/O: catalogue/issue = %d/%d",
			len(s.requestsTo(linkTypePath)), len(s.requestsTo(ticketPath)))
	}

	now = now.Add(time.Minute)
	if _, err := p.FetchDetail(policyContext(5), "ABC-12"); err != nil {
		t.Fatalf("newer epoch after barrier deadline: %v", err)
	}
	if _, err := p.FetchDetail(policyContext(5), "ABC-12"); err != nil {
		t.Fatalf("cached Detail after barrier deadline: %v", err)
	}
	if got := len(s.requestsTo(linkTypePath)); got != 2 {
		t.Errorf("catalogue reads = %d, want refusal plus lapsed-barrier retry", got)
	}
	if got := len(s.requestsTo(ticketPath)); got != 2 {
		t.Errorf("issue reads = %d, want two Details after deadline", got)
	}

	_, staleErr := p.FetchDetail(policyContext(4), "ABC-12")
	stale, ok := provider.InspectRateLimitRefusal(staleErr, now)
	if !ok || !stale.ExpiredOnly || stale.KnownReset || !stale.ResetAt.IsZero() {
		t.Fatalf("stale epoch policy = %+v/%t, want expired-only refusal", stale, ok)
	}
	var classified *provider.Error
	if !errors.As(staleErr, &classified) || !classified.RateLimit.ResetAt.Equal(deadline.UTC()) || classified.RateLimit.RetryAfter != 0 {
		t.Fatalf("stale epoch classified error = %+v, want original absolute deadline %s", classified, deadline)
	}
	if got := len(s.requestsTo(linkTypePath)); got != 2 {
		t.Errorf("catalogue reads after stale replay = %d, want no new I/O", got)
	}
	if got := len(s.requestsTo(ticketPath)); got != 2 {
		t.Errorf("issue reads after stale replay = %d, want no new I/O", got)
	}
	if got := len(s.requestsTo(commentsPath)); got != 2 {
		t.Errorf("comment reads after stale replay = %d, want no new I/O", got)
	}
}

func TestFetchDetailUnknownCatalogueRateBarrierAllowsNewerPolicyEpoch(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		linkTypePath: {
			{status: http.StatusTooManyRequests, body: `{}`},
			{file: "issue_link_types.json"},
		},
		ticketPath:   {{file: "detail_full.json"}},
		commentsPath: {{file: "comments_page.json"}},
	})
	p := newProvider(s)
	policyContext := func(epoch uint64) context.Context {
		return provider.WithRequestPolicy(t.Context(), provider.RequestPolicy{Epoch: epoch})
	}

	_, refusal := p.FetchDetail(policyContext(4), "ABC-12")
	if refusal == nil {
		t.Fatal("unknown rate refusal was not returned")
	}
	if _, err := p.FetchDetail(policyContext(4), "ABC-12"); !errors.Is(err, refusal) {
		t.Fatalf("same epoch error = %v, want retained refusal", err)
	}
	if _, err := p.FetchDetail(policyContext(5), "ABC-12"); err != nil {
		t.Fatalf("newer epoch retry after unknown barrier: %v", err)
	}
	if got := len(s.requestsTo(linkTypePath)); got != 2 {
		t.Errorf("catalogue reads = %d, want refusal plus newer-epoch retry", got)
	}
}

func TestFetchDetailElapsedCatalogueResetRequiresNewerPolicyEpoch(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		linkTypePath: {
			{status: http.StatusTooManyRequests, body: `{}`, headers: map[string]string{"X-RateLimit-Reset": "2000-01-01T00:00:00Z"}},
			{file: "issue_link_types.json"},
		},
		ticketPath:   {{file: "detail_full.json"}},
		commentsPath: {{file: "comments_page.json"}},
	})
	p := newProvider(s)
	policyContext := func(epoch uint64) context.Context {
		return provider.WithRequestPolicy(t.Context(), provider.RequestPolicy{Epoch: epoch})
	}

	_, refusal := p.FetchDetail(policyContext(4), "ABC-12")
	if refusal == nil {
		t.Fatal("elapsed reset refusal was not returned to the current operation")
	}
	for _, epoch := range []uint64{3, 4} {
		if _, err := p.FetchDetail(policyContext(epoch), "ABC-12"); !errors.Is(err, refusal) {
			t.Errorf("epoch %d error = %v, want retained elapsed refusal", epoch, err)
		}
	}
	if got := len(s.requestsTo(linkTypePath)); got != 1 {
		t.Errorf("catalogue reads before newer admission = %d, want one refusing request", got)
	}
	if _, err := p.FetchDetail(policyContext(5), "ABC-12"); err != nil {
		t.Fatalf("newer-epoch retry after elapsed reset: %v", err)
	}
	if got := len(s.requestsTo(linkTypePath)); got != 2 {
		t.Errorf("catalogue reads = %d, want elapsed refusal plus newer-epoch retry", got)
	}
}

func TestFetchDetailExpiredFutureCatalogueBarrierRetainsSameEpoch(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		linkTypePath: {
			{status: http.StatusTooManyRequests, body: `{}`, headers: map[string]string{"Retry-After": "60"}},
			{file: "issue_link_types.json"},
		},
		ticketPath:   {{file: "detail_full.json"}},
		commentsPath: {{file: "comments_page.json"}},
	})
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	p := newProvider(s, jira.WithNow(func() time.Time { return now }))
	ctx := provider.WithRequestPolicy(t.Context(), provider.RequestPolicy{Epoch: 4})

	_, refusal := p.FetchDetail(ctx, "ABC-12")
	if refusal == nil {
		t.Fatal("future rate refusal was not returned to the current operation")
	}
	deadline := now.Add(time.Minute)
	now = deadline

	_, retainedErr := p.FetchDetail(ctx, "ABC-12")
	retained, ok := provider.InspectRateLimitRefusal(retainedErr, now)
	if !ok || !retained.ExpiredOnly || retained.KnownReset {
		t.Fatalf("same-epoch retry policy = %+v/%t, want expired-only refusal", retained, ok)
	}
	metadata, ok := provider.RateLimitMetadataOf(retainedErr)
	if !ok || !metadata.ResetAt.Equal(deadline.UTC()) || metadata.RetryAfter != 0 {
		t.Fatalf("same-epoch retry metadata = %+v/%t, want absolute deadline %s", metadata, ok, deadline)
	}
	if got := len(s.requestsTo(linkTypePath)); got != 1 {
		t.Errorf("catalogue reads = %d, want the initial refusal only", got)
	}
}

func TestFetchDetailOrdinaryCatalogueFailureIsTerminalFallback(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		linkTypePath: {{status: http.StatusInternalServerError, body: `{}`}},
		ticketPath:   {{file: "detail_full.json"}},
		commentsPath: {{file: "comments_page.json"}},
	})
	p := newProvider(s)
	for range 2 {
		detail, err := p.FetchDetail(t.Context(), "ABC-12")
		if err != nil || len(detail.Links) == 0 {
			t.Fatalf("fallback Detail = %+v, %v", detail, err)
		}
	}
	if got := len(s.requestsTo(linkTypePath)); got != 1 {
		t.Errorf("catalogue reads = %d, want one terminal ordinary failure", got)
	}
	if got := len(s.requestsTo(ticketPath)); got != 2 {
		t.Errorf("issue reads = %d, want fallback to proceed twice", got)
	}
}

func TestFetchDetailConcurrentCatalogueWaitersShareRateBarrier(t *testing.T) {
	const callers = 8
	var catalogueCalls, otherCalls atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != linkTypePath {
			otherCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		catalogueCalls.Add(1)
		startOnce.Do(func() { close(started) })
		<-release
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(s.Close)
	p := jira.New(fixtureHost,
		jira.WithBaseURL(s.URL),
		jira.WithCredentials(jira.Credentials{Email: fixtureEmail, Token: fixtureToken}),
	)

	var ready, done sync.WaitGroup
	ready.Add(callers)
	done.Add(callers)
	errs := make(chan error, callers)
	for range callers {
		go func() {
			defer done.Done()
			ready.Done()
			ctx := provider.WithRequestPolicy(t.Context(), provider.RequestPolicy{Epoch: 9})
			_, err := p.FetchDetail(ctx, "ABC-12")
			errs <- err
		}()
	}
	ready.Wait()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("catalogue loader did not start")
	}
	waiterCtx, cancelWaiter := context.WithCancel(provider.WithRequestPolicy(t.Context(), provider.RequestPolicy{Epoch: 9}))
	cancelledWaiter := make(chan error, 1)
	go func() {
		_, err := p.FetchDetail(waiterCtx, "ABC-12")
		cancelledWaiter <- err
	}()
	cancelWaiter()
	select {
	case err := <-cancelledWaiter:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled waiter error = %v, want its own cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled waiter remained blocked behind live loader")
	}
	if catalogueCalls.Load() != 1 {
		t.Errorf("waiter cancellation started another catalogue request")
	}
	close(release)
	done.Wait()
	close(errs)
	for err := range errs {
		if _, ok := provider.InspectRateLimitRefusal(err, time.Time{}); !ok {
			t.Errorf("waiter error = %v, want shared refusal", err)
		}
	}
	if catalogueCalls.Load() != 1 || otherCalls.Load() != 0 {
		t.Errorf("catalogue/other calls = %d/%d, want 1/0", catalogueCalls.Load(), otherCalls.Load())
	}
}

func TestFetchDetailCancelledCatalogueLoaderLetsLiveWaiterReplaceIt(t *testing.T) {
	fixture := func(name string) string {
		t.Helper()
		body, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		return string(body)
	}
	response := func(body string) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}
	}
	linkTypesBody := fixture("issue_link_types.json")
	detailBody := fixture("detail_full.json")
	commentsBody := fixture("comments_page.json")

	var catalogueCalls, active, maxActive atomic.Int64
	firstStarted := make(chan struct{})
	var firstOnce sync.Once
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case linkTypePath:
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
			active.Add(-1)
			return response(linkTypesBody), nil
		case ticketPath:
			return response(detailBody), nil
		case commentsPath:
			return response(commentsBody), nil
		default:
			return nil, errors.New("unexpected Jira path " + req.URL.Path)
		}
	})}
	p := jira.New(fixtureHost,
		jira.WithBaseURL("https://jira.example.test"),
		jira.WithHTTPClient(client),
		jira.WithCredentials(jira.Credentials{Email: fixtureEmail, Token: fixtureToken}),
	)

	loaderCtx, cancelLoader := context.WithCancel(provider.WithRequestPolicy(t.Context(), provider.RequestPolicy{Epoch: 3}))
	loaderDone := make(chan error, 1)
	go func() {
		_, err := p.FetchDetail(loaderCtx, "ABC-12")
		loaderDone <- err
	}()
	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled catalogue loader did not start")
	}

	cancelledWaiter, cancelWaiter := context.WithCancel(provider.WithRequestPolicy(t.Context(), provider.RequestPolicy{Epoch: 3}))
	cancelWaiter()
	if _, err := p.FetchDetail(cancelledWaiter, "ABC-12"); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled waiter error = %v, want its own cancellation", err)
	}

	waiterDone := make(chan error, 1)
	go func() {
		ctx := provider.WithRequestPolicy(t.Context(), provider.RequestPolicy{Epoch: 3})
		_, err := p.FetchDetail(ctx, "ABC-12")
		waiterDone <- err
	}()
	cancelLoader()
	if err := <-loaderDone; !errors.Is(err, context.Canceled) {
		t.Errorf("loader error = %v, want context.Canceled", err)
	}
	select {
	case err := <-waiterDone:
		if err != nil {
			t.Errorf("live waiter: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("live waiter did not replace cancelled loader")
	}
	if catalogueCalls.Load() != 2 || maxActive.Load() != 1 {
		t.Errorf("catalogue calls/max active = %d/%d, want 2/1", catalogueCalls.Load(), maxActive.Load())
	}
}

func TestFetchDetailCatalogueCancellationRacingRefusalInstallsBarrier(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	ctx, cancel := context.WithCancel(provider.WithRequestPolicy(t.Context(), provider.RequestPolicy{Epoch: 12}))
	refusal := provider.RateLimitErrorf(provider.RateLimitMetadata{RetryAfter: time.Minute}, "jira: catalogue rate refusal")
	var calls atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.URL.Path != linkTypePath {
			return nil, errors.New("unexpected Jira path " + req.URL.Path)
		}
		cancel()
		return nil, refusal
	})}
	p := jira.New(fixtureHost,
		jira.WithBaseURL("https://jira.example.test"),
		jira.WithHTTPClient(client),
		jira.WithCredentials(jira.Credentials{Email: fixtureEmail, Token: fixtureToken}),
		jira.WithNow(func() time.Time { return now }),
	)

	_, err := p.FetchDetail(ctx, "ABC-12")
	if !errors.Is(err, context.Canceled) || !errors.Is(err, refusal) {
		t.Fatalf("racing error = %v, want cancellation and raw refusal", err)
	}
	decision, ok := provider.InspectRateLimitRefusal(err, now)
	deadline := now.Add(time.Minute)
	if !ok || !decision.KnownReset || !decision.ResetAt.Equal(deadline) {
		t.Fatalf("racing refusal = %+v/%t, want known one-minute reset", decision, ok)
	}

	retryCtx := provider.WithRequestPolicy(t.Context(), provider.RequestPolicy{Epoch: 12})
	_, retryErr := p.FetchDetail(retryCtx, "ABC-12")
	if errors.Is(retryErr, context.Canceled) {
		t.Fatalf("same-epoch error = %v, want retained refusal without cancellation", retryErr)
	}
	retained, ok := provider.InspectRateLimitRefusal(retryErr, now)
	if !ok || !retained.KnownReset || !retained.ResetAt.Equal(deadline) {
		t.Fatalf("same-epoch refusal = %+v/%t, want deadline %s", retained, ok, deadline)
	}
	metadata, ok := provider.RateLimitMetadataOf(retryErr)
	if !ok || !metadata.ResetAt.Equal(deadline.UTC()) || metadata.RetryAfter != 0 {
		t.Fatalf("same-epoch metadata = %+v/%t, want absolute deadline %s", metadata, ok, deadline)
	}
	if calls.Load() != 1 {
		t.Errorf("HTTP calls = %d, want only the paid-for refusing request", calls.Load())
	}
}

func TestFetchDetailCatalogueCancellationRacingElapsedRefusalRetainsNewBarrier(t *testing.T) {
	readFixture := func(name string) string {
		t.Helper()
		body, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		return string(body)
	}
	response := func(body string) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}
	}
	linkTypesBody := readFixture("issue_link_types.json")
	detailBody := readFixture("detail_full.json")
	commentsBody := readFixture("comments_page.json")

	initialNow := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	now := initialNow
	knownRefusal := provider.RateLimitErrorf(
		provider.RateLimitMetadata{ResetAt: initialNow.Add(time.Minute)},
		"jira: initial catalogue rate refusal",
	)
	elapsedRefusal := provider.RateLimitErrorf(
		provider.RateLimitMetadata{ResetAt: initialNow.Add(time.Minute)},
		"jira: elapsed catalogue rate refusal",
	)
	var catalogueCalls atomic.Int64
	var cancelReplacement context.CancelFunc
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case linkTypePath:
			switch catalogueCalls.Add(1) {
			case 1:
				return nil, knownRefusal
			case 2:
				cancelReplacement()
				return nil, elapsedRefusal
			default:
				return response(linkTypesBody), nil
			}
		case ticketPath:
			return response(detailBody), nil
		case commentsPath:
			return response(commentsBody), nil
		default:
			return nil, errors.New("unexpected Jira path " + req.URL.Path)
		}
	})}
	p := jira.New(fixtureHost,
		jira.WithBaseURL("https://jira.example.test"),
		jira.WithHTTPClient(client),
		jira.WithCredentials(jira.Credentials{Email: fixtureEmail, Token: fixtureToken}),
		jira.WithNow(func() time.Time { return now }),
	)

	initialCtx := provider.WithRequestPolicy(t.Context(), provider.RequestPolicy{Epoch: 11})
	if _, err := p.FetchDetail(initialCtx, "ABC-12"); !errors.Is(err, knownRefusal) {
		t.Fatalf("initial error = %v, want known refusal", err)
	}
	now = initialNow.Add(time.Minute)

	replacementCtx, cancel := context.WithCancel(provider.WithRequestPolicy(t.Context(), provider.RequestPolicy{Epoch: 12}))
	cancelReplacement = cancel
	_, err := p.FetchDetail(replacementCtx, "ABC-12")
	if !errors.Is(err, context.Canceled) || !errors.Is(err, elapsedRefusal) {
		t.Fatalf("racing error = %v, want cancellation and raw elapsed refusal", err)
	}
	decision, ok := provider.InspectRateLimitRefusal(err, now)
	if !ok || !decision.ExpiredOnly {
		t.Fatalf("racing refusal = %+v/%t, want elapsed-only refusal", decision, ok)
	}

	for _, epoch := range []uint64{11, 12} {
		retryCtx := provider.WithRequestPolicy(t.Context(), provider.RequestPolicy{Epoch: epoch})
		_, retryErr := p.FetchDetail(retryCtx, "ABC-12")
		if !errors.Is(retryErr, elapsedRefusal) {
			t.Errorf("epoch %d error = %v, want retained elapsed refusal", epoch, retryErr)
		}
	}
	if catalogueCalls.Load() != 2 {
		t.Errorf("catalogue calls before newer admission = %d, want initial and elapsed refusals", catalogueCalls.Load())
	}
	newerCtx := provider.WithRequestPolicy(t.Context(), provider.RequestPolicy{Epoch: 13})
	if _, err := p.FetchDetail(newerCtx, "ABC-12"); err != nil {
		t.Fatalf("newer-epoch retry after elapsed refusal: %v", err)
	}
	if catalogueCalls.Load() != 3 {
		t.Errorf("catalogue calls = %d, want initial refusal, elapsed refusal, and newer-epoch retry", catalogueCalls.Load())
	}
}

func TestFetchDetailFailures(t *testing.T) {
	tests := []struct {
		name string
		id   model.TicketID
		resp *response
		want providertest.Want
	}{
		{
			name: "an empty id",
			id:   "",
			want: providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{"does not name a Jira issue"},
			},
		},
		{
			name: "a malformed id",
			id:   "not a key",
			want: providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{"does not name a Jira issue"},
			},
		},
		{
			name: "an unknown issue",
			id:   "ABC-12",
			resp: &response{status: http.StatusNotFound, file: "errors_not_found.json"},
			want: providertest.Want{
				Kind:     provider.KindBadRef,
				Contains: []string{"ABC-12 not found (or you lack access)"},
				Secret:   fixtureToken,
			},
		},
		{
			// A drill-in classifies exactly as the polled path does: a 401 on
			// Enter is the same auth failure it is on a refresh.
			name: "an unauthorized read",
			id:   "ABC-12",
			resp: &response{status: http.StatusUnauthorized, file: "errors_auth.json"},
			want: providertest.Want{
				Kind:     provider.KindAuth,
				Contains: []string{"authentication failed (401)"},
				Secret:   fixtureToken,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			responses := map[string][]response{linkTypePath: {{file: "issue_link_types.json"}}}
			if tt.resp != nil {
				responses[ticketPath] = []response{*tt.resp}
			}
			s := newReplayServer(t, responses)
			p := newProvider(s)

			_, err := p.FetchDetail(context.Background(), tt.id)
			providertest.CheckError(t, "jira", err, tt.want)
			if tt.resp == nil && len(s.recorded()) != 0 {
				t.Error("a malformed ticket id reached the network")
			}
		})
	}
}
