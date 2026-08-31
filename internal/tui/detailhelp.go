package tui

import (
	"strings"

	help "charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
)

// detailHelpRoles gives the responsive Detail footer a named view of every
// binding it lays out. DetailKeyMap remains the keyboard authority; this is the
// single adapter from commands to presentation roles.
type detailHelpRoles struct {
	mouse        key.Binding
	mouseWheel   key.Binding
	mouseFollow  key.Binding
	back         key.Binding
	quit         key.Binding
	follow       key.Binding
	links        key.Binding
	nextLink     key.Binding
	previousLink key.Binding
	parent       key.Binding
	refresh      key.Binding
	help         key.Binding
	legend       key.Binding
	up           key.Binding
	down         key.Binding
	pageUp       key.Binding
	pageDown     key.Binding
	home         key.Binding
	end          key.Binding
}

func detailHelpRolesFrom(keys DetailKeyMap) detailHelpRoles {
	links := keys.NextLink
	links.SetHelp("tab/⇧tab", "links")
	return detailHelpRoles{
		mouse:        keys.ToggleMouse,
		mouseWheel:   keys.MouseWheel,
		mouseFollow:  keys.MouseFollow,
		back:         keys.Back,
		quit:         keys.Quit,
		follow:       keys.Follow,
		links:        links,
		nextLink:     keys.NextLink,
		previousLink: keys.PreviousLink,
		parent:       keys.Parent,
		refresh:      keys.Refresh,
		help:         keys.Help,
		legend:       keys.Legend,
		up:           keys.Up,
		down:         keys.Down,
		pageUp:       keys.PageUp,
		pageDown:     keys.PageDown,
		home:         keys.Home,
		end:          keys.End,
	}
}

func (r detailHelpRoles) defaultShortBindings() []key.Binding {
	return []key.Binding{
		r.mouse, r.back, r.quit, r.follow, r.links,
		r.up, r.down, r.parent, r.refresh, r.help,
	}
}

func (r detailHelpRoles) priorityShortBindings() []key.Binding {
	parent := r.parent
	if !parent.Enabled() {
		parent = key.Binding{}
	}
	return []key.Binding{
		r.mouse, r.back, r.quit, r.follow, r.links, parent, r.help,
	}
}

func (r detailHelpRoles) supplementalShortBindings() []key.Binding {
	return []key.Binding{r.up, r.down, r.refresh}
}

func (r detailHelpRoles) actionBindings() []key.Binding {
	return []key.Binding{
		r.mouse, r.mouseWheel, r.mouseFollow, r.back, r.follow, r.nextLink, r.previousLink,
		r.parent, r.refresh, r.help, r.legend, r.quit,
	}
}

func (r detailHelpRoles) actionsWithoutMouse() []key.Binding {
	return []key.Binding{
		r.back, r.follow, r.nextLink, r.previousLink,
		r.parent, r.refresh, r.help, r.legend, r.quit,
	}
}

func (r detailHelpRoles) scrollBindings() []key.Binding {
	return []key.Binding{r.up, r.down, r.pageUp, r.pageDown, r.home, r.end}
}

func (r detailHelpRoles) fullGroups() [][]key.Binding {
	return [][]key.Binding{r.actionBindings(), r.scrollBindings()}
}

func (r detailHelpRoles) stackedFullBindings() []key.Binding {
	stacked := r.actionBindings()
	return append(stacked, r.scrollBindings()...)
}

func compactDetailFullHelp(r detailHelpRoles) [][]key.Binding {
	mouse := r.mouse
	if mouse.Help().Desc == mouseEnabledHelp {
		mouse.SetHelp("m", mouseEnabledCompactHelp)
	}
	bindings := []key.Binding{mouse, r.mouseWheel, r.mouseFollow}
	if r.parent.Enabled() {
		bindings = append(bindings, helpOnlyBinding("esc/u", "back/watchlist"))
	} else {
		bindings = append(bindings, r.back)
	}
	if r.links.Enabled() {
		bindings = append(bindings, helpOnlyBinding("tab/⇧tab", "links"))
	}
	if r.follow.Enabled() {
		bindings = append(bindings, r.follow)
	}
	bindings = append(bindings,
		helpOnlyBinding("r/L/?/q", "refresh/legend/help/quit"),
		helpOnlyBinding("↑/↓/k/j", "scroll"),
		helpOnlyBinding("pgup/pgdn", "page"),
		helpOnlyBinding("g/G", "first/last"),
	)
	return [][]key.Binding{bindings}
}

