package tui

import (
	"charm.land/bubbles/v2/key"
)

// KeyMap is the monitor's complete keyboard surface. Every binding carries its
// help text, so bubbles/help renders the footer from this one declaration and
// the help line cannot drift from what the keys actually do.
//
// Nothing a filter would want is bound here — not "/", not "f", not "h". Those
// belong to the filtering work, and a key shipped early is a key that has to be
// un-shipped.
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
	// Open is reserved for the Ticket Detail drill-in and does nothing today.
	Open key.Binding
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

// ShortHelp returns the bindings the one-line footer shows.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Refresh, k.Help, k.Quit}
}

// FullHelp returns every binding, grouped into the columns "?" expands to.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown},
		{k.Home, k.End, k.Open},
		{k.Refresh, k.Help, k.Quit},
	}
}
