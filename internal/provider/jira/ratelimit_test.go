package jira_test

import (
	"testing"

	"github.com/niekcandaele/sitrep/internal/provider/jira"
)

func TestJiraDoesNotDeclareRateLimitBudgetCapability(t *testing.T) {
	if jira.New("jira.example.test").Capabilities().RateLimitBudget {
		t.Error("Jira declared unsupported RateLimitBudget capability")
	}
}
