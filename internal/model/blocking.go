package model

import "slices"

// Blocker is one Ticket standing between another Ticket and being picked up.
type Blocker struct {
	// Target is the blocker's identity as the Link that first named it wrote
	// it. For a Ghost Ticket it is the Ghost's resolved identity, so a Blocker
	// and the matching entry in Ghosts always agree.
	Target LinkTarget
	// Member is true when the blocker is a Watchlist member, false when it is a
	// Ghost Ticket or an anonymous Link target.
	Member bool
	// Status is the blocker's authoritative Status Category: a member's own
	// Ticket.Status, or a Ghost's resolved Status.
	Status StatusCategory
	// StatusKnown is false when the blocker's Status could not be read, which
	// leaves the dependent not Actionable and visibly unverified.
	StatusKnown bool
}

// Satisfied reports whether this blocker permits its dependent to be
// Actionable: its Status is known and has come to rest.
func (b Blocker) Satisfied() bool { return b.StatusKnown && b.Status.IsFinished() }

// Actionability is the derived blocking state of one Watchlist member.
type Actionability struct {
	// TicketID identifies the member this state belongs to.
	TicketID TicketID
	// Status is the member's own Status Category, copied for convenience.
	Status StatusCategory
	// LinksKnown is false when this member's Links could not be read, which
	// leaves it not Actionable however clean the blockers we did see are.
	LinksKnown bool
	// Actionable is the derived property: Status is Todo, Links were readable,
	// and every blocker is Satisfied.
	Actionable bool
	// Blockers are every distinct Ticket this member is BlockedBy, deduplicated
	// on (blocked, blocker) identity, in order of first discovery over the
	// canonical walk. A blocker discovered through another member's Blocks Link
	// therefore lands before or after this member's own BlockedBy blockers
	// depending on that member's Watchlist position.
	Blockers []Blocker
	// InCycle is true when this member sits on a BlockedBy cycle. It never
	// changes Actionable; it reports that the issue data is malformed.
	InCycle bool
}

// Unmet returns the blockers that do not permit this member to be Actionable,
// in Blockers order. It is nil when every blocker is Satisfied.
func (a Actionability) Unmet() []Blocker {
	var unmet []Blocker
	for _, b := range a.Blockers {
		if !b.Satisfied() {
			unmet = append(unmet, b)
		}
	}
	return unmet
}

// HasUnknown reports whether anything about this member's blocking state could
// not be read: its own Links, or any blocker's Status. It is what separates
// "unverified" from "cleanly blocked".
func (a Actionability) HasUnknown() bool {
	if !a.LinksKnown {
		return true
	}
	for _, b := range a.Blockers {
		if !b.StatusKnown {
			return true
		}
	}
	return false
}

// GhostTicket is a Link target that is not a Watchlist member. Its Links are
// never followed: a Ghost is a leaf, and Ghosts never gain Ghosts.
type GhostTicket struct {
	// Target is the Ghost's identity, taken from the first Link that named it,
	// with the first non-Unknown Status any Link gave it.
	Target LinkTarget
}

// BlockingGraph is the derived blocking structure of one Watchlist: its members
// with their Actionability, the Ghost Tickets its Links reach, and the
// BlockedBy cycles its members sit on.
//
// Unlike Progress this is not a plain struct. Its fields are unexported and
// reached through accessors because it carries an internal index that callers
// must not mutate, and because every returned slice is a fresh copy rather than
// a handle on the graph's own storage.
//
// The zero value is a graph over no Tickets with no blocking information.
type BlockingGraph struct {
	supported bool
	members   []Actionability
	index     map[TicketID]int
	ghosts    []GhostTicket
	cycles    [][]TicketID
}

// BlockingLinksSupported reports whether the serving Provider declared the
// BlockingLinks Capability. When it is false the graph claims nothing: no
// blockers, no Ghosts, no cycles and nothing Actionable, because a Provider
// that cannot express "blocks" has nothing to say about direction.
func (g BlockingGraph) BlockingLinksSupported() bool { return g.supported }

// Members returns every Watchlist member's Actionability in canonical order:
// the order Tickets were given to BuildBlockingGraph.
func (g BlockingGraph) Members() []Actionability {
	out := make([]Actionability, len(g.members))
	for i, m := range g.members {
		out[i] = cloneActionability(m)
	}
	return out
}

// For returns one member's Actionability by TicketID. The second result is
// false when id names no member, which includes every Ghost Ticket.
func (g BlockingGraph) For(id TicketID) (Actionability, bool) {
	i, ok := g.index[id]
	if !ok {
		return Actionability{}, false
	}
	return cloneActionability(g.members[i]), true
}

// Ghosts returns the Ghost Tickets the Watchlist's blocking Links reach, in
// first-appearance order over the canonical walk.
func (g BlockingGraph) Ghosts() []GhostTicket {
	out := make([]GhostTicket, len(g.ghosts))
	copy(out, g.ghosts)
	return out
}

