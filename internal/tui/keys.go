package tui

import (
	"charm.land/bubbles/v2/key"
)

// KeyMap is the monitor's list-mode keyboard surface. Every binding carries its
// help text, so bubbles/help renders the footer from this one declaration and
// the help line cannot drift from what the keys actually do.
//
// While the find box is open the keyboard belongs to SearchKeyMap instead, and
// every key this map declares is ordinary text.
type KeyMap struct {
	// Up moves the selection to the previous Ticket.
	Up key.Binding
	// Down moves the selection to the next Ticket.
	Down key.Binding
	// PageUp moves the selection back one body-height page.
	PageUp key.Binding
	// PageDown moves the selection forward one body-height page.
	PageDown key.Binding
	// Home selects the first Ticket.
	Home key.Binding
	// End selects the last Ticket.
	End key.Binding
	// Refresh forces a refresh now.
	Refresh key.Binding
	// Open opens the selected Ticket's Detail. With nothing selectable — an
	// empty Watchlist, or a filter matching nothing — it does nothing.
	Open key.Binding
	// HideFinished toggles hiding Done and Cancelled Tickets.
	HideFinished key.Binding
	// Frontier opens the Frontier: the same Watchlist drawn as blocking-graph
	// nodes. It is offered only when there is a Watchlist to draw.
	Frontier key.Binding
	// Find opens the fuzzy find over Ticket keys and titles.
	Find key.Binding
	// ClearFilter drops both filter criteria. It is enabled only while a
	// filter is active, which is what keeps esc from claiming two meanings at
	// once: with nothing filtered the same key falls through to Quit.
	ClearFilter key.Binding
	// ToggleMouse enables or disables terminal mouse capture.
	ToggleMouse key.Binding
	// MouseSelect describes list click selection in expanded help only.
	MouseSelect key.Binding
	// MouseOpen describes list double-click opening in expanded help only.
	MouseOpen key.Binding
	// MouseWheel describes list wheel selection in expanded help only.
	MouseWheel key.Binding
	// Help toggles the full help listing.
	Help key.Binding
	// Legend toggles the session-local slack-space legend.
	Legend key.Binding
	// Quit ends the program.
	Quit key.Binding
}

// DefaultKeyMap returns the monitor's bindings.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "down"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("pgup"),
			key.WithHelp("pgup", "page up"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("pgdown"),
			key.WithHelp("pgdn", "page down"),
		),
		Home: key.NewBinding(
			key.WithKeys("home", "g"),
			key.WithHelp("g", "first"),
		),
		End: key.NewBinding(
			key.WithKeys("end", "G"),
			key.WithHelp("G", "last"),
		),
		Refresh: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "refresh"),
		),
		Open: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "open"),
		),
		HideFinished: key.NewBinding(
			key.WithKeys("d"),
			key.WithHelp("d", "hide finished"),
		),
		Frontier: key.NewBinding(
			key.WithKeys("v"),
			key.WithHelp("v", "frontier"),
			key.WithDisabled(),
		),
		Find: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "find"),
		),
		ClearFilter: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "clear filter"),
			key.WithDisabled(),
		),
		ToggleMouse: key.NewBinding(
			key.WithKeys("m"),
			key.WithHelp("m", mouseEnabledHelp),
		),
		MouseSelect: helpOnlyBinding("click", "select Ticket"),
		MouseOpen:   helpOnlyBinding("double-click", "open Ticket"),
		MouseWheel:  helpOnlyBinding("wheel", "move selection"),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		Legend: key.NewBinding(
			key.WithKeys("L"),
			key.WithHelp("L", "legend"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c", "esc"),
			key.WithHelp("q", "quit"),
		),
	}
}

// ShortHelp returns the stable priority order for the one-line list footer.
// The model keeps the mouse escape hatch, open, quit, and Help visible from 60
// columns upward while spending remaining room on this sequence. Every omitted
// binding remains available in FullHelp and through keyboard dispatch.
func (k KeyMap) ShortHelp() []key.Binding {
	// Frontier stays last so Help remains the on-screen route to the complete
	// binding list before the optional graph shortcut competes for room.
	return []key.Binding{k.ToggleMouse, k.Open, k.Quit, k.Up, k.Down, k.HideFinished, k.Find,
		k.ClearFilter, k.Help, k.Frontier}
}

