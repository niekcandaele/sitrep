package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/github"
)

var partialDetailBatchIDs = []model.TicketID{"batch-node-a", "batch-node-b", "batch-node-c"}

type detailBatchRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

func batchIDs(count int) []model.TicketID {
	ids := make([]model.TicketID, count)
	for i := range ids {
		ids[i] = model.TicketID(fmt.Sprintf("NODE-ID-%03d-SECRET", i))
	}
	return ids
}

func detailNodeJSON(id, body string) map[string]any {
	return map[string]any{
		"id":         id,
		"number":     1,
		"url":        "https://example.test/issues/1",
		"body":       body,
		"repository": map[string]any{"nameWithOwner": "acme/widgets"},
		"comments":   map[string]any{"totalCount": 0, "nodes": []any{}},
		"blockedBy":  map[string]any{"nodes": []any{}},
		"blocking":   map[string]any{"nodes": []any{}},
	}
}

func generatedDetailData(tb testing.TB, variables map[string]any) map[string]any {
	tb.Helper()
	data := make(map[string]any, len(variables))
	for i := range len(variables) {
		suffix := strconv.Itoa(i)
		rawID, ok := variables["id"+suffix]
		if !ok {
			tb.Errorf("variables omit id%s: %#v", suffix, variables)
			continue
		}
		id, ok := rawID.(string)
		if !ok {
			tb.Errorf("id%s = %T, want string", suffix, rawID)
			continue
		}
		data["detail"+suffix] = detailNodeJSON(id, "body for "+id)
	}
	return data
}

func decodeDetailBatchRequest(tb testing.TB, r *http.Request) detailBatchRequest {
	tb.Helper()
	var request detailBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		tb.Errorf("decoding request: %v", err)
	}
	return request
}

func writeDetailBatchResponse(tb testing.TB, w http.ResponseWriter, data map[string]any) {
	tb.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"data": data}); err != nil {
		tb.Errorf("encoding response: %v", err)
	}
}

func detailBatchBody(t *testing.T, data map[string]any, graphErrors []map[string]any) string {
	t.Helper()
	payload := map[string]any{"data": data}
	if graphErrors != nil {
		payload["errors"] = graphErrors
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encoding response body: %v", err)
	}
	return string(encoded)
}

func httpBatchProvider(endpoint string, opts ...github.Option) *github.Provider {
	base := []github.Option{
		github.WithEndpoint(endpoint),
		github.WithTokenSource(func(context.Context, string) (string, error) { return fixtureToken, nil }),
		github.WithUserAgent("sitrep/test"),
	}
	return github.New("github.com", append(base, opts...)...)
}

func requireDetailFailures(t *testing.T, err error, ids []model.TicketID) *provider.DetailFailures {
	t.Helper()
	var failures *provider.DetailFailures
	if !errors.As(err, &failures) {
		t.Fatalf("error = %v, want *provider.DetailFailures", err)
	}
	if len(failures.Failures) != len(ids) {
		t.Fatalf("failures = %#v, want exactly %v", failures.Failures, ids)
	}
	for _, id := range ids {
		if failures.Failures[id] == nil {
			t.Errorf("failure for %q is nil", id)
		}
	}
	return failures
}

