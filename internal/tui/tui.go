// Package tui is the full-screen terminal app's shell, in the spirit of lazygit: program
// setup, the tab bar, layout, status bar, key routing, the report popup and the progress
// of a running job. Each tab belongs to its feature slice (record, settings,
// recordings), which the shell talks to through kit.Tab.
//
// Nothing leaves the alternate screen any more: a job draws its own progress here rather
// than suspending the app for line-oriented output.
//
// Everything slow runs off the Bubble Tea event loop. Rendering and compressing are
// ffmpeg and WebGPU work on a goroutine, reporting through a counter the 100ms tick
// polls; the record tab runs its capture on a goroutine too.
package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/waarispierre/recordo/internal/domain/config"
	"github.com/waarispierre/recordo/internal/domain/session"
	"github.com/waarispierre/recordo/internal/features/record"
	"github.com/waarispierre/recordo/internal/features/recordings"
	"github.com/waarispierre/recordo/internal/tui/kit"
)

// Overrides are per-run settings that would otherwise come from CLI flags.
type Overrides = kit.Overrides

// Deps are the tabs' side effects, injected so tests can run the real app against fakes.
type Deps struct {
	Record     record.TabDeps
	Recordings recordings.TabDeps
}

// Run shows the app until the user quits. It must run under macos.Main when the webcam
// may be used.
func Run(o Overrides) error {
	configPath, err := session.ConfigPath()
	if err != nil {
		return err
	}
	// Before the alternate screen, so the "wrote default config" note on a first run is
	// printed where it can be read.
	if _, err := config.LoadOrCreate(configPath); err != nil {
		return err
	}
	m, err := New(o, configPath, Deps{Record: record.RealTabDeps(), Recordings: recordings.RealTabDeps()})
	if err != nil {
		return err
	}
	final, runErr := tea.NewProgram(m).Run()
	if fm, ok := final.(*Model); ok && fm != nil {
		m = fm
	}
	// The program can end with work still in flight (an interrupt from outside, say);
	// a recording must still be finalised rather than abandoned.
	m.shutdown()
	if runErr != nil {
		return runErr
	}
	if m.fatal != nil {
		return m.fatal
	}
	// Quitting mid-recording still finalises the capture, so say where it went — the
	// popup that would normally carry that never gets a frame to be drawn in.
	if m.farewell != "" {
		for _, line := range strings.Split(m.farewell, "\n") {
			fmt.Printf("  %s\n", line)
		}
	}
	return nil
}
