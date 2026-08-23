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
	// Find opens the fuzzy find over Ticket keys and titles.
	Find key.Binding
	// ClearFilter drops both filter criteria. It is enabled only while a
	// filter is active, which is what keeps esc from claiming two meanings at
	// once: with nothing filtered the same key falls through to Quit.
	ClearFilter key.Binding
	// ToggleMouse enables or disables terminal mouse capture.
	ToggleMouse key.Binding
	// Help toggles the full help listing.
	Help key.Binding
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
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c", "esc"),
			key.WithHelp("q", "quit"),
		),
	}
}

// ShortHelp returns the bindings the one-line footer shows. The mouse escape
// hatch comes first so terminals too narrow for the normal primary-action trio
// still explain how to recover text selection. Open and quit follow, keeping all
// three visible at 80 columns. Every binding remains available in FullHelp.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.ToggleMouse, k.Open, k.Quit, k.Up, k.Down, k.HideFinished, k.Find, k.ClearFilter, k.Help}
}

// FullHelp returns every binding in two dense columns. The model stacks these
// groups into one column when they would not fit side by side, so expanded help
// remains complete rather than letting bubbles omit the trailing group.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.ToggleMouse, k.Open, k.Refresh, k.Help, k.Quit},
		{k.Up, k.Down, k.PageUp, k.PageDown, k.Home, k.End, k.HideFinished, k.Find, k.ClearFilter},
	}
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
	// Help toggles the full help listing.
	Help key.Binding
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
		Help:        list.Help,
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
