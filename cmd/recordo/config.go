package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/waarispierre/recordo/internal/config"
)

func configCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read and write settings",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return configMenu() },
	}
	cmd.AddCommand(
		&cobra.Command{
			Use: "menu", Short: "Browse and edit settings interactively (the default)", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { return configMenu() },
		},
		&cobra.Command{
			Use: "show", Short: "Print every setting and its value", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { return configShow() },
		},
		&cobra.Command{
			Use: "get <key>", Short: "Read one setting, e.g. camera.zoom_percent", Args: cobra.ExactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error { return configGet(args[0]) },
		},
		&cobra.Command{
			Use: "set <key> <value>", Short: "Write one setting, e.g. camera.zoom_percent 30", Args: cobra.ExactArgs(2),
			RunE: func(_ *cobra.Command, args []string) error {
				path, err := configPath()
				if err != nil {
					return err
				}
				if _, err := config.LoadOrCreate(path); err != nil {
					return err
				}
				key, value, err := setSetting(path, args[0], args[1])
				if err != nil {
					return err
				}
				say("%s %s = %s", tick, cyan.Render(key), bold.Render(value))
				return nil
			},
		},
		&cobra.Command{
			Use: "path", Short: "Print the config file path", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				path, err := configPath()
				if err == nil {
					fmt.Println(path)
				}
				return err
			},
		},
		&cobra.Command{
			Use: "edit", Short: "Open the config in $EDITOR", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				_, path, err := loadConfig()
				if err != nil {
					return err
				}
				return openEditor(path)
			},
		},
		&cobra.Command{
			Use: "reset", Short: "Restore defaults", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				if err := resetConfig(); err != nil {
					return err
				}
				say("%s restored defaults", tick)
				return nil
			},
		},
	)
	return cmd
}

func configShow() error {
	_, path, err := loadConfig()
	if err != nil {
		return err
	}
	entries, err := config.Flatten(path)
	if err != nil {
		return err
	}
	fmt.Println()
	say("%s", dim.Render(path))
	fmt.Println()
	for _, e := range entries {
		say("%s %s%s", cyan.Render(fmt.Sprintf("%-28s", e.Key)), swatch(e.Value), e.Value)
	}
	fmt.Println()
	return nil
}

func configGet(key string) error {
	_, path, err := loadConfig()
	if err != nil {
		return err
	}
	entries, err := config.Flatten(path)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Key == key {
			fmt.Println(e.Value)
			return nil
		}
	}
	return fmt.Errorf("no such setting: %s", key)
}

// setSetting writes one value, then reloads so an invalid value is reported now rather
// than at render time, rolling the file back if it does not load. Colours are accepted as
// hex, the form people actually have on hand, and stored as float triples.
func setSetting(path, key, value string) (string, string, error) {
	if config.IsColourKey(key) {
		converted, ok := config.RGBToTOML(value)
		if !ok {
			return "", "", fmt.Errorf("%s is not a colour — try #5C66C7 or 0.36, 0.40, 0.78", value)
		}
		value = converted
	}
	before, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", path, err)
	}
	if err := config.SetValue(path, key, value); err != nil {
		return "", "", err
	}
	cfg, err := config.LoadOrCreate(path)
	if err == nil {
		_, err = cfg.BackgroundPath()
	}
	if err != nil {
		if werr := os.WriteFile(path, before, 0o600); werr != nil {
			return "", "", fmt.Errorf("%s = %s is not valid (%v), and restoring the file failed: %w", key, value, err, werr)
		}
		return "", "", fmt.Errorf("%s = %s is not valid: %w", key, value, err)
	}
	return key, value, nil
}

func resetConfig() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	_, err = config.LoadOrCreate(path)
	return err
}

func openEditor(path string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "nano"
	}
	// $EDITOR may carry arguments ("code -w"); run it through the shell like git does.
	cmd := exec.Command("/bin/sh", "-c", editor+` "$1"`, "sh", path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

const (
	menuEditor = "\x00editor"
	menuReset  = "\x00reset"
	menuDone   = "\x00done"
)

// configMenu is the interactive settings browser: pick a setting, edit it, repeat. Piped
// or redirected input cannot drive a menu, so it prints the settings instead.
func configMenu() error {
	_, path, err := loadConfig()
	if err != nil {
		return err
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return configShow()
	}
	cursor := ""
	for {
		settings, err := config.Settings(path)
		if err != nil {
			return err
		}
		if len(settings) == 0 {
			say("config is empty — try %s", cyan.Render("recordo config reset"))
			return nil
		}
		// Pad the key column so values line up and the list scans vertically.
		width := 0
		for _, s := range settings {
			width = max(width, len(s.Key))
		}
		var options []huh.Option[string]
		for _, s := range settings {
			label := fmt.Sprintf("%-*s  %s%s", width, s.Key, swatch(s.Value), dim.Render(s.Value))
			options = append(options, huh.NewOption(label, s.Key))
		}
		options = append(options,
			huh.NewOption(cyan.Render("open in $EDITOR"), menuEditor),
			huh.NewOption(yellow.Render("reset to defaults"), menuReset),
			huh.NewOption(green.Render("done"), menuDone),
		)
		choice := cursor
		err = huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().Title("settings").Options(options...).Height(min(len(options)+2, 20)).Value(&choice),
		)).Run()
		// Esc and Ctrl-C both mean "leave things as they are".
		if errors.Is(err, huh.ErrUserAborted) || choice == menuDone {
			return nil
		}
		if err != nil {
			return err
		}
		cursor = choice

		switch choice {
		case menuReset:
			if err := resetConfig(); err != nil {
				return err
			}
			say("%s restored defaults", tick)
			cursor = ""
			continue
		case menuEditor:
			if err := openEditor(path); err != nil {
				return err
			}
			continue
		}

		var setting config.Setting
		for _, s := range settings {
			if s.Key == choice {
				setting = s
			}
		}
		editSetting(path, setting)
	}
}

// editSetting asks for a new value. Blank, or the current value, keeps it.
func editSetting(path string, s config.Setting) {
	desc := fmt.Sprintf("now %s — blank keeps it", s.Value)
	if config.IsColourKey(s.Key) {
		desc = "hex like #5C66C7, or r,g,b — blank keeps it"
	}
	if s.Help != "" {
		desc = s.Help + "\n" + desc
	}
	entered := ""
	err := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title(s.Key).Description(desc).Value(&entered),
	)).Run()
	entered = strings.TrimSpace(entered)
	if err != nil || entered == "" || entered == strings.TrimSpace(s.Value) {
		return
	}
	key, value, err := setSetting(path, s.Key, entered)
	if err != nil {
		say("%s %s rejected: %s — keeping %s", fail, bold.Render(entered), lastLine(err.Error()), bold.Render(s.Value))
		return
	}
	say("%s %s = %s", tick, cyan.Render(key), bold.Render(value))
}

// lastLine is the actual complaint in a multi-line parse error.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
