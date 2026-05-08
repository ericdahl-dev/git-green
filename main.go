package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericdahl-dev/git-green/internal/config"
	githubclient "github.com/ericdahl-dev/git-green/internal/github"
	"github.com/ericdahl-dev/git-green/internal/poller"
	"github.com/ericdahl-dev/git-green/internal/state"
	"github.com/ericdahl-dev/git-green/internal/ui"
)

type model struct {
	dashboard   ui.Dashboard
	showHelp    bool
	pollCh      <-chan state.Snapshot
	pollCancel  context.CancelFunc
	pollCtx     context.Context
	poller      *poller.Poller
	pollChWrite chan state.Snapshot
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

func (m model) Init() tea.Cmd {
	return waitForSnapshot(m.pollCh)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
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
		case "r":
			m.poller.ForceRefresh(m.pollCtx, m.pollChWrite)
			return m, nil
		case "o":
			m.openInBrowser()
			return m, nil
		}

	case state.Snapshot:
		cmds = append(cmds, waitForSnapshot(m.pollCh))
		var dashCmd tea.Cmd
		m.dashboard, dashCmd = m.dashboard.Update(msg)
		cmds = append(cmds, dashCmd)
		return m, tea.Batch(cmds...)
	}

	var cmd tea.Cmd
	m.dashboard, cmd = m.dashboard.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m model) View() string {
	if m.showHelp {
		return ui.Help{}.View()
	}
	return m.dashboard.View()
}

func (m *model) openInBrowser() {
	repo := m.dashboard.SelectedRepo()
	if repo != nil && len(repo.Runs) > 0 {
		exec.Command("open", repo.Runs[0].HTMLURL).Start()
	}
}

func configPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(home, ".config", "git-green", "config.toml")
}

func main() {
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

	m := model{
		dashboard:   ui.NewDashboard(state.Snapshot{}),
		pollCh:      writeCh,
		pollCancel:  func() { cancel(); stopPoller() },
		pollCtx:     ctx,
		poller:      p,
		pollChWrite: writeCh,
	}

	prog := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
