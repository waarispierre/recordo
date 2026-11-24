package main

import (
	"bufio"
	"fmt"
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
		if exists(s.Capture()) {
			shown = append(shown, s)
		}
	}
	if len(shown) == 0 {
		say("no recordings yet — run %s", cyan.Render("recordo"))
		return nil
	}
	fmt.Println()
	say("%s", dim.Render(root))
	for _, s := range shown {
		state := yellow.Render("raw only")
		if s.HasExport() {
			state = green.Render("rendered")
		}
		say("%s  %s", cyan.Render(filepath.Base(s.Dir)), state)
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
		say("nothing to prune")
		return nil
	}

	fmt.Println()
	for _, s := range targets {
		action := yellow.Render("drop raw capture")
		if all {
			action = red.Render("delete entirely")
		}
		say("%s  %s", cyan.Render(filepath.Base(s.Dir)), action)
	}
	fmt.Println()
	say("%d recording(s), %.1f MB", len(targets), float64(freed)/1_048_576)
	fmt.Println()
	if all {
		say("%s this removes the rendered videos too", red.Render("!"))
		fmt.Println()
	}
	if !yes {
		fmt.Print("  " + bold.Render("proceed? [y/N]:") + " ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			say("cancelled")
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
	say("%s freed %.1f MB", tick, float64(freed)/1_048_576)
	return nil
}