func TestFetchDetailsBatches100AliasesInOneRequest(t *testing.T) {
	ids := batchIDs(100)
	var (
		mu       sync.Mutex
		requests []detailBatchRequest
	)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := decodeDetailBatchRequest(t, r)
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()
		writeDetailBatchResponse(t, w, generatedDetailData(t, request.Variables))
	}))
	t.Cleanup(s.Close)

	details, err := httpBatchProvider(s.URL).FetchDetails(t.Context(), ids)
	if err != nil {
		t.Fatalf("FetchDetails: %v", err)
	}
	if len(details) != len(ids) {
		t.Fatalf("details = %d, want %d", len(details), len(ids))
	}
	for _, id := range ids {
		if details[id].TicketID != id {
			t.Errorf("details[%q].TicketID = %q, want requested identity", id, details[id].TicketID)
		}
	}

	mu.Lock()
	gotRequests := append([]detailBatchRequest(nil), requests...)
	mu.Unlock()
	if len(gotRequests) != 1 {
		t.Fatalf("requests = %d, want one 100-alias POST", len(gotRequests))
	}
	request := gotRequests[0]
	if len(request.Variables) != len(ids) {
		t.Fatalf("variables = %d, want %d", len(request.Variables), len(ids))
	}
	if !strings.HasPrefix(strings.TrimSpace(request.Query), "query") || strings.Contains(request.Query, "mutation") {
		t.Errorf("Detail batch document is not read-only: %s", request.Query)
	}
	for i, id := range ids {
		suffix := strconv.Itoa(i)
		if got := request.Variables["id"+suffix]; got != string(id) {
			t.Errorf("id%s variable = %v, want %q", suffix, got, id)
		}
		for _, token := range []string{
			"$id" + suffix + ":ID!",
			"detail" + suffix + ": node(id:$id" + suffix + ") { ...DetailFields }",
		} {
			if !strings.Contains(request.Query, token) {
				t.Errorf("batch document omits %q", token)
			}
		}
		if strings.Contains(request.Query, string(id)) {
			t.Errorf("batch document interpolated Ticket ID %q instead of using a variable", id)
		}
	}
	if strings.Count(request.Query, "fragment DetailFields on Issue") != 1 ||
		strings.Count(request.Query, "...DetailFields") != len(ids) {
		t.Errorf("batch document does not apply one shared Detail fragment to every alias")
	}
	for _, field := range []string{
		"id number url body", "comments(last:100)", "blockedBy(first:50)", "blocking(first:50)",
	} {
		if !strings.Contains(request.Query, field) {
			t.Errorf("batch Detail fragment omits %q", field)
		}
	}
}

func TestFetchDetailsChunksAtSharedAliasBound(t *testing.T) {
	ids := batchIDs(101)
	var (
		mu          sync.Mutex
		chunkIDs    [][]model.TicketID
		requestNo   int
		firstOnce   sync.Once
		secondOnce  sync.Once
		releaseOnce sync.Once
	)
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := decodeDetailBatchRequest(t, r)
		requested := make([]model.TicketID, len(request.Variables))
		for i := range requested {
			requested[i] = model.TicketID(request.Variables["id"+strconv.Itoa(i)].(string))
		}
		mu.Lock()
		requestNo++
		n := requestNo
		chunkIDs = append(chunkIDs, requested)
		mu.Unlock()

		switch n {
		case 1:
			firstOnce.Do(func() { close(firstStarted) })
			<-releaseFirst
		case 2:
			secondOnce.Do(func() { close(secondStarted) })
		}
		writeDetailBatchResponse(t, w, generatedDetailData(t, request.Variables))
	}))
	t.Cleanup(s.Close)
	t.Cleanup(release)

	type fetchResult struct {
		details map[model.TicketID]model.Detail
		err     error
	}
	done := make(chan fetchResult, 1)
	go func() {
		details, err := httpBatchProvider(s.URL).FetchDetails(t.Context(), ids)
		done <- fetchResult{details: details, err: err}
	}()

	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first Detail chunk did not start")
	}
	select {
	case <-secondStarted:
		release()
		t.Fatal("second Detail chunk started before the first completed")
	case <-time.After(100 * time.Millisecond):
	}
	release()

	var result fetchResult
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("FetchDetails did not finish after releasing the first chunk")
	}
	if result.err != nil {
		t.Fatalf("FetchDetails: %v", result.err)
	}
	if len(result.details) != len(ids) {
		t.Fatalf("details = %d, want %d", len(result.details), len(ids))
	}
	mu.Lock()
	gotChunks := append([][]model.TicketID(nil), chunkIDs...)
	gotRequests := requestNo
	mu.Unlock()
	if gotRequests != 2 {
		t.Fatalf("requests = %d, want two sequential chunks", gotRequests)
	}
	gotSizes := []int{len(gotChunks[0]), len(gotChunks[1])}
	if want := []int{100, 1}; !reflect.DeepEqual(gotSizes, want) {
		t.Errorf("chunk sizes = %v, want %v", gotSizes, want)
	}
	flattened := append(append([]model.TicketID(nil), gotChunks[0]...), gotChunks[1]...)
	if !reflect.DeepEqual(flattened, ids) {
		t.Errorf("requested IDs = %v, want canonical input order", flattened)
	}
}

