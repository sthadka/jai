package tui

import "github.com/charmbracelet/bubbles/key"

// KeyMap defines all TUI keybindings.
type KeyMap struct {
	// Navigation.
	Up       key.Binding
	Down     key.Binding
	HalfUp   key.Binding
	HalfDown key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	GoTop    key.Binding
	GoBottom key.Binding

	// View switching.
	TabNext  key.Binding
	TabPrev  key.Binding
	Tab1     key.Binding
	Tab2     key.Binding
	Tab3     key.Binding
	Tab4     key.Binding
	Tab5     key.Binding
	Tab6     key.Binding
	Tab7     key.Binding
	Tab8     key.Binding
	Tab9     key.Binding

	// Actions.
	Select   key.Binding
	Sort     key.Binding
	Group    key.Binding
	Filter   key.Binding
	Refresh  key.Binding
	Open     key.Binding
	Edit     key.Binding
	Comment  key.Binding
	Quit     key.Binding
	Back     key.Binding
	Help     key.Binding
}

// DefaultKeys returns the default keybindings.
func DefaultKeys() KeyMap {
	return KeyMap{
		Up:       key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k/↑", "up")),
		Down:     key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/↓", "down")),
		HalfUp:   key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("ctrl+u", "half up")),
		HalfDown: key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "half down")),
		PageUp:   key.NewBinding(key.WithKeys("pgup", "ctrl+b"), key.WithHelp("pgup", "page up")),
		PageDown: key.NewBinding(key.WithKeys("pgdown", "ctrl+f"), key.WithHelp("pgdn", "page down")),
		GoTop:    key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g/home", "top")),
		GoBottom: key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G/end", "bottom")),

		TabNext: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next view")),
		TabPrev: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev view")),
		Tab1:    key.NewBinding(key.WithKeys("1")),
		Tab2:    key.NewBinding(key.WithKeys("2")),
		Tab3:    key.NewBinding(key.WithKeys("3")),
		Tab4:    key.NewBinding(key.WithKeys("4")),
		Tab5:    key.NewBinding(key.WithKeys("5")),
		Tab6:    key.NewBinding(key.WithKeys("6")),
		Tab7:    key.NewBinding(key.WithKeys("7")),
		Tab8:    key.NewBinding(key.WithKeys("8")),
		Tab9:    key.NewBinding(key.WithKeys("9")),

		Select:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open detail")),
		Sort:    key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort")),
		Group:   key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "group by")),
		Filter:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Open:    key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open in browser")),
		Edit:    key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit field")),
		Comment: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "comment")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Back:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back/clear")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	}
}
