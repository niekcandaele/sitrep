package tui

import (
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

// titled is a Ticket with a real title, which is what the fuzzy find reads.
func titled(key, title string, status model.StatusCategory) model.Ticket {
	t := ticket(key, status)
	t.Title = title
	return t
}

// keys renders a Ticket slice as the keys a test asserts on: order is part of
// the contract, so the assertion has to be able to see it.
func keys(tickets []model.Ticket) string {
	out := make([]string, 0, len(tickets))
	for _, t := range tickets {
		out = append(out, t.Key)
	}
	return strings.Join(out, " ")
}

// fixtureTickets is the ten-Ticket fixture the goldens are drawn from, so the
// table tests and the frames are talking about the same Epic.
func fixtureTickets() []model.Ticket { return fake.FixtureSnapshot().Tickets }

func TestFilterActive(t *testing.T) {
	tests := []struct {
		name   string
		filter Filter
		want   bool
	}{
		{"the zero Filter narrows nothing", Filter{}, false},
		{"hiding finished work narrows", Filter{HideFinished: true}, true},
		{"a query narrows", Filter{Query: "shard"}, true},
		{"a whitespace-only query narrows nothing", Filter{Query: "  \t "}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.filter.Active(); got != tt.want {
				t.Errorf("Active() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterApply(t *testing.T) {
	tickets := []model.Ticket{
		titled("#1", "Draft the shard sync protocol", model.StatusInProgress),
		titled("#2", "Emit sync telemetry", model.StatusDone),
		titled("#3", "Mirror shards to the legacy feed", model.StatusCancelled),
		titled("#4", "Cache shard lookups", model.StatusTodo),
		titled("#5", "Whatever the Provider meant by this", model.StatusUnknown),
	}

	tests := []struct {
		name   string
		filter Filter
		want   string
	}{
		{"the zero Filter admits everything in input order", Filter{}, "#1 #2 #3 #4 #5"},
		{
			name:   "hiding finished work drops Done and Cancelled and keeps Unknown",
			filter: Filter{HideFinished: true},
			want:   "#1 #4 #5",
		},
		{"a query alone spans every Status Category", Filter{Query: "shard"}, "#1 #3 #4"},
		{
			name:   "the two criteria compose with AND",
			filter: Filter{HideFinished: true, Query: "shard"},
			want:   "#1 #4",
		},
		{"a whitespace-only query is the identity", Filter{Query: "   "}, "#1 #2 #3 #4 #5"},
		{"a query nothing answers admits nothing", Filter{Query: "zzz"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := keys(tt.filter.Apply(tickets)); got != tt.want {
				t.Errorf("Apply() kept %q, want %q", got, tt.want)
			}
		})
	}
}

// Filtering is applied on the way to the screen, so it must leave the reading
// it was handed exactly as it found it.
func TestFilterApplyDoesNotDisturbItsInput(t *testing.T) {
	tickets := fixtureTickets()
	before := keys(tickets)

	Filter{HideFinished: true, Query: "shard"}.Apply(tickets)

	if after := keys(tickets); after != before {
		t.Errorf("Apply() mutated its input:\n got %q\nwant %q", after, before)
	}
}

// An admit-everything Filter hands back the slice it was given rather than a
// copy: the unfiltered screen is the common case and does not pay for filtering.
func TestFilterApplyOnAnEmptySlice(t *testing.T) {
	if got := (Filter{HideFinished: true}).Apply(nil); len(got) != 0 {
		t.Errorf("Apply(nil) = %v, want nothing", got)
	}
	if got := (Filter{}).Apply(nil); got != nil {
		t.Errorf("the zero Filter allocated for nothing: %v", got)
	}
}

func TestMatchesQuery(t *testing.T) {
	// The fixture's four "shard" Tickets, its cross-repo Ticket and its
	// accented title, so the table and the golden frames agree on the data.
	var (
		shardProtocol = titled("#112", "Draft the shard sync protocol", model.StatusInProgress)
		gadget        = titled("#7", "Teach the gadget agent to speak sync v2", model.StatusInProgress)
		metrique      = titled("#117", "Renseigner la métrique « éclair » du tableau de bord", model.StatusTodo)
	)
	// A Ticket carrying everything the find must not read.
	noisy := titled("#116", "Protocol conformance tests", model.StatusTodo)
	noisy.NativeStatus = "Selected for Development"
	noisy.Assignees = []model.User{{Login: "mara-vos"}}
	noisy.Repository = "acme/widgets"
	noisy.URL = "https://tracker.example.test/acme/widgets/116"

	tests := []struct {
		name   string
		ticket model.Ticket
		query  string
		want   bool
	}{
		{"an empty query matches", shardProtocol, "", true},
		{"a whitespace-only query matches", shardProtocol, " \t ", true},
		{"a substring of the title matches", shardProtocol, "shard", true},
		{"a subsequence with gaps matches", shardProtocol, "dsp", true},
		{"an out-of-order subsequence does not match", shardProtocol, "psd", false},
		{"an upper-case query matches a lower-case title", shardProtocol, "SHARD", true},
		{"a lower-case query matches an upper-case title", shardProtocol, "draft", true},
		{"the key matches without its hash", shardProtocol, "112", true},
		{"the key matches with its hash", shardProtocol, "#112", true},
		{"the cross-repo key matches", gadget, "#7", true},
		{"two terms both present match", shardProtocol, "shard sync", true},
		{"two terms in either order match", shardProtocol, "sync shard", true},
		{"a second absent term narrows to nothing", shardProtocol, "shard telemetry", false},
		{"the key and the title match together", shardProtocol, "112 shard", true},
		{"an accented title matches when typed accented", metrique, "métrique", true},
		{"an accented title does not match unaccented", metrique, "metrique", false},
		{"a Native Status matches nothing", noisy, "Selected for Development", false},
		{"another Native Status matches nothing", noisy, "not planned", false},
		{"an assignee login matches nothing", noisy, "mara-vos", false},
		{"a repository matches nothing", noisy, "acme/widgets", false},
		{"a URL matches nothing", noisy, "tracker.example.test", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesQuery(tt.ticket, tt.query); got != tt.want {
				t.Errorf("matchesQuery(%q, %q) = %v, want %v", tt.ticket.Key, tt.query, got, tt.want)
			}
		})
	}
}

// The hide toggle reads the Status Category and nothing else, for every
// category the model defines.
func TestHideFinishedFollowsIsFinished(t *testing.T) {
	for _, c := range model.AllStatusCategories() {
		kept := Filter{HideFinished: true}.Apply([]model.Ticket{ticket("#1", c)})
		if hidden := len(kept) == 0; hidden != c.IsFinished() {
			t.Errorf("%s: hidden = %v, want IsFinished() = %v", c, hidden, c.IsFinished())
		}
	}
}
