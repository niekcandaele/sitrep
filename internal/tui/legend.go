package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/niekcandaele/sitrep/internal/model"
)

// legendFact is the one vocabulary source shared by List, Detail, and Frontier.
// Rendering contexts select facts; they do not restate their names or meaning.
type legendFact int

const (
	legendActionable legendFact = iota
	legendBlocked
	legendCycle
	legendOutsideWatchlist
	legendPending
	legendLinksFailed
	legendUnknownBlocker
	legendAnonymousBlockers
	legendStatusChannel
)

type legendDefinition struct {
	name        string
	description string
}

var legendCatalog = map[legendFact]legendDefinition{
	legendActionable:        {"●", "actionable — all known blockers are satisfied"},
	legendBlocked:           {"blocked by N", "light border — known named unfinished canonical blockers"},
	legendCycle:             {"CYCLE", "double border — cyclic blocking evidence; it does not change Actionability"},
	legendOutsideWatchlist:  {"NOT IN WATCHLIST", "dashed border — linked Ticket outside this Watchlist"},
	legendPending:           {"PENDING", "waiting for complete member Links evidence"},
	legendLinksFailed:       {"LINKS FAILED", "retryable failure of this Ticket's Links read"},
	legendUnknownBlocker:    {"BLOCKER UNKNOWN", "blocker state unresolved"},
	legendAnonymousBlockers: {"+N unnamed blocker(s)", "identity-less blockers are disclosed separately"},
	legendStatusChannel:     {"status border", "colour reflects Tracker status, not blockedness"},
}

func legendLines(facts []legendFact, cycles [][]model.TicketID, keys map[model.TicketID]string, width int) []string {
	definitions := make([]legendDefinition, 0, len(facts))
	for _, fact := range facts {
		definitions = append(definitions, legendCatalog[fact])
	}
	return legendDefinitionLines(definitions, cycles, keys, width)
}

func legendDefinitionLines(definitions []legendDefinition, cycles [][]model.TicketID,
	keys map[model.TicketID]string, width int) []string {
	if width <= 0 || len(definitions) == 0 {
		return nil
	}
	lines := []string{"Legend · L hide"}
	for _, definition := range definitions {
		lines = append(lines, definition.name+" — "+definition.description)
	}
	for i, cycle := range cycles {
		members := make([]string, 0, len(cycle))
		for _, id := range cycle {
			key := keys[id]
			if key == "" {
				return nil
			}
			members = append(members, key)
		}
		lines = append(lines, fmt.Sprintf("cycle %d: %s", i+1, strings.Join(members, ", ")))
	}
	for _, line := range lines {
		if lipgloss.Width(line) > width {
			return nil
		}
	}
	return lines
}

func frontierLegendNodes(f frontierState) []frontierNode {
	return frontierNodes(f.graph, f.input.Tickets, f.isResolved())
}

func frontierLegendKeys(f frontierState) map[model.TicketID]string {
	return frontierLegendKeysForNodes(frontierLegendNodes(f))
}

func frontierLegendKeysForNodes(nodes []frontierNode) map[model.TicketID]string {
	keys := make(map[model.TicketID]string, len(nodes))
	for _, node := range nodes {
		if _, exists := keys[node.id]; !exists || node.member {
			keys[node.id] = node.key
		}
	}
	return keys
}

func frontierLegendKeysForLayout(layout frontierLayout) map[model.TicketID]string {
	keys := make(map[model.TicketID]string, len(layout.order))
	for _, id := range layout.order {
		if node, ok := layout.nodes[id]; ok {
			if _, exists := keys[node.id]; !exists || node.member {
				keys[node.id] = node.key
			}
		}
	}
	return keys
}

func frontierLegendFacts(f frontierState, id model.TicketID) []legendFact {
	a, member := f.graph.For(id)
	if !f.isResolved() {
		if member {
			return []legendFact{legendPending}
		}
		return []legendFact{legendOutsideWatchlist}
	}
	if !member {
		return []legendFact{legendOutsideWatchlist, legendStatusChannel}
	}
	facts := make([]legendFact, 0, 6)
	if a.Actionable {
		facts = append(facts, legendActionable)
	}
	if knownUnmetBlockers(a) > 0 {
		facts = append(facts, legendBlocked)
	}
	if a.InCycle {
		facts = append(facts, legendCycle)
	}
	if !a.LinksKnown {
		facts = append(facts, legendLinksFailed)
	}
	if hasUnknownBlocker(a) {
		facts = append(facts, legendUnknownBlocker)
	}
	if anonymousBlockers(a) > 0 {
		facts = append(facts, legendAnonymousBlockers)
	}
	return append(facts, legendStatusChannel)
}

