package jsonout

import (
	"encoding/json"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

// cycles is always an array on the wire. BlockingGraph.Cycles() happens to
// return a non-nil empty slice today, so the normalization that upholds the
// convention is never reached through the renderer -- and a nil slice marshals
// as null, which is the one thing README says this key never is.
func TestCyclesDocNormalizesNilToAnArray(t *testing.T) {
	for _, tt := range []struct {
		name   string
		cycles [][]model.TicketID
		want   string
	}{
		{"nil", nil, `[]`},
		{"empty", [][]model.TicketID{}, `[]`},
		{"one cycle", [][]model.TicketID{{"a", "b"}}, `[["a","b"]]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(cyclesDoc(tt.cycles))
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if string(raw) != tt.want {
				t.Errorf("cycles = %s, want %s", raw, tt.want)
			}
		})
	}
}
