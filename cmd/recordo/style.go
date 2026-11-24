package main

import (
	"fmt"

	"charm.land/lipgloss/v2"

	"github.com/waarispierre/recordo/internal/domain/config"
)

// Terminal styles. Printing goes through lipgloss.Printf/Println, which drop colour when
// output is piped and downsample it for terminals that cannot show it.
var (
	bold   = lipgloss.NewStyle().Bold(true)
	dim    = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	cyan   = lipgloss.NewStyle().Foreground(lipgloss.Cyan)
	green  = lipgloss.NewStyle().Foreground(lipgloss.Green)
	yellow = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	red    = lipgloss.NewStyle().Foreground(lipgloss.Red)

	tick = green.Render("✓")
	warn = yellow.Render("!")
	fail = red.Render("✗")
	dot  = dim.Render("·")
)

// say prints one indented line.
func say(format string, a ...any) { lipgloss.Printf("  "+format+"\n", a...) }

// swatch is a block in the setting's own colour, so RGB triples read at a glance.
func swatch(value string) string {
	if r, g, b, ok := config.ParseRGB(value); ok {
		c := lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", r, g, b))
		return lipgloss.NewStyle().Foreground(c).Render("██") + " "
	}
	return ""
}