// FullHelp returns every binding in two dense columns. The model stacks these
// groups into one column when they would not fit side by side, so expanded help
// remains complete rather than letting bubbles omit the trailing group.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.ToggleMouse, k.MouseSelect, k.MouseOpen, k.MouseWheel, k.Open, k.Frontier, k.Refresh, k.Help, k.Legend, k.Quit},
		{k.Up, k.Down, k.PageUp, k.PageDown, k.Home, k.End, k.HideFinished, k.Find, k.ClearFilter},
	}
}

func compactListFullHelp(k KeyMap) [][]key.Binding {
	mouse := k.ToggleMouse
	if mouse.Help().Desc == mouseEnabledHelp {
		mouse.SetHelp("m", mouseEnabledCompactHelp)
	}
	bindings := []key.Binding{
		mouse,
		k.MouseSelect,
		k.MouseOpen,
		k.MouseWheel,
		helpOnlyBinding("↑/↓/k/j", "move"),
		helpOnlyBinding("pgup/pgdn", "page"),
		helpOnlyBinding("g/G", "first/last"),
		helpOnlyBinding("enter/r", "open/refresh"),
		helpOnlyBinding("d /", "hide finished/find"),
		helpOnlyBinding("L/?/q", "legend/help/quit"),
	}
	if k.Frontier.Enabled() {
		bindings = append(bindings, k.Frontier)
	}
	if k.ClearFilter.Enabled() {
		bindings = append(bindings, k.ClearFilter)
	}
	return [][]key.Binding{bindings}
}

func helpOnlyBinding(keyText, description string) key.Binding {
	return key.NewBinding(key.WithKeys("__help_only"), key.WithHelp(keyText, description))
}

// DetailKeyMap is the keyboard while a Ticket's Detail is open. It reuses list
// bindings where key and meaning match and adds relationship focus/follow
// actions. Quit is separate from Back: q and ctrl+c always quit, while Esc pops
// the Trail, returns a root Detail to its armed list, or quits a decoded root
// with no list.
type DetailKeyMap struct {
	// Up scrolls the body up one line.
	Up key.Binding
	// Down scrolls the body down one line.
	Down key.Binding
	// PageUp scrolls the body back one page.
	PageUp key.Binding
	// PageDown scrolls the body forward one page.
	PageDown key.Binding
	// Home scrolls to the top of the Detail.
	Home key.Binding
	// End scrolls to the bottom of the Detail.
	End key.Binding
	// Refresh re-reads this Ticket's Detail, and only this Ticket's.
	Refresh key.Binding
	// NextLink moves focus to the next capability-visible relationship.
	NextLink key.Binding
	// PreviousLink moves focus to the previous capability-visible relationship.
	PreviousLink key.Binding
	// Follow opens the focused Link target in Detail.
	Follow key.Binding
	// Back pops one Trail entry first. At the root it returns to the armed list,
	// or quits when a directly decoded Ticket has no list behind it.
	Back key.Binding
	// Parent opens the root Watchlist context and is enabled only when one is
	// available.
	Parent key.Binding
	// ToggleMouse enables or disables terminal mouse capture.
	ToggleMouse key.Binding
	// MouseWheel describes body scrolling in expanded help only.
	MouseWheel key.Binding
	// MouseFollow describes clicking a visible Link in expanded help only.
	MouseFollow key.Binding
	// Help toggles the full help listing.
	Help key.Binding
	// Legend toggles the session-local slack-space legend.
	Legend key.Binding
	// Quit ends the program.
	Quit key.Binding
}