// detailHelpDiscoveryWidth is the narrowest supported Detail layout. From this
// boundary onward the short footer always names how to expand the remaining keys.
const detailHelpDiscoveryWidth = 42

// detailHelpKeys applies Detail's responsive layout without making list body
// measurements depend on the screen currently in front.
func (m Model) detailHelpKeys() help.KeyMap {
	return responsiveDetailHelpKeys(m.help, m.detailKeys, m.width)
}

func responsiveDetailHelpKeys(renderer help.Model, keys DetailKeyMap, width int) responsiveHelpKeyMap {
	roles := detailHelpRolesFrom(keys)
	unbounded := renderer
	unbounded.SetWidth(0)
	short := compactDetailShortHelp(unbounded, roles, width)
	full := roles.fullGroups()
	if width > 0 && lipgloss.Width(unbounded.FullHelpView(full)) > width {
		full = [][]key.Binding{roles.stackedFullBindings()}
	}
	return responsiveHelpKeyMap{short: short, full: full}
}

func compactDetailShortHelp(renderer help.Model, roles detailHelpRoles, width int) []key.Binding {
	if width <= 0 {
		return roles.defaultShortBindings()
	}
	priority := roles.priorityShortBindings()
	budget := max(width, 1)
	fits := func(bindings []key.Binding) bool {
		return lipgloss.Width(renderer.ShortHelpView(bindings)) <= budget
	}
	if fits(priority) {
		return fittingDetailShortHelp(
			renderer,
			priority,
			roles.supplementalShortBindings(),
			budget,
		)
	}
	if descriptive, ok := descriptiveDetailShortBinding(roles, budget); ok {
		return []key.Binding{descriptive}
	}
	if !roles.links.Enabled() {
		return []key.Binding{compactDetailShortBinding(roles, width)}
	}

	roles.links.SetHelp("tab/⇧", "")
	priority = roles.priorityShortBindings()
	if fits(priority) {
		return priority
	}
	roles.links.SetHelp("tab/⇧tab", "links")

	if roles.mouse.Help().Desc == mouseEnabledHelp {
		roles.mouse.SetHelp("m", mouseEnabledTerseHelp)
	}
	priority = roles.priorityShortBindings()
	if fits(priority) {
		return priority
	}

	roles.quit.SetHelp("q", "")
	roles.follow.SetHelp("↵", "follow")
	priority = roles.priorityShortBindings()
	if fits(priority) {
		return priority
	}

	return []key.Binding{compactDetailShortBinding(roles, width)}
}

func fittingDetailShortHelp(
	renderer help.Model,
	priority []key.Binding,
	supplemental []key.Binding,
	budget int,
) []key.Binding {
	bindings := append([]key.Binding(nil), priority...)
	for _, binding := range supplemental {
		candidate := append(append([]key.Binding(nil), bindings...), binding)
		if lipgloss.Width(renderer.ShortHelpView(candidate)) > budget {
			break
		}
		bindings = candidate
	}
	return bindings
}

func descriptiveDetailShortBinding(roles detailHelpRoles, budget int) (key.Binding, bool) {
	mouse := "m " + mouseDisabledHelp
	if roles.mouse.Help().Desc != mouseDisabledHelp {
		mouse = "m " + mouseEnabledHelp
	}
	tokens := detailCompactHelpTokens{
		mouse: mouse,
		back:  "esc " + roles.back.Help().Desc,
		quit:  "q quit",
	}
	if roles.follow.Enabled() {
		tokens.follow = "enter follow"
	}
	if roles.links.Enabled() {
		tokens.links = "tab/⇧tab links"
	}
	if budget >= detailHelpDiscoveryWidth {
		tokens.help = "? help"
	}
	if roles.parent.Enabled() {
		tokens.parent = "u watchlist"
	}
	if binding, ok := tokens.bindingIfFits(budget); ok {
		return binding, true
	}
	if tokens.parent != "" {
		tokens.parent = "u↑"
		if binding, ok := tokens.bindingIfFits(budget); ok {
			return binding, true
		}
	}
	if tokens.links != "" {
		tokens.links = "tab/⇧"
		if binding, ok := tokens.bindingIfFits(budget); ok {
			return binding, true
		}
	}
	return key.Binding{}, false
}

