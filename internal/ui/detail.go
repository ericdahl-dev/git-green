package ui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ericdahl-dev/git-green/internal/state"
)

type BackMsg struct{}

var (
	detailTitleStyle = lipgloss.NewStyle().Bold(true).MarginBottom(1)
	jobGreen         = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	jobRed           = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	jobYellow        = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	jobFaint         = lipgloss.NewStyle().Faint(true)
)

type Detail struct {
	repo state.RepoState
}

func NewDetail(repo state.RepoState) Detail {
	return Detail{repo: repo}
}

func (d Detail) CurrentRepo() state.RepoState {
	return d.repo
}

func (d Detail) Init() tea.Cmd { return nil }

func (d Detail) Update(msg tea.Msg) (Detail, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "backspace":
			return d, func() tea.Msg { return BackMsg{} }
		}
	case state.Snapshot:
		for _, r := range msg.Repos {
			if r.Owner == d.repo.Owner && r.Name == d.repo.Name {
				d.repo = r
				break
			}
		}
	}
	return d, nil
}

func (d Detail) View() string {
	header := fmt.Sprintf("%s  %s", d.repo.Stoplight.String(), d.repo.FullName())
	out := detailTitleStyle.Render(header) + "\n"

	if d.repo.Err != nil {
		out += jobRed.Render(fmt.Sprintf("  ⚠ Error: %v", d.repo.Err)) + "\n\n"
	}

	if len(d.repo.Runs) == 0 {
		out += jobFaint.Render("  No runs found.") + "\n"
	}

	for _, run := range d.repo.Runs {
		status := run.Conclusion
		if status == "" {
			status = run.Status
		}
		wfLine := fmt.Sprintf("  %s  %s", workflowStatusIcon(status), run.WorkflowName)
		out += wfLine + "\n"
		for _, job := range run.Jobs {
			jobStatus := job.Conclusion
			if jobStatus == "" {
				jobStatus = job.Status
			}
			jobLine := fmt.Sprintf("      %s  %s", jobStatusIcon(jobStatus), job.Name)
			out += jobLine + "\n"
		}
		out += "\n"
	}

	out += hintStyle.Render("esc/backspace back  o open  r refresh  ? help")
	return out
}

func workflowStatusIcon(status string) string {
	switch status {
	case "success", "neutral", "skipped":
		return jobGreen.Render("✓")
	case "failure", "timed_out", "action_required":
		return jobRed.Render("✗")
	case "queued", "in_progress":
		return jobYellow.Render("●")
	default:
		return jobFaint.Render("○")
	}
}

func jobStatusIcon(status string) string {
	return workflowStatusIcon(status)
}