// DefaultDetailKeyMap returns the Detail screen's bindings.
func DefaultDetailKeyMap() DetailKeyMap {
	list := DefaultKeyMap()
	return DetailKeyMap{
		Up:       list.Up,
		Down:     list.Down,
		PageUp:   list.PageUp,
		PageDown: list.PageDown,
		Home:     list.Home,
		End:      list.End,
		Refresh:  list.Refresh,
		NextLink: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "next link"),
			key.WithDisabled(),
		),
		PreviousLink: key.NewBinding(
			key.WithKeys("shift+tab"),
			key.WithHelp("⇧tab", "previous link"),
			key.WithDisabled(),
		),
		Follow: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "follow"),
			key.WithDisabled(),
		),
		Back: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "back"),
		),
		Parent: key.NewBinding(
			key.WithKeys("u"),
			key.WithHelp("u", "watchlist"),
			key.WithDisabled(),
		),
		ToggleMouse: list.ToggleMouse,
		MouseWheel:  helpOnlyBinding("wheel", "scroll body"),
		MouseFollow: helpOnlyBinding("click Link", "follow"),
		Help:        list.Help,
		Legend:      list.Legend,
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
	}
}

// ShortHelp returns the bindings the Detail screen's one-line footer shows.
// Parent appears only when a root Watchlist context is available. This footer
// line is the complete walk-up affordance: no second help line and no box.
func (k DetailKeyMap) ShortHelp() []key.Binding {
	return detailHelpRolesFrom(k).defaultShortBindings()
}

// FullHelp keeps the mouse escape hatch and essential Detail actions together;
// the model stacks both groups when the terminal cannot show them side by side.
func (k DetailKeyMap) FullHelp() [][]key.Binding {
	return detailHelpRolesFrom(k).fullGroups()
}

// FrontierKeyMap is the keyboard while the Frontier is open. It reuses list
// bindings where key and meaning match. Arrows move graph focus along its axes;
// page keys move the vertical canvas window without moving focus. Neither d nor
// / appears: filters do not apply on this screen, and the footer says so rather
// than binding a key that would quietly lie.
type FrontierKeyMap struct {
	// Up moves focus to the previous node in this column.
	Up key.Binding
	// Down moves focus to the next node in this column.
	Down key.Binding
	// Left moves focus to the blocker side.
	Left key.Binding
	// Right moves focus to the dependent side.
	Right key.Binding
	// PageUp moves the canvas window backward by one current visible body-height page.
	PageUp key.Binding
	// PageDown moves the canvas window forward by one current visible body-height page.
	PageDown key.Binding
	// Home focuses the first node in canonical order.
	Home key.Binding
	// End focuses the last node in canonical order.
	End key.Binding
	// Open opens the focused node's Ticket, Ghost Tickets included.
	Open key.Binding
	// Toggle returns to the list, dropping every outstanding Detail read.
	Toggle key.Binding
	// Refresh re-issues the Detail reads that never succeeded.
	Refresh key.Binding
	// ToggleMouse enables or disables terminal mouse capture.
	ToggleMouse key.Binding
	// MouseSelect describes click focus in expanded help only.
	MouseSelect key.Binding
	// MouseOpen describes double-click opening in expanded help only.
	MouseOpen key.Binding
	// MouseWheel describes wheel scrolling in expanded help only.
	MouseWheel key.Binding
	// Help toggles the full help listing.
	Help key.Binding
	// Legend toggles the session-local slack-space legend.
	Legend key.Binding
	// Quit ends the program.
	Quit key.Binding
}