func frontierLegendFactsForNode(f frontierState, node frontierNode) []legendFact {
	if node.member && node.id == "" {
		if !f.isResolved() {
			return []legendFact{legendPending}
		}
		switch node.emphasis.badge {
		case "CYCLE":
			return []legendFact{legendCycle, legendStatusChannel}
		case "ACTIONABLE":
			return []legendFact{legendActionable, legendStatusChannel}
		default:
			return []legendFact{legendStatusChannel}
		}
	}
	return frontierLegendFacts(f, node.id)
}

func frontierLegendFactsForID(f frontierState, id model.TicketID, member bool) []legendFact {
	if !member {
		return []legendFact{legendOutsideWatchlist}
	}
	if id != "" {
		return frontierLegendFacts(f, id)
	}
	for _, node := range frontierLegendNodes(f) {
		if node.id == id && node.member {
			return frontierLegendFactsForNode(f, node)
		}
	}
	return frontierLegendFacts(f, id)
}

func allFrontierLegendFacts(f frontierState) []legendFact {
	if !f.isResolved() {
		seenMember := false
		seenOutsideWatchlist := false
		for _, node := range frontierLegendNodes(f) {
			if node.member {
				seenMember = true
			} else {
				seenOutsideWatchlist = true
			}
		}
		facts := make([]legendFact, 0, 2)
		if seenMember {
			facts = append(facts, legendPending)
		}
		if seenOutsideWatchlist {
			facts = append(facts, legendOutsideWatchlist)
		}
		return facts
	}
	seen := make(map[legendFact]bool)
	facts := make([]legendFact, 0, len(legendCatalog))
	for _, node := range frontierLegendNodes(f) {
		for _, fact := range frontierLegendFactsForNode(f, node) {
			seen[fact] = true
		}
	}
	for fact := legendActionable; fact <= legendStatusChannel; fact++ {
		if seen[fact] {
			facts = append(facts, fact)
		}
	}
	return facts
}

func frontierDenseLegendDefinition(state frontierDenseState) legendDefinition {
	name := string(state.glyph())
	switch state {
	case frontierDensePending:
		return legendDefinition{name, "pending — waiting for complete Links evidence"}
	case frontierDenseCycle:
		return legendDefinition{name, "cycle — cyclic blocking evidence"}
	case frontierDenseLinksFailed:
		return legendDefinition{name, "Links failed — retryable Links read failure"}
	case frontierDenseUnknown:
		return legendDefinition{name, "unknown — unresolved or anonymous blocker evidence"}
	case frontierDenseNonActionable:
		return legendDefinition{name, "non-actionable — not Todo or has known unfinished blockers"}
	case frontierDenseActionable:
		return legendDefinition{name, "actionable — Todo with all known blockers satisfied"}
	default:
		return legendDefinition{name, "outside Watchlist — linked Ticket outside this Watchlist"}
	}
}

func frontierDenseLegendLinesForLayout(layout frontierLayout, cycles [][]model.TicketID,
	keys map[model.TicketID]string, width int) []string {
	if len(layout.order) == 0 {
		return nil
	}
	seen := [frontierDenseStateCount]bool{}
	for _, id := range layout.order {
		if node, ok := layout.nodes[id]; ok {
			seen[frontierDenseStateFor(node)] = true
		}
	}
	return frontierDenseLegendLinesForStates(seen, cycles, keys, width)
}

func frontierDenseLegendLinesForStates(seen [frontierDenseStateCount]bool,
	cycles [][]model.TicketID, keys map[model.TicketID]string, width int) []string {
	order := [...]frontierDenseState{
		frontierDensePending,
		frontierDenseCycle,
		frontierDenseLinksFailed,
		frontierDenseUnknown,
		frontierDenseNonActionable,
		frontierDenseActionable,
		frontierDenseOutsideWatchlist,
	}
	definitions := make([]legendDefinition, 0, len(order)+1)
	for _, state := range order {
		if seen[state] {
			definitions = append(definitions, frontierDenseLegendDefinition(state))
		}
	}
	definitions = append(definitions, legendDefinition{
		name: "Key colour", description: "Tracker Status Category; glyph shape is Frontier state",
	})
	return legendDefinitionLines(definitions, cycles, keys, width)
}

func frontierCyclesForLegend(f frontierState) [][]model.TicketID {
	if !f.isResolved() {
		return nil
	}
	return f.graph.Cycles()
}

func listLegendLines(markers listMarkers, width int) []string {
	if !markers.active {
		return nil
	}
	return legendLines([]legendFact{legendActionable}, nil, nil, width)
}

