package main

import "github.com/spf13/cobra"

// runTUI opens the full-screen app. Until internal/tui lands, a bare `recordo` records,
// exactly as `recordo record` does.
func runTUI(cmd *cobra.Command) error { return cmdRecord(cmd) }
