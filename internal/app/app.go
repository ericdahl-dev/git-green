// Package app is the root Bubble Tea model: it routes between the Dashboard,
// the Repo manager and the help overlay, and keeps the Poller in step with
// what the Dashboard shows.
package app

import (
	"context"
	"os/exec"
	"runtime"

	bspin "github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ericdahl-dev/git-green/internal/config"
	githubclient "github.com/ericdahl-dev/git-green/internal/github"
	"github.com/ericdahl-dev/git-green/internal/state"
	"github.com/ericdahl-dev/git-green/internal/ui"
)

// Poller is what the App steers. The real one polls GitHub; tests substitute
// one that records what it was asked.
type Poller interface {
	// SetExpandedRepos says which Repos are open, as "owner/name"; only
	// those fetch per-run and per-PR detail.
	SetExpandedRepos(names []string)
	// Refresh asks for a poll cycle now.
	Refresh()
	// Reload swaps in an edited Config and polls with it now.
	Reload(cfg *config.Config)
}

// Options wires an App to its surroundings.
type Options struct {
	Config    *config.Config
	Poller    Poller
	Initial   state.Snapshot        // shown until the first poll lands
	Snapshots <-chan state.Snapshot // one per poll cycle; closed on stop
	// Ctx bounds re-runs, so they are cancelled when the app exits.
	Ctx context.Context
	// Stop shuts the Poller down on quit.
	Stop func()
	// Open shows a URL to the user. Defaults to the OS browser.
	Open func(url string)
}

type screen int

const (
	screenDashboard screen = iota
	screenManage
)

// Model is the root Bubble Tea model.
type Model struct {
	opts      Options
	cfg       *config.Config
	screen    screen
	dashboard ui.Dashboard
	manage    ui.Manage
	showHelp  bool
	winWidth  int
	fetching  bool
	spinner   bspin.Model
}

// New builds the root model.
func New(o Options) Model {
	if o.Open == nil {
		o.Open = OpenInBrowser
	}
	if o.Stop == nil {
		o.Stop = func() {}
	}
	if o.Ctx == nil {
		o.Ctx = context.Background()
	}
	return Model{
		opts:      o,
		cfg:       o.Config,
		screen:    screenDashboard,
		dashboard: ui.NewDashboard(o.Initial).WithRerunner(o.Ctx, rerunner{cfg: o.Config}),
		manage:    ui.NewManage(o.Config),
		winWidth:  80,
		fetching:  true,
		spinner: bspin.New(
			bspin.WithSpinner(bspin.MiniDot),
			bspin.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("205"))),
		),
	}
}

// rerunner re-runs a workflow run through a client authenticated with the
// token for that repo's org, the same way the poller picks one per repo.
type rerunner struct {
	cfg *config.Config
}

func (r rerunner) RerunFailedJobs(ctx context.Context, owner, name string, runID int64) error {
	token, err := r.cfg.TokenForOrg(owner)
	if err != nil {
		return err
	}
	return githubclient.New(token).RerunFailedJobs(ctx, owner, name, runID)
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

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		waitForSnapshot(m.opts.Snapshots),
		kickSpinner(m.spinner),
	)
}

// refresh marks a poll as in flight and asks the Poller for it.
func (m Model) refresh() (Model, tea.Cmd) {
	m.fetching = true
	m.opts.Poller.Refresh()
	return m, kickSpinner(m.spinner)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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

	case ui.ExpandedReposMsg:
		// A row opened or closed: the poller now needs a different amount of
		// detail for that repo, so refresh instead of waiting for the tick.
		m.opts.Poller.SetExpandedRepos(msg.Repos)
		return m.refresh()

	case ui.ConfigChangedMsg:
		m.cfg = msg.Config
		m.dashboard = m.dashboard.WithRerunner(m.opts.Ctx, rerunner{cfg: m.cfg})
		m.fetching = true
		m.opts.Poller.Reload(m.cfg)
		return m, kickSpinner(m.spinner)

	case tea.KeyMsg:
		if m.screen == screenManage {
			var manCmd tea.Cmd
			m.manage, manCmd = m.manage.Update(msg)
			return m, manCmd
		}

		// While the dashboard holds a re-run confirmation it owns every key but
		// the hard quit, so enter/esc reach the prompt instead of the root.
		if m.dashboard.AwaitingConfirm() && msg.String() != "ctrl+c" {
			var dashCmd tea.Cmd
			m.dashboard, dashCmd = m.dashboard.Update(msg)
			return m, dashCmd
		}

		switch msg.String() {
		case "q", "ctrl+c":
			m.opts.Stop()
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
			var cmd, dashCmd tea.Cmd
			m, cmd = m.refresh()
			m.dashboard, dashCmd = m.dashboard.Update(msg)
			return m, tea.Batch(cmd, dashCmd)
		case "o":
			if u := m.dashboard.SelectedRunURL(); u != "" {
				m.opts.Open(u)
			}
			return m, nil
		}

	case state.Snapshot:
		m.fetching = false
		cmds = append(cmds, waitForSnapshot(m.opts.Snapshots))
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

func (m Model) View() string {
	if m.showHelp {
		return ui.RenderHelp(m.winWidth)
	}
	title := ui.TitleLine(m.fetching, m.spinner.View(), m.dashboard.Throttles())
	switch m.screen {
	case screenManage:
		return title + m.manage.View()
	default:
		return title + m.dashboard.BodyView()
	}
}

// OpenInBrowser opens u in the OS default browser.
func OpenInBrowser(u string) {
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
