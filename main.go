package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	bspin "github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ericdahl-dev/git-green/internal/config"
	githubclient "github.com/ericdahl-dev/git-green/internal/github"
	"github.com/ericdahl-dev/git-green/internal/poller"
	"github.com/ericdahl-dev/git-green/internal/state"
	"github.com/ericdahl-dev/git-green/internal/ui"
	"github.com/ericdahl-dev/git-green/internal/wizard"
)

type screen int

const (
	screenDashboard screen = iota
	screenManage
)

type model struct {
	screen      screen
	dashboard   ui.Dashboard
	manage      ui.Manage
	showHelp    bool
	pollCh      <-chan state.Snapshot
	pollCancel  context.CancelFunc
	pollCtx     context.Context
	poller      *poller.Poller
	pollChWrite chan state.Snapshot
	winWidth    int
	fetching    bool
	spinner     bspin.Model
	cfg         *config.Config
}

func waitForSnapshot(ch <-chan state.Snapshot) tea.Cmd {
	return func() tea.Msg {
		snap, ok := <-ch
		if !ok {
			return nil
		}
		return snap
	}
}

func kickSpinner(s bspin.Model) tea.Cmd {
	return func() tea.Msg {
		return s.Tick()
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		waitForSnapshot(m.pollCh),
		kickSpinner(m.spinner),
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.winWidth = msg.Width

	case bspin.TickMsg:
		if !m.fetching {
			return m, nil
		}
		var sc tea.Cmd
		m.spinner, sc = m.spinner.Update(msg)
		cmds = append(cmds, sc)

	case ui.BackMsg:
		m.screen = screenDashboard
		return m, nil

	case ui.ConfigChangedMsg:
		m.cfg = msg.Config
		m.poller.ReloadConfig(m.cfg, m.pollCtx, m.pollChWrite)
		m.fetching = true
		cmds = append(cmds, kickSpinner(m.spinner))
		return m, tea.Batch(cmds...)

	case tea.KeyMsg:
		if m.screen == screenManage {
			var manCmd tea.Cmd
			m.manage, manCmd = m.manage.Update(msg)
			return m, manCmd
		}

		switch msg.String() {
		case "q", "ctrl+c":
			m.pollCancel()
			return m, tea.Quit
		case "?": 
			m.showHelp = !m.showHelp
			return m, nil
		case "esc":
			m.showHelp = false
			return m, nil
		case "m":
			m.screen = screenManage
			m.manage = ui.NewManage(m.cfg)
			return m, nil
		case "r":
			m.fetching = true
			m.poller.ForceRefresh(m.pollCtx, m.pollChWrite)
			cmds = append(cmds, kickSpinner(m.spinner))
			var dashCmd tea.Cmd
			m.dashboard, dashCmd = m.dashboard.Update(msg)
			cmds = append(cmds, dashCmd)
			return m, tea.Batch(cmds...)
		case "o":
			m.openSelectedURL()
			return m, nil
		}

	case state.Snapshot:
		m.fetching = false
		cmds = append(cmds, waitForSnapshot(m.pollCh))
		var dashCmd tea.Cmd
		m.dashboard, dashCmd = m.dashboard.Update(msg)
		cmds = append(cmds, dashCmd)
		return m, tea.Batch(cmds...)
	}

	if m.screen == screenManage {
		var manCmd tea.Cmd
		m.manage, manCmd = m.manage.Update(msg)
		cmds = append(cmds, manCmd)
	} else {
		var dashCmd tea.Cmd
		m.dashboard, dashCmd = m.dashboard.Update(msg)
		cmds = append(cmds, dashCmd)
	}
	return m, tea.Batch(cmds...)
}

func (m model) View() string {
	if m.showHelp {
		return ui.RenderHelp(m.winWidth)
	}
	title := ui.TitleLine(m.fetching, m.spinner.View())
	switch m.screen {
	case screenManage:
		return title + m.manage.View()
	default:
		return title + m.dashboard.BodyView()
	}
}

func (m *model) openSelectedURL() {
	u := m.dashboard.SelectedRunURL()
	if u == "" {
		return
	}
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", u)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		c = exec.Command("xdg-open", u)
	}
	_ = c.Start()
}

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

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "init" {
		os.Exit(runInit(os.Args[2:]))
	}

	cfg, err := config.Load(configPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "git-green: %v\n", err)
		os.Exit(1)
	}

	factory := func(token string) poller.Fetcher {
		return githubclient.New(token)
	}

	p := poller.New(cfg, factory)
	ctx, cancel := context.WithCancel(context.Background())

	writeCh := make(chan state.Snapshot, 4)
	readCh, stopPoller := p.Start(ctx)

	go func() {
		for snap := range readCh {
			writeCh <- snap
		}
		close(writeCh)
	}()

	spin := bspin.New(
		bspin.WithSpinner(bspin.MiniDot),
		bspin.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("205"))),
	)

	m := model{
		screen:      screenDashboard,
		dashboard:   ui.NewDashboard(p.Snapshot()),
		manage:      ui.NewManage(cfg),
		cfg:         cfg,
		pollCh:      writeCh,
		pollCancel:  func() { cancel(); stopPoller() },
		pollCtx:     ctx,
		poller:      p,
		pollChWrite: writeCh,
		winWidth:    80,
		fetching:    true,
		spinner:     spin,
	}

	prog := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
