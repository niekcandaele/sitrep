package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/niekcandaele/sitrep/internal/detailfanout"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/github"
)

type githubFrontierProviderSpy struct {
	provider.Provider
	singularCalls atomic.Int64
	pluralCalls   atomic.Int64
}

func (p *githubFrontierProviderSpy) FetchDetail(ctx context.Context, id model.TicketID) (model.Detail, error) {
	p.singularCalls.Add(1)
	return p.Provider.FetchDetail(ctx, id)
}

func (p *githubFrontierProviderSpy) FetchDetails(ctx context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, error) {
	p.pluralCalls.Add(1)
	return p.Provider.FetchDetails(ctx, ids)
}

type githubFrontierGraphQLRequest struct {
	Query     string            `json:"query"`
	Variables map[string]string `json:"variables"`
}

func TestFrontierGitHubBatchFanoutSeatsOneHundredMembers(t *testing.T) {
	t.Run("all aliases succeed", func(t *testing.T) {
		testFrontierGitHubBatchFanout(t, -1)
	})
	t.Run("one alias-local failure", func(t *testing.T) {
		testFrontierGitHubBatchFanout(t, 37)
	})
}

func testFrontierGitHubBatchFanout(t *testing.T, failedAlias int) {
	const members = 100

	var (
		requestsMu sync.Mutex
		requests   []githubFrontierGraphQLRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}

		var request githubFrontierGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode GraphQL request: %v", err)
			return
		}
		requestsMu.Lock()
		requests = append(requests, request)
		requestsMu.Unlock()

		data := make(map[string]any, len(request.Variables))
		for i := 0; i < len(request.Variables); i++ {
			id := request.Variables["id"+strconv.Itoa(i)]
			data["detail"+strconv.Itoa(i)] = map[string]any{
				"id":         id,
				"number":     i + 1,
				"url":        fmt.Sprintf("https://example.test/issues/%d", i+1),
				"body":       "",
				"repository": map[string]any{"nameWithOwner": "acme/widgets"},
				"comments":   map[string]any{"totalCount": 0, "nodes": []any{}},
				"blockedBy":  map[string]any{"nodes": []any{}},
				"blocking":   map[string]any{"nodes": []any{}},
			}
		}
		response := map[string]any{"data": data}
		if failedAlias >= 0 {
			response["errors"] = []any{map[string]any{
				"type": "FORBIDDEN", "message": "comments unavailable",
				"path": []any{"detail" + strconv.Itoa(failedAlias), "comments", "nodes"},
			}}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode GraphQL response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	githubProvider := github.New("github.com",
		github.WithEndpoint(server.URL),
		github.WithTokenSource(func(context.Context, string) (string, error) { return "test-token", nil }),
		github.WithUserAgent("sitrep/test"),
	)
	providerSpy := &githubFrontierProviderSpy{Provider: githubProvider}
	clock := newClock()
	tickets := make([]model.Ticket, members)
	for i := range tickets {
		id := model.TicketID(fmt.Sprintf("NODE-ID-%03d", i))
		tickets[i] = model.Ticket{
			ID: id, Key: fmt.Sprintf("#%d", i+1), Title: fmt.Sprintf("member %03d", i+1), Status: model.StatusTodo,
		}
	}
	successfulMembers := members
	var failedID model.TicketID
	if failedAlias >= 0 {
		successfulMembers--
		failedID = tickets[failedAlias].ID
	}
	input := ListInput{
		Header:       Header{Key: "GH-FANOUT", Title: "one hundred members"},
		Tickets:      tickets,
		Capabilities: githubProvider.Capabilities(),
		FetchedAt:    clock.now(),
	}

	session := startWith(t, clock, Options{
		Initial:      &input,
		DetailFanout: detailfanout.FromProvider(providerSpy),
		Interval:     0,
		Now:          clock.now,
		NoMouse:      true,
	})
	session.waitFor(t, "GH-FANOUT")
	session.tm.Send(keyPress("v"))
	waitUntil(t, "the one logical GitHub Detail batch", func() bool {
		return providerSpy.pluralCalls.Load() == 1
	})
	session.waitFor(t, fmt.Sprintf("%d actionable", successfulMembers))
	m, _ := session.finish(t)

	if got := providerSpy.pluralCalls.Load(); got != 1 {
		t.Errorf("logical FetchDetails calls = %d, want 1", got)
	}
	if got := providerSpy.singularCalls.Load(); got != 0 {
		t.Errorf("singular FetchDetail calls = %d, want 0", got)
	}

	requestsMu.Lock()
	gotRequests := append([]githubFrontierGraphQLRequest(nil), requests...)
	requestsMu.Unlock()
	if len(gotRequests) != 1 {
		t.Fatalf("GraphQL requests = %d, want 1", len(gotRequests))
	}
	request := gotRequests[0]
	if got := len(request.Variables); got != members {
		t.Errorf("GraphQL variables = %d, want %d", got, members)
	}
	for i, ticket := range tickets {
		suffix := strconv.Itoa(i)
		if got := request.Variables["id"+suffix]; got != string(ticket.ID) {
			t.Errorf("id%s = %q, want %q", suffix, got, ticket.ID)
		}
		for _, token := range []string{
			"$id" + suffix + ":ID!",
			"detail" + suffix + ": node(id:$id" + suffix + ") { ...DetailFields }",
		} {
			if !strings.Contains(request.Query, token) {
				t.Errorf("GraphQL batch omits %q", token)
			}
		}
		if strings.Contains(request.Query, string(ticket.ID)) {
			t.Errorf("GraphQL batch interpolated Ticket ID %q", ticket.ID)
		}
	}
	if got := strings.Count(request.Query, "...DetailFields"); got != members {
		t.Errorf("DetailFields applications = %d, want %d", got, members)
	}

	if m.mode != modeFrontier {
		t.Fatalf("mode = %v, want Frontier", m.mode)
	}
	if m.frontier.refusal != nil {
		t.Fatalf("cold non-hostile Frontier refused its canvas: %+v", m.frontier.refusal)
	}
	if got := m.frontier.planned; got != members {
		t.Errorf("planned Details = %d, want %d", got, members)
	}
	if got := m.frontier.done; got != members {
		t.Errorf("completed Details = %d, want %d", got, members)
	}
	wantFailures := members - successfulMembers
	if got := len(m.frontier.failed); got != wantFailures {
		t.Errorf("failed Details = %d, want %d", got, wantFailures)
	}
	if got := len(m.frontier.failureErrors); got != wantFailures {
		t.Errorf("failure errors = %d, want %d", got, wantFailures)
	}
	if m.frontier.protocolWarning != nil {
		t.Errorf("alias-local member failure became a protocol warning: %v", m.frontier.protocolWarning)
	}
	if got := len(m.details); got != successfulMembers {
		t.Errorf("cached Details = %d, want %d", got, successfulMembers)
	}
	if got := len(m.frontier.input.Links); got != successfulMembers {
		t.Fatalf("seated Links = %d, want %d", got, successfulMembers)
	}
	for i, ticket := range tickets {
		_, cached := m.details[ticket.ID]
		_, seated := m.frontier.input.Links[ticket.ID]
		actionability, member := m.frontier.graph.For(ticket.ID)
		if !member {
			t.Errorf("graph omitted Watchlist member %q", ticket.ID)
			continue
		}
		if i == failedAlias {
			if cached || seated || actionability.LinksKnown || actionability.Actionable {
				t.Errorf("failed alias %q leaked evidence/actionability: cached=%t seated=%t value=%+v",
					ticket.ID, cached, seated, actionability)
			}
			continue
		}
		if !cached || !seated || !actionability.LinksKnown || !actionability.Actionable {
			t.Errorf("successful sibling %q was not seated as known and Actionable: cached=%t seated=%t value=%+v",
				ticket.ID, cached, seated, actionability)
		}
	}
	if failedAlias >= 0 {
		if _, failed := m.frontier.failed[failedID]; !failed {
			t.Errorf("failed alias %q is absent from failure set %v", failedID, m.frontier.failed)
		}
		failure := m.frontier.failureErrors[failedID]
		if failure == nil || !strings.Contains(failure.Error(), "comments unavailable") {
			t.Errorf("failed alias error = %v, want alias-local GraphQL error", failure)
		}
	}
}