func TestFetchDetailsCanonicalizesFirstSeenNonEmptyIDs(t *testing.T) {
	requestCh := make(chan detailBatchRequest, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := decodeDetailBatchRequest(t, r)
		requestCh <- request
		writeDetailBatchResponse(t, w, generatedDetailData(t, request.Variables))
	}))
	t.Cleanup(s.Close)

	input := []model.TicketID{"", "second", "first", "", "second", "third", "first"}
	details, err := httpBatchProvider(s.URL).FetchDetails(t.Context(), input)
	if err != nil {
		t.Fatalf("FetchDetails: %v", err)
	}
	request := <-requestCh
	wantVariables := map[string]any{"id0": "second", "id1": "first", "id2": "third"}
	if !reflect.DeepEqual(request.Variables, wantVariables) {
		t.Errorf("variables = %#v, want first-seen canonical %#v", request.Variables, wantVariables)
	}
	if len(details) != 3 {
		t.Errorf("details = %v, want three canonical entries", details)
	}
}

func TestFetchDetailsEmptyInputDoesNoTokenOrHTTPIO(t *testing.T) {
	var tokenCalls, httpCalls atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		httpCalls.Add(1)
		return nil, errors.New("unexpected HTTP request")
	})}
	p := github.New("github.com",
		github.WithHTTPClient(client),
		github.WithTokenSource(func(context.Context, string) (string, error) {
			tokenCalls.Add(1)
			return fixtureToken, nil
		}),
	)

	for _, ids := range [][]model.TicketID{nil, {"", ""}} {
		details, err := p.FetchDetails(t.Context(), ids)
		if err != nil || details == nil || len(details) != 0 {
			t.Errorf("FetchDetails(%v) = %v, %v; want non-nil empty, nil", ids, details, err)
		}
	}
	if tokenCalls.Load() != 0 || httpCalls.Load() != 0 {
		t.Errorf("token/HTTP calls = %d/%d, want 0/0", tokenCalls.Load(), httpCalls.Load())
	}
}

func TestFetchDetailsFixturePreservesSiblingsAndDiscardsErroredAlias(t *testing.T) {
	s := newReplayServer(t, response{file: "detail_batch_partial.json"})
	details, err := newProvider(s).FetchDetails(t.Context(), partialDetailBatchIDs)
	if len(details) != 2 || details["batch-node-a"].TicketID != "batch-node-a" ||
		details["batch-node-c"].TicketID != "batch-node-c" {
		t.Fatalf("details = %#v, want successful aliases 0 and 2", details)
	}
	if _, leaked := details["batch-node-b"]; leaked {
		t.Error("partial data for errored alias batch-node-b was accepted")
	}
	failures := requireDetailFailures(t, err, partialDetailBatchIDs[1:2])
	failure := failures.Failures["batch-node-b"]
	if provider.KindOf(failure) != provider.KindAuth {
		t.Errorf("failure kind = %s, want auth", provider.KindOf(failure))
	}
	for _, message := range []string{"comments unavailable", "dependencies unavailable"} {
		if !strings.Contains(failure.Error(), message) {
			t.Errorf("failure = %q, want grouped message %q", failure, message)
		}
	}
	if got := len(s.recorded()); got != 1 {
		t.Errorf("requests = %d, want one batch and no singular fallback", got)
	}
}