func detailLegendCycles(cycles [][]model.TicketID, id model.TicketID, member bool) [][]model.TicketID {
	if !member {
		return nil
	}
	matching := make([][]model.TicketID, 0)
	for _, cycle := range cycles {
		for _, cycleMember := range cycle {
			if cycleMember == id {
				matching = append(matching, cycle)
				break
			}
		}
	}
	return matching
}

func (m Model) detailLegendLines(doc detailDocument) []string {
	if !m.legendVisible || m.detailReturn != modeFrontier || m.detail.offset != 0 || !m.frontier.isResolved() || len(doc.Lines) >= m.detailBodyHeight() {
		return nil
	}
	id := m.detail.ticket.ID
	facts := frontierLegendFactsForID(m.frontier, id, m.detail.provenance.fromFrontier())
	cycles := detailLegendCycles(frontierCyclesForLegend(m.frontier), id, m.detail.provenance.fromFrontier())
	if len(cycles) > 0 {
		hasCycleFact := false
		for _, fact := range facts {
			if fact == legendCycle {
				hasCycleFact = true
				break
			}
		}
		if !hasCycleFact {
			facts = append([]legendFact{legendCycle}, facts...)
		}
	}
	return legendLines(facts, cycles, frontierLegendKeys(m.frontier), m.width)
}

func replaceTrailingBodyLines(body string, used int, legend []string, style func(...string) string) string {
	if len(legend) == 0 {
		return body
	}
	lines := strings.Split(body, "\n")
	if used+len(legend) > len(lines) {
		return body
	}
	for i, line := range legend {
		lines[used+i] = style(line)
	}
	return strings.Join(lines, "\n")
}

func (m Model) frontierCycleHeaderCountsWithCycles(nodes, ghosts, actionable, known int, cycles [][]model.TicketID) string {
	tally := fmt.Sprintf("%d actionable", actionable)
	if known == 0 {
		tally = "actionable unknown"
	}
	base := "frontier" + separator + plural(nodes, "node", "nodes")
	if len(cycles) == 0 {
		if ghosts > 0 {
			base += separator + plural(ghosts, "ghost", "ghosts")
		}
		return base + separator + tally
	}

	cyclesLabel := plural(len(cycles), "blocking cycle", "blocking cycles")
	fullCycle := cyclesLabel + " (cyclic evidence)"
	withGhosts := []string{base}
	if ghosts > 0 {
		withGhosts = append(withGhosts, plural(ghosts, "ghost", "ghosts"))
	}
	withGhosts = append(withGhosts, tally, fullCycle)
	withoutOptional := []string{base, fullCycle}
	withoutParenthetical := []string{base, cyclesLabel}
	minimum := []string{"frontier", cyclesLabel}
	candidates := [][]string{withGhosts, withoutOptional, withoutParenthetical, minimum}
	budget := rateLimitHeader(m.frontier.input.Capabilities, m.frontier.input.RateLimitBudget)
	for index, segments := range candidates {
		counts := strings.Join(segments, separator)
		withBudget := budget.appendToLine(counts, m.staleness(), m.width)
		if lipgloss.Width(withBudget)+lipgloss.Width(m.staleness())+1 <= m.width &&
			(!budget.valid || strings.Contains(withBudget, "budget ") || index == len(candidates)-1) {
			return counts
		}
	}
	return "frontier" + separator + "…"
}

func (m Model) frontierLegendBody(lines []string, height int, cycles [][]model.TicketID) []string {
	if !m.legendVisible || !m.frontierShowsCanvas() || m.frontier.offsetX != 0 || m.frontier.offsetY != 0 {
		return lines
	}
	inner := frontierInnerRect(m.width, height)
	layout := m.frontier.layout
	if layout.width > inner.W || layout.height > inner.H {
		return lines
	}
	facts := allFrontierLegendFacts(m.frontier)
	legendCycles := cycles
	if !m.frontier.isResolved() {
		legendCycles = nil
	}
	if layout.direction == frontierRanksVertical {
		legend := legendLines(facts, legendCycles, frontierLegendKeys(m.frontier), inner.W-layout.width-1)
		startX, startY := inner.X+layout.width+1, inner.Y
		if len(legend) == 0 || startY+len(legend) > inner.Y+inner.H {
			return lines
		}
		for i, line := range legend {
			lines[startY+i] = ansi.Truncate(lines[startY+i], startX, "") + m.styles.Muted.Render(line)
		}
		return lines
	}
	legend := legendLines(facts, legendCycles, frontierLegendKeys(m.frontier), inner.W)
	startY := inner.Y + layout.height + 1
	if len(legend) == 0 || startY+len(legend) > inner.Y+inner.H {
		return lines
	}
	for i, line := range legend {
		lines[startY+i] = strings.Repeat(" ", inner.X) + m.styles.Muted.Render(line)
	}
	return lines
}