// Cycles returns each BlockedBy cycle's node IDs — members and Ghosts alike —
// sorted by canonical node index, with the cycles themselves ordered by their
// smallest index. A self-blocking Ticket is a cycle of one.
func (g BlockingGraph) Cycles() [][]TicketID {
	out := make([][]TicketID, len(g.cycles))
	for i, c := range g.cycles {
		out[i] = make([]TicketID, len(c))
		copy(out[i], c)
	}
	return out
}

func cloneActionability(a Actionability) Actionability {
	out := a
	if a.Blockers != nil {
		out.Blockers = make([]Blocker, len(a.Blockers))
		copy(out.Blockers, a.Blockers)
	}
	return out
}

// anonymousNode marks an edge whose blocker Link carried no Target ID: it still
// blocks, but there is no identity to draw a node for.
const anonymousNode = -1

// blockEdge is one deduplicated "blocked is blocked by blocker" relation over
// canonical node indices.
type blockEdge struct {
	blocked int
	blocker int
	// named is the LinkTarget the first Link to record this edge carried. It
	// supplies an anonymous blocker's whole identity and a member blocker's
	// display identity; a Ghost blocker takes the Ghost's resolved Target.
	named LinkTarget
}

// BuildBlockingGraph derives Actionability, Ghost Tickets and BlockedBy cycles
// for one Watchlist. It is pure: it retains and mutates nothing the caller
// passed, and reads no clock.
//
// tickets are the Watchlist's members in the Provider's stable Selector order.
// That order is the graph's canonical order and everything deterministic
// derives from it.
//
// links are the members' Links, as read from each member's Detail.Links.
// Presence of the key is the tri-state that makes fail-closed real: a TicketID
// absent from links had its Links fetch fail, interrupted or never issued, so
// its blocking state is unknown and it is never Actionable. A key present with
// a nil or empty slice means the Links were read and there genuinely are none.
// A caller that simply omits failed fetches from the map gets fail-closed
// behaviour for free, and a nil map is the honest answer before any Detail has
// resolved: nothing is Actionable yet.
//
// caps are the serving Provider's Capabilities; only BlockingLinks is read.
func BuildBlockingGraph(tickets []Ticket, links map[TicketID][]Link, caps Capabilities) BlockingGraph {
	g := BlockingGraph{
		supported: caps.BlockingLinks,
		members:   make([]Actionability, 0, len(tickets)),
		index:     make(map[TicketID]int, len(tickets)),
	}
	for _, t := range tickets {
		if _, dup := g.index[t.ID]; !dup {
			g.index[t.ID] = len(g.members)
		}
		g.members = append(g.members, Actionability{TicketID: t.ID, Status: t.Status})
	}
	if !caps.BlockingLinks {
		return g
	}

	for i, t := range tickets {
		if _, ok := links[t.ID]; ok {
			g.members[i].LinksKnown = true
		}
	}

	ghostIndex := make(map[TicketID]int, len(tickets))
	var edges []blockEdge
	seen := make(map[[2]int]bool)

	// node returns the canonical index of the node target names, registering a
	// Ghost when the target is not a member. Ghosts follow the members, in
	// first-appearance order.
	node := func(target LinkTarget) int {
		if i, ok := g.index[target.ID]; ok {
			return i
		}
		if target.ID == "" {
			return anonymousNode
		}
		if i, ok := ghostIndex[target.ID]; ok {
			if g.ghosts[i].Target.Status == StatusUnknown {
				g.ghosts[i].Target.Status = target.Status
				g.ghosts[i].Target.NativeStatus = target.NativeStatus
			}
			return len(g.members) + i
		}
		ghostIndex[target.ID] = len(g.ghosts)
		g.ghosts = append(g.ghosts, GhostTicket{Target: target})
		return len(g.members) + len(g.ghosts) - 1
	}

	record := func(blocked, blocker int, named LinkTarget) {
		if blocked == anonymousNode {
			return
		}
		if blocker != anonymousNode {
			key := [2]int{blocked, blocker}
			if seen[key] {
				return
			}
			seen[key] = true
		}
		edges = append(edges, blockEdge{blocked: blocked, blocker: blocker, named: named})
	}

	for i, t := range tickets {
		if !g.members[i].LinksKnown {
			continue
		}
		for _, l := range links[t.ID] {
			switch l.Kind {
			case LinkBlockedBy:
				record(i, node(l.Target), l.Target)
			case LinkBlocks:
				self := linkTargetFromTicket(t)
				record(node(l.Target), i, self)
			case LinkRelates:
				// Relates carries no ordering, so it is not an edge and
				// names no Ghost.
			}
		}
	}

	nodeCount := len(g.members) + len(g.ghosts)
	adjacency := make([][]int, nodeCount)
	for _, e := range edges {
		if e.blocker != anonymousNode {
			adjacency[e.blocked] = append(adjacency[e.blocked], e.blocker)
		}
		if e.blocked < len(g.members) {
			g.members[e.blocked].Blockers = append(g.members[e.blocked].Blockers,
				g.blocker(e, tickets))
		}
	}

	for i := range g.members {
		m := &g.members[i]
		m.Actionable = m.LinksKnown && m.Status == StatusTodo
		for _, b := range m.Blockers {
			if !b.Satisfied() {
				m.Actionable = false
				break
			}
		}
	}

	g.cycles = findCycles(adjacency, g.nodeID)
	for _, c := range g.cycles {
		for _, id := range c {
			if i, ok := g.index[id]; ok {
				g.members[i].InCycle = true
			}
		}
	}
	return g
}

