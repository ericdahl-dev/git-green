package ui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ericdahl-dev/git-green/internal/aggregator"
	"github.com/ericdahl-dev/git-green/internal/state"
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).MarginBottom(1)
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	normalStyle   = lipgloss.NewStyle()
	staleStyle    = lipgloss.NewStyle().Faint(true)
	hintStyle     = lipgloss.NewStyle().Faint(true)
)

type SelectRepoMsg int // index of selected repo

type Dashboard struct {
	snapshot state.Snapshot
	cursor   int
}

func NewDashboard(snap state.Snapshot) Dashboard {
	return Dashboard{snapshot: snap}
}

func (d Dashboard) Init() tea.Cmd { return nil }

func (d Dashboard) Update(msg tea.Msg) (Dashboard, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if d.cursor > 0 {
				d.cursor--
			}
		case "down", "j":
			if d.cursor < len(d.snapshot.Repos)-1 {
				d.cursor++
			}
		case "enter":
			return d, func() tea.Msg { return SelectRepoMsg(d.cursor) }
		}
	case state.Snapshot:
		d.snapshot = msg
		if d.cursor >= len(d.snapshot.Repos) && len(d.snapshot.Repos) > 0 {
			d.cursor = len(d.snapshot.Repos) - 1
		}
	}
	return d, nil
}

func (d Dashboard) SelectedRepo() *state.RepoState {
	if len(d.snapshot.Repos) == 0 {
		return nil
	}
	r := d.snapshot.Repos[d.cursor]
	return &r
}

func (d Dashboard) View() string {
	out := titleStyle.Render("git-green") + "\n"

	if len(d.snapshot.Repos) == 0 {
		out += staleStyle.Render("  No repos configured.") + "\n"
	}

	for i, r := range d.snapshot.Repos {
		row := repoRow(r)
		if i == d.cursor {
			out += selectedStyle.Render("▶ "+row) + "\n"
		} else {
			out += normalStyle.Render("  "+row) + "\n"
		}
	}

	out += "\n" + hintStyle.Render("↑/↓ navigate  enter detail  r refresh  o open  q quit  ? help")
	return out
}

func repoRow(r state.RepoState) string {
	icon := r.Stoplight.String()
	name := r.FullName()

	summary := workflowSummary(r)

	row := fmt.Sprintf("%s  %-40s %s", icon, name, summary)
	if r.IsStale() {
		age := time.Since(*r.StaleAt).Round(time.Second)
		row = staleStyle.Render(row + fmt.Sprintf("  ⚠ last seen %s ago", age))
	}
	return row
}

func workflowSummary(r state.RepoState) string {
	if r.Err != nil && len(r.Runs) == 0 {
		return "error"
	}
	if len(r.Runs) == 0 {
		return "no runs"
	}
	// Find the most notable run to show.
	for _, run := range r.Runs {
		s := run.Conclusion
		if s == "" {
			s = run.Status
		}
		light := aggregator.RunStatus(s)
		if aggregator.Aggregate([]aggregator.RunStatus{light}) == r.Stoplight {
			label := s
			if label == "" {
				label = "unknown"
			}
			return fmt.Sprintf("%s · %s", run.WorkflowName, label)
		}
	}
	run := r.Runs[0]
	s := run.Conclusion
	if s == "" {
		s = run.Status
	}
	return fmt.Sprintf("%s · %s", run.WorkflowName, s)
}
