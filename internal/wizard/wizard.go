package wizard

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/ericdahl-dev/git-green/internal/config"
)

// ErrUserAborted is returned when the user cancels the init form.
var ErrUserAborted = huh.ErrUserAborted

// RunInteractive collects one repo via Huh and writes a starter config.
func RunInteractive(path string, force bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("config already exists at %s (use --force to overwrite)", path)
	}

	var owner, name, branch string
	branch = "main"

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("GitHub owner").
				Description("Organization or user that owns the repository.").
				Value(&owner).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("owner is required")
					}
					return nil
				}),
			huh.NewInput().
				Title("Repository name").
				Value(&name).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("repository name is required")
					}
					return nil
				}),
			huh.NewInput().
				Title("Branch").
				Description("Leave as main or set your default branch. Empty uses GitHub default.").
				Value(&branch),
		).Title("git-green init").Description("Create a starter config.toml"),
	)

	if err := form.Run(); err != nil {
		return err
	}

	return config.WriteStarter(path, owner, name, branch)
}
