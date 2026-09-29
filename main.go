package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ericdahl-dev/git-green/internal/app"
	"github.com/ericdahl-dev/git-green/internal/config"
	githubclient "github.com/ericdahl-dev/git-green/internal/github"
	"github.com/ericdahl-dev/git-green/internal/poller"
	"github.com/ericdahl-dev/git-green/internal/wizard"
)

func configPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(home, ".config", "git-green", "config.toml")
}

func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	force := fs.Bool("force", false, "overwrite existing config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path := configPath()
	if err := wizard.RunInteractive(path, *force); err != nil {
		if errors.Is(err, wizard.ErrUserAborted) {
			return 1
		}
		fmt.Fprintf(os.Stderr, "git-green init: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "Wrote %s\n", path)
	return 0
}

// version is overwritten at build time via -ldflags "-X main.version=...".
// goreleaser sets it from the git tag; a plain `go build` leaves it as "dev".
var version = "dev"

func helpText() string {
	return `git-green — terminal dashboard for GitHub Actions CI status

Usage:
  git-green            launch the dashboard
  git-green init       create a starter config
  git-green --version  print the version
  git-green --help     show this help
`
}

// runCommand handles the non-TUI invocations. It reports whether args were
// handled here, along with the exit code to use when they were. Anything it
// does not recognise falls through to launching the dashboard.
func runCommand(args []string, out io.Writer) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "init":
		return runInit(args[1:]), true
	case "help", "-help", "--help":
		_, _ = fmt.Fprint(out, helpText())
		return 0, true
	case "version", "-version", "--version":
		_, _ = fmt.Fprintf(out, "git-green %s\n", version)
		return 0, true
	}
	return 0, false
}

func main() {
	if code, handled := runCommand(os.Args[1:], os.Stdout); handled {
		os.Exit(code)
	}

	cfg, err := config.Load(configPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "git-green: %v\n", err)
		os.Exit(1)
	}

	p := poller.New(cfg, func(token string) poller.Fetcher {
		return githubclient.New(token)
	})
	ctx, cancel := context.WithCancel(context.Background())
	snapshots, stopPoller := p.Start(ctx)

	m := app.New(app.Options{
		Config:    cfg,
		Poller:    p,
		Initial:   p.Snapshot(),
		Snapshots: snapshots,
		Ctx:       ctx,
		Stop:      func() { cancel(); stopPoller() },
	})

	prog := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