type detailCompactHelpTokens struct {
	mouse  string
	back   string
	quit   string
	follow string
	links  string
	help   string
	parent string
}

func (t detailCompactHelpTokens) parts() []string {
	parts := make([]string, 0, 7)
	for _, part := range []string{t.mouse, t.back, t.quit, t.follow, t.links, t.help, t.parent} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func joinDetailHelpParts(parts []string, budget int) string {
	spaced := strings.Join(parts, " · ")
	if lipgloss.Width(spaced) <= budget {
		return spaced
	}
	return strings.Join(parts, "·")
}

func (t detailCompactHelpTokens) bindingIfFits(budget int) (key.Binding, bool) {
	for _, separator := range []string{" · ", "·"} {
		text := strings.Join(t.parts(), separator)
		if lipgloss.Width(text) <= budget {
			return syntheticDetailHelpBinding(text), true
		}
	}
	return key.Binding{}, false
}

func (t detailCompactHelpTokens) fitWhole(budget int) string {
	parts := t.parts()
	text := joinDetailHelpParts(parts, budget)
	for len(parts) > 1 && lipgloss.Width(text) > budget {
		parts = parts[:len(parts)-1]
		text = joinDetailHelpParts(parts, budget)
	}
	if lipgloss.Width(text) > budget {
		return joinDetailHelpParts([]string{"m"}, budget)
	}
	return text
}

func syntheticDetailHelpBinding(text string) key.Binding {
	return key.NewBinding(key.WithKeys("__detail_help"), key.WithHelp(text, ""))
}

func compactDetailShortBinding(roles detailHelpRoles, width int) key.Binding {
	budget := max(width, 1)
	showHelp := budget >= detailHelpDiscoveryWidth
	captureEnabled := roles.mouse.Help().Desc != mouseDisabledHelp
	parentEnabled := roles.parent.Enabled()

	if roles.links.Enabled() && !roles.follow.Enabled() {
		mouse := "m " + mouseDisabledHelp
		if captureEnabled {
			mouse = "m " + mouseEnabledCompactHelp
		}
		tokens := detailCompactHelpTokens{
			mouse: mouse,
			back:  "esc " + roles.back.Help().Desc,
			quit:  "q quit",
			links: "tab/⇧",
		}
		if showHelp {
			tokens.help = "? help"
		}
		if parentEnabled {
			tokens.parent = "u watchlist"
		}
		if lipgloss.Width(joinDetailHelpParts(tokens.parts(), budget)) > budget && parentEnabled {
			tokens.parent = "u↑"
		}
		if lipgloss.Width(joinDetailHelpParts(tokens.parts(), budget)) > budget {
			tokens.back = "esc"
		}
		if lipgloss.Width(joinDetailHelpParts(tokens.parts(), budget)) > budget && captureEnabled {
			tokens.mouse = "m/⇧drag"
		}
		if lipgloss.Width(joinDetailHelpParts(tokens.parts(), budget)) > budget && captureEnabled {
			tokens.mouse = "m/⇧"
		}
		if lipgloss.Width(joinDetailHelpParts(tokens.parts(), budget)) > budget {
			tokens.quit = "q"
		}
		if lipgloss.Width(joinDetailHelpParts(tokens.parts(), budget)) > budget && !captureEnabled {
			tokens.mouse = "m"
		}
		return syntheticDetailHelpBinding(tokens.fitWhole(budget))
	}

	if !roles.links.Enabled() {
		mouse := "m " + mouseDisabledHelp
		if captureEnabled {
			mouse = "m " + mouseEnabledCompactHelp
		}
		tokens := detailCompactHelpTokens{
			mouse: mouse,
			back:  "esc " + roles.back.Help().Desc,
			quit:  "q quit",
		}
		if showHelp {
			tokens.help = "? help"
		}
		if parentEnabled {
			tokens.parent = "u watchlist"
		}
		if lipgloss.Width(joinDetailHelpParts(tokens.parts(), budget)) > budget && parentEnabled {
			tokens.parent = "u↑"
		}
		if lipgloss.Width(joinDetailHelpParts(tokens.parts(), budget)) > budget && captureEnabled {
			tokens.mouse = "m/⇧drag"
		}
		if lipgloss.Width(joinDetailHelpParts(tokens.parts(), budget)) > budget {
			tokens.quit = "q"
		}
		if lipgloss.Width(joinDetailHelpParts(tokens.parts(), budget)) > budget {
			tokens.back = "esc"
		}
		if lipgloss.Width(joinDetailHelpParts(tokens.parts(), budget)) > budget {
			tokens.mouse = "m"
		}
		return syntheticDetailHelpBinding(tokens.fitWhole(budget))
	}

	mouse := "m " + mouseDisabledHelp
	if captureEnabled {
		mouse = "m/⇧drag"
	}
	tokens := detailCompactHelpTokens{
		mouse: mouse,
		back:  "esc " + roles.back.Help().Desc,
		quit:  "q",
		links: "tab/⇧ links",
	}
	if roles.follow.Enabled() {
		tokens.follow = "follow↵"
	}
	if showHelp {
		tokens.help = "? help"
	}
	if parentEnabled {
		tokens.parent = "u watchlist"
	}
	if lipgloss.Width(joinDetailHelpParts(tokens.parts(), budget)) > budget && parentEnabled {
		tokens.parent = "u↑"
	}
	if lipgloss.Width(joinDetailHelpParts(tokens.parts(), budget)) > budget && captureEnabled {
		tokens.mouse = "m/⇧"
	}
	if lipgloss.Width(joinDetailHelpParts(tokens.parts(), budget)) > budget {
		tokens.links = "tab/⇧"
	}
	if lipgloss.Width(joinDetailHelpParts(tokens.parts(), budget)) > budget {
		tokens.follow = "↵"
	}
	if lipgloss.Width(joinDetailHelpParts(tokens.parts(), budget)) > budget {
		tokens.back = "esc"
	}
	if lipgloss.Width(joinDetailHelpParts(tokens.parts(), budget)) > budget {
		tokens.mouse = "m"
	}
	return syntheticDetailHelpBinding(tokens.fitWhole(budget))
}

// detailHelpView keeps the complete text-selection recovery wording reachable in
// narrow expanded help without forcing every action into one vertical column.
// Footer rendering and body measurement both consume this exact output.
func (m Model) detailHelpView() string {
	keys := m.detailHelpKeys()
	if !m.help.ShowAll {
		short := keys.ShortHelp()
		if len(short) == 1 {
			unbounded := m.help
			unbounded.SetWidth(0)
			return truncateLine(unbounded.ShortHelpView(short), m.width)
		}
		return m.help.View(keys)
	}
	if m.width > 0 && m.width <= 42 && m.height > 0 && m.height <= 16 {
		compact := m.help
		compact.SetWidth(m.width)
		return compact.FullHelpView(compactDetailFullHelp(detailHelpRolesFrom(m.detailKeys)))
	}
	if m.width <= 0 {
		return m.help.View(keys)
	}

	roles := detailHelpRolesFrom(m.detailKeys)
	unbounded := m.help
	unbounded.SetWidth(0)
	if lipgloss.Width(unbounded.FullHelpView(roles.fullGroups())) <= m.width {
		return m.help.View(keys)
	}

	// The mouse item gets its own row, so it is the compact spelling that fits
	// rather than the full one cut mid-word: "shift-drag" is the recovery this
	// row exists to advertise, and half of it advertises nothing.
	mouseItem := roles.mouse
	if lipgloss.Width(unbounded.ShortHelpView([]key.Binding{mouseItem})) > m.width {
		if compact, ok := compactMouseHelp(mouseItem); ok {
			mouseItem.SetHelp("m", compact)
		}
	}
	mouseLines := truncateLine(unbounded.ShortHelpView([]key.Binding{mouseItem}), m.width)
	if roles.mouse.Help().Desc == mouseEnabledHelp {
		// Separate the capture-recovery affordance from the action grid. The extra
		// row also keeps enabled and disabled mouse-help geometry distinct, so a
		// capture toggle follows the normal body-height re-clamp path.
		mouseLines += "\n"
	}
	actions := []key.Binding{roles.mouseWheel, roles.mouseFollow}
	actions = append(actions, roles.actionsWithoutMouse()...)
	remaining := [][]key.Binding{actions, roles.scrollBindings()}
	if lipgloss.Width(unbounded.FullHelpView(remaining)) > m.width {
		stacked := actions
		stacked = append(stacked, roles.scrollBindings()...)
		remaining = [][]key.Binding{stacked}
	}
	columns := m.help
	columns.SetWidth(m.width)
	return mouseLines + "\n" + columns.FullHelpView(remaining)
}