// blocker resolves one edge into the Blocker its dependent carries. A member's
// Status is its own authoritative Ticket.Status rather than the second-hand
// copy a LinkTarget carries.
func (g BlockingGraph) blocker(e blockEdge, tickets []Ticket) Blocker {
	switch {
	case e.blocker == anonymousNode:
		return Blocker{Target: e.named}
	case e.blocker < len(g.members):
		status := tickets[e.blocker].Status
		return Blocker{
			Target:      e.named,
			Member:      true,
			Status:      status,
			StatusKnown: status != StatusUnknown,
		}
	default:
		target := g.ghosts[e.blocker-len(g.members)].Target
		return Blocker{
			Target:      target,
			Status:      target.Status,
			StatusKnown: target.Status != StatusUnknown,
		}
	}
}

// nodeID maps a canonical node index back to its TicketID.
func (g BlockingGraph) nodeID(i int) TicketID {
	if i < len(g.members) {
		return g.members[i].TicketID
	}
	return g.ghosts[i-len(g.members)].Target.ID
}

// linkTargetFromTicket synthesizes the LinkTarget a member would have been
// named by, for the reverse edge a Blocks Link creates.
func linkTargetFromTicket(t Ticket) LinkTarget {
	return LinkTarget{
		ID:           t.ID,
		Key:          t.Key,
		Title:        t.Title,
		URL:          t.URL,
		Status:       t.Status,
		NativeStatus: t.NativeStatus,
	}
}

// findCycles returns the strongly connected components of adjacency that are
// cycles: every component of two or more nodes, plus every single node with a
// self-loop. Tarjan visits each node once, so cyclic input terminates
// structurally rather than by a guard someone has to remember.
//
// Nodes within a cycle come out in canonical index order and cycles in order of
// their smallest index, so no map iteration can reach the output.
func findCycles(adjacency [][]int, id func(int) TicketID) [][]TicketID {
	n := len(adjacency)
	const unvisited = -1
	index := make([]int, n)
	low := make([]int, n)
	onStack := make([]bool, n)
	for i := range index {
		index[i] = unvisited
	}
	var stack []int
	next := 0
	var components [][]int

	var strongConnect func(v int)
	strongConnect = func(v int) {
		index[v] = next
		low[v] = next
		next++
		stack = append(stack, v)
		onStack[v] = true

		for _, w := range adjacency[v] {
			switch {
			case index[w] == unvisited:
				strongConnect(w)
				low[v] = min(low[v], low[w])
			case onStack[w]:
				low[v] = min(low[v], index[w])
			}
		}

		if low[v] != index[v] {
			return
		}
		var component []int
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[w] = false
			component = append(component, w)
			if w == v {
				break
			}
		}
		components = append(components, component)
	}

	for v := 0; v < n; v++ {
		if index[v] == unvisited {
			strongConnect(v)
		}
	}

	var cycles []cycle
	for _, component := range components {
		if len(component) == 1 && !hasSelfLoop(adjacency, component[0]) {
			continue
		}
		nodes := make([]int, len(component))
		copy(nodes, component)
		slices.Sort(nodes)
		ids := make([]TicketID, len(nodes))
		for i, v := range nodes {
			ids[i] = id(v)
		}
		cycles = append(cycles, cycle{first: nodes[0], ids: ids})
	}
	// Tarjan emits components in reverse topological order; order them by their
	// smallest canonical node index instead, which is stable and readable.
	slices.SortFunc(cycles, func(a, b cycle) int { return a.first - b.first })

	out := make([][]TicketID, 0, len(cycles))
	for _, c := range cycles {
		out = append(out, c.ids)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// cycle is one detected cycle plus the canonical index it sorts on.
type cycle struct {
	first int
	ids   []TicketID
}

func hasSelfLoop(adjacency [][]int, v int) bool {
	for _, w := range adjacency[v] {
		if w == v {
			return true
		}
	}
	return false
}
