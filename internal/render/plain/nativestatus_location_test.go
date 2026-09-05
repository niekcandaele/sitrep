package plain

import (
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

// TestContextualStatusPolicyLivesInPlain pins where the contextual display
// policy is declared. ADR-0007 puts it in the shared shaping layer, not on the
// thin model: this file is package plain, so it compiles only while that holds.
// The import-direction half of the ADR is enforced by depguard in .golangci.yml,
// which catches edges the Go compiler does not already reject as cycles.
func TestContextualStatusPolicyLivesInPlain(t *testing.T) {
	var ticket model.Ticket
	_ = ShowsNativeStatus(ticket)
	_ = StatusField(ticket)
}
