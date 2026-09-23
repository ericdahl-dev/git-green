package wizard

import (
	"fmt"
	"os"

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

	var repo, branch string
	branch = "main"

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Repository").
				Description("owner/name, or paste a GitHub URL.").
				Value(&repo).
				Validate(func(s string) error {
					_, _, err := config.ParseRepoRef(s)
					return err
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

	owner, name, err := config.ParseRepoRef(repo)
	if err != nil {
		return err
	}
	return config.WriteStarter(path, owner, name, branch)
}