func TestFetchDetailsFailsClosedOnMalformedOrMismatchedData(t *testing.T) {
	ids := []model.TicketID{"requested-a", "requested-b"}
	validA := detailNodeJSON(string(ids[0]), "success")
	validB := detailNodeJSON(string(ids[1]), "success")
	tests := []struct {
		name            string
		body            string
		data            map[string]any
		wantSuccess     bool
		wantKind        provider.Kind
		wantFailureText string
		wantAllFailures bool
		forbiddenMapKey model.TicketID
	}{
		{name: "missing data envelope", body: `{}`, wantKind: provider.KindUnavailable, wantFailureText: "missing data alias", wantAllFailures: true},
		{name: "null data envelope", data: nil, wantKind: provider.KindUnavailable, wantFailureText: "missing data alias", wantAllFailures: true},
		{name: "missing alias", data: map[string]any{"detail0": validA}, wantSuccess: true, wantKind: provider.KindUnavailable, wantFailureText: "missing data alias"},
		{name: "null node", data: map[string]any{"detail0": validA, "detail1": nil}, wantSuccess: true, wantKind: provider.KindBadRef, wantFailureText: "no ticket found"},
		{name: "non-Issue node", data: map[string]any{"detail0": validA, "detail1": map[string]any{"__typename": "PullRequest"}}, wantSuccess: true, wantKind: provider.KindBadRef, wantFailureText: "no ticket found"},
		{
			name: "mismatched node ID", data: map[string]any{"detail0": validA, "detail1": detailNodeJSON("substituted", "wrong")},
			wantSuccess: true, wantKind: provider.KindUnknown,
			wantFailureText: `provider: detail for "requested-b" returned TicketID "substituted"`,
			forbiddenMapKey: "substituted",
		},
		{
			name: "unexpected data alias", data: map[string]any{"detail0": validA, "detail1": validB, "detail2": validA},
			wantKind: provider.KindUnavailable, wantFailureText: "unexpected data alias", wantAllFailures: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.body
			if body == "" {
				body = detailBatchBody(t, tt.data, nil)
			}
			s := newReplayServer(t, response{body: body})
			details, err := newProvider(s).FetchDetails(t.Context(), ids)
			failedIDs := ids[1:]
			if tt.wantAllFailures {
				failedIDs = ids
			}
			failures := requireDetailFailures(t, err, failedIDs)
			for _, id := range failedIDs {
				failure := failures.Failures[id]
				if got := provider.KindOf(failure); got != tt.wantKind {
					t.Errorf("failure[%q] kind = %s, want %s", id, got, tt.wantKind)
				}
				if !strings.Contains(failure.Error(), tt.wantFailureText) {
					t.Errorf("failure[%q] = %q, want %q", id, failure, tt.wantFailureText)
				}
			}
			if tt.wantSuccess {
				if len(details) != 1 || details[ids[0]].TicketID != ids[0] {
					t.Errorf("details = %#v, want only requested-a", details)
				}
			} else if len(details) != 0 {
				t.Errorf("details = %#v, want response-wide failure", details)
			}
			if tt.forbiddenMapKey != "" {
				if _, substituted := details[tt.forbiddenMapKey]; substituted {
					t.Errorf("mismatched response was substituted under key %q", tt.forbiddenMapKey)
				}
			}
			if got := len(s.recorded()); got != 1 {
				t.Errorf("requests = %d, want one failed batch and no singular fallback", got)
			}
		})
	}
}

