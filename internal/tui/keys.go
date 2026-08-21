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
	// empty collection, or a filter matching nothing — it does nothing.
	Open key.Binding
	// HideFinished toggles hiding Done and Cancelled Tickets.
	HideFinished key.Binding
	// Find opens the fuzzy find over Ticket keys and titles.
	Find key.Binding
	// ClearFilter drops both filter criteria. It is enabled only while a
	// filter is active, which is what keeps esc from claiming two meanings at
	// once: with nothing filtered the same key falls through to Quit.
	ClearFilter key.Binding
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

// ShortHelp returns the bindings the one-line footer shows. ClearFilter appears
// only when it is enabled, so the footer never offers to clear a filter that is
// not there — and Quit is advertised as "q", never as "esc", because esc means
// whichever rung of the ladder the session is on.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.HideFinished, k.Find, k.ClearFilter, k.Refresh, k.Help, k.Quit}
}

// FullHelp returns every binding, grouped into the columns "?" expands to.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown},
		{k.Home, k.End, k.Open},
		{k.HideFinished, k.Find, k.ClearFilter},
		{k.Refresh, k.Help, k.Quit},
	}
}

// DetailKeyMap is the keyboard while a Ticket's Detail is open. It reuses the
// list's bindings wherever the key and the meaning are the same, so the two
// screens cannot come to disagree about what pgdn does, and adds exactly one of
// its own: Back.
//
// Quit is deliberately not the list's: there esc is the last rung of the filter
// ladder, and here esc means "one level up" — back to the list. q and ctrl+c
// still quit outright from both, so a Detail can never trap anyone.
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
	// Back returns to the list, leaving it exactly as it was. With no list
	// behind the screen — a Ticket decoded straight into Detail — there is
	// nowhere to go back to and esc is the ladder's last rung: it quits.
	Back key.Binding
	// Parent opens the collection this Ticket belongs to in the monitor. It is
	// enabled only when there is one to walk up into.
	Parent key.Binding
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
		Back: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "back"),
		),
		Parent: key.NewBinding(
			key.WithKeys("u"),
			key.WithHelp("u", "epic"),
			key.WithDisabled(),
		),
		Help: list.Help,
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
	}
}

// ShortHelp returns the bindings the Detail screen's one-line footer shows.
// Parent appears only when it is enabled, so the footer never offers to open a
// collection this Ticket has none of — and that footer line is the whole
// affordance for the walk-up: no second help line, no box.
func (k DetailKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Back, k.Parent, k.Refresh, k.Help, k.Quit}
}

// FullHelp returns every binding, grouped into the columns "?" expands to.
func (k DetailKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown},
		{k.Home, k.End},
		{k.Back, k.Parent, k.Refresh},
		{k.Help, k.Quit},
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
		Quit: key.NewBinding(
			key.WithKeys("ctrl+c"),
			key.WithHelp("ctrl+c", "quit"),
		),
	}
}

// ShortHelp returns the bindings the find box's footer shows.
func (k SearchKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Apply, k.Cancel, k.Move, k.Quit}
}

// FullHelp returns the same bindings: the find box has no second tier.
func (k SearchKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}
