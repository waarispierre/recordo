package kit

import "charm.land/bubbles/v2/key"

func binding(k, desc string, keys ...string) key.Binding {
	return key.NewBinding(key.WithKeys(keys...), key.WithHelp(k, desc))
}

// Keys every tab uses. Quit and Switch are handled by the shell; tabs list them in their
// status-bar hints.
var (
	Up     = binding("k", "up", "k", "up")
	Down   = binding("j", "down", "j", "down")
	Enter  = binding("enter", "select", "enter")
	Quit   = binding("q", "quit", "q", "esc", "ctrl+c")
	Switch = binding("tab", "switch", "tab", "l")
	Yes    = binding("y", "yes", "y", "Y")
)

// Bind is a key binding with its status-bar hint.
func Bind(k, desc string, keys ...string) key.Binding { return binding(k, desc, keys...) }
