package github

import (
	"strings"
	"testing"
	"time"
)

func TestRateLimitBudgetParsingIsStrictAndOptional(t *testing.T) {
	reset := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	for _, test := range []struct {
		name      string
		remaining string
		resetsAt  string
		valid     bool
	}{
		{"valid zero", "0", `"2030-01-02T03:04:05Z"`, true},
		{"negative", "-1", `"2030-01-02T03:04:05Z"`, false},
		{"fraction", "1.5", `"2030-01-02T03:04:05Z"`, false},
		{"string", `"1"`, `"2030-01-02T03:04:05Z"`, false},
		{"null", "null", `"2030-01-02T03:04:05Z"`, false},
		{"missing reset", "1", "", false},
		{"malformed reset", "1", `"nope"`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := (rateLimitNode{Remaining: []byte(test.remaining), ResetAt: []byte(test.resetsAt)}).budget()
			if ok != test.valid {
				t.Fatalf("budget() valid = %v, want %v", ok, test.valid)
			}
			if ok && (got.Remaining != 0 || !got.ResetsAt.Equal(reset)) {
				t.Errorf("budget() = %+v, want zero remaining at %s", got, reset)
			}
		})
	}
}

func TestResolveDocumentsSelectBudgetOnceAndDetailDoesNot(t *testing.T) {
	for name, document := range map[string]string{
		"query":    queryMembershipDocument,
		"epic":     epicQuery,
		"ref list": buildRefListQuery(2),
	} {
		t.Run(name, func(t *testing.T) {
			if got := strings.Count(document, "rateLimit { remaining resetAt }"); got != 1 {
				t.Errorf("budget selections = %d, want 1", got)
			}
		})
	}
	for name, document := range map[string]string{
		"detail":         detailQuery,
		"batched detail": buildDetailBatchQuery(2),
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(document, "rateLimit") {
				t.Error("Detail document selects rateLimit")
			}
		})
	}
}