// DefaultFrontierKeyMap returns the Frontier's bindings.
func DefaultFrontierKeyMap() FrontierKeyMap {
	list := DefaultKeyMap()
	return FrontierKeyMap{
		// The list's keys, the graph's words: "up" and "down" beside "blocker
		// side" and "wheel scroll" read as scrolling the canvas, which is what
		// the wheel does and these do not.
		Up: key.NewBinding(
			key.WithKeys(list.Up.Keys()...),
			key.WithHelp("↑/k", "previous node"),
		),
		Down: key.NewBinding(
			key.WithKeys(list.Down.Keys()...),
			key.WithHelp("↓/j", "next node"),
		),
		Left: key.NewBinding(
			key.WithKeys("left", "h"),
			key.WithHelp("←/h", "blocker side"),
		),
		Right: key.NewBinding(
			key.WithKeys("right", "l"),
			key.WithHelp("→/l", "dependent side"),
		),
		PageUp:   list.PageUp,
		PageDown: list.PageDown,
		Home: key.NewBinding(
			key.WithKeys("home", "g"),
			key.WithHelp("g", "first node"),
		),
		End: key.NewBinding(
			key.WithKeys("end", "G"),
			key.WithHelp("G", "last node"),
		),
		Open: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "open Ticket"),
		),
		Toggle: key.NewBinding(
			key.WithKeys("v", "esc"),
			// Both keys are named: esc is the one a reader reaches for to leave
			// a screen, and advertising only v hides it on the footer and in the
			// ? panel alike.
			key.WithHelp("v/esc", "list"),
		),
		Refresh: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "re-read Details"),
		),
		ToggleMouse: list.ToggleMouse,
		MouseSelect: helpOnlyBinding("click", "select node"),
		MouseOpen:   helpOnlyBinding("double-click", "open Ticket"),
		MouseWheel:  helpOnlyBinding("wheel", "scroll"),
		Help:        list.Help,
		Legend:      list.Legend,
		// esc belongs to Toggle here, so Quit is q and ctrl+c only — the same
		// separation the Detail screen makes.
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
	}
}

// ShortHelp returns the bindings the Frontier's one-line footer shows, in the
// order they survive a narrow terminal: the one-line footer is cut from the
// right, so first is what a reader keeps.
//
// The escape hatches come first — leaving the screen, retrying the reads, the
// help panel, quitting. r is the only remedy for the failure banner drawn
// directly above this line, and "? help" is not discoverable by anything.
// Opening a Ticket and the mouse toggle follow, then movement, which is
// discoverable by trying the arrow keys. This is the same trade
// KeyMap.ShortHelp names, made the same way, so the two footers are visibly one
// decision.
func (k FrontierKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Toggle, k.Refresh, k.Help, k.Quit, k.Open, k.ToggleMouse,
		k.Up, k.Down, k.Left, k.Right}
}

// FullHelp returns every binding in two dense columns; the model stacks them
// when the terminal cannot show them side by side.
func (k FrontierKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.ToggleMouse, k.MouseSelect, k.MouseOpen, k.MouseWheel, k.Open, k.Toggle, k.Refresh, k.Help, k.Legend, k.Quit},
		{k.Up, k.Down, k.Left, k.Right, k.PageUp, k.PageDown, k.Home, k.End},
	}
}

// SearchKeyMap is the keyboard while the find box is open. It is deliberately
// tiny: everything it does not name is text, so searching for "queue" types a
// q rather than quitting the program.
type SearchKeyMap struct {
	// Apply commits the query and closes the box, leaving the list narrowed.
	Apply key.Binding
	// Cancel abandons the query and closes the box.
	Cancel key.Binding
	// Move steps the list selection without leaving the box.
	Move key.Binding
	// MouseHint explains how to select terminal text while capture is enabled. It
	// has no command path and is hidden when capture is disabled.
	MouseHint key.Binding
	// Quit ends the program from inside the box. Only ctrl+c: a plain q is
	// text here.
	Quit key.Binding
}

// DefaultSearchKeyMap returns the find box's bindings.
func DefaultSearchKeyMap() SearchKeyMap {
	return SearchKeyMap{
		Apply: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "apply"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel"),
		),
		Move: key.NewBinding(
			key.WithKeys("up", "down", "pgup", "pgdown"),
			key.WithHelp("↑/↓", "move"),
		),
		MouseHint: key.NewBinding(
			key.WithKeys("shift+drag"),
			key.WithHelp("shift-drag", searchMouseHintHelp),
		),
		Quit: key.NewBinding(
			key.WithKeys("ctrl+c"),
			key.WithHelp("ctrl+c", "quit"),
		),
	}
}

// ShortHelp returns the bindings the find box's footer shows.
func (k SearchKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.MouseHint, k.Cancel, k.Apply, k.Move, k.Quit}
}

// FullHelp returns the same bindings: the find box has no second tier.
func (k SearchKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}
