// Package recordings is the recordings slice: list past recordings and prune them.
package recordings

import (
	"bufio"
	"fmt"
	"github.com/waarispierre/recordo/internal/cli"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/waarispierre/recordo/internal/domain/session"
)

func cmdList() error {
	all, err := session.All()
	if err != nil {
		return err
	}
	root, err := session.RecordingsDir()
	if err != nil {
		return err
	}
	var shown []*session.Session
	for _, s := range all {
		if cli.Exists(s.Capture()) {
			shown = append(shown, s)
		}
	}
	if len(shown) == 0 {
		cli.Say("no recordings yet — run %s", cli.Cyan.Render("recordo"))
		return nil
	}
	fmt.Println()
	cli.Say("%s", cli.Dim.Render(root))
	for _, s := range shown {
		state := cli.Yellow.Render("raw only")
		if s.HasExport() {
			state = cli.Green.Render("rendered")
		}
		cli.Say("%s  %s", cli.Cyan.Render(filepath.Base(s.Dir)), state)
	}
	fmt.Println()
	return nil
}

func pruneCommand() *cobra.Command {
	var olderThan uint
	var all, yes bool
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete raw captures, keeping the rendered videos",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmdPrune(cmd.Flags().Changed("older-than"), int64(olderThan), all, yes)
		},
	}
	cmd.Flags().UintVar(&olderThan, "older-than", 0, "only recordings at least this many days old")
	cmd.Flags().BoolVar(&all, "all", false, "delete the rendered videos too — removes the recordings entirely")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func cmdPrune(byAge bool, olderThan int64, all, yes bool) error {
	sessions, err := session.All()
	if err != nil {
		return err
	}
	var targets []*session.Session
	var freed uint64
	for _, s := range sessions {
		if byAge {
			age, ok := s.AgeDays()
			if !ok || age < olderThan {
				continue
			}
		}
		// Without --all, only touch recordings that have already been rendered, so
		// pruning can never destroy the only copy of something.
		if !all && !(s.HasExport() && s.RawBytes() > 0) {
			continue
		}
		targets = append(targets, s)
		if all {
			freed += s.Bytes()
		} else {
			freed += s.RawBytes()
		}
	}
	if len(targets) == 0 {
		cli.Say("nothing to prune")
		return nil
	}

	fmt.Println()
	for _, s := range targets {
		action := cli.Yellow.Render("drop raw capture")
		if all {
			action = cli.Red.Render("delete entirely")
		}
		cli.Say("%s  %s", cli.Cyan.Render(filepath.Base(s.Dir)), action)
	}
	fmt.Println()
	cli.Say("%d recording(s), %.1f MB", len(targets), float64(freed)/1_048_576)
	fmt.Println()
	if all {
		cli.Say("%s this removes the rendered videos too", cli.Red.Render("!"))
		fmt.Println()
	}
	if !yes {
		fmt.Print("  " + cli.Bold.Render("proceed? [y/N]:") + " ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			cli.Say("cancelled")
			return nil
		}
	}
	for _, s := range targets {
		if all {
			if err := os.RemoveAll(s.Dir); err != nil {
				return fmt.Errorf("remove %s: %w", s.Dir, err)
			}
		} else if err := s.DropRaw(); err != nil {
			return err
		}
	}
	cli.Say("%s freed %.1f MB", cli.Tick, float64(freed)/1_048_576)
	return nil
}

// Commands are `recordo list` and `recordo prune`.
func Commands() []*cobra.Command {
	return []*cobra.Command{
		{
			Use: "list", Short: "List past recordings", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { return cmdList() },
		},
		pruneCommand(),
	}
}
