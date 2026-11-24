package settings

// The settings tab: every setting with its help text, edited in place with the same
// write-verify-roll-back contract as `config set`.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/waarispierre/recordo/internal/domain/config"
	"github.com/waarispierre/recordo/internal/tui/kit"
)

var editBinding = kit.Bind("enter", "edit", "enter")

// edit is a setting being edited in the popup.
type edit struct {
	key   string
	input textinput.Model
}

// Tab is the settings tab.
type Tab struct {
	kit.Base
	host       kit.Host
	configPath string
	rows       int
	width      int

	settings []config.Setting
	list     kit.List
	editing  *edit
}

// NewTab reads the settings, with the config file's own comments as help.
func NewTab(host kit.Host, configPath string) (*Tab, error) {
	settings, err := config.Settings(configPath)
	if err != nil {
		return nil, err
	}
	return &Tab{host: host, configPath: configPath, settings: settings}, nil
}

func (t *Tab) Title() string { return "settings" }

func (t *Tab) Keys() []key.Binding { return []key.Binding{editBinding, kit.Switch, kit.Quit} }

func (t *Tab) Modal() bool { return t.editing != nil }

func (t *Tab) Update(msg tea.Msg) tea.Cmd {
	cmd := t.update(msg)
	t.list.Scroll(t.rows - 2)
	return cmd
}

func (t *Tab) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case kit.LayoutMsg:
		t.rows, t.width = msg.PanelRows, msg.Width
	case tea.KeyPressMsg:
		if t.editing != nil {
			return t.editKey(msg)
		}
		switch {
		case key.Matches(msg, kit.Down):
			t.list.Move(1, len(t.settings))
		case key.Matches(msg, kit.Up):
			t.list.Move(-1, len(t.settings))
		case key.Matches(msg, editBinding):
			if s, ok := t.selected(); ok {
				t.host.SetStatus(fmt.Sprintf("editing %s — enter to save, esc to cancel", s.Key))
				in := kit.NewInput(t.width)
				t.editing = &edit{key: s.Key, input: in}
				return t.editing.input.Focus()
			}
		}
	default:
		// Cursor blinks and the like.
		if t.editing != nil {
			var cmd tea.Cmd
			t.editing.input, cmd = t.editing.input.Update(msg)
			return cmd
		}
	}
	return nil
}

func (t *Tab) selected() (config.Setting, bool) {
	i := t.list.Selected
	if i < 0 || i >= len(t.settings) {
		return config.Setting{}, false
	}
	return t.settings[i], true
}

func (t *Tab) editKey(k tea.KeyPressMsg) tea.Cmd {
	e := t.editing
	switch k.Code {
	case tea.KeyEscape:
		t.editing = nil
		t.host.SetStatus("cancelled")
		return nil
	case tea.KeyEnter:
	default:
		var cmd tea.Cmd
		e.input, cmd = e.input.Update(k)
		return cmd
	}
	t.editing = nil
	value := strings.TrimSpace(e.input.Value())
	// Blank means "leave it alone".
	if value == "" {
		t.host.SetStatus("unchanged")
		return nil
	}
	return t.commit(e.key, value)
}

// commit writes one setting, verifies it, and rolls back. Same contract as the
// non-interactive path: an invalid value must never be left in the file.
func (t *Tab) commit(settingKey, value string) tea.Cmd {
	previous := ""
	for _, s := range t.settings {
		if s.Key == settingKey {
			previous = s.Value
		}
	}
	if config.IsColourKey(settingKey) {
		v, ok := config.RGBToTOML(value)
		if !ok {
			t.host.SetStatus(fmt.Sprintf("%s is not a colour — try #5C66C7", value))
			return nil
		}
		value = v
	}

	before, err := os.ReadFile(t.configPath)
	if err != nil {
		return t.host.Fail(err)
	}
	err = config.SetValue(t.configPath, settingKey, value)
	if err == nil {
		var cfg config.Config
		if cfg, err = config.LoadOrCreate(t.configPath); err == nil {
			_, err = cfg.BackgroundPath()
		}
	}
	if err != nil {
		if werr := os.WriteFile(t.configPath, before, 0o644); werr != nil {
			return t.host.Fail(werr)
		}
		t.host.SetStatus(fmt.Sprintf("rejected: %s — keeping %s", rootCause(err), previous))
		return nil
	}
	t.host.SetStatus(fmt.Sprintf("%s = %s", settingKey, value))
	if err := t.reload(); err != nil {
		return t.host.Fail(err)
	}
	return nil
}

func (t *Tab) reload() error {
	settings, err := config.Settings(t.configPath)
	if err != nil {
		return err
	}
	t.settings = settings
	if len(t.settings) == 0 {
		t.list.Selected = 0
	} else {
		t.list.Selected = min(t.list.Selected, len(t.settings)-1)
	}
	return nil
}

// rootCause is the innermost error's last non-blank line: the part that says what was
// actually wrong, without the "parse <path>:" wrapping around it.
func rootCause(err error) string {
	for u := errors.Unwrap(err); u != nil; u = errors.Unwrap(u) {
		err = u
	}
	lines := strings.Split(err.Error(), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return "invalid"
}

func (t *Tab) Draw(c *lipgloss.Canvas, r kit.Rect) {
	leftW := r.W * 62 / 100
	left := kit.Rect{X: r.X, Y: r.Y, W: leftW, H: r.H}
	right := kit.Rect{X: r.X + leftW, Y: r.Y, W: r.W - leftW, H: r.H}

	width := 0
	for _, s := range t.settings {
		width = max(width, len(s.Key))
	}
	items := make([]string, len(t.settings))
	for i, s := range t.settings {
		item := fmt.Sprintf("%-*s  ", width, s.Key)
		// A colour is far easier to judge as a block than as three floats.
		if r, g, b, ok := config.ParseRGB(s.Value); ok {
			item += kit.Plain.Foreground(kit.RGB(r, g, b)).Render("██ ")
		}
		items[i] = item + kit.Dim.Render(s.Value)
	}
	t.list.Draw(c, left, " settings ", items)

	help := ""
	if s, ok := t.selected(); ok {
		help = s.Help
	}
	inner := kit.DrawBlock(c, right, " what it does ", kit.Plain)
	kit.Put(c, inner, kit.Plain.Foreground(kit.Gray).Render(kit.Wrap([]string{help}, inner.W)))
}

func (t *Tab) DrawPopup(c *lipgloss.Canvas, screen kit.Rect) {
	e := t.editing
	if e == nil {
		return
	}
	hint := ""
	if config.IsColourKey(e.key) {
		hint = "hex like #5C66C7, or r, g, b"
	} else if s, ok := t.selected(); ok {
		hint = "now " + s.Value
	}
	kit.DrawEditPopup(c, screen, e.key, hint, e.input)
}