func TestFetchDetailsResponseWideFailuresCoverEveryChunkID(t *testing.T) {
	ids := []model.TicketID{"a", "b", "c"}
	valid := map[string]any{
		"detail0": detailNodeJSON("a", "a"),
		"detail1": detailNodeJSON("b", "b"),
		"detail2": detailNodeJSON("c", "c"),
	}
	tests := []struct {
		name     string
		response response
		kind     provider.Kind
		contains string
		wantID   bool
	}{
		{name: "auth status", response: response{status: http.StatusUnauthorized, body: `{}`}, kind: provider.KindAuth, contains: "authentication failed"},
		{
			name: "rate status", response: response{status: http.StatusForbidden, body: `{}`, headers: map[string]string{"x-ratelimit-remaining": "0", "x-ratelimit-reset": "1767225600"}},
			kind: provider.KindRateLimit, contains: "rate limit exceeded",
		},
		{name: "server status", response: response{status: http.StatusInternalServerError, body: `{}`}, kind: provider.KindUnavailable, contains: "unexpected response 500"},
		{name: "malformed JSON", response: response{body: `{"data": {`}, kind: provider.KindUnavailable, contains: "decoding the response"},
		{
			name:     "pathless GraphQL error",
			response: response{body: detailBatchBody(t, valid, []map[string]any{{"type": "FORBIDDEN", "message": "pathless refusal"}})},
			kind:     provider.KindAuth, contains: "pathless refusal",
		},
		{
			name: "alias NOT_FOUND cannot mask pathless rate limit",
			response: response{
				body: detailBatchBody(t, valid, []map[string]any{
					{"type": "NOT_FOUND", "message": "local miss", "path": []any{"detail0"}},
					{"type": "RATE_LIMITED", "message": "global rate limit"},
				}),
				headers: map[string]string{"x-ratelimit-reset": "1767225600"},
			},
			kind: provider.KindRateLimit, contains: "rate limit exceeded",
		},
		{
			name: "unknown alias NOT_FOUND is malformed",
			response: response{body: detailBatchBody(t, valid, []map[string]any{{
				"type": "NOT_FOUND", "message": "unattributable miss", "path": []any{"detail99"},
			}})},
			kind: provider.KindUnavailable, contains: "unattributable NOT_FOUND", wantID: true,
		},
		{
			name:     "malformed GraphQL path",
			response: response{body: detailBatchBody(t, valid, []map[string]any{{"type": "FORBIDDEN", "message": "malformed path", "path": []any{7, "body"}}})},
			kind:     provider.KindAuth, contains: "malformed path",
		},
		{
			name:     "unknown GraphQL alias",
			response: response{body: detailBatchBody(t, valid, []map[string]any{{"type": "FORBIDDEN", "message": "unknown alias", "path": []any{"detail99", "body"}}})},
			kind:     provider.KindAuth, contains: "unknown alias",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReplayServer(t, tt.response)
			details, err := newProvider(s).FetchDetails(t.Context(), ids)
			if len(details) != 0 {
				t.Errorf("details = %#v, want response-wide failure to discard all data", details)
			}
			failures := requireDetailFailures(t, err, ids)
			for _, id := range ids {
				failure := failures.Failures[id]
				if got := provider.KindOf(failure); got != tt.kind {
					t.Errorf("failure[%q] kind = %s, want %s", id, got, tt.kind)
				}
				if !strings.Contains(failure.Error(), tt.contains) {
					t.Errorf("failure[%q] = %q, want %q", id, failure, tt.contains)
				}
				if tt.wantID && !strings.Contains(failure.Error(), string(id)) {
					t.Errorf("failure[%q] = %q, want requested ID in malformed-response diagnostic", id, failure)
				}
			}
			if got := len(s.recorded()); got != 1 {
				t.Errorf("requests = %d, want one batch and no singular fallbacks", got)
			}
		})
	}
}

