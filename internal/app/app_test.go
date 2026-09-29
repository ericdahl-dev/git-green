package app

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ericdahl-dev/git-green/internal/aggregator"
	"github.com/ericdahl-dev/git-green/internal/config"
	githubclient "github.com/ericdahl-dev/git-green/internal/github"
	"github.com/ericdahl-dev/git-green/internal/state"
	"github.com/ericdahl-dev/git-green/internal/ui"
)

// fakePoller records what the App asks of it.
type fakePoller struct {
	expanded  [][]string
	refreshes int
	reloads   []*config.Config
}

func (f *fakePoller) SetExpandedRepos(names []string) { f.expanded = append(f.expanded, names) }
func (f *fakePoller) Refresh()                        { f.refreshes++ }
func (f *fakePoller) Reload(cfg *config.Config)       { f.reloads = append(f.reloads, cfg) }

type harness struct {
	m       Model
	poller  *fakePoller
	opened  []string
	stopped bool
}

func newHarness(repos ...state.RepoState) *harness {
	h := &harness{poller: &fakePoller{}}
	h.m = New(Options{
		Config:    &config.Config{},
		Poller:    h.poller,
		Initial:   state.New(repos),
		Snapshots: make(chan state.Snapshot),
		Open:      func(u string) { h.opened = append(h.opened, u) },
		Stop:      func() { h.stopped = true },
	})
	return h
}

// send delivers msg, then feeds back any App-level message its command
// produces (as Bubble Tea would), so a key's knock-on effects land.
func (h *harness) send(msg tea.Msg) tea.Cmd {
	next, cmd := h.m.Update(msg)
	h.m = next.(Model)
	return cmd
}

func (h *harness) key(k string) tea.Cmd {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	switch k {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "ctrl+c":
		msg = tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	return h.send(msg)
}

// drain runs cmd and delivers the ExpandedReposMsg it carries, as Bubble Tea
// would. Commands that block (timers, channel reads) are abandoned.
func (h *harness) drain(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(50 * time.Millisecond):
		return
	}
	switch m := msg.(type) {
	case tea.BatchMsg:
		for _, c := range m {
			h.drain(c)
		}
	case ui.ExpandedReposMsg:
		h.send(m)
	}
}

func repo(name string, light aggregator.Stoplight) state.RepoState {
	return state.RepoState{Owner: "o", Name: name, Stoplight: light,
		Runs: []githubclient.WorkflowRun{{WorkflowName: "CI", HTMLURL: "https://example.test/" + name}}}
}

func TestExpandingARepoSteersThePoller(t *testing.T) {
	h := newHarness(repo("a", aggregator.StoplightGreen))
	h.drain(h.key("enter"))

	if len(h.poller.expanded) != 1 || strings.Join(h.poller.expanded[0], ",") != "o/a" {
		t.Errorf("expanded = %v, want [[o/a]]", h.poller.expanded)
	}
	if h.poller.refreshes != 1 {
		t.Errorf("refreshes = %d, want 1 so the new detail loads now", h.poller.refreshes)
	}
}

func TestRKeyRefreshes(t *testing.T) {
	h := newHarness(repo("a", aggregator.StoplightGreen))
	h.key("r")
	if h.poller.refreshes != 1 {
		t.Errorf("refreshes = %d, want 1", h.poller.refreshes)
	}
}

func TestConfigChangeReloadsThePoller(t *testing.T) {
	h := newHarness(repo("a", aggregator.StoplightGreen))
	cfg := &config.Config{}
	h.send(ui.ConfigChangedMsg{Config: cfg})
	if len(h.poller.reloads) != 1 || h.poller.reloads[0] != cfg {
		t.Errorf("reloads = %v, want the new config", h.poller.reloads)
	}
}

func TestOKeyOpensTheSelectedRun(t *testing.T) {
	h := newHarness(repo("a", aggregator.StoplightGreen))
	h.key("o")
	if len(h.opened) != 1 || h.opened[0] != "https://example.test/a" {
		t.Errorf("opened = %v", h.opened)
	}
}

func TestManagerAndBack(t *testing.T) {
	h := newHarness(repo("a", aggregator.StoplightGreen))
	h.key("m")
	if h.m.screen != screenManage {
		t.Fatal("m should open the Repo manager")
	}
	// Keys go to the manager now, so q does not quit.
	h.key("q")
	if h.stopped {
		t.Error("q inside the Repo manager must not quit")
	}
	h.send(ui.BackMsg{})
	if h.m.screen != screenDashboard {
		t.Error("BackMsg should return to the Dashboard")
	}
}

func TestQuitStopsThePoller(t *testing.T) {
	h := newHarness(repo("a", aggregator.StoplightGreen))
	h.key("q")
	if !h.stopped {
		t.Error("q must stop the Poller")
	}
}