func TestFetchDetailsContinuesAfterOrdinaryFailedChunk(t *testing.T) {
	ids := batchIDs(101)
	var requests atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"temporary failure"}`))
			return
		}
		request := decodeDetailBatchRequest(t, r)
		writeDetailBatchResponse(t, w, generatedDetailData(t, request.Variables))
	}))
	t.Cleanup(s.Close)

	details, err := httpBatchProvider(s.URL).FetchDetails(t.Context(), ids)
	if len(details) != 1 || details[ids[100]].TicketID != ids[100] {
		t.Errorf("details = %#v, want successful final chunk only", details)
	}
	failures := requireDetailFailures(t, err, ids[:100])
	for _, id := range ids[:100] {
		if provider.KindOf(failures.Failures[id]) != provider.KindUnavailable {
			t.Errorf("failure[%q] kind = %s, want unavailable", id, provider.KindOf(failures.Failures[id]))
		}
	}
	if requests.Load() != 2 {
		t.Errorf("requests = %d, want failed chunk followed by successful chunk", requests.Load())
	}
}

func TestFetchDetailsPreCancelledInputDoesNoTokenOrHTTPIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var tokenCalls, httpCalls atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		httpCalls.Add(1)
		return nil, errors.New("unexpected HTTP request")
	})}
	p := github.New("github.com",
		github.WithHTTPClient(client),
		github.WithTokenSource(func(context.Context, string) (string, error) {
			tokenCalls.Add(1)
			return fixtureToken, nil
		}),
	)

	details, err := p.FetchDetails(ctx, []model.TicketID{"a"})
	if !errors.Is(err, context.Canceled) || details == nil || len(details) != 0 {
		t.Errorf("details/error = %#v/%v, want non-nil empty/context.Canceled", details, err)
	}
	if tokenCalls.Load() != 0 || httpCalls.Load() != 0 {
		t.Errorf("token/HTTP calls = %d/%d, want 0/0", tokenCalls.Load(), httpCalls.Load())
	}
}

func TestFetchDetailsCancellationStopsAfterBlockedSecondChunk(t *testing.T) {
	ids := batchIDs(201)
	var requests atomic.Int64
	secondStarted := make(chan struct{})
	secondRelease := make(chan struct{})
	var releaseOnce sync.Once
	releaseSecond := func() { releaseOnce.Do(func() { close(secondRelease) }) }
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch n := requests.Add(1); n {
		case 1:
			request := decodeDetailBatchRequest(t, r)
			writeDetailBatchResponse(t, w, generatedDetailData(t, request.Variables))
		case 2:
			close(secondStarted)
			<-secondRelease
		default:
			t.Errorf("unexpected request %d after cancellation", n)
		}
	}))
	t.Cleanup(s.Close)
	t.Cleanup(releaseSecond)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	type result struct {
		details map[model.TicketID]model.Detail
		err     error
	}
	done := make(chan result, 1)
	go func() {
		details, err := httpBatchProvider(s.URL).FetchDetails(ctx, ids)
		done <- result{details: details, err: err}
	}()

	select {
	case <-secondStarted:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("second Detail chunk did not start")
	}

	var got result
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("FetchDetails did not return after cancellation")
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", got.err)
	}
	var failures *provider.DetailFailures
	if errors.As(got.err, &failures) {
		t.Errorf("cancellation was joined with invented failures: %#v", failures.Failures)
	}
	if len(got.details) != 100 {
		t.Fatalf("details = %d, want first completed chunk's 100 successes", len(got.details))
	}
	for _, id := range ids[:100] {
		if got.details[id].TicketID != id {
			t.Errorf("completed detail %q was not preserved", id)
		}
	}
	for _, id := range ids[100:] {
		if _, invented := got.details[id]; invented {
			t.Errorf("uncompleted detail %q was invented", id)
		}
	}
	if requests.Load() != 2 {
		t.Errorf("requests = %d, want first chunk plus cancelled second and no third", requests.Load())
	}
}

func TestFetchDetailsTreatsProviderLocalWrappedCancellationAsOrdinaryFailure(t *testing.T) {
	ids := batchIDs(101)
	var requests atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, fmt.Errorf("provider child: %w", context.Canceled)
	})}
	p := github.New("github.com",
		github.WithHTTPClient(client),
		github.WithTokenSource(func(context.Context, string) (string, error) { return fixtureToken, nil }),
	)

	details, err := p.FetchDetails(context.Background(), ids)
	if len(details) != 0 {
		t.Errorf("details = %#v, want no transport successes", details)
	}
	failures := requireDetailFailures(t, err, ids)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("DetailFailures does not expose wrapped context.Canceled: %v", err)
	}
	for _, id := range ids {
		failure := failures.Failures[id]
		if !errors.Is(failure, context.Canceled) {
			t.Errorf("failure[%q] = %v, want wrapped context.Canceled", id, failure)
		}
		if provider.KindOf(failure) != provider.KindUnavailable {
			t.Errorf("failure[%q] kind = %s, want unavailable", id, provider.KindOf(failure))
		}
	}
	if requests.Load() != 2 {
		t.Errorf("requests = %d, want both ordinary failed chunks", requests.Load())
	}
}

func TestFetchDetailAndFetchDetailsUseTheSameFieldFragment(t *testing.T) {
	id := model.TicketID("shared-fragment-id")
	s := newReplayServer(t,
		response{body: detailBatchBody(t, map[string]any{"node": detailNodeJSON(string(id), "singular")}, nil)},
		response{body: detailBatchBody(t, map[string]any{"detail0": detailNodeJSON(string(id), "batch")}, nil)},
	)
	p := newProvider(s)
	if _, err := p.FetchDetail(t.Context(), id); err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}
	if _, err := p.FetchDetails(t.Context(), []model.TicketID{id}); err != nil {
		t.Fatalf("FetchDetails: %v", err)
	}

	requests := s.recorded()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want singular then batch", len(requests))
	}
	fragments := make([]string, len(requests))
	for i, request := range requests {
		at := strings.Index(request.query, "fragment DetailFields on Issue")
		if at < 0 {
			t.Fatalf("request %d omits the DetailFields fragment: %s", i, request.query)
		}
		fragments[i] = request.query[at:]
		if strings.Count(request.query, "fragment DetailFields on Issue") != 1 ||
			strings.Count(request.query, "...DetailFields") != 1 {
			t.Errorf("request %d does not define and apply the shared fragment exactly once", i)
		}
	}
	if fragments[0] != fragments[1] {
		t.Errorf("singular and batch Detail fragments differ:\n--- singular ---\n%s\n--- batch ---\n%s", fragments[0], fragments[1])
	}
}

func TestFetchDetailsThroughSanitizedCleansSuccessAndPreservesPartialFailure(t *testing.T) {
	s := newReplayServer(t, response{file: "detail_batch_partial.json"})
	p := provider.Sanitized(newProvider(s))
	details, err := p.FetchDetails(t.Context(), partialDetailBatchIDs)
	if strings.Contains(details["batch-node-a"].Description, "\x1b") {
		t.Errorf("sanitized description still contains an escape: %q", details["batch-node-a"].Description)
	}
	if !strings.Contains(details["batch-node-a"].Description, "red") {
		t.Errorf("sanitized description lost ordinary text: %q", details["batch-node-a"].Description)
	}
	failures := requireDetailFailures(t, err, partialDetailBatchIDs[1:2])
	failure := failures.Failures["batch-node-b"]
	if provider.KindOf(failure) != provider.KindAuth || !strings.Contains(failure.Error(), "comments unavailable") {
		t.Errorf("partial failure = %v (%s), want inspectable GitHub auth cause", failure, provider.KindOf(failure))
	}
	if got := len(s.recorded()); got != 1 {
		t.Errorf("requests = %d, want one native batch through the sanitizer", got)
	}
}

func BenchmarkFetchDetails100Aliases(b *testing.B) {
	ids := batchIDs(100)
	var requests atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		request := decodeDetailBatchRequest(b, r)
		writeDetailBatchResponse(b, w, generatedDetailData(b, request.Variables))
	}))
	defer s.Close()
	p := httpBatchProvider(s.URL)

	b.ResetTimer()
	for b.Loop() {
		details, err := p.FetchDetails(context.Background(), ids)
		if err != nil {
			b.Fatalf("FetchDetails: %v", err)
		}
		if len(details) != len(ids) {
			b.Fatalf("details = %d, want %d", len(details), len(ids))
		}
	}
	b.StopTimer()
	gotRequests := requests.Load()
	b.ReportMetric(float64(gotRequests)/float64(b.N), "requests/op")
	if gotRequests != int64(b.N) {
		b.Fatalf("requests = %d, want exactly one per operation (%d)", gotRequests, b.N)
	}
}
